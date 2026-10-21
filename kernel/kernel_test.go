package kernel

import (
	"bytes"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var ctx = context.Background()

// fixture is a throwaway repository with three commits: c1 and c2 on
// master (recorded as mainline), c3 on a side branch (stable-only).
type fixture struct {
	dir        string
	c1, c2, c3 string
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Author", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Carol Committer", "GIT_COMMITTER_EMAIL=carol@example.com",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700003600 +0000",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "master")
	write(t, filepath.Join(dir, "a.c"), "int a;\nint b;\n")
	gitRun(t, dir, "add", "a.c")
	gitRun(t, dir, "commit", "-q", "-m", "Initial commit")
	c1 := gitRun(t, dir, "rev-parse", "HEAD")

	write(t, filepath.Join(dir, "a.c"), "int a;\n  int b;\nint c;\n")
	write(t, filepath.Join(dir, "bin.dat"), "\x00\x01\x02\xff")
	gitRun(t, dir, "add", "a.c", "bin.dat")
	gitRun(t, dir, "commit", "-q", "-m", "net: fix leak in foo\n\nThe body explains it.\n\nFixes: "+c1[:12]+" (\"Initial commit\")\nLink: https://lore.kernel.org/r/123\nReported-by: syzbot+0123456789abcdef0123@syzkaller.appspotmail.com")
	c2 := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "refs/remotes/mainline/master", c2)

	gitRun(t, dir, "checkout", "-q", "-b", "stable-only")
	write(t, filepath.Join(dir, "s.c"), "stable only\n")
	gitRun(t, dir, "add", "s.c")
	gitRun(t, dir, "commit", "-q", "-m", "stable: backport")
	c3 := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "master")
	return &fixture{dir: dir, c1: c1, c2: c2, c3: c3}
}

func TestPresentCommits(t *testing.T) {
	fx := newFixture(t)
	missing := "ffffffffffffffffffffffffffffffffffffffff"
	blob := gitRun(t, fx.dir, "rev-parse", fx.c1+":a.c")
	got, err := presentCommits(ctx, fx.dir, []string{fx.c3, fx.c1, fx.c2[:12], missing, blob, "zzz", fx.c1})
	if err != nil {
		t.Fatal(err)
	}
	if want := slices.Sorted(slices.Values([]string{fx.c1, fx.c2, fx.c3})); !slices.Equal(got, want) {
		t.Errorf("presentCommits = %v, want %v", got, want)
	}
	if got, err := presentCommits(ctx, fx.dir, nil); err != nil || len(got) != 0 {
		t.Errorf("empty input: %v %v", got, err)
	}
}

func TestSubjects(t *testing.T) {
	fx := newFixture(t)
	gitRun(t, fx.dir, "commit", "-q", "--allow-empty", "-m", "net: a subject\nthat folds\n\nand a body")
	folded := gitRun(t, fx.dir, "rev-parse", "HEAD")
	got, err := Subjects(ctx, fx.dir, []string{fx.c2, fx.c1, "ffffffffffffffffffffffffffffffffffffffff", fx.c2, folded})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{fx.c1: "Initial commit", fx.c2: "net: fix leak in foo", folded: "net: a subject that folds"}
	if !maps.Equal(got, want) {
		t.Errorf("Subjects = %q, want %q", got, want)
	}
	if s, err := Subjects(ctx, fx.dir, nil); err != nil || len(s) != 0 {
		t.Errorf("empty: %v %v", s, err)
	}
}

// Diffs ignores whitespace-only changes and skips missing commits.
func TestDiffs(t *testing.T) {
	fx := newFixture(t)
	diffs, err := Diffs(ctx, fx.dir, []string{fx.c1, fx.c2, strings.Repeat("0", 40)})
	if err != nil || len(diffs) != 2 || !strings.Contains(diffs[fx.c1], "+int a;") {
		t.Fatalf("Diffs = %q, %v", diffs, err)
	}
	if d := diffs[fx.c2]; !strings.Contains(d, "+int c;") || strings.Contains(d, "-int b;") || !strings.Contains(d, "diff --git a/a.c b/a.c") {
		t.Errorf("diff should ignore whitespace-only change: %q", d)
	}
}

