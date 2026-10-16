package kgraph

import (
	"slices"
	"strings"

	"github.com/linux-analysis/kgraph/kernel"
)

// Commit is a commit of the graph.
type Commit struct {
	g *Graph
	i uint32
}

// Commit finds the one commit that the full or abbreviated hex id names,
// in either case; ok is false when no commit or several do.
func (g *Graph) Commit(id string) (Commit, bool) {
	if len(id) == 0 || len(id) > 40 {
		return Commit{}, false
	}
	i, ok := g.resolve([]byte(strings.ToLower(id)))
	if !ok {
		return Commit{}, false
	}
	return Commit{g, uint32(i)}, true
}

// Ambiguous tells whether the ids of several commits start with the
// abbreviated id, in either case, so that Commit finds none.
func (g *Graph) Ambiguous(id string) bool {
	if len(id) >= 40 {
		return false
	}
	_, found := g.prefix([]byte(strings.ToLower(id)))
	return found > 1
}

// Hash is the full hex id of the commit.
func (c Commit) Hash() string { return c.g.hashes[c.i].String() }

// Mainline tells whether the mainline tip or a mainline tag reaches the
// commit.
func (c Commit) Mainline() bool { return c.g.mainline(c.i) }

// Tree is where the commit is: mainline, else stable.
func (c Commit) Tree() string { return c.g.treeOf(c.i) }

// Date is the committer date, Unix time.
func (c Commit) Date() int64 { return c.g.date[c.i] }

// Tag is the first release tag in version order that contains the commit,
// such as v6.7-rc1 or v6.6.8; "" when none does yet.
func (c Commit) Tag() string {
	if t := c.g.firstTag[c.i]; t != none {
		return c.g.tags[t].Name
	}
	return ""
}

// Release is the mainline release of Tag, -rc folded: v6.7 for v6.7-rc1.
// It is Unreleased when Tag is a stable tag or "".
func (c Commit) Release() kernel.Release {
	if t := c.g.firstTag[c.i]; t != none && !c.g.tags[t].Stable {
		return c.g.tags[t].Release
	}
	return kernel.Unreleased
}

// SeriesRelease is the release a stable Tag is a patch level of: v6.6 for
// v6.6.8. It is Unreleased when Tag is a mainline tag or "".
func (c Commit) SeriesRelease() kernel.Release {
	if t := c.g.firstTag[c.i]; t != none && c.g.tags[t].Stable {
		return c.g.tags[t].Release
	}
	return kernel.Unreleased
}

// Upstream is the mainline commit a backport was made from: the one its
// message names, or else the only earlier mainline commit with its author
// and subject. ok is false for a commit that is no backport.
func (c Commit) Upstream() (Commit, bool) { return c.g.commit(c.g.upstream[c.i]) }

// Reverts is the commit that the commit reverts; ok is false when it
// reverts none.
func (c Commit) Reverts() (Commit, bool) { return c.g.commit(c.g.revert[c.i]) }

// RevertedBy are the commits that revert the commit, oldest first.
func (c Commit) RevertedBy() []Commit {
	return c.g.commits(c.g.reverters[c.g.rstart[c.i]:c.g.rstart[c.i+1]])
}

// Patch is the patch the commit carries: its own, or for a backport its
// upstream's.
func (c Commit) Patch() Patch { return Patch{c.g, c.g.of[c.i]} }

// commit is the commit with index i, ok false for none.
func (g *Graph) commit(i int32) (Commit, bool) {
	if i == none {
		return Commit{}, false
	}
	return Commit{g, uint32(i)}, true
}

func (g *Graph) commits(list []uint32) []Commit {
	if len(list) == 0 {
		return nil
	}
	out := make([]Commit, len(list))
	for j, i := range list {
		out[j] = Commit{g, i}
	}
	return out
}

// Patch is a change: the mainline commits that are the same patch merged
// more than once, its copies, with the stable commits backported from any
// of them. Every question about a change is a question about its patch,
// whichever of its commits names it. A commit outside mainline that is no
// backport is a patch of its own.
type Patch struct {
	g *Graph
	c uint32 // the canonical copy
}

// Commit is the canonical copy of the patch: the copy in the earliest
// release, untagged ones last.
func (p Patch) Commit() Commit { return Commit{p.g, p.c} }

// Copies are the copies of the patch, canonical first.
func (p Patch) Copies() []Commit { return p.g.commits(p.g.members[p.g.mstart[p.c]:p.g.mstart[p.c+1]]) }

// Fixes are the patches that the Fixes: tags of the patch's commits name,
// its copies' first and then its backports', in message order: by id, or
// by the quoted subject when no commit of the graph has the id.
func (p Patch) Fixes() []Patch {
	g := p.g
	var out []Patch
	for _, run := range [][]uint32{g.members[g.mstart[p.c]:g.mstart[p.c+1]], g.backports[g.bstart[p.c]:g.bstart[p.c+1]]} {
		for _, m := range run {
			for _, t := range g.fixes[g.fstart[m]:g.fstart[m+1]] {
				if f := (Patch{g, g.of[t]}); f != p && !slices.Contains(out, f) {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// FixedBy are the patches with a commit whose Fixes: tags name the patch,
// oldest first.
func (p Patch) FixedBy() []Patch {
	return p.g.patchList(p.g.fixedBy[p.g.fbstart[p.c]:p.g.fbstart[p.c+1]])
}

func (g *Graph) patchList(list []uint32) []Patch {
	if len(list) == 0 {
		return nil
	}
	out := make([]Patch, len(list))
	for j, c := range list {
		out[j] = Patch{g, c}
	}
	return out
}
