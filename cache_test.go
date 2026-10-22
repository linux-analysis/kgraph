package kgraph

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/linux-analysis/kgraph/kernel"
)

// dump lists what g tells of each of its commits, in hash order.
func dump(g *Graph) string {
	var b strings.Builder
	for _, i := range g.order {
		h := g.hashes[i].String()
		c, _ := lookupOf(g, h)
		fmt.Fprintf(&b, "%+v %v %v %v %q %v %v\n", c, fixesOf(g, h), fixedByOf(g, h), backportsOf(g, h), revertsOf(g, h), revertedOf(g, h), copiesOf(g, h))
	}
	return b.String()
}

// buildWith builds the graph of dir with the cache file and returns its
// log.
func buildWith(t *testing.T, dir string, rem kernel.Remotes, cache string) (*Graph, string) {
	t.Helper()
	var log bytes.Buffer
	g, err := buildTree(ctx, dir, rem, Options{Cache: cache}, slog.New(slog.NewTextHandler(&log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return g, log.String()
}

func wantLog(t *testing.T, log string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(log, p) {
			t.Errorf("log lacks %q:\n%s", p, log)
		}
	}
}

// A build from the cache reads only the new commits, and the facts they
// change about old commits come out as from a build without it.
func TestBuildCache(t *testing.T) {
	k := newKernel(t)
	cache := filepath.Join(t.TempDir(), "graph.cache")
	g, log := buildWith(t, k.dir, remotes, cache)
	n := len(g.hashes)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=0 streamed=%d", n, n))
	before, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}

	g, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=0", n, n))
	if dump(g) != dump(build(t, k.dir, remotes)) {
		t.Error("unchanged tips: another graph")
	}
	if after, err := os.Stat(cache); err != nil || !os.SameFile(before, after) {
		t.Errorf("unchanged tips: cache rewritten (%v)", err)
	}

	k.git("checkout", "-q", "master")
	again := k.commit("net: fix foo again\n\nFixes: " + k.m["add"][:8])
	// The subject a Fixes: tag of mm: fix bar quotes is no longer unique.
	k.commit("net: add foo")
	k.git("update-ref", "refs/remotes/mainline/master", "HEAD")
	k.git("checkout", "-q", "linux-6.0.y")
	backport := k.commit("net: fix foo again") // no upstream line
	k.git("tag", "v6.0.3")
	k.git("update-ref", "refs/remotes/stable/linux-6.0.y", "HEAD")

	g, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=3", n+3, n))
	if dump(g) != dump(build(t, k.dir, remotes)) {
		t.Error("new commits: another graph than without the cache")
	}
	for _, tt := range []struct {
		name      string
		got, want []string
	}{
		{"FixedBy of an old commit", fixedByOf(g, k.m["add"]), []string{k.m["fix"], again}},
		{"Fixes: by a subject no longer unique", fixesOf(g, k.m["bar"]), []string{k.m["init"]}},
		{"backport by author and subject", backportsOf(g, again), []string{backport}},
		{"old backport in a new tag", backportsOf(g, k.m["late"]), []string{k.s["untagged60"]}},
	} {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

// A cached commit that no tip reaches any more, as one of a rewound
// branch, leaves the cache, and nothing else is read again: the graph is
// the one a build without the cache reads.
func TestBuildCacheRewound(t *testing.T) {
	k := newKernel(t)
	cache := filepath.Join(t.TempDir(), "graph.cache")
	n := len(build(t, k.dir, remotes).hashes)
	buildWith(t, k.dir, remotes, cache)
	same := func(step string, g *Graph, log string) {
		t.Helper()
		if strings.Contains(log, "does not fit") {
			t.Errorf("%s: the cache was read again:\n%s", step, log)
		}
		if dump(g) != dump(build(t, k.dir, remotes)) {
			t.Errorf("%s: another graph than without the cache", step)
		}
	}

	// The build reads a mapped cache, so a write into it would fault.
	k.git("update-ref", "refs/remotes/stable/linux-6.0.y", "v6.0.2^{commit}")
	g, log := buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=0 dropped=1", n-1, n))
	if _, ok := lookupOf(g, k.s["untagged60"]); ok {
		t.Error("a commit of a rewound branch is in the graph")
	}
	same("rewound", g, log)
	// The cache written without the commit loads, so its columns check.
	g, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("cached=%d streamed=0 dropped=0", n-1))
	same("after the rewind", g, log)

	// A rebase: the branch gets a commit, then another in its place.
	k.git("checkout", "-q", "-B", "linux-6.0.y", "v6.0.2")
	k.commit("net: fix bar")
	k.git("update-ref", "refs/remotes/stable/linux-6.0.y", "HEAD")
	_, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("cached=%d streamed=1 dropped=0", n-1))
	k.git("reset", "-q", "--hard", "v6.0.2")
	k.commit("net: fix bar again")
	k.git("update-ref", "refs/remotes/stable/linux-6.0.y", "HEAD")
	g, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=1 dropped=1", n, n))
	same("rebased", g, log)

	// Rebased again, and git pruned the tip the cache knows.
	pruned := k.git("rev-parse", "HEAD")
	k.git("reset", "-q", "--hard", "v6.0.2")
	k.commit("net: fix bar once more")
	k.git("update-ref", "refs/remotes/stable/linux-6.0.y", "HEAD")
	if err := os.Remove(filepath.Join(k.dir, ".git", "objects", pruned[:2], pruned[2:])); err != nil {
		t.Fatal(err)
	}
	g, log = buildWith(t, k.dir, remotes, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=1 dropped=1", n, n))
	same("pruned", g, log)
}

// A cache the build cannot use is read again from git and replaced.
func TestBuildCacheUnusable(t *testing.T) {
	k := newKernel(t)
	want := dump(build(t, k.dir, remotes))
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	buildWith(t, k.dir, remotes, good)
	valid, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	other := newRepo(t)
	other.commit("Unrelated")
	foreign := filepath.Join(dir, "foreign")
	buildWith(t, other.dir, kernel.Remotes{}, foreign)
	foreignBytes, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatal(err)
	}
	version := slices.Clone(valid)
	version[len(cacheMagic)]++
	s, err := loadStore(good)
	if err != nil {
		t.Fatal(err)
	}
	s.facts.reverts[0].id = bytes.Repeat([]byte("a"), 41)
	long := filepath.Join(dir, "long")
	if err := s.save(long); err != nil {
		t.Fatal(err)
	}
	longID, err := os.ReadFile(long)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, log string
		data      []byte
	}{
		{"garbage", "is not a commit graph cache", []byte("garbage")},
		{"truncated", "unexpected EOF", valid[:len(valid)/2]},
		{"trailing bytes", "trailing bytes", append(slices.Clone(valid), 0)},
		{"another version", fmt.Sprintf("has version %d, not %d", cacheVersion+1, cacheVersion), version},
		{"another repository", "does not fit", foreignBytes},
		{"id too long", "id too long", longID},
	} {
		cache := filepath.Join(dir, "cache")
		if err := os.WriteFile(cache, tt.data, 0o644); err != nil {
			t.Fatal(err)
		}
		g, log := buildWith(t, k.dir, remotes, cache)
		wantLog(t, log, tt.log, "cached=0")
		if dump(g) != want {
			t.Errorf("%s: another graph", tt.name)
		}
		if _, log := buildWith(t, k.dir, remotes, cache); !strings.Contains(log, "streamed=0") {
			t.Errorf("%s: the cache was not replaced:\n%s", tt.name, log)
		}
	}
}

// A shallow tree keeps no cache, and neither does a cancelled build.
func TestBuildCacheNotWritten(t *testing.T) {
	k := newKernel(t)
	shallow := filepath.Join(t.TempDir(), "shallow")
	k.git("clone", "-q", "--depth=1", "file://"+k.dir, shallow)
	cache := filepath.Join(t.TempDir(), "graph.cache")
	buildWith(t, shallow, kernel.Remotes{}, cache)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("shallow tree: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := buildTree(cancelled, k.dir, remotes, Options{Cache: cache}, quiet); err == nil {
		t.Error("cancelled build succeeded")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("cancelled build: %v", err)
	}
}
