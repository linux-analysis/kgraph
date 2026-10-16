package kernel

import (
	"maps"
	"path/filepath"
	"testing"
)

func TestAdapted(t *testing.T) {
	f := newFixture(t)
	commit := func(content, msg string) string {
		t.Helper()
		write(t, filepath.Join(f.dir, "x.c"), content)
		gitRun(t, f.dir, "add", "x.c")
		gitRun(t, f.dir, "commit", "-q", "-m", msg)
		return gitRun(t, f.dir, "rev-parse", "HEAD")
	}
	gitRun(t, f.dir, "checkout", "-q", "-b", "upstream", f.c1)
	commit("a\nb\nc\n", "x: add")
	up := commit("a\nb\nfix();\nc\n", "x: fix")
	// The stable branch has other lines around the fix.
	gitRun(t, f.dir, "checkout", "-q", "-b", "linux-6.1.y", f.c1)
	commit("a\nB\nc\n", "x: add")
	clean := commit("a\nB\n\tfix();\nc\n", "x: fix")
	adapted := commit("a\nB\n\tfix(1);\nc\n", "x: fix again")
	missing := commit("a\nB\n\tfix(3);\nc\n", "x: fix")

	got, err := Adapted(ctx, f.dir, map[string]string{clean: up, adapted: up,
		missing: "0123456789abcdef0123456789abcdef01234567"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{clean: false, adapted: true}; !maps.Equal(got, want) {
		t.Errorf("Adapted = %v, want %v", got, want)
	}
}

// A backport to a file that moved since changes the same lines as its
// upstream commit, by withoutPaths; changedLines keeps the paths apart,
// and the ---/+++ lines of a diff are no changed lines.
func TestChangedLinesOfMovedFile(t *testing.T) {
	diff := func(path, body string) string {
		return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,2 +1,2 @@\n" + body
	}
	up := changedLines(diff("arch/arm64/kernel/pi/patch-scs.c", " a\n-b\n+c;\n"))
	moved := changedLines(diff("arch/arm64/kernel/patch-scs.c", " a\n-b\n+ c ;\n"))
	if moved == up {
		t.Error("changedLines of two files are the same")
	}
	if got, want := withoutPaths(moved), withoutPaths(up); got != want || want != "+c;\n-b" {
		t.Errorf("withoutPaths of the moved file = %q, want %q, +c;\\n-b", got, want)
	}
	if withoutPaths(changedLines(diff("arch/arm64/kernel/patch-scs.c", " a\n-b\n+d;\n"))) == withoutPaths(up) {
		t.Error("other lines changed the same")
	}
}
