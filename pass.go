package kgraph

import (
	"bytes"
	"os"
	"slices"
)

// pass is what every pass reads: the commits with the facts their messages
// state, where each commit is, and the indexes of the pool and of mainline
// by their keys. A pass takes what earlier passes made as arguments and
// returns what it makes; it writes nothing else. derive runs them in the
// order their arguments ask for.
type pass struct {
	*store
	*topology
	n        int
	pool     []bool // pooled, per commit: mainline and linux-next, the patches in their own right
	mainline []bool
	idents   keyed // the pool by author and subject
	subjects keyed // mainline by subject
	fixups   map[string]hash
}

func newPass(s *store, t *topology, fixups map[string]hash) *pass {
	n := len(s.hashes)
	p := &pass{store: s, topology: t, n: n, pool: make([]bool, n), mainline: make([]bool, n), fixups: fixups}
	for i := range n {
		p.pool[i], p.mainline[i] = t.pooled(uint32(i)), t.mainline(uint32(i))
	}
	p.idents, p.subjects = keyIndex(s.facts.ident, p.pool), keyIndex(s.facts.subject, p.mainline)
	return p
}

// merge tells whether commit i is a merge.
func (p *pass) merge(i int) bool { return len(p.parents.of(uint32(i))) > 1 }

// how tells how find found the commit an id names.
type how int

const (
	unresolved how = iota
	byID
	byTable   // by the table of corrected ids
	bySubject // by the subject the message quotes
)

// find finds the commit that an id of a message names: the one with the
// id, else the one the table of corrected ids gives the id, else the only
// mainline patch with the subject the message quotes, when it quotes one,
// by its canonical copy, as of gives it.
func (p *pass) find(fx rawFix, of []uint32) (int32, how) {
	if t, ok := p.resolve(fx.id); ok {
		return t, byID
	}
	if h, listed := p.fixups[string(fx.id)]; listed {
		if t, ok := p.idx(h); ok {
			return t, byTable
		}
	}
	if fx.quoted {
		hits := withKey(p.subjects, fx.subject)
		if len(hits) > 0 && !slices.ContainsFunc(hits, func(h uint32) bool { return of[h] != of[hits[0]] }) {
			return int32(of[hits[0]]), bySubject
		}
	}
	return none, unresolved
}

// readFixups reads a table of Fixes: ids that name no commit, as
// verhaal's fixes.txt: a line holds the id as written and the full id of
// the commit it means, or NOT_VALID, which names none. An empty path is
// no table.
func readFixups(path string) (map[string]hash, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fixups := map[string]hash{}
	for line := range bytes.Lines(data) {
		f := bytes.Fields(line)
		if len(f) < 2 || f[0][0] == '#' {
			continue
		}
		if h, ok := parseHash(f[1]); ok {
			fixups[string(bytes.ToLower(f[0]))] = h
		}
	}
	return fixups, nil
}
