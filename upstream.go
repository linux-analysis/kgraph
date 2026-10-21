package kgraph

// upstreamStats count how findUpstreams found the upstreams.
type upstreamStats struct {
	named, identity, rejected int
}

// findNamed finds the commit that the message of each commit names as its
// upstream, mainline or not, by the upstream lines.
func findNamed(p *pass) []int32 {
	named := filled(p.n)
	for _, u := range p.facts.named {
		if t, ok := p.idx(u.id); ok {
			named[u.commit] = t
		}
	}
	return named
}

// findUpstreams finds the mainline commit each backport was made from. A
// mainline commit has no upstream, and neither has a linux-next commit: it
// is a patch on its way to mainline.
//
// The strategies, in order:
//   - the message: the commit named names, which must be mainline;
//   - the identity: the only mainline patch with its author and subject
//     that is not newer, by its canonical copy, as copyOf gives it.
func findUpstreams(p *pass, named, revert []int32, copyOf []uint32) (upstream []int32, st upstreamStats) {
	// A revert of a backport quotes the backport's upstream line, so a
	// commit that reverts the commit it names, or a commit naming the same
	// one, has no upstream by its message.
	upstream = filled(p.n)
	for i, u := range named {
		if u == none || p.pool[i] || !p.mainline[u] {
			continue
		}
		if r := revert[i]; r != none && (r == u || named[r] == u) {
			st.rejected++
			continue
		}
		upstream[i] = u
		st.named++
	}
	// Copies of one patch share the key, and are one candidate.
	for i, in := range p.pool {
		if in || upstream[i] != none {
			continue
		}
		up := int32(none)
		for _, k := range withKey(p.idents, p.facts.ident[i]) {
			if !p.mainline[k] || p.date[k] > p.date[i] {
				continue
			}
			if c := int32(copyOf[k]); up == none || up == c {
				up = c
			} else {
				up = none
				break
			}
		}
		if up != none {
			upstream[i] = up
			st.identity++
		}
	}
	return upstream, st
}
