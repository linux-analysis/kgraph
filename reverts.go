package kgraph

// findReverts finds what each commit reverts: what its message names by
// id, or for a Revert "..." whose id names no commit, or which names none,
// the commit with the subject it quotes that is not newer than the revert.
// Those found by the subject alone are guessed, for ownReverts to judge
// once the backports are known.
func findReverts(p *pass, copyOf []uint32) (revert []int32, guessed []uint32) {
	revert = filled(p.n)
	for _, r := range p.facts.reverts {
		t, how := p.find(r.rawFix, copyOf, p.date[r.commit])
		if how == unresolved || t == int32(r.commit) {
			continue
		}
		revert[r.commit] = t
		if how == bySubject {
			guessed = append(guessed, r.commit)
		}
	}
	return revert, guessed
}

// ownReverts settles the reverts of the stable and vendor branches in
// place, once the backports are known, and counts the guessed ones it
// keeps. A branch's own revert, no backport, that names a mainline or
// linux-next commit, or another series' backport whose message it copies,
// reverts the newest backport of that patch the series or branch has
// before it, but not one that brings several patches at once. A guess
// by the subject holds when it found such a backport, when the series or
// branch has the commit, or for the backport of a mainline revert of it;
// else the branch reverted a commit of its own by that name.
func ownReverts(p *pass, revert []int32, guessed []uint32, upstream []int32, of, backports, bstart []uint32) (kept int) {
	moved := map[uint32]bool{}
	for i, t := range revert {
		if t == none || p.pool[i] || upstream[i] != none {
			continue
		}
		tg, ok := p.targetOf(uint32(i))
		if !ok || !p.pool[t] && p.has(tg, uint32(t)) {
			continue
		}
		own := int32(none)
		for _, b := range backports[bstart[of[t]]:bstart[of[t]+1]] {
			if upstream[b] != none && p.has(tg, b) && p.before(b, uint32(i)) < 0 && (own == none || p.before(b, uint32(own)) > 0) {
				own = int32(b)
			}
		}
		if own != none {
			revert[i], moved[uint32(i)] = own, true
		}
	}
	for _, i := range guessed {
		tg, ok := p.targetOf(i)
		if u := upstream[i]; p.pool[i] || moved[i] || u != none && revert[u] == revert[i] || ok && p.has(tg, uint32(revert[i])) {
			kept++
		} else {
			revert[i] = none
		}
	}
	return kept
}
