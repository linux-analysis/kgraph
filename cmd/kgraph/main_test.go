package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700003600 +0000",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func capture(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return captureIn(t, "", args...)
}

// captureIn runs kgraph with stdin as its standard input.
func captureIn(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String() + errOut.String(), code
}

func TestVersion(t *testing.T) {
	if out, code := capture(t, "--version"); code != 0 || out != "kgraph dev\n" {
		t.Errorf("--version = %q, %d", out, code)
	}
}

// release names the release of each tag or version, and fails on one that
// is none; a directory that is no repository is said to be so.
func TestRelease(t *testing.T) {
	out, code := capture(t, "release", "v6.12-rc3", "2.6.32.71")
	if code != 0 || !strings.Contains(out, "v6.12-rc3\tv6.12\t6012000") || !strings.Contains(out, "2.6.32.71\tv2.6.32\t2006032") {
		t.Errorf("release = %q, %d", out, code)
	}
	out, code = capture(t, "release", "v6.12", "nonsense", "v2.4.0")
	if code != 1 || !strings.Contains(out, "v6.12\tv6.12\t6012000") || !strings.Contains(out, "nonsense\tnot a release") ||
		!strings.Contains(out, "v2.4.0\tnot a release") {
		t.Errorf("release of no release = %q, %d, want 1", out, code)
	}
	dir := t.TempDir()
	if out, code := capture(t, "--tree", dir, "lookup", "abc"); code != 1 || !strings.Contains(out, dir+" is not a git repository") {
		t.Errorf("lookup in a directory that is no repository = %q, %d, want 1", out, code)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, "--tree", file, "lookup", "abc"); code != 1 || !strings.Contains(out, file+" is not a git repository") {
		t.Errorf("lookup in a file = %q, %d, want 1", out, code)
	}
	if _, code := capture(t, "--tree", t.TempDir(), "nonsense"); code != 2 {
		t.Errorf("unknown command exited %d, want 2", code)
	}
}

