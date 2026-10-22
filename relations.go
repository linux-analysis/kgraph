package kgraph

import (
	"bytes"
	"cmp"
	"math"
	"slices"
)

// fixStats count how findFixes resolved the Fixes: tags.
type fixStats struct {
	fixes, tabled, rescued, unresolved int
}

// findFixes resolves the Fixes: tags of each commit, grouped by commit in
// message order, each target once and never the commit itself; a merge,
// which quotes the messages of the commits it brings, has none. An id no
// commit of the graph matches, as an id of a maintainer tree rebased
// before it reached mainline, or one several match, is the commit the
// table of corrected ids gives it, or else the only mainline patch with
// the subject it quotes.
func findFixes(p *pass, copyOf []uint32) (fixes, fstart []uint32, st fixStats) {
	fstart = make([]uint32, 1, p.n+1)
	next := 0
	for i := range p.n {
		for ; next < len(p.facts.fixes) && p.facts.fixes[next].commit == uint32(i); next++ {
			fx := p.facts.fixes[next]
			if p.merge(i) {
				continue
			}
			st.fixes++
			t, how := p.find(fx.rawFix, copyOf, math.MaxInt64)
			switch how {
			case byTable:
				st.tabled++
			case bySubject:
				st.rescued++
			case unresolved:
				st.unresolved++
				continue
			}
			if t != int32(i) && !slices.Contains(fixes[fstart[i]:], uint32(t)) {
				fixes = append(fixes, uint32(t))
			}
		}
		fstart = append(fstart, uint32(len(fixes)))
	}
	return fixes, fstart, st
}

// findBackports lists the backports of each patch under its canonical
// copy, in version order of their tags: the commits with its upstream, and
// those several pairs with it.
func findBackports(p *pass, upstream []int32, several keyed, of []uint32) (backports, bstart []uint32) {
	return group(p.n, func(emit func(key, v uint32)) {
		for i, u := range upstream {
			if u != none {
				emit(of[u], uint32(i))
			}
		}
		for j, c := range several.keys {
			emit(of[several.commits[j]], uint32(c))
		}
	}, func(a, b uint32) int {
		return cmp.Or(cmp.Compare(uint32(p.firstTag[a]), uint32(p.firstTag[b])), bytes.Compare(p.hashes[a][:], p.hashes[b][:]))
	})
}

// findFixedBy lists the patches with a commit that names each patch in its
// Fixes: tags, under its canonical copy, each once and oldest first. The
// tags of a backport count for its patch: a stable maintainer adds them.
func findFixedBy(p *pass, fixes, fstart, of []uint32) (fixedBy, fbstart []uint32) {
	fixedBy, fbstart = group(p.n, func(emit func(key, v uint32)) {
		for i := range p.n {
			for _, t := range fixes[fstart[i]:fstart[i+1]] {
				if of[t] != of[i] {
					emit(of[t], of[i])
				}
			}
		}
	}, p.byDate)
	return compactGroups(fixedBy, fbstart)
}

// findReverters lists the commits that revert each commit, oldest first.
func findReverters(p *pass, revert []int32) (reverters, rstart []uint32) {
	return group(p.n, func(emit func(key, v uint32)) {
		for i, r := range revert {
			if r != none {
				emit(uint32(r), uint32(i))
			}
		}
	}, p.byDate)
}

// depStats count the dependencies findDeps found and those it could not
// resolve.
type depStats struct {
	deps, unresolved int
}

// findDeps finds what each patch needs first: what the Depends-on: tags,
// the Cc: stable prerequisites and the text of its commits name, and for
// the fix a Stable-dep-of: tag names, the patch of the stable commit that
// carries the tag. A backport's message is the mainline message, so its
// tags count for its patch, and the same pair from several branches
// counts once. A merge's tags are those of the commits it brings.
func findDeps(p *pass, of []uint32) (needs, neededBy keyed, st depStats) {
	var pairs []uint64 // needer<<32 | needed
	for k, refs := range [][]fixRef{p.facts.deps, p.facts.depOf} {
		for _, d := range refs {
			if p.merge(int(d.commit)) {
				continue
			}
			t, how := p.find(d.rawFix, of, math.MaxInt64)
			if how == unresolved {
				st.unresolved++
				continue
			}
			a, b := of[d.commit], of[t]
			if k == 1 { // a Stable-dep-of: tag names the commit that needs it
				a, b = b, a
			}
			if a != b {
				pairs = append(pairs, uint64(a)<<32|uint64(b))
			}
		}
	}
	slices.Sort(pairs)
	pairs = slices.Compact(pairs)
	st.deps = len(pairs)
	needs = pairIndex(pairs)
	for j, pr := range pairs {
		pairs[j] = pr<<32 | pr>>32
	}
	return needs, pairIndex(pairs), st
}
