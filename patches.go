package kgraph

import "cmp"

// patches tie each commit to its patch, the change itself: the pool
// commits that are one patch merged more than once, its copies, with the
// backports of any of them. A patch goes by its canonical copy, the one in
// the earliest release, untagged ones last and a mainline one before a
// next one. A backport belongs to the patch of its upstream; any other
// commit outside the pool is a patch of its own.
type patches struct {
	of      []uint32 // the canonical copy of the patch of each commit
	members []uint32 // the copies of each patch under its canonical copy, canonical first
	mstart  []uint32
}

// twin is a pair of pool commits with one author, author date and
// subject; line tells that one descends from the other.
type twin struct {
	a, b uint32
	line bool
}

// findTwins lists the twins of the pool.
func findTwins(p *pass) []twin {
	var out []twin
	// Twins share the author and subject sum, so one run of idents holds
	// them all.
	idents := p.idents
	for lo := 0; lo < len(idents.keys); {
		hi := lo + 1
		for hi < len(idents.keys) && idents.keys[hi] == idents.keys[lo] {
			hi++
		}
		run := idents.commits[lo:hi]
		for j, a := range run {
			for _, b := range run[j+1:] {
				if p.facts.twin[a] == p.facts.twin[b] {
					out = append(out, twin{a, b, p.ancestor(a, b) || p.ancestor(b, a)})
				}
			}
		}
		lo = hi
	}
	return out
}

// onALine lists the commits of the twins that descend from one another,
// whose lines findCopies compares.
func onALine(twins []twin) []uint32 {
	var out []uint32
	for _, t := range twins {
		if t.line {
			out = append(out, t.a, t.b)
		}
	}
	return out
}

// findCopies ties the copies of each patch in the pool and gives each pool
// commit its canonical copy, and any other commit itself. twins counts the
// commits with copies.
//
// Twins are copies, as git am and git cherry-pick keep the author, author
// date and subject: a fix applied to a -fixes branch and again through
// -next. When one descends from the other, as a fix applied again after
// its revert, they are copies if they change the same lines: a script
// that regenerates a commit keeps its date too. So is a commit whose
// upstream line names another with its subject, as a fix reworked for
// -fixes gets a new author date.
func findCopies(p *pass, named []int32, twins []twin) (copyOf []uint32, nTwins int) {
	copyOf = make([]uint32, p.n) // each commit links towards one copy of its patch
	for i := range copyOf {
		copyOf[i] = uint32(i)
	}
	find := func(c uint32) uint32 {
		for copyOf[c] != c {
			copyOf[c] = copyOf[copyOf[c]]
			c = copyOf[c]
		}
		return c
	}
	join := func(a, b uint32) { copyOf[find(a)] = find(b) }
	lines := map[uint32]hash{}
	for _, l := range p.facts.lines {
		lines[l.commit] = l.id
	}
	for _, t := range twins {
		la, oka := lines[t.a]
		lb, okb := lines[t.b]
		if !t.line || oka && okb && la == lb {
			join(t.a, t.b)
		}
	}
	for i, u := range named {
		if u != none && p.pool[i] && p.pool[u] && p.facts.subject[i] == p.facts.subject[u] {
			join(uint32(i), uint32(u))
		}
	}
	copies, cstart := group(p.n, func(emit func(key, v uint32)) {
		for i, in := range p.pool {
			if in {
				emit(find(uint32(i)), uint32(i))
			}
		}
	}, p.canonical)
	for r := range p.n {
		c := copies[cstart[r]:cstart[r+1]]
		for _, m := range c {
			copyOf[m] = c[0]
		}
		if len(c) > 1 {
			nTwins += len(c)
		}
	}
	for i, in := range p.pool {
		if !in {
			copyOf[i] = uint32(i)
		}
	}
	return copyOf, nTwins
}

// canonical orders the copies of a patch: by their first tags, untagged
// ones last, a mainline one before a next one, then by date.
func (p *pass) canonical(a, b uint32) int {
	notMainline := func(c uint32) int {
		if p.mainline[c] {
			return 0
		}
		return 1
	}
	return cmp.Or(cmp.Compare(uint32(p.firstTag[a]), uint32(p.firstTag[b])), cmp.Compare(notMainline(a), notMainline(b)), p.byDate(a, b))
}

// findPatches finds the patch of every commit: a pool commit's is that of
// its copies, a backport's that of its upstream, and any other commit is a
// patch of its own.
func findPatches(p *pass, upstream []int32, copyOf []uint32) (pt patches) {
	pt.of = make([]uint32, p.n)
	for i, in := range p.pool {
		switch u := upstream[i]; {
		case in:
			pt.of[i] = copyOf[i]
		case u != none:
			pt.of[i] = copyOf[u]
		default:
			pt.of[i] = uint32(i)
		}
	}
	pt.members, pt.mstart = group(p.n, func(emit func(key, v uint32)) {
		for i, in := range p.pool {
			if in || upstream[i] == none {
				emit(pt.of[i], uint32(i))
			}
		}
	}, p.canonical)
	return pt
}
