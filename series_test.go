package kgraph

import (
	"slices"
	"testing"

	"github.com/linux-analysis/kgraph/kernel"
)

// A series that picked both copies of a patch and reverted the second pick
// as a duplicate keeps the first backport.
func TestDuplicateBackportReverted(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("drm: fix")
	at := r.clock
	r.git("checkout", "-q", "-b", "side", "v5.0")
	r.commit("drm: prepare")
	x2 := r.commitAt(at, "alice@example.com", "drm: fix")
	r.git("checkout", "-q", "master")
	r.git("merge", "-q", "--no-ff", "-m", "Merge side", "side")
	r.git("tag", "v5.1")
	r.commitAs("bob@example.com", "Revert \"drm: fix\"\n\nThis reverts commit "+x2+".")
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	b1 := r.commit("drm: fix\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	b2 := r.commit("drm: fix\n\ncommit " + x2 + " upstream.")
	r.git("tag", "v5.0.2")
	r.commit("Revert \"drm: fix\"\n\nThis reverts commit " + b2 + ".")
	r.git("tag", "v5.0.3")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	if got := backportsOf(g, x); !slices.Equal(got, []string{b1}) {
		t.Errorf("Backports = %v, want [%s]", got, b1)
	}
}

// A revert and the reapplication that a series imported in one second come
// in the order of the branch, though the revert names another series'
// backport.
func TestRevertReappliedInOneSecond(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	r.commit("net: tidy")
	r.git("tag", "v5.1")
	x := r.commit("net: fix foo")
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "linux-5.1.y", "v5.1")
	b51 := r.commit("net: fix foo\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.1.1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	r.commit("net: fix foo\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	at := r.clock + 60
	r.commitAt(at, "alice@example.com", "Revert \"net: fix foo\"\n\nThis reverts commit "+b51+".")
	redo := r.commitAt(at, "alice@example.com", "net: fix foo\n\ncommit "+x+" upstream.")
	r.git("tag", "v5.0.2")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	if got := backportsOf(g, x); !slices.Equal(got, []string{redo, b51}) {
		t.Errorf("Backports = %v, want [%s %s]", got, redo, b51)
	}
}

// A backport of a mainline revert undoes the reverted commit though its
// message names none, by its upstream line alone.
func TestRevertBackportWithoutRevertLine(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("serial: drop foo")
	r.git("tag", "v5.1")
	rev := r.commitAs("bob@example.com", "serial: Revert \"serial: drop foo\"\n\nThis reverts commit "+x+".")
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	r.commit("serial: drop foo\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	r.commit("serial: Revert \"serial: drop foo\"\n\ncommit " + rev + " upstream.")
	r.git("tag", "v5.0.2")
	r.git("checkout", "-q", "-b", "linux-5.1.y", "v5.1")
	r.commit("serial: Revert \"serial: drop foo\"\n\ncommit " + rev + " upstream.")
	r.git("tag", "v5.1.1")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	if got := backportsOf(g, x); got != nil {
		t.Errorf("Backports(x) = %v, want none: 5.0 took the revert", got)
	}
}

// A series' own revert of its backport reverts that backport when it
// names the upstream, as "This reverts upstream commit X from stable";
// the mainline commit is no one's to revert.
func TestOwnRevertOfBackport(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	r.commit("net: tidy")
	r.git("tag", "v5.1")
	x := r.commit("random: use static branch")
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "linux-5.1.y", "v5.1")
	b51 := r.commit("random: use static branch\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.1.1")
	byID := r.commit("Revert \"random: use static branch\"\n\nThis reverts upstream commit " + x + "\nfrom stable.")
	r.git("tag", "v5.1.2")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	if got := revertsOf(g, byID); got != b51 {
		t.Errorf("Reverts = %q, want %s", got, b51)
	}
	if revertedOf(g, x) {
		t.Error("the mainline commit is reverted")
	}
	if got := backportsOf(g, x); got != nil {
		t.Errorf("Backports = %v, want none", got)
	}
}
