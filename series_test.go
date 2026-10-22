package kgraph

import (
	"reflect"
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

// The follow-ups of a commit are the mainline commits whose Fixes: tags
// name its patch, listed with the series that have the patch but lack
// them: not a follow-up some commit reverts, nor one every such series
// has; a stable commit stands for its patch, and one no series has has
// none.
func TestFollowups(t *testing.T) {
	k := newKernel(t)
	g := build(t, k.dir, remotes)
	series := targets(t, g, "6.0", "5.15", "v6.1")
	for name, want := range map[string][]followup{
		// The fix is in 5.15 and 6.1 (6.0 reverted its backport); arm: fix
		// baz names it and has only an untagged backport.
		k.m["fix"]: {{Hash: k.m["late"], Series: []string{"5.15", "6.1"}}},
		// net: fix foo was reverted; mm: fix bar is wherever net: add foo is.
		k.m["add"]:    nil,
		k.m["init"]:   {{Hash: k.m["bar"], Series: []string{"5.15"}}},
		k.s["fix515"]: {{Hash: k.m["late"], Series: []string{"5.15", "6.1"}}},
		k.m["twin1"]:  nil,
		k.m["tip"]:    nil,
	} {
		if got := followupsOf(g, name, series); !reflect.DeepEqual(got, want) {
			t.Errorf("Followups(%s) = %v, want %v", name, got, want)
		}
	}
}

// A fix one series reverted is still a follow-up for another series that
// has the commit and never had the fix; mainline reverting it would make
// it none. The series that reverted the backport has no backport.
func TestFollowupsRevertedElsewhere(t *testing.T) {
	r := newRepo(t)
	x := r.commit("net: add foo")
	r.git("tag", "v5.0")
	r.commit("net: tidy")
	r.git("tag", "v5.1")
	fix := r.commit("net: fix foo\n\nFixes: " + x[:12])
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	backport := r.commit("net: fix foo\n\ncommit " + fix + " upstream.")
	r.git("tag", "v5.0.1")
	r.commit("Revert \"net: fix foo\"\n\nThis reverts commit " + fix + ".")
	r.git("tag", "v5.0.2")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	got := followupsOf(g, x, targets(t, g, "5.0", "5.1"))
	if want := []followup{{fix, []string{"5.1"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Followups = %v, want %v", got, want)
	}
	if got := backportsOf(g, fix); got != nil {
		t.Errorf("Backports = %v, want none: 5.0 reverted %s", got, backport)
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
// message names none: a stable one by its upstream line alone, a vendor
// one by SUSE's subject alone.
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
	r.git("checkout", "-q", "-b", "sl", "v5.1")
	pick := r.commitAs("suse@example.com", "serial: Revert \"serial: drop foo\" (bsc#1).\n\nsuse-commit: 0123")
	r.git("update-ref", "refs/remotes/suse/sl", "HEAD")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{Vendors: []string{"suse"}})
	if c := lookup(t, g, pick); c.Upstream != rev {
		t.Fatalf("upstream of the pick = %q, want %s", c.Upstream, rev)
	}
	ts := targets(t, g, "5.1", "suse/sl")
	want := []Presence{{Series: "5.1"}, {Series: "suse/sl"}}
	if got := inSeriesOf(g, x, ts); !slices.Equal(got, want) {
		t.Errorf("InSeries(x) = %v, want %v: the backports revert it", got, want)
	}
	want = []Presence{{Series: "5.1", Tag: "v5.1.1", Commit: stable}, {Series: "suse/sl", Commit: pick}}
	if got := inSeriesOf(g, rev, ts); !slices.Equal(got, want) {
		t.Errorf("InSeries(revert) = %v, want %v", got, want)
	}
	if got := backportsOf(g, x); got != nil {
		t.Errorf("Backports(x) = %v, want none: 5.0 took the revert", got)
	}
}

// A series' own revert of its backport reverts that backport, whether it
// names the upstream, as "This reverts upstream commit X from stable", or
// only quotes its subject; the mainline commit is no one's to revert.
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
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	b50 := r.commit("random: use static branch\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	bySubject := r.commit("Revert \"random: use static branch\"\n\nIt broke 5.0.")
	r.git("tag", "v5.0.2")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	for revert, want := range map[string]string{byID: b51, bySubject: b50} {
		if got := revertsOf(g, revert); got != want {
			t.Errorf("Reverts(%s) = %q, want %s", revert, got, want)
		}
	}
	if revertedOf(g, x) {
		t.Error("the mainline commit is reverted")
	}
	want := []Presence{{Series: "5.0"}, {Series: "5.1"}}
	if got := inSeriesOf(g, x, targets(t, g, "5.0", "5.1")); !slices.Equal(got, want) {
		t.Errorf("InSeries = %v, want %v", got, want)
	}
	if got := backportsOf(g, x); got != nil {
		t.Errorf("Backports = %v, want none", got)
	}
}

// A vendor merge quotes the Fixes: tags of the commits it brings, and is
// no fix itself; a fix only a vendor branch has is no follow-up, as no
// other series or branch can take it.
func TestVendorFixesNoFollowups(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("net: add foo")
	r.git("tag", "v5.1")
	fix := r.commit("net: fix foo\n\nFixes: " + x[:12] + " (\"net: add foo\")")
	r.git("tag", "v5.2-rc1")
	r.git("checkout", "-q", "-b", "c9s", "v5.1")
	own := r.commitAs("rh@example.com", "net: fix foo for RHEL\n\nUpstream Status: RHEL-Only\n\nFixes: "+x[:12])
	r.git("checkout", "-q", "-b", "mr", "v5.1")
	r.commitAs("rh@example.com", "wifi: tidy")
	r.git("checkout", "-q", "c9s")
	r.clock += 3600
	r.git("merge", "-q", "--no-ff", "-m", "Merge: net: fix foo\n\nFixes: "+x[:12]+" (\"net: add foo\")", "mr")
	r.git("update-ref", "refs/remotes/centos/main", "HEAD")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{Vendors: []string{"centos"}})
	if got := fixedByOf(g, x); !slices.Equal(got, []string{fix, own}) {
		t.Errorf("FixedBy = %v, want [%s %s]", got, fix, own)
	}
	want := []followup{{fix, []string{"centos/main", "5.1"}}}
	if got := followupsOf(g, x, targets(t, g, "centos/main", "5.1")); !reflect.DeepEqual(got, want) {
		t.Errorf("Followups = %v, want %v", got, want)
	}
}

// A vendor branch's revert of its own pick reverts that pick, though the
// branch has the mainline commit too: SUSE's branches merge the history of
// older ones, whose picks and reverts the merge leaves out of the code.
func TestOwnRevertOfOldPick(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("nvme: allow 64-bit results")
	r.git("tag", "v5.1")
	r.git("checkout", "-q", "-b", "old", "v5.0")
	pick := r.commitAs("suse@example.com", "nvme: allow 64-bit results (bnc#1 5.0.8).")
	revert := r.commitAs("suse@example.com", "Revert \"nvme: allow 64-bit results\" (bnc#1 5.0.9).")
	r.git("checkout", "-q", "-b", "sl", "v5.1")
	r.clock += 3600
	r.git("merge", "-q", "-s", "ours", "-m", "Merge the old history", "old")
	r.git("update-ref", "refs/remotes/suse/sl", "HEAD")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{Vendors: []string{"suse"}})
	if got := revertsOf(g, revert); got != pick {
		t.Errorf("Reverts = %q, want the pick %s", got, pick)
	}
	if got := inSeriesOf(g, x, targets(t, g, "suse/sl")); got[0].Commit != x {
		t.Errorf("InSeries = %v, want it through %s", got, x)
	}
}

