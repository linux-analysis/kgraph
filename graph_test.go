package kgraph

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linux-analysis/kgraph/kernel"
)

var ctx = context.Background()

// repo is a throwaway repository whose commits get increasing dates.
type repo struct {
	t        *testing.T
	dir      string
	clock    int64
	authored int64 // the author date of the next commit, when not the clock
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	r := &repo{t: t, dir: t.TempDir(), clock: 1700000000}
	r.git("init", "-q", "-b", "master")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return r.gitAs("alice@example.com", args...)
}

func (r *repo) gitAs(email string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	date, authored := fmt.Sprintf("%d +0000", r.clock), fmt.Sprintf("%d +0000", cmp.Or(r.authored, r.clock))
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL="+email, "GIT_AUTHOR_DATE="+authored,
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com", "GIT_COMMITTER_DATE="+date,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// commit records an empty commit by alice and returns its hash.
func (r *repo) commit(msg string) string {
	r.t.Helper()
	return r.commitAs("alice@example.com", msg)
}

// commitAt records an empty commit by email at Unix time at, and leaves
// the clock where it was.
func (r *repo) commitAt(at int64, email, msg string) string {
	r.t.Helper()
	now := r.clock
	defer func() { r.clock = now }()
	r.clock = at - 3600
	return r.commitAs(email, msg)
}

// pick records a commit by alice that keeps the author date at, as git
// cherry-pick does, and is committed now.
func (r *repo) pick(at int64, msg string) string {
	r.t.Helper()
	r.authored = at
	defer func() { r.authored = 0 }()
	return r.commit(msg)
}

// write writes content to file and stages it for the next commit.
func (r *repo) write(file, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", file)
}

// twin records a copy of a patch by alice at Unix time at, as another
// branch from base brings it again, and merges it into the current branch.
func (r *repo) twin(base string, at int64, msg string) string {
	r.t.Helper()
	cur := r.git("symbolic-ref", "--short", "HEAD")
	r.git("checkout", "-q", "--detach", base)
	r.commitAs("bob@example.com", "queue: start")
	h := r.commitAt(at, "alice@example.com", msg)
	r.git("checkout", "-q", cur)
	r.clock += 3600
	r.git("merge", "-q", "--no-ff", "-m", "Merge "+h[:12], h)
	return h
}

func (r *repo) commitAs(email, msg string) string {
	r.t.Helper()
	r.clock += 3600
	file := r.t.TempDir() + "/msg"
	if err := os.WriteFile(file, []byte(msg), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.gitAs(email, "commit", "-q", "--allow-empty", "--cleanup=verbatim", "-F", file)
	return r.git("rev-parse", "HEAD")
}

var (
	remotes = kernel.Remotes{Mainline: "mainline", Stable: "stable"}
	quiet   = slog.New(slog.DiscardHandler)
)

func build(t *testing.T, dir string, rem kernel.Remotes) *Graph {
	t.Helper()
	g, _ := buildWith(t, dir, rem, "")
	return g
}

// buildTree discovers the tree at dir and builds its graph.
func buildTree(ctx context.Context, dir string, rem kernel.Remotes, o Options, log *slog.Logger) (*Graph, error) {
	l, err := Discover(ctx, dir, rem, o)
	if err != nil {
		return nil, err
	}
	return l.Build(ctx, log)
}

// targets reads names as the targets of g.
func targets(t *testing.T, g *Graph, names ...string) []Target {
	t.Helper()
	ts, err := g.layout.Targets(names)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func lookup(t *testing.T, g *Graph, h string) info {
	t.Helper()
	c, ok := lookupOf(g, h)
	if !ok {
		t.Fatalf("%s is not in the graph", h)
	}
	return c
}

// kernel is a small kernel-like tree: mainline with release tags, the
// stable series 5.15 and 6.0, and a third-party branch.
type kernelRepo struct {
	*repo
	m      map[string]string // mainline commits by name
	s      map[string]string // stable commits by name
	other  string
	rename map[string]string // hash -> name, for messages
}

func newKernel(t *testing.T) *kernelRepo {
	t.Helper()
	k := &kernelRepo{repo: newRepo(t), m: map[string]string{}, s: map[string]string{}}
	r := k.repo
	k.m["init"] = r.commit("Initial import")
	r.git("tag", "v5.15", k.m["init"])
	k.m["add"] = r.commit("net: add foo\n\nAdds foo.")
	r.git("tag", "-a", "-m", "Linux 6.0-rc1", "v6.0-rc1", k.m["add"])
	r.git("tag", "v6.0", k.m["add"])
	k.m["fix"] = r.commit("net: fix foo\n\nFixes: " + k.m["add"][:12] + " (\"net: add foo\")")
	r.git("tag", "v6.1-rc1", k.m["fix"])
	// A Fixes: id of a rebased maintainer tree: only the subject names it.
	k.m["bar"] = r.commit("mm: fix bar\n\n  Fixes: commit deadbeef1234 (\"net: add foo\")\nLink: https://lore.kernel.org/r/1/Fixes: " +
		k.m["init"][:12] + " (\"Initial import\")\nCo-prefixes: " + k.m["add"][:12])
	k.m["dup1"] = r.commitAs("bob@example.com", "x86: tweak")
	k.m["dup2"] = r.commitAs("bob@example.com", "x86: tweak")
	r.git("tag", "v6.1", k.m["dup2"])
	k.m["revert"] = r.commit("Revert \"net: fix foo\"\n\nThis reverts commit\n" + k.m["fix"] + ".")
	k.m["late"] = r.commit("arm: fix baz\n\nFixes: " + k.m["fix"][:6])
	k.m["once"] = r.commit("fs: fix once")
	k.m["again"] = r.commit("fs: fix again")
	k.m["twin1"] = r.commit("drm: fix qux")
	r.git("tag", "v6.2-rc2")
	quxAt := r.clock
	// The same patch merged again through another branch keeps its author
	// date; the email in any case.
	r.git("checkout", "-q", "-b", "qux", k.m["twin1"]+"^")
	k.m["twin2"] = r.commitAt(quxAt, "Alice@Example.com", "drm: fix qux")
	r.git("checkout", "-q", "master")
	r.clock += 3600
	r.git("merge", "-q", "--no-ff", "-m", "Merge window opens", "qux")
	k.m["tip"] = r.git("rev-parse", "HEAD")
	r.git("update-ref", "refs/remotes/mainline/master", k.m["tip"])

	r.git("checkout", "-q", "-b", "linux-6.0.y", "v6.0")
	k.s["fix60"] = r.commit("net: fix foo\n\ncommit " + k.m["fix"] + " upstream.")
	k.s["bar60"] = r.commitAs("ALICE@example.com", "mm: fix bar") // no upstream line: by author, in any case, and subject
	r.git("tag", "v6.0.1")
	k.s["revert60"] = r.commit("Revert \"net: fix foo\"\n\nThis reverts commit " + k.s["fix60"] + " which is\ncommit " + k.m["fix"] + " upstream.")
	k.s["tweak60"] = r.commitAs("bob@example.com", "x86: tweak") // two mainline candidates
	k.s["qux60"] = r.commitAt(quxAt, "alice@example.com", "drm: fix qux\n\ncommit "+k.m["twin1"]+" upstream.")
	r.git("tag", "-a", "-m", "Linux 6.0.2", "v6.0.2")
	k.s["untagged60"] = r.commit("arm: fix baz\n\ncommit " + k.m["late"] + " upstream.")
	r.git("update-ref", "refs/remotes/stable/linux-6.0.y", "HEAD")

	r.git("checkout", "-q", "-b", "linux-5.15.y", "v5.15")
	k.s["fix515"] = r.commit("net: fix foo\n\n[ Upstream commit " + k.m["fix"] + " ]")
	k.s["quirk515"] = r.commit("stable: quirk")
	k.s["unquirk515"] = r.commit("Revert \"stable: quirk\"\n\nThis reverts commit " + k.s["quirk515"] + ".\n\ncommit " + k.m["revert"] + " upstream.")
	k.s["early515"] = r.commitAt(1700000000, "alice@example.com", "arm: fix baz") // older than its mainline namesake
	r.git("tag", "v5.15.1")
	// A revert of a backport that names the upstream, and a revert of a
	// revert of a backport.
	k.s["once515"] = r.commit("fs: fix once\n\ncommit " + k.m["once"] + " upstream.")
	k.s["again515"] = r.commit("fs: fix again\n\ncommit " + k.m["again"] + " upstream.")
	k.s["undo515"] = r.commit("Revert \"fs: fix again\"\n\nThis reverts commit " + k.s["again515"] + ".")
	r.git("tag", "v5.15.2")
	k.s["unonce515"] = r.commit("Revert \"fs: fix once\"\n\nThis reverts commit " + k.m["once"] + ".")
	k.s["redo515"] = r.commit("Revert \"Revert \"fs: fix again\"\"\n\nThis reverts commit " + k.s["undo515"] + ".")
	r.git("tag", "v5.15.3")
	r.git("update-ref", "refs/remotes/stable/linux-5.15.y", "HEAD")

	r.git("checkout", "-q", "-b", "elsewhere", k.m["tip"])
	k.other = r.commit("other: tree only")
	r.git("update-ref", "refs/remotes/other/x", k.other)
	r.git("checkout", "-q", "master")
	r.git("reset", "-q", "--hard", k.m["tip"])
	r.git("tag", "v6.2-rc1", k.m["tip"]) // ignored: mainline is past it anyway
	r.git("tag", "-d", "v6.2-rc1")
	tree := r.git("rev-parse", k.m["init"]+"^{tree}")
	r.git("tag", "v2.6.11", tree) // a tag of a tree, as the real v2.6.11 is
	r.git("tag", "v6.0.3-rc1", k.s["untagged60"])
	return k
}

// Resolve names a commit by its full id or a unique prefix, in any case; a
// prefix several commits share names none, and so does a commit no tip of
// the graph reaches.
func TestResolve(t *testing.T) {
	k := newKernel(t)
	g := build(t, k.dir, remotes)
	fix := k.m["fix"]
	for _, id := range []string{fix, fix[:12], strings.ToUpper(fix[:7])} {
		if got, ok := resolveOf(g, id); !ok || got != fix {
			t.Errorf("Resolve(%q) = %q, %v, want %s", id, got, ok, fix)
		}
	}
	for _, id := range []string{"", "zz", strings.Repeat("0", 41), k.other, k.other[:12]} {
		if got, ok := resolveOf(g, id); ok {
			t.Errorf("Resolve(%q) = %q, want none", id, got)
		}
	}
	byNibble := map[byte][]string{}
	for _, h := range slices.Concat(slices.Collect(maps.Values(k.m)), slices.Collect(maps.Values(k.s))) {
		byNibble[h[0]] = append(byNibble[h[0]], h)
	}
	for n, hs := range byNibble {
		if _, ok := resolveOf(g, string(n)); ok != (len(hs) == 1) {
			t.Errorf("Resolve(%q) ok = %v with %d commits", n, ok, len(hs))
		}
		if amb := g.Ambiguous(strings.ToUpper(string(n))); amb != (len(hs) > 1) {
			t.Errorf("Ambiguous(%q) = %v with %d commits", n, amb, len(hs))
		}
	}
	if g.Ambiguous(fix) || g.Ambiguous(fix[:12]) || g.Ambiguous("zz") {
		t.Error("Ambiguous of one commit's id or of no id")
	}
}

// sortByKey orders like a stable sort by key, over every digit.
func TestSortByKey(t *testing.T) {
	keys := []uint64{3 << 48, 1, 3 << 48, 2, 1 << 16, 0, 1, 1<<48 | 1}
	vals := []uint32{0, 1, 2, 3, 4, 5, 6, 7}
	type kv struct {
		k uint64
		v uint32
	}
	var want []kv
	for i := range keys {
		want = append(want, kv{keys[i], vals[i]})
	}
	slices.SortStableFunc(want, func(a, b kv) int { return cmp.Compare(a.k, b.k) })
	sortByKey(keys, vals)
	for i, w := range want {
		if keys[i] != w.k || vals[i] != w.v {
			t.Fatalf("sortByKey = %v %v, want %v", keys, vals, want)
		}
	}
}

// index orders hashes that share their leading eight bytes by the rest.
func TestIndexTies(t *testing.T) {
	s := newStore()
	for _, id := range []string{
		"00000000000000010000000000000000000000ff",
		"0000000000000001000000000000000000000000",
		"0000000000000000ffffffffffffffffffffffff",
	} {
		h, _ := parseHash([]byte(id))
		s.hashes = append(s.hashes, h)
	}
	s.index()
	if want := []uint32{2, 1, 0}; !slices.Equal(s.order, want) {
		t.Errorf("order = %v, want %v", s.order, want)
	}
}

// idx and prefix find what a scan of every hash finds, among hashes that
// share their leading eight bytes or sixteen digits and at the ends of the
// fanout.
func TestLookup(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	s := newStore()
	ids := []string{
		"0000000000000000000000000000000000000000",
		"0000000000000000000000000000000000000001",
		"ffffffffffffffffffffffffffffffffffffffff",
		"ffffffffffffffff000000000000000000000000",
		"0123456789abcdef0123456789abcdef01234567",
		"0123456789abcdef0123456789abcdef01234568",
		"0123456789abcdef9123456789abcdef01234567",
		"0123456789abcde00000000000000000000000ab",
	}
	for range 5000 {
		var h hash
		for k := range h {
			h[k] = byte(rng.Uint32())
		}
		h[0] &= 0x0f // crowd the buckets of a sixteenth of the fanout
		ids = append(ids, h.String())
	}
	for _, id := range ids {
		h, _ := parseHash([]byte(id))
		s.hashes = append(s.hashes, h)
	}
	s.index()
	scan := func(p string) (int32, bool) {
		match, found := int32(none), 0
		for i, id := range ids {
			if strings.HasPrefix(id, p) {
				match, found = int32(i), found+1
			}
		}
		if found != 1 {
			return none, false
		}
		return match, true
	}
	var probes []string
	for i, id := range ids {
		for n := 1; n <= 40; n++ {
			if i < 100 || n%7 == 0 || n == 16 || n == 17 || n == 40 {
				probes = append(probes, id[:n])
			}
		}
		probes = append(probes, id[:39]+"g", strings.ToUpper(id[:12]))
	}
	for range 2000 {
		probes = append(probes, fmt.Sprintf("%016x%016x%08x", rng.Uint64(), rng.Uint64(), rng.Uint32()))
	}
	for _, p := range probes {
		want, wok := scan(p)
		if got, found := s.prefix([]byte(p)); found == 1 != wok || wok && got != want {
			t.Fatalf("prefix(%s) = %d, %d; a scan finds %d, %v", p, got, found, want, wok)
		}
		if h, full := parseHash([]byte(p)); full && strings.ToLower(p) == p {
			if got, ok := s.idx(h); got != want || ok != wok {
				t.Fatalf("idx(%s) = %d, %v; a scan finds %d, %v", p, got, ok, want, wok)
			}
		}
	}
}

func TestSubjectOf(t *testing.T) {
	for _, tt := range []struct{ msg, want string }{
		{"net: fix foo\n\nbody\n", "net: fix foo"},
		{"\n\n  \nnet: fix foo  \n", "net: fix foo"},
		{"net: fix foo\nwrapped\t\n\nbody", "net: fix foo wrapped"},
		{"net: fix\x1f foo", "net: fix\x1f foo"},
		{"", ""},
	} {
		if got := string(subjectOf([]byte(tt.msg))); got != tt.want {
			t.Errorf("subjectOf(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestBuild(t *testing.T) {
	k := newKernel(t)
	g := build(t, k.dir, remotes)
	rel := func(s string) kernel.Release { return kernel.ParseRelease(s) }
	un := kernel.Unreleased
	for _, tt := range []struct {
		name     string
		hash     string
		mainline bool
		tag      string
		release  kernel.Release
		seriesR  kernel.Release
		upstream string
	}{
		{"initial", k.m["init"], true, "v5.15", rel("v5.15"), un, ""},
		{"rc1 before the final tag", k.m["add"], true, "v6.0-rc1", rel("v6.0"), un, ""},
		{"rc1", k.m["fix"], true, "v6.1-rc1", rel("v6.1"), un, ""},
		{"final", k.m["dup2"], true, "v6.1", rel("v6.1"), un, ""},
		{"after the last tag", k.m["tip"], true, "", un, un, ""},
		{"named upstream", k.s["fix60"], false, "v6.0.1", un, rel("v6.0"), k.m["fix"]},
		{"upstream by author and subject", k.s["bar60"], false, "v6.0.1", un, rel("v6.0"), k.m["bar"]},
		{"revert quoting the upstream line", k.s["revert60"], false, "v6.0.2", un, rel("v6.0"), k.m["revert"]},
		{"several mainline candidates", k.s["tweak60"], false, "v6.0.2", un, rel("v6.0"), ""},
		{"untagged backport", k.s["untagged60"], false, "", un, un, k.m["late"]},
		{"[ Upstream commit ]", k.s["fix515"], false, "v5.15.1", un, rel("v5.15"), k.m["fix"]},
		{"revert of a stable commit", k.s["unquirk515"], false, "v5.15.1", un, rel("v5.15"), k.m["revert"]},
		{"older than its namesake", k.s["early515"], false, "v5.15.1", un, rel("v5.15"), ""},
	} {
		c := lookup(t, g, tt.hash)
		want := info{Hash: tt.hash, Mainline: tt.mainline, Tree: "stable", Tag: tt.tag, Release: tt.release, SeriesRelease: tt.seriesR,
			Upstream: tt.upstream, Date: c.Date}
		if tt.mainline {
			want.Tree = "mainline"
		}
		if c != want {
			t.Errorf("%s:\n got %+v\nwant %+v", tt.name, c, want)
		}
	}
	if c, ok := lookupOf(g, strings.ToUpper(k.m["fix"])); !ok || c.Hash != k.m["fix"] {
		t.Errorf("upper-case lookup: %+v, %v", c, ok)
	}
	for _, h := range []string{k.other, "nonsense"} {
		if _, ok := lookupOf(g, h); ok {
			t.Errorf("Lookup(%s) found a commit", h)
		}
	}
	if c, ok := lookupOf(g, k.m["fix"][:12]); !ok || c.Hash != k.m["fix"] {
		t.Errorf("abbreviated lookup: %+v, %v", c, ok)
	}
	if c := lookup(t, g, k.m["fix"]); c.Date <= lookup(t, g, k.m["add"]).Date {
		t.Errorf("dates do not increase: %+v", c)
	}

	for _, tt := range []struct {
		name string
		got  []string
		want []string
	}{
		{"Fixes: by id", fixesOf(g, k.m["fix"]), []string{k.m["add"]}},
		{"Fixes: by the quoted subject, after a link, not in a word", fixesOf(g, k.m["bar"]), []string{k.m["add"], k.m["init"]}},
		{"Fixes: of six digits", fixesOf(g, k.m["late"]), []string{k.m["fix"]}},
		{"no Fixes:", fixesOf(g, k.m["add"]), nil},
		{"FixedBy", fixedByOf(g, k.m["add"]), []string{k.m["fix"], k.m["bar"]}},
		{"FixedBy, the root", fixedByOf(g, k.m["init"]), []string{k.m["bar"]}},
		{"FixedBy, none", fixedByOf(g, k.m["bar"]), nil},
		{"backport reverted in 6.0", backportsOf(g, k.m["fix"]), []string{k.s["fix515"]}},
		{"backport by author and subject", backportsOf(g, k.m["bar"]), []string{k.s["bar60"]}},
		{"backport of a twin", backportsOf(g, k.m["twin2"]), []string{k.s["qux60"]}},
		{"untagged backport", backportsOf(g, k.m["late"]), nil},
		{"backport its series reverted by naming the upstream", backportsOf(g, k.m["once"]), nil},
		{"backport reverted and applied again", backportsOf(g, k.m["again"]), nil},
		{"reverts of stable commits", backportsOf(g, k.m["revert"]), nil},
		{"a stable commit stands for its patch", backportsOf(g, k.s["fix60"]), []string{k.s["fix515"]}},
		{"unknown commit", backportsOf(g, k.other), nil},
		{"Copies, earliest release first", copiesOf(g, k.m["twin2"]), []string{k.m["twin1"], k.m["twin2"]}},
		{"Copies, the other one", copiesOf(g, k.m["twin1"]), []string{k.m["twin1"], k.m["twin2"]}},
		{"Copies, another author date", copiesOf(g, k.m["dup1"]), []string{k.m["dup1"]}},
		{"Copies, a backport standing for its patch", copiesOf(g, k.s["qux60"]), []string{k.m["twin1"], k.m["twin2"]}},
		{"Copies, none", copiesOf(g, k.m["fix"]), []string{k.m["fix"]}},
	} {
		if !slices.Equal(tt.got, tt.want) || (tt.want == nil) != (tt.got == nil) {
			t.Errorf("%s: got %v, want %v", tt.name, tt.got, tt.want)
		}
	}
	if got := revertsOf(g, k.m["revert"]); got != k.m["fix"] {
		t.Errorf("revert across a line break: got %q", got)
	}
	if got := revertsOf(g, k.s["revert60"]); got != k.s["fix60"] {
		t.Errorf("stable revert: got %q", got)
	}
	if got := revertsOf(g, k.m["fix"]); got != "" {
		t.Errorf("not a revert: got %q", got)
	}
	if !revertedOf(g, k.m["fix"]) || !revertedOf(g, k.s["fix60"]) || revertedOf(g, k.m["add"]) || revertedOf(g, k.other) {
		t.Error("Reverted")
	}
}

// A branch with the name of a release tag does not hide the tag.
func TestBuildTagNamedLikeBranch(t *testing.T) {
	k := newKernel(t)
	k.git("branch", "v6.1", k.m["dup2"])
	if c := lookup(t, build(t, k.dir, remotes), k.m["dup2"]); c.Tag != "v6.1" {
		t.Errorf("tag shadowed by a branch: %+v", c)
	}
}

// A bare clone reads the same as the checkout it was cloned from.
func TestBuildBare(t *testing.T) {
	k := newKernel(t)
	bare := t.TempDir() + "/linux.git"
	k.git("clone", "-q", "--mirror", k.dir, bare)
	want, got := build(t, k.dir, remotes), build(t, bare, remotes)
	for _, h := range slices.Concat(slices.Collect(maps.Values(k.m)), slices.Collect(maps.Values(k.s))) {
		if w, g := lookup(t, want, h), lookup(t, got, h); g != w {
			t.Errorf("%s in the bare clone: %+v, want %+v", h, g, w)
		}
	}
}

// A mainline ref left behind by a failed fetch does not make the commits
// that the mainline tags fetched with stable reach stable.
func TestBuildStaleMainlineRef(t *testing.T) {
	k := newKernel(t)
	k.git("update-ref", "refs/remotes/mainline/master", k.m["add"])
	k.git("tag", "v6.2-rc1", k.m["late"])
	g := build(t, k.dir, remotes)
	if c := lookup(t, g, k.m["revert"]); !c.Mainline || c.Tag != "v6.2-rc1" {
		t.Errorf("commit behind a mainline tag: %+v", c)
	}
	if _, ok := lookupOf(g, k.m["tip"]); ok {
		t.Error("a commit no tip reaches is in the graph")
	}
}

// A Fixes: id that names no commit is the one the fixes table gives it; a
// NOT_VALID line there leaves the quoted subject to name it.
func TestBuildFixesTable(t *testing.T) {
	r := newRepo(t)
	add := r.commit("net: add foo")
	fix := r.commit("net: fix foo\n\nFixes: 0123456789ab")
	again := r.commit("net: fix foo again\n\nFixes: 0123456789ac (\"net: add foo\")")
	table := filepath.Join(t.TempDir(), "fixes.txt")
	if err := os.WriteFile(table, []byte("# invalid valid\n0123456789AB "+add+"\n0123456789ac NOT_VALID\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := buildTree(ctx, r.dir, kernel.Remotes{}, Options{Fixes: table}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{fix, again} {
		if got := fixesOf(g, h); !slices.Equal(got, []string{add}) {
			t.Errorf("Fixes(%s) = %v, want [%s]", h, got, add)
		}
	}
	if got := fixesOf(build(t, r.dir, kernel.Remotes{}), fix); got != nil {
		t.Errorf("without the table: Fixes = %v", got)
	}
	if _, err := buildTree(ctx, r.dir, kernel.Remotes{}, Options{Fixes: table + ".missing"}, quiet); err == nil {
		t.Error("missing table: no error")
	}
}

// A fix reworked for -fixes and cherry-picked with -x has a new author
// date; its line naming the commit and the subject make it a copy, of
// the twins of that commit too, but not with another subject.
func TestCopiesByCherryPick(t *testing.T) {
	r := newRepo(t)
	base := r.commit("init")
	next := r.commit("drm: fix qux")
	quxAt := r.clock
	r.git("tag", "v6.1")
	again := r.twin(base, quxAt, "drm: fix qux")
	fixes := r.commit("drm: fix qux\n\n(cherry picked from commit " + next + ")")
	other := r.commit("drm: fix quux\n\n(cherry picked from commit " + next + ")")
	g := build(t, r.dir, kernel.Remotes{})
	want := []string{next, again, fixes}
	for _, h := range want {
		if got := copiesOf(g, h); !slices.Equal(got, want) {
			t.Errorf("Copies(%s) = %v, want %v", h, got, want)
		}
	}
	if got := copiesOf(g, other); !slices.Equal(got, []string{other}) {
		t.Errorf("Copies(other) = %v, want [%s]", got, other)
	}
}

// A commit with the author, author date and subject of an older one it
// descends from is a copy of it when it changes the same lines, as a fix
// applied again after its revert, and else no copy: a script that
// regenerates a commit keeps the date of the first. The lines read for it
// go to the cache.
func TestTwinsOnOneLine(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.write("cpufeatures.h", "A\n")
	first := r.commit("tools: sync cpufeatures")
	syncAt := r.clock
	r.write("fix.c", "fix\n")
	fix := r.commit("net: fix foo")
	fixAt := r.clock
	r.git("tag", "v6.1")
	r.write("cpufeatures.h", "A\nB\n")
	second := r.commitAt(syncAt, "alice@example.com", "tools: sync cpufeatures")
	r.git("rm", "-q", "fix.c")
	r.commit("Revert \"net: fix foo\"\n\nThis reverts commit " + fix + ".")
	r.write("fix.c", "fix\n")
	again := r.pick(fixAt, "net: fix foo")
	r.git("tag", "v6.2")
	cache := filepath.Join(t.TempDir(), "graph.cache")
	for _, cached := range []bool{false, true} {
		g, log := buildWith(t, r.dir, kernel.Remotes{}, cache)
		if cached != strings.Contains(log, "streamed=0") {
			t.Errorf("cached %v: %s", cached, log)
		}
		for _, h := range []string{first, second} {
			if got := copiesOf(g, h); !slices.Equal(got, []string{h}) {
				t.Errorf("Copies(%s) = %v, want itself alone", h, got)
			}
		}
		if got := copiesOf(g, again); !slices.Equal(got, []string{fix, again}) {
			t.Errorf("Copies(again) = %v, want [%s %s]", got, fix, again)
		}
		if got := inSeriesOf(g, fix, targets(t, g, "6.2")); got[0].Commit != again {
			t.Errorf("InSeries(fix) = %v, want it through %s", got, again)
		}
	}
}

// An identity or a subject that copies of one patch share names that
// patch, by its canonical copy: in a stable branch and a Fixes: tag whose
// id names no commit.
func TestSubjectOfCopies(t *testing.T) {
	r := newRepo(t)
	base := r.commit("init")
	r.git("tag", "v6.0")
	first := r.commit("drm: fix qux")
	second := r.twin(base, r.clock, "drm: fix qux")
	fix := r.commitAs("bob@example.com", "drm: fix qux again\n\nFixes: deadbeef1234 (\"drm: fix qux\")")
	r.git("tag", "v6.1")
	r.git("checkout", "-q", "-b", "linux-6.0.y", "v6.0")
	stable := r.commit("drm: fix qux")
	r.git("tag", "v6.0.1")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	copies := copiesOf(g, second)
	if len(copies) != 2 || !slices.Contains(copies, first) {
		t.Fatalf("Copies = %v, want %s and %s", copies, first, second)
	}
	canonical := copies[0]
	if c := lookup(t, g, stable); c.Upstream != canonical {
		t.Errorf("upstream = %q, want the canonical copy %s", c.Upstream, canonical)
	}
	if got := fixesOf(g, fix); !slices.Equal(got, []string{canonical}) {
		t.Errorf("Fixes(fix) = %v, want [%s]", got, canonical)
	}
}

// Every question about a commit is one about its patch: a fix that names
// one copy names them all, and the answer names each patch by its
// canonical copy.
func TestPatchOfCopies(t *testing.T) {
	r := newRepo(t)
	base := r.commit("init")
	first := r.commit("drm: fix qux")
	quxAt := r.clock
	r.git("tag", "v6.1")
	second := r.twin(base, quxAt, "drm: fix qux")
	fix := r.commit("drm: fix qux harder\n\nFixes: " + second[:12] + " (\"drm: fix qux\")")
	again := r.twin(first, r.clock, "drm: fix qux harder\n\nFixes: "+first[:12]+" (\"drm: fix qux\")")
	user := r.commit("drm: use qux")
	g := build(t, r.dir, kernel.Remotes{})
	if got := copiesOf(g, second); !slices.Equal(got, []string{first, second}) {
		t.Fatalf("Copies(second) = %v, want both", got)
	}
	for _, h := range []string{first, second} {
		if got := fixedByOf(g, h); !slices.Equal(got, []string{fix}) {
			t.Errorf("FixedBy(%s) = %v, want the fix once, by its canonical copy", h, got)
		}
	}
	for _, h := range []string{fix, again} {
		if got := fixesOf(g, h); !slices.Equal(got, []string{first}) {
			t.Errorf("Fixes(%s) = %v, want the canonical copy alone", h, got)
		}
	}

	// The Fixes: tag a stable maintainer adds to a backport counts for its
	// patch.
	r.git("checkout", "-q", "-b", "linux-6.1.y", "v6.1")
	backport := r.commit("drm: use qux\n\ncommit " + user + " upstream.\n\nFixes: " + fix[:12])
	r.git("tag", "v6.1.1")
	r.git("checkout", "-q", "master")
	g = build(t, r.dir, kernel.Remotes{})
	for _, h := range []string{user, backport} {
		if got := fixesOf(g, h); !slices.Equal(got, []string{fix}) {
			t.Errorf("Fixes(%s) = %v, want the fix the backport names", h, got)
		}
	}
	if got := fixedByOf(g, again); !slices.Equal(got, []string{user}) {
		t.Errorf("FixedBy(again) = %v, want [%s]", got, user)
	}
}

// Without remotes, as in a plain clone, HEAD is mainline and the tags still
// count; without a stable remote a commit only a stable branch has is not
// in the graph. A mainline remote whose ref is missing fails the build
// rather than trust HEAD.
func TestBuildWithoutRemotes(t *testing.T) {
	k := newKernel(t)
	if _, err := buildTree(ctx, k.dir, kernel.Remotes{Mainline: "gone", Stable: "stable"}, Options{}, quiet); err == nil ||
		!strings.Contains(err.Error(), "refs/remotes/gone/master") {
		t.Errorf("missing mainline ref: %v", err)
	}
	for _, rem := range []kernel.Remotes{{}, {Mainline: "mainline"}} {
		g := build(t, k.dir, rem)
		if c := lookup(t, g, k.m["tip"]); !c.Mainline {
			t.Errorf("%+v: HEAD is not mainline: %+v", rem, c)
		}
		if c := lookup(t, g, k.s["fix60"]); c.Mainline || c.Tag != "v6.0.1" || c.Upstream != k.m["fix"] {
			t.Errorf("%+v: stable commit: %+v", rem, c)
		}
		if _, ok := lookupOf(g, k.s["untagged60"]); ok {
			t.Errorf("%+v: a commit only a stable branch has is in the graph", rem)
		}
		if got := backportsOf(g, k.m["bar"]); !slices.Equal(got, []string{k.s["bar60"]}) {
			t.Errorf("%+v: backports %v", rem, got)
		}
	}
}

// The workers' batches are put back in stream order.
func TestBuildBatches(t *testing.T) {
	k := newKernel(t)
	want := build(t, k.dir, remotes)
	defer func(n int) { batchSize = n }(batchSize)
	batchSize = 2
	got := build(t, k.dir, remotes)
	if !slices.Equal(got.hashes, want.hashes) || !slices.Equal(got.upstream, want.upstream) ||
		!slices.Equal(got.fixes, want.fixes) || !slices.Equal(got.firstTag, want.firstTag) {
		t.Error("small batches give another graph")
	}
}

// A failing read stops git, and a cancelled build returns the context's
// error, without hanging on a full pipe either way.
func TestBuildStops(t *testing.T) {
	k := newKernel(t)
	big := strings.Repeat("filler line\n", 1<<16) // far over a pipe buffer
	k.commit("huge\n\n" + big)
	k.git("update-ref", "refs/remotes/mainline/master", "HEAD")
	within := func(f func() error) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- f() }()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("Build did not return")
			return nil
		}
	}
	defer func(n int) { maxRecord = n }(maxRecord)
	maxRecord = 4096
	err := within(func() error { _, err := buildTree(ctx, k.dir, remotes, Options{}, quiet); return err })
	if err == nil || !strings.Contains(err.Error(), "read git log") {
		t.Errorf("oversized record: got %v", err)
	}
	maxRecord = 64 << 20
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = within(func() error { _, err := buildTree(cancelled, k.dir, remotes, Options{}, quiet); return err })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: got %v", err)
	}
}

// NoStable leaves every stable commit out, also those a vendor branch
// brings along, and changes nothing about mainline.
func TestBuildNoStable(t *testing.T) {
	k := newKernel(t)
	k.git("checkout", "-q", "-b", "u2", k.s["fix60"])
	own := k.commitAs("vendor@example.com", "uek: own change")
	k.git("update-ref", "refs/remotes/uek/u2", "HEAD")
	k.git("checkout", "-q", "master")
	rem := remotes
	rem.Vendors = []string{"uek"}
	var log bytes.Buffer
	g, err := buildTree(ctx, k.dir, rem, Options{NoStable: true}, slog.New(slog.NewTextHandler(&log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	wantLog(t, log.String(), "series=0")
	for name, h := range k.s {
		if _, ok := lookupOf(g, h); ok {
			t.Errorf("stable commit %s is in the graph", name)
		}
	}
	if _, ok := lookupOf(g, own); ok || slices.Contains(g.layout.Trees(), "uek/u2") {
		t.Errorf("the vendor branch is in the graph: %v", g.layout.Trees())
	}
	if c := lookup(t, g, k.m["fix"]); !c.Mainline || c.Tag != "v6.1-rc1" {
		t.Errorf("fix = %+v, want mainline in v6.1-rc1", c)
	}
	if got := fixesOf(g, k.m["fix"]); !slices.Equal(got, []string{k.m["add"]}) {
		t.Errorf("Fixes = %v, want %v", got, k.m["add"])
	}
	if got := backportsOf(g, k.m["fix"]); len(got) != 0 {
		t.Errorf("Backports = %v, want none", got)
	}
}

// linux-next is a tree of its own: its commits are in the graph under the
// tree next, are no backports, fix mainline commits and tie to their
// mainline twins, and leave the cache when next is rebuilt without them.
func TestBuildNext(t *testing.T) {
	k := newKernel(t)
	k.git("checkout", "-q", "-b", "next", k.m["tip"])
	fix := k.commit("fs: fix again more\n\nFixes: " + k.m["again"][:12] + " (\"fs: fix again\")")
	pick := k.commit("fs: fix again\n\n(cherry picked from commit " + k.m["again"] + ")")
	k.git("update-ref", "refs/remotes/next/master", "HEAD")
	k.git("checkout", "-q", "master")
	rem := remotes
	rem.Next = "next"
	cache := filepath.Join(t.TempDir(), "graph.cache")
	g, log := buildWith(t, k.dir, rem, cache)
	n := len(g.hashes)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=0 streamed=%d dropped=0", n, n))
	if c := lookup(t, g, fix); c.Mainline || c.Tree != "next" || c.Tag != "" || c.Upstream != "" {
		t.Errorf("next fix = %+v", c)
	}
	if c := lookup(t, g, pick); c.Tree != "next" || c.Upstream != "" {
		t.Errorf("next pick = %+v, want no upstream", c)
	}
	for h, want := range map[string]string{k.m["fix"]: "mainline", k.s["fix60"]: "stable"} {
		if c := lookup(t, g, h); c.Tree != want {
			t.Errorf("%s: tree %q, want %q", h, c.Tree, want)
		}
	}
	if got := fixedByOf(g, k.m["again"]); !slices.Equal(got, []string{fix}) {
		t.Errorf("FixedBy = %v, want %v", got, fix)
	}
	if got := copiesOf(g, pick); !slices.Equal(got, []string{k.m["again"], pick}) {
		t.Errorf("Copies = %v, want the mainline copy first", got)
	}
	if got := backportsOf(g, k.m["again"]); len(got) != 0 {
		t.Errorf("Backports = %v, want none: a next copy is no backport", got)
	}
	if got := followupsOf(g, k.m["again"], targets(t, g, "5.15", "6.0")); len(got) != 1 || got[0].Hash != fix || !slices.Equal(got[0].Series, []string{"5.15"}) {
		t.Errorf("Followups = %+v, want the next fix, lacking in 5.15", got)
	}

	before, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	_, log = buildWith(t, k.dir, rem, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=0 dropped=0", n, n))
	if after, err := os.Stat(cache); err != nil || !os.SameFile(before, after) {
		t.Errorf("an unchanged next rewrote the cache (%v)", err)
	}
	k.git("update-ref", "refs/remotes/next/master", k.m["tip"])
	g, log = buildWith(t, k.dir, rem, cache)
	wantLog(t, log, fmt.Sprintf("commits=%d cached=%d streamed=0 dropped=2", n-2, n))
	if _, ok := lookupOf(g, fix); ok || strings.Contains(log, "does not fit") {
		t.Errorf("rebuilt next: fix found %v, log %s", ok, log)
	}
	if g, log = buildWith(t, k.dir, remotes, cache); len(g.hashes) != n-2 || strings.Contains(log, "does not fit") {
		t.Errorf("without a next remote: %d commits, log %s", len(g.hashes), log)
	}
	if _, err := buildTree(ctx, k.dir, rem, Options{Next: "nonexistent"}, quiet); err == nil || err.Error() != "kernel tree "+k.dir+" has no nonexistent" {
		t.Errorf("an unresolvable next ref: %v, want it named alone", err)
	}
	if _, err := buildTree(ctx, k.dir, remotes, Options{Next: "refs/remotes/next/master"}, quiet); err == nil {
		t.Error("a next ref without a next remote built")
	}
}

// A vendor remote's branches are tips: their commits are in the graph under
// the branch's name, their picks of mainline commits, also through a stable
// backport, are backports, and InSeries tells whether the branch has a
// mainline commit, through its base, a pick or a revert, or a commit of its
// own.
func TestBuildVendor(t *testing.T) {
	k := newKernel(t)
	k.git("checkout", "-q", "-b", "u2", "v6.0")
	pick := k.commit("net: fix foo\n\ncommit " + k.m["fix"] + " upstream.\n(cherry picked from commit " + k.s["fix60"] + ")")
	chase := k.commit("arm: fix baz\n\n(cherry picked from commit " + k.s["untagged60"] + ")")
	own := k.commitAs("vendor@example.com", "uek: own change")
	revert := k.commit("Revert \"net: fix foo\"\n\nThis reverts commit " + pick + ".")
	// A revert of a stable backport the branch never took.
	k.commitAs("vendor@example.com", "uek: drop the bar fix\n\nThis reverts commit "+k.s["bar60"]+".")
	k.git("update-ref", "refs/remotes/uek/u2", "HEAD")
	k.git("symbolic-ref", "refs/remotes/uek/HEAD", "refs/remotes/uek/u2")
	k.git("checkout", "-q", "master")
	rem := remotes
	rem.Vendors = []string{"uek"}
	cache := filepath.Join(t.TempDir(), "graph.cache")
	g, _ := buildWith(t, k.dir, rem, cache)
	if !slices.Equal(g.layout.Trees(), []string{"mainline", "stable", "uek/u2"}) {
		t.Errorf("trees = %v", g.layout.Trees())
	}
	if got := g.layout.VendorBranches(); !slices.Equal(got, []string{"uek/u2"}) {
		t.Errorf("VendorBranches = %v", got)
	}
	for name, tt := range map[string]struct{ hash, tree, upstream string }{
		"pick":      {pick, "uek/u2", k.m["fix"]},
		"chase":     {chase, "uek/u2", k.m["late"]},
		"own":       {own, "uek/u2", ""},
		"revert":    {revert, "uek/u2", k.m["revert"]}, // by author and subject, as a stable revert
		"base":      {k.m["add"], "mainline", ""},
		"stable":    {k.s["fix60"], "stable", k.m["fix"]},
		"untagged":  {k.s["untagged60"], "stable", k.m["late"]},
		"elsewhere": {k.m["again"], "mainline", ""},
	} {
		if c := lookup(t, g, tt.hash); c.Tree != tt.tree || c.Upstream != tt.upstream {
			t.Errorf("%s: tree %q upstream %q, want %q %q", name, c.Tree, c.Upstream, tt.tree, tt.upstream)
		}
	}
	if got := backportsOf(g, k.m["late"]); !slices.Equal(got, []string{chase}) {
		t.Errorf("Backports(late) = %v, want the untagged vendor pick %s", got, chase)
	}
	if got := backportsOf(g, k.m["fix"]); !slices.Equal(got, []string{k.s["fix515"]}) {
		t.Errorf("Backports(fix) = %v, want %s alone: the vendor pick is reverted", got, k.s["fix515"])
	}
	in := func(c string) Presence { return Presence{Series: "uek/u2", Commit: c} }
	for name, tt := range map[string]struct {
		hash string
		want []Presence
	}{
		"base":            {k.m["add"], []Presence{in(k.m["add"]), {Series: "6.0", Tag: "v6.0", Commit: k.m["add"]}}},
		"reverted pick":   {k.m["fix"], []Presence{in(""), {Series: "6.0"}}},
		"stable backport": {k.s["fix515"], []Presence{in(""), {Series: "6.0"}}},
		"chased pick":     {k.m["late"], []Presence{in(chase), {Series: "6.0"}}},
		"after the base":  {k.m["again"], []Presence{in(""), {Series: "6.0"}}},
		"own":             {own, []Presence{in(own), {Series: "6.0"}}},
		// A revert of a vendor-only commit counts for nothing, as for stable.
		"revert": {revert, []Presence{in(""), {Series: "6.0"}}},
	} {
		if got := inSeriesOf(g, tt.hash, targets(t, g, "uek/u2", "6.0")); !slices.Equal(got, tt.want) {
			t.Errorf("InSeries(%s) = %v, want %v", name, got, tt.want)
		}
	}
	// Mainline reverted fix, so bar is the one follow-up; 6.0 has it.
	if got := followupsOf(g, k.m["add"], targets(t, g, "uek/u2", "6.0")); len(got) != 1 || got[0].Hash != k.m["bar"] || !slices.Equal(got[0].Series, []string{"uek/u2"}) {
		t.Errorf("Followups(add) = %+v, want bar lacking in uek/u2", got)
	}
	// The vendor revert of bar60 is no business of 6.0's.
	if got := backportsOf(g, k.m["bar"]); !slices.Equal(got, []string{k.s["bar60"]}) {
		t.Errorf("Backports(bar) = %v, want [%s]", got, k.s["bar60"])
	}
	if _, log := buildWith(t, k.dir, rem, cache); !strings.Contains(log, "streamed=0") {
		t.Errorf("vendor tips not cached:\n%s", log)
	}
}

// A branch that points at an annotated tag stands for the tagged commit,
// as git log takes it, so the commit is in the branch's tree and the cache
// fits the next build.
func TestBuildBranchAtTag(t *testing.T) {
	k := newKernel(t)
	k.git("checkout", "-q", "-b", "u2", "v6.0")
	own := k.commitAs("vendor@example.com", "uek: own change")
	k.git("tag", "-a", "-m", "uek release", "uek-1", own)
	k.git("update-ref", "refs/remotes/uek/u2", "uek-1")
	k.git("checkout", "-q", "master")
	rem := remotes
	rem.Vendors = []string{"uek"}
	cache := filepath.Join(t.TempDir(), "graph.cache")
	g, _ := buildWith(t, k.dir, rem, cache)
	if c := lookup(t, g, own); c.Tree != "uek/u2" {
		t.Errorf("tree %q, want uek/u2", c.Tree)
	}
	if _, log := buildWith(t, k.dir, rem, cache); !strings.Contains(log, "streamed=0") || strings.Contains(log, "does not fit") {
		t.Errorf("second build:\n%s", log)
	}
}
