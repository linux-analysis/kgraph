package kgraph

import "slices"

// upstreamStats count how findUpstreams found the upstreams.
type upstreamStats struct {
	named, identity, subject, rejected int
}

// findNamed finds the commit that the message of each commit names as its
// upstream, mainline or not, by the upstream lines, and for a commit of a
// vendor branch alone also by the weak forms: stable commits quote other
// commits the same way. A weak form names the upstream itself, so a
// mainline commit it names wins over a strong form, which may come from
// the quoted upstream message. A commit that names several mainline
// commits by the weak forms, as Red Hat's squashed rebases do, names none:
// several pairs it with each, by commit.
func findNamed(p *pass) (named []int32, several keyed) {
	named = filled(p.n)
	for _, u := range p.facts.named {
		if t, ok := p.idx(u.id); ok {
			named[u.commit] = t
		}
	}
	var pairs []uint64 // commit<<32 | upstream
	weak := p.facts.weak
	for lo := 0; lo < len(weak); {
		c, hi := weak[lo].commit, lo+1
		for hi < len(weak) && weak[hi].commit == c {
			hi++
		}
		var ts, ups []int32
		for _, u := range weak[lo:hi] {
			if t, ok := p.idx(u.id); ok && p.vendorOnly(c) && !p.merge(int(c)) && !slices.Contains(ts, t) {
				ts = append(ts, t)
				if p.mainline[t] {
					ups = append(ups, t)
				}
			}
		}
		lo = hi
		switch {
		case len(ups) > 1:
			named[c] = none
			for _, u := range ups {
				pairs = append(pairs, uint64(c)<<32|uint64(u))
			}
		case len(ups) == 1:
			named[c] = ups[0]
		case len(ts) > 0 && named[c] == none:
			named[c] = ts[0]
		}
	}
	return named, pairIndex(pairs)
}

// findUpstreams finds the mainline commit each backport was made from. A
// mainline commit has no upstream, and neither has a linux-next commit: it
// is a patch on its way to mainline. Nor has a merge, though a vendor's
// merge of a request quotes the upstream lines of the commits it brings.
//
// The strategies, in order:
//   - the message: the commit named names, which must be mainline; a
//     commit with several has none, and no other strategy runs for it;
//   - the identity: the only mainline patch with its author and subject
//     that is not newer, by its canonical copy, as copyOf gives it;
//   - for a vendor commit, the subject: the only such mainline patch with
//     its subject alone, as SUSE's patches keep neither an upstream line
//     nor the upstream author; not for one that reverts a commit outside
//     mainline and linux-next, the branch's own revert of its backport.
func findUpstreams(p *pass, named, revert []int32, several keyed, copyOf []uint32) (upstream []int32, st upstreamStats) {
	// A pick of a stable backport names the backport, so a named commit
	// outside mainline stands for the one it names in turn.
	chase := func(u int32) int32 {
		if u != none && !p.mainline[u] && named[u] != none {
			return named[u]
		}
		return u
	}
	// A revert of a backport quotes the backport's upstream line, so a
	// commit that reverts the commit it names, or a commit naming the same
	// one, has no upstream by its message.
	upstream = filled(p.n)
	for i, u := range named {
		u = chase(u)
		if u == none || p.pool[i] || p.merge(i) || !p.mainline[u] {
			continue
		}
		if r := revert[i]; r != none && (r == u || chase(named[r]) == u) {
			st.rejected++
			continue
		}
		upstream[i] = u
		st.named++
	}
	multi := make([]bool, p.n)
	for _, c := range several.keys {
		multi[c] = true
	}
	// Copies of one patch share the key, and are one candidate.
	only := func(idx keyed, key uint64, i int) int32 {
		up := int32(none)
		for _, k := range withKey(idx, key) {
			if !p.mainline[k] || p.date[k] > p.date[i] {
				continue
			}
			if c := int32(copyOf[k]); up == none || up == c {
				up = c
			} else {
				return none
			}
		}
		return up
	}
	for i, in := range p.pool {
		if in || p.merge(i) || upstream[i] != none || multi[i] {
			continue
		}
		u := only(p.idents, p.facts.ident[i], i)
		switch {
		case u != none:
			st.identity++
		case p.vendorOnly(uint32(i)) && (revert[i] == none || p.pool[revert[i]]):
			if u = only(p.subjects, p.facts.subject[i], i); u != none {
				st.subject++
			}
		}
		upstream[i] = u
	}
	return upstream, st
}
