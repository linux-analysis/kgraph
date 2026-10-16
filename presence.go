package kgraph

import "slices"

// Presence is whether a stable series has a patch, and where.
type Presence struct {
	Series string `json:"series"` // a stable series such as 6.6
	// Tag is the release of the series that has had the patch since: the
	// series' base release when the series branched with it, else the
	// first tag of the earliest backport, or revert of its revert, that
	// still carries it; "" when the series lacks it.
	Tag string `json:"tag,omitempty"`
	// Commit is the earliest commit that still carries the patch: a copy,
	// a backport, or a revert of its revert; "" when the series lacks it.
	Commit string `json:"commit,omitempty"`
}

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

// state is where a target stands with a patch.
type state uint8

const (
	absent   state = iota // it never had the patch
	present               // it has the patch
	reverted              // it had the patch and reverted it
)

// replay plays the steps that target t has, in order, and tells where t
// stands with the patch at the end and which steps carry it then, oldest
// first. A step that applies the patch carries it. One that undoes a
// carrier takes out that carrier, and those a merge made one change with
// it before the undo: the carriers it descends from that are apart from
// the line of the one it undoes. So a series that picked the patch twice
// and reverted the second pick still has the first, while a revert of
// one copy in mainline takes out both. One that undoes any other commit
// takes out every carrier.
func (g *Graph) replay(steps []step, t Target) (st state, carriers []int) {
	had := false
	for k, s := range steps {
		if !g.has(t, s.commit) {
			continue
		}
		if s.apply {
			carriers, had = append(carriers, k), true
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
	switch {
	case len(carriers) > 0:
		return present, carriers
	case had:
		return reverted, nil
	}
	return absent, nil
}

// In tells for each of ts, stable series, whether it has the patch, as
// replaying the timeline of the patch tells, and since when. A series has
// the commits of its releases and of its base release.
func (p Patch) In(ts []Target) []Presence {
	g := p.g
	steps := g.timeline(p.c)
	out := make([]Presence, len(ts))
	for i, t := range ts {
		out[i].Series = t.Name
		st, carriers := g.replay(steps, t)
		if st != present {
			continue
		}
		c := steps[carriers[0]].commit
		out[i].Commit = g.hashes[c].String()
		if t.tree < 0 {
			out[i].Tag = g.tags[g.firstTag[c]].Name
			if !g.tags[g.firstTag[c]].Stable {
				out[i].Tag = t.series.String()
			}
		}
	}
	return out
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
		t, ok := g.targetOf(b)
		if !ok {
			continue // in no release yet
		}
		if _, carriers := g.replay(steps, t); slices.ContainsFunc(carriers, func(k int) bool { return steps[k].commit == b }) {
			out = append(out, b)
		}
	}
	return g.commits(out)
}

// Related is a later fix of a patch, with the series that lack it.
type Related struct {
	Patch  Patch
	Series []string
}

// Followups lists the follow-up fixes of the patch, the patches with a
// commit whose Fixes: tags name it, with the series of ts that have the
// patch and never had the fix. A fix that reverts the patch is none, nor
// is one that mainline reverted; a series that reverted the fix does not
// lack it.
func (p Patch) Followups(ts []Target) []Related {
	g := p.g
	var have []Target
	for i, pr := range p.In(ts) {
		if pr.Commit != "" {
			have = append(have, ts[i])
		}
	}
	if len(have) == 0 {
		return nil
	}
	mainline := Target{Name: "mainline", tree: 0}
	var out []Related
	for _, f := range p.FixedBy() {
		if g.revertsPatch(f.c, p.c) {
			continue
		}
		steps := g.timeline(f.c)
		if st, _ := g.replay(steps, mainline); st == reverted {
			continue
		}
		if lack := g.lacking(steps, have, func(st state) bool { return st == absent }); lack != nil {
			out = append(out, Related{f, lack})
		}
	}
	return out
}

// revertsPatch tells whether a copy of patch f reverts a commit of patch
// c.
func (g *Graph) revertsPatch(f, c uint32) bool {
	for _, m := range g.members[g.mstart[f]:g.mstart[f+1]] {
		if r := g.revert[m]; r != none && g.of[r] == c {
			return true
		}
	}
	return false
}

// lacking names the targets of ts where the patch of steps stands as lacks
// says.
func (g *Graph) lacking(steps []step, ts []Target, lacks func(state) bool) []string {
	var out []string
	for _, t := range ts {
		if st, _ := g.replay(steps, t); lacks(st) {
			out = append(out, t.Name)
		}
	}
	return out
}
