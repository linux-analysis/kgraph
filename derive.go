package kgraph

// buildStats count what the passes found, for the build log.
type buildStats struct {
	mainline int
	upstream upstreamStats
	fixes    fixStats
	twins    int
}

// derive runs the passes over the store and the topology of g, in the
// order their arguments ask for, and keeps what they make in g. The copies
// come first, so that a subject or an identity copies share names one
// patch. Reverts come twice: the upstreams set aside a commit that reverts
// what it names, and once the backports are known, a branch's own revert
// goes to the branch's backport.
func (g *Graph) derive(p *pass, twins []twin) buildStats {
	var st buildStats
	for _, m := range p.mainline {
		if m {
			st.mainline++
		}
	}
	named := findNamed(p)
	copyOf, nTwins := findCopies(p, named, twins)
	st.twins = nTwins
	revert := findReverts(p)
	upstream, ust := findUpstreams(p, named, revert, copyOf)
	st.upstream = ust
	pt := findPatches(p, upstream, copyOf)
	g.backports, g.bstart = findBackports(p, upstream, pt.of)
	ownReverts(p, revert, upstream, pt.of, g.backports, g.bstart)
	fixes, fstart, fst := findFixes(p, copyOf)
	st.fixes = fst
	g.revert, g.upstream, g.fixes, g.fstart, g.patches = revert, upstream, fixes, fstart, pt
	g.fixedBy, g.fbstart = findFixedBy(p, fixes, fstart, pt.of)
	g.reverters, g.rstart = findReverters(p, revert)
	return st
}
