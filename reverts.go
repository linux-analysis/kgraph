package kgraph

// findReverts finds what each commit reverts: the commit its message names
// by id.
func findReverts(p *pass) []int32 {
	revert := filled(p.n)
	for _, r := range p.facts.reverts {
		if t, ok := p.resolve(r.id); ok && t != int32(r.commit) {
			revert[r.commit] = t
		}
	}
	return revert
}

// ownReverts settles the reverts of the stable and vendor branches in
// place, once the backports are known. A branch's own revert, no backport,
// that names a mainline or linux-next commit, or another series' backport
// whose message it copies, reverts the newest backport of that patch the
// series or branch has before it, but not one that brings several patches
// at once.
func ownReverts(p *pass, revert, upstream []int32, of, backports, bstart []uint32) {
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
			revert[i] = own
		}
	}
}
