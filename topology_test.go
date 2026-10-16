package kgraph

import (
	"testing"

	"github.com/linux-analysis/kgraph/kernel"
)

// A commit is an ancestor of the commits that descend from it on any line,
// through merges and release tags, and of no commit on a parallel branch;
// within one second a commit comes after its ancestors.
func TestAncestor(t *testing.T) {
	r := newRepo(t)
	init := r.commit("init")
	r.git("tag", "v5.0")
	x := r.commit("x: on master")
	r.git("checkout", "-q", "-b", "side", init)
	y := r.commit("y: on a side branch")
	r.git("checkout", "-q", "master")
	r.git("merge", "-q", "--no-ff", "-m", "Merge side", "side")
	r.git("tag", "v5.1")
	z := r.commit("z: after v5.1")
	w := r.commitAt(r.clock, "alice@example.com", "w: within z's second")
	r.git("checkout", "-q", "-b", "linux-5.0.y", "v5.0")
	s1 := r.commit("s1")
	s2 := r.commit("s2")
	r.git("tag", "v5.0.1")
	s3 := r.commit("s3")
	r.git("tag", "v5.0.2")
	r.git("checkout", "-q", "master")
	g := build(t, r.dir, kernel.Remotes{})
	at := func(h string) uint32 {
		c, ok := g.Commit(h)
		if !ok {
			t.Fatalf("%s is not in the graph", h)
		}
		return c.i
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{init, z, true}, {x, z, true}, {y, z, true}, {z, w, true}, {x, x, true},
		{x, y, false}, {y, x, false}, {z, x, false}, {w, z, false},
		{s1, s3, true}, {init, s3, true}, {s2, s3, true},
		{x, s3, false}, {s3, s1, false}, {s1, z, false},
	} {
		if got := g.ancestor(at(c.a), at(c.b)); got != c.want {
			t.Errorf("ancestor(%s, %s) = %v, want %v", c.a[:7], c.b[:7], got, c.want)
		}
	}
	if g.date[at(z)] != g.date[at(w)] || g.before(at(z), at(w)) >= 0 {
		t.Errorf("before(z, w) = %d, want z first within one second", g.before(at(z), at(w)))
	}
}
