package kgraph

import (
	"bytes"
	"cmp"
	"slices"
)

// fixStats count how findFixes resolved the Fixes: tags.
type fixStats struct {
	fixes, tabled, rescued, unresolved int
}

// findFixes resolves the Fixes: tags of each commit, grouped by commit in
// message order, each target once and never the commit itself. An id no
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
			st.fixes++
			t, how := p.find(fx.rawFix, copyOf)
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
// copy, in version order of their tags.
func findBackports(p *pass, upstream []int32, of []uint32) (backports, bstart []uint32) {
	return group(p.n, func(emit func(key, v uint32)) {
		for i, u := range upstream {
			if u != none {
				emit(of[u], uint32(i))
			}
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