func TestFindRemotes(t *testing.T) {
	fx := newFixture(t)
	if rem, err := FindRemotes(ctx, fx.dir); err != nil || rem.Mainline != "" || rem.Stable != "" {
		t.Errorf("no remotes: %+v %v", rem, err)
	}
	// Resolution by URL only: no fetch happens here, so the real kernel.org
	// URLs are safe to register.
	gitRun(t, fx.dir, "remote", "add", "vanilla", "https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git")
	if rem, err := FindRemotes(ctx, fx.dir); err != nil || rem.Mainline != "vanilla" || rem.Stable != "" || rem.Next != "" || rem.Vendors != nil {
		t.Errorf("mainline only: %+v %v", rem, err)
	}
	gitRun(t, fx.dir, "remote", "add", "stable", "https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/")
	rem, err := FindRemotes(ctx, fx.dir)
	if err != nil || rem.Mainline != "vanilla" || rem.Stable != "stable" || rem.MainlineRef() != "refs/remotes/vanilla/master" {
		t.Errorf("resolve by url: %+v %v", rem, err)
	}
	gitRun(t, fx.dir, "remote", "remove", "vanilla")
	gitRun(t, fx.dir, "remote", "remove", "stable")

	// linux-next by URL only: a remote merely named next is not it.
	gitRun(t, fx.dir, "remote", "add", "next", filepath.Join(t.TempDir(), "next.git"))
	if rem, err := FindRemotes(ctx, fx.dir); err != nil || rem.Next != "" {
		t.Errorf("next by name: %+v %v", rem, err)
	}
	gitRun(t, fx.dir, "remote", "set-url", "next", "https://git.kernel.org/pub/scm/linux/kernel/git/next/linux-next.git")
	if rem, err := FindRemotes(ctx, fx.dir); err != nil || rem.Next != "next" || rem.NextRef() != "refs/remotes/next/master" {
		t.Errorf("next by url: %+v %v", rem, err)
	}
	gitRun(t, fx.dir, "remote", "remove", "next")

	// Vendor trees by URL, in name order; a maintainer tree is none.
	for name, url := range map[string]string{
		"a-uek":     "https://github.com/oracle/linux-uek.git",
		"b-suse":    "git@github.com:SUSE/kernel",
		"c-c9s":     "https://gitlab.com/redhat/centos-stream/src/kernel/centos-stream-9.git",
		"d-ubuntu":  "https://git.launchpad.net/~ubuntu-kernel/ubuntu/+source/linux/+git/noble",
		"e-android": "https://android.googlesource.com/kernel/common",
		"axboe":     "git://git.kernel.org/pub/scm/linux/kernel/git/axboe/linux.git",
	} {
		gitRun(t, fx.dir, "remote", "add", name, url)
	}
	if rem, err := FindRemotes(ctx, fx.dir); err != nil || !slices.Equal(rem.Vendors, []string{"a-uek", "b-suse", "c-c9s", "d-ubuntu", "e-android"}) {
		t.Errorf("vendors by url: %+v %v", rem, err)
	}
	for _, name := range []string{"a-uek", "b-suse", "c-c9s", "d-ubuntu", "e-android", "axboe"} {
		gitRun(t, fx.dir, "remote", "remove", name)
	}

	// Other common spellings: scp-style SSH and the older linux-stable name.
	for _, tc := range []struct{ mainline, stable string }{
		{"git@github.com:torvalds/linux.git", "https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux-stable.git"},
		{"ssh://git@github.com/torvalds/linux", "git@gitolite.kernel.org:pub/scm/linux/kernel/git/stable/linux"},
	} {
		gitRun(t, fx.dir, "remote", "add", "up", tc.mainline)
		gitRun(t, fx.dir, "remote", "add", "st", tc.stable)
		rem, err := FindRemotes(ctx, fx.dir)
		if err != nil || rem.Mainline != "up" || rem.Stable != "st" {
			t.Errorf("resolve %s, %s: %+v %v", tc.mainline, tc.stable, rem, err)
		}
		gitRun(t, fx.dir, "remote", "remove", "up")
		gitRun(t, fx.dir, "remote", "remove", "st")
	}

	// Resolution by name.
	gitRun(t, fx.dir, "remote", "add", "mainline", filepath.Join(t.TempDir(), "upstream.git"))
	gitRun(t, fx.dir, "remote", "add", "stable", filepath.Join(t.TempDir(), "upstream.git"))
	rem, err = FindRemotes(ctx, fx.dir)
	if err != nil || rem.Mainline != "mainline" || rem.Stable != "stable" {
		t.Errorf("resolve by name: %+v %v", rem, err)
	}
}