// A branch that picked a patch and merged a stable release with its
// backport has one change: reverting the pick after the merge takes the
// patch out, reverting it before the merge leaves the backport.
func TestRevertPickAroundMerge(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("checkpatch: add a tag")
	r.git("tag", "v5.1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	backport := r.commit("checkpatch: add a tag\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	for _, after := range []bool{true, false} {
		branch := map[bool]string{true: "after", false: "before"}[after]
		r.git("checkout", "-q", "-b", branch, "v5.0")
		pick := r.commit("UPSTREAM: checkpatch: add a tag\n\n(cherry picked from commit " + x + ")")
		revert := "Revert \"UPSTREAM: checkpatch: add a tag\"\n\nThis reverts commit " + pick + "."
		if !after {
			r.commit(revert)
		}
		r.clock += 3600
		r.git("merge", "-q", "--no-ff", "-m", "Merge 5.0.1", "v5.0.1")
		if after {
			r.commit(revert)
		}
		r.git("update-ref", "refs/remotes/android/"+branch, "HEAD")
	}
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{Vendors: []string{"android"}})
	want := []Presence{{Series: "android/after"}, {Series: "android/before", Commit: backport}}
	if got := inSeriesOf(g, x, targets(t, g, "android/after", "android/before")); !slices.Equal(got, want) {
		t.Errorf("InSeries = %v, want %v", got, want)
	}
}

// A revert by the quoted subject reverts the commit before it, though the
// commit applied again later has the subject too.
func TestRevertSubjectOfLaterCommit(t *testing.T) {
	r := newRepo(t)
	x := r.commit("wifi: bump the FW API")
	revert := r.commitAs("bob@example.com", "Revert \"wifi: bump the FW API\"\n\nIt broke.")
	r.commit("wifi: bump the FW API")
	g := build(t, r.dir, kernel.Remotes{})
	if got := revertsOf(g, revert); got != x {
		t.Errorf("Reverts = %q, want %s", got, x)
	}
}

// A vendor branch's own revert of its backport, by id, gets no upstream
// from the subject of a mainline revert of the patch.
func TestVendorRevertOfBackportHasNoUpstream(t *testing.T) {
	r := newRepo(t)
	r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("bpf: add foo")
	r.commitAs("bob@example.com", "Revert \"bpf: add foo\"\n\nThis reverts commit "+x+".")
	r.git("tag", "v5.1")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	b := r.commit("bpf: add foo\n\ncommit " + x + " upstream.")
	r.git("tag", "v5.0.1")
	r.git("checkout", "-q", "-b", "a16", "v5.0.1")
	own := r.commitAs("greg@example.com", "Revert \"bpf: add foo\"\n\nThis reverts commit "+b+".")
	r.git("update-ref", "refs/remotes/android/a16", "HEAD")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{Vendors: []string{"android"}})
	if c := lookup(t, g, own); c.Upstream != "" {
		t.Errorf("upstream = %s, want none", c.Upstream)
	}
	if got := revertsOf(g, own); got != b {
		t.Errorf("Reverts = %q, want %s", got, b)
	}
}
