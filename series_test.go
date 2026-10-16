package kgraph

import (
	"slices"
	"testing"

	"github.com/linux-analysis/kgraph/kernel"
)

// A series has a commit when the last of its commits that apply or undo
// the commit applies it: its base release with the commit's earliest copy,
// a released backport of a copy, a revert of one of these, or a revert of
// that revert; not an untagged backport. A stable commit stands for its
// upstream.
func TestInSeries(t *testing.T) {
	k := newKernel(t)
	g := build(t, k.dir, remotes)
	series := targets(t, g, "6.0", "5.15", "v6.1", "7.0")
	none := func(s string) Presence { return Presence{Series: s} }
	// 6.0 reverted the backport, and mainline the fix before 7.0.
	fixIn := []Presence{none("6.0"), {Series: "5.15", Tag: "v5.15.1", Commit: k.s["fix515"]},
		{Series: "6.1", Tag: "v6.1", Commit: k.m["fix"]}, none("7.0")}
	for name, want := range map[string][]Presence{
		k.m["fix"]:    fixIn,
		k.s["fix515"]: fixIn, // a stable commit stands for its upstream
		k.m["add"]:    {{Series: "6.0", Tag: "v6.0", Commit: k.m["add"]}, none("5.15"), {Series: "6.1", Tag: "v6.1", Commit: k.m["add"]}, {Series: "7.0", Tag: "v7.0", Commit: k.m["add"]}},
		// The backport of a twin is a backport of both copies.
		k.m["twin2"]: {{Series: "6.0", Tag: "v6.0.2", Commit: k.s["qux60"]}, none("5.15"), none("6.1"), {Series: "7.0", Tag: "v7.0", Commit: k.m["twin1"]}},
		// An untagged backport and an older namesake are no backports.
		k.m["late"]: {none("6.0"), none("5.15"), none("6.1"), {Series: "7.0", Tag: "v7.0", Commit: k.m["late"]}},
		// 5.15 reverted the backport by naming its upstream.
		k.m["once"]: {none("6.0"), none("5.15"), none("6.1"), {Series: "7.0", Tag: "v7.0", Commit: k.m["once"]}},
		// 5.15 reverted the backport and then the revert.
		k.m["again"]: {none("6.0"), {Series: "5.15", Tag: "v5.15.3", Commit: k.s["redo515"]}, none("6.1"), {Series: "7.0", Tag: "v7.0", Commit: k.m["again"]}},
		k.other:      {none("6.0"), none("5.15"), none("6.1"), none("7.0")},
	} {
		if got := inSeriesOf(g, name, series); !slices.Equal(got, want) {
			t.Errorf("InSeries(%s) = %v, want %v", name, got, want)
		}
	}
	if ts, err := g.layout.Targets([]string{"6.0", "bogus"}); err == nil {
		t.Errorf("Targets(bogus) = %v, want an error", ts)
	}
}

// A series that picked both copies of a patch and reverted the second pick
// as a duplicate still has the first; mainline, which merged the copies
// into one change, lacks the patch once it reverts either.
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
	want := []Presence{{Series: "5.0", Tag: "v5.0.1", Commit: b1}, {Series: "5.1", Tag: "v5.1", Commit: x}, {Series: "5.2"}}
	if got := inSeriesOf(g, x2, targets(t, g, "5.0", "5.1", "5.2")); !slices.Equal(got, want) {
		t.Errorf("InSeries = %v, want %v", got, want)
	}
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
	want := []Presence{{Series: "5.0", Tag: "v5.0.2", Commit: redo}}
	if got := inSeriesOf(g, x, targets(t, g, "5.0")); !slices.Equal(got, want) {
		t.Errorf("InSeries = %v, want %v", got, want)
	}
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
	stable := r.commit("serial: Revert \"serial: drop foo\"\n\ncommit " + rev + " upstream.")
	r.git("tag", "v5.1.1")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	ts := targets(t, g, "5.1")
	want := []Presence{{Series: "5.1"}}
	if got := inSeriesOf(g, x, ts); !slices.Equal(got, want) {
		t.Errorf("InSeries(x) = %v, want %v: the backport reverts it", got, want)
	}
	want = []Presence{{Series: "5.1", Tag: "v5.1.1", Commit: stable}}
	if got := inSeriesOf(g, rev, ts); !slices.Equal(got, want) {
		t.Errorf("InSeries(revert) = %v, want %v", got, want)
	}
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
	want := []Presence{{Series: "5.1"}}
	if got := inSeriesOf(g, x, targets(t, g, "5.1")); !slices.Equal(got, want) {
		t.Errorf("InSeries = %v, want %v", got, want)
	}
	if got := backportsOf(g, x); got != nil {
		t.Errorf("Backports = %v, want none", got)
	}
}
