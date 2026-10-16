package kgraph

import (
	"slices"

	"github.com/linux-analysis/kgraph/kernel"
)

// step is a commit that applies a patch or undoes it, with the commit it
// undoes, or none.
type step struct {
	commit uint32
	apply  bool
	undoes int32
}

// timeline lists the commits that apply or undo the patch c: its copies
// and backports apply it, a commit that reverts one of these undoes it,
// and a revert of that revert applies it again. A mainline revert brings
// the copies and backports of its own patch along: a backport of a revert
// undoes what the revert undoes, whatever its message names. A backport that reverts a stable-only commit is none of them.
// They come in the order before gives.
func (g *Graph) timeline(c uint32) []step {
	var steps []step
	seen := map[uint32]bool{} // abbreviated ids can make reverts a cycle
	var visit func(x uint32, apply bool, undoes int32)
	patch := func(p uint32, apply bool, undoes int32) {
		for _, x := range g.members[g.mstart[p]:g.mstart[p+1]] {
			visit(x, apply, undoes)
		}
		for _, b := range g.backports[g.bstart[p]:g.bstart[p+1]] {
			if r := g.revert[b]; r == none || g.mainline(uint32(r)) {
				visit(b, apply, undoes)
			}
		}
	}
	visit = func(x uint32, apply bool, undoes int32) {
		if seen[x] {
			return
		}
		seen[x] = true
		if r := g.revert[x]; r != none {
			undoes = r
		}
		steps = append(steps, step{x, apply, undoes})
		for _, r := range g.reverters[g.rstart[x]:g.rstart[x+1]] {
			if g.pooled(r) {
				patch(g.of[r], !apply, int32(x))
			} else {
				visit(r, !apply, int32(x))
			}
		}
	}
	patch(c, true, none)
	slices.SortStableFunc(steps, func(a, b step) int { return g.before(a.commit, b.commit) })
	return steps
}

// replay plays the steps that the stable series that branches from
// release series has, in order, and tells which steps carry the patch at
// the end, oldest first. A step that applies the patch carries it. One
// that undoes a carrier takes out that carrier, and those a merge made one
// change with it before the undo: the carriers it descends from that are
// apart from the line of the one it undoes. So a series that picked the
// patch twice and reverted the second pick still has the first, while a
// revert of one copy in mainline takes out both. One that undoes any
// other commit takes out every carrier.
func (g *Graph) replay(steps []step, series kernel.Release) (carriers []int) {
	for k, s := range steps {
		if !g.has(series, s.commit) {
			continue
		}
		if s.apply {
			carriers = append(carriers, k)
			continue
		}
		y := uint32(s.undoes)
		if s.undoes == none || !slices.ContainsFunc(carriers, func(k int) bool { return steps[k].commit == y }) {
			carriers = carriers[:0]
			continue
		}
		carriers = slices.DeleteFunc(carriers, func(k int) bool {
			c := steps[k].commit
			return c == y || g.ancestor(c, s.commit) && !g.ancestor(c, y) && !g.ancestor(y, c)
		})
	}
	return carriers
}

// Backports are the stable backports of the patch, in version order of
// their tags, as the kernel CNA's dyad counts them: those in a release,
// and none that its own series undid, by reverting it or the patch; other
// series play no part.
func (p Patch) Backports() []Commit {
	g := p.g
	steps := g.timeline(p.c)
	var out []uint32
	for _, b := range g.backports[g.bstart[p.c]:g.bstart[p.c+1]] {
		series, ok := g.targetOf(b)
		if !ok {
			continue // in no release yet
		}
		if slices.ContainsFunc(g.replay(steps, series), func(k int) bool { return steps[k].commit == b }) {
			out = append(out, b)
		}
	}
	return g.commits(out)
}