// A tree without remotes is read from HEAD; the first tag that contains a
// commit gives its release, and a Fixes: tag links the commits. The tree
// is the current directory unless --tree says otherwise, and its cache
// lives under the user cache directory unless --cache does.
func TestLookupAndFixes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	caches := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", caches)
	gitRun(t, dir, "init", "-q", "-b", "master")
	write := func(content, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.c"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "add", "a.c")
		gitRun(t, dir, "commit", "-q", "-m", msg)
		return gitRun(t, dir, "rev-parse", "HEAD")
	}
	intro := write("int a;\n", "a: add")
	gitRun(t, dir, "tag", "v6.1")
	fix := write("int a = 1;\n", "a: fix\n\nFixes: "+intro[:12]+" (\"a: add\")\n")
	gitRun(t, dir, "tag", "v6.2-rc1")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	side := write("int b;\n", "b: on a branch the graph does not read")
	gitRun(t, dir, "checkout", "-q", "master")

	t.Chdir(dir)
	out, code := capture(t, "tags")
	if code != 0 || strings.TrimSpace(out) != "v6.1" {
		t.Errorf("tags = %q, %d", out, code)
	}
	out, code = capture(t, "lookup", fix)
	if code != 0 || !strings.Contains(out, fix+"\tmainline") {
		t.Errorf("lookup in the current directory = %q, %d", out, code)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if out, code := capture(t, "lookup", fix); code != 0 || !strings.Contains(out, fix+"\tmainline") {
		t.Errorf("lookup in a subdirectory = %q, %d", out, code)
	}
	t.Chdir(dir)
	if files, _ := filepath.Glob(filepath.Join(caches, "kgraph", "*.cache")); len(files) != 1 {
		t.Errorf("cache files under the user cache directory: %v, want one", files)
	}
	cache := filepath.Join(t.TempDir(), "graph.cache")
	out, code = capture(t, "--tree", dir, "--cache", cache, "lookup", fix, strings.Repeat("0", 40))
	if code != 0 || !strings.Contains(out, fix+"\tmainline\ttag v6.2-rc1\trelease v6.2") || !strings.Contains(out, "names no commit") {
		t.Errorf("lookup = %q, %d", out, code)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("cache not written: %v", err)
	}
	out, code = capture(t, "--tree", dir, "--cache", cache, "--json", "fixes", fix)
	if code != 0 || !strings.Contains(out, `"hash": "`+intro+`"`) || !strings.Contains(out, `"release": "v6.1"`) ||
		!strings.Contains(out, `"tree": "mainline"`) || strings.Contains(out, `"mainline"`+":") {
		t.Errorf("fixes = %q, %d", out, code)
	}
	out, code = capture(t, "--tree", dir, "fixed-by", intro[:12])
	if code != 0 || !strings.Contains(out, fix) {
		t.Errorf("fixed-by = %q, %d", out, code)
	}
	out, code = capture(t, "--tree", dir, "lookup", strings.ToUpper(fix[:12]), "zz")
	if code != 0 || !strings.Contains(out, fix+"\tmainline") || !strings.Contains(out, "zz\tis not a commit id") {
		t.Errorf("lookup by prefix = %q, %d", out, code)
	}
	out, code = captureIn(t, fix[:12]+"\n"+intro+" zz\n", "--tree", dir, "lookup", "-")
	if code != 0 || strings.Count(out, fix+"\t") != 1 || !strings.Contains(out, fix+"\tmainline") || !strings.Contains(out, intro+"\tmainline") ||
		!strings.Contains(out, "zz\tis not a commit id") {
		t.Errorf("lookup from stdin = %q, %d", out, code)
	}
	if _, code := captureIn(t, "", "--tree", dir, "lookup", "-"); code != 2 {
		t.Errorf("lookup of an empty stdin exited %d, want 2", code)
	}
	out, code = captureIn(t, fix+"\n"+intro[:12]+" zz\n", "--tree", dir, "fixes", "-")
	if code != 0 || !strings.Contains(out, fix+"\t"+intro+"\tmainline") || strings.Contains(out, "\n"+intro+"\t") ||
		!strings.Contains(out, "zz\tis not a commit id") {
		t.Errorf("fixes from stdin = %q, %d", out, code)
	}
	if out, code := capture(t, "--tree", dir, "backports", strings.Repeat("0", 40)); code != 0 || !strings.Contains(out, "names no commit") {
		t.Errorf("backports of a missing commit = %q, %d", out, code)
	}
	out, code = capture(t, "--tree", dir, "reverts", fix, "zz")
	if code != 0 || !strings.HasPrefix(out, fix+"\treverted false\nzz\tis not a commit id\n") {
		t.Errorf("reverts = %q, %d", out, code)
	}
	// A miss says why: no commit id, a commit the graph does not read, no
	// commit at all; JSON names it too, and a reverts row of a miss tells
	// no reverted.
	out, code = capture(t, "--tree", dir, "lookup", "v6.1", side, strings.Repeat("0", 40))
	if code != 0 || !strings.Contains(out, "v6.1\tis not a commit id\n") || !strings.Contains(out, side+"\tis not in the graph: no branch or tag it reads reaches it\n") ||
		!strings.Contains(out, strings.Repeat("0", 40)+"\tnames no commit\n") {
		t.Errorf("lookup of misses = %q, %d", out, code)
	}
	out, code = capture(t, "--tree", dir, "--json", "reverts", "v6.1", side, fix)
	if code != 0 || !strings.Contains(out, `"missing": "not-an-id"`) || !strings.Contains(out, `"missing": "not-in-graph"`) || strings.Count(out, `"reverted"`) != 1 {
		t.Errorf("reverts of misses as JSON = %q, %d", out, code)
	}
	// - reads standard input in its place among other hashes, and flags may
	// follow the command.
	out, code = captureIn(t, fix+"\n", "--tree", dir, "lookup", intro, "-")
	if code != 0 || !strings.Contains(out, intro+"\tmainline") || !strings.Contains(out, fix+"\tmainline") {
		t.Errorf("lookup of a hash and - = %q, %d", out, code)
	}
	out, code = capture(t, "--tree", dir, "lookup", "--json", fix)
	if code != 0 || !strings.Contains(out, `"hash": "`+fix+`"`) {
		t.Errorf("lookup --json after the command = %q, %d", out, code)
	}
	if _, code := capture(t, "--tree", dir, "lookup", "--bogus", fix); code != 2 {
		t.Errorf("an unknown flag after the command exited %d, want 2", code)
	}
	if out, _ := capture(t, "--tree", dir, "lookup", "--", "--json"); !strings.Contains(out, "--json\tis not a commit id") {
		t.Errorf("an argument after -- = %q", out)
	}
	// A --cache that is a directory is a usage error.
	if out, code := capture(t, "--tree", dir, "--cache", t.TempDir(), "lookup", fix); code != 2 || !strings.Contains(out, "is a directory") {
		t.Errorf("a directory as --cache = %q, %d, want 2", out, code)
	}
}
