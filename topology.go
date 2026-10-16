package kgraph

import (
	"cmp"
	"math/bits"
	"sync"

	"github.com/linux-analysis/kgraph/kernel"
)

// topology is where each commit is: the trees whose tips reach it, the
// first release tag that contains it and its generation, which orders it
// after its ancestors.
type topology struct {
	store    *store
	trees    []tree   // a tree's index is its bit of reach
	tags     []tag    // in version order
	reach    []uint64 // the bits of the trees whose tips reach each commit
	firstTag []int32  // index into tags, none when no tag contains the commit
	gen      []uint32 // one more than the highest generation of the parents, 1 for a root
	tagAt    map[uint32]int32
	kinds    [stableTree + 1]uint64
	walks    *sync.Pool // of *walk, for ancestor
}

// walk is what ancestor has seen: the commits marked with the current
// stamp.
type walk struct {
	stamp uint32
	seen  []uint32
	stack []uint32
}

// newTopology walks the trees and the tags of l over the commits of s.
// Walking the tags in version order gives each commit the first one that
// contains it.
func newTopology(s *store, l *Layout) topology {
	n := len(s.hashes)
	t := topology{store: s, trees: l.trees, tags: l.tags, reach: make([]uint64, n), firstTag: filled(n), gen: generations(s), tagAt: map[uint32]int32{},
		walks: &sync.Pool{New: func() any { return &walk{seen: make([]uint32, n)} }}}
	for b, tr := range l.trees {
		bit := uint64(1) << b
		t.kinds[tr.kind] |= bit
		for _, from := range tr.tips {
			s.walk(from, func(c uint32) bool {
				if t.reach[c]&bit != 0 {
					return false
				}
				t.reach[c] |= bit
				return true
			})
		}
	}
	for ti, tg := range l.tags {
		if c, ok := s.idx(tg.commit); ok {
			t.tagAt[uint32(c)] = int32(ti)
		}
		s.walk(tg.commit, func(c uint32) bool {
			if t.firstTag[c] != none {
				return false
			}
			t.firstTag[c] = int32(ti)
			return true
		})
	}
	return t
}

// generations numbers the commits of s by generation, parents first.
func generations(s *store) []uint32 {
	gen := make([]uint32, len(s.hashes))
	var stack []uint32
	for i := range gen {
		stack = append(stack, uint32(i))
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			if gen[c] != 0 {
				stack = stack[:len(stack)-1]
				continue
			}
			g, ready := uint32(1), true
			for _, p := range s.parents.of(c) {
				if gen[p] == 0 {
					stack = append(stack, p)
					ready = false
				} else {
					g = max(g, gen[p]+1)
				}
			}
			if ready {
				gen[c] = g
				stack = stack[:len(stack)-1]
			}
		}
	}
	return gen
}

// in tells whether a tree of kind k reaches commit i.
func (t *topology) in(i uint32, k treeKind) bool { return t.reach[i]&t.kinds[k] != 0 }

// mainline tells whether commit i is mainline: the mainline tip or a
// mainline tag reaches it.
func (t *topology) mainline(i uint32) bool { return t.in(i, mainlineTree) }

// pooled tells whether commit i is a patch in its own right, mainline, and
// so no backport.
func (t *topology) pooled(i uint32) bool { return t.mainline(i) }

// treeOf names the tree of commit i: the first of the trees that reach it,
// mainline first.
func (t *topology) treeOf(i uint32) string {
	if b := bits.TrailingZeros64(t.reach[i]); b < len(t.trees) {
		return t.trees[b].name
	}
	return ""
}

// has tells whether the stable series that branches from release series
// has commit x: whether x is in one of the series' releases or in its base
// release.
func (t *topology) has(series kernel.Release, x uint32) bool {
	if t.firstTag[x] == none {
		return false
	}
	tag := t.tags[t.firstTag[x]]
	return tag.Stable && tag.Release == series || !tag.Stable && tag.Release <= series
}

// targetOf is the stable series of a stable commit, by its tag: the
// release the series branches from. ok is false for one in no release
// yet.
func (t *topology) targetOf(x uint32) (series kernel.Release, ok bool) {
	if t.firstTag[x] == none {
		return 0, false
	}
	return t.tags[t.firstTag[x]].Release, true
}

// before orders commits as a branch applied them: by their first tags,
// untagged ones last, then by committer date, and a commit after its
// ancestors within the second.
func (t *topology) before(a, b uint32) int {
	return cmp.Or(cmp.Compare(uint32(t.firstTag[a]), uint32(t.firstTag[b])), cmp.Compare(t.store.date[a], t.store.date[b]), cmp.Compare(t.gen[a], t.gen[b]))
}

// ancestor tells whether commit a is an ancestor of commit b, or b itself.
// The walk down from b stops at a's generation, and at a release tag,
// which tells by its first tag whether it has a.
func (t *topology) ancestor(a, b uint32) bool {
	if a == b {
		return true
	}
	// Every tree that reaches b reaches its ancestors.
	if t.gen[a] >= t.gen[b] || t.reach[b]&^t.reach[a] != 0 {
		return false
	}
	w := t.walks.Get().(*walk)
	defer t.walks.Put(w)
	if w.stamp++; w.stamp == 0 {
		clear(w.seen)
		w.stamp = 1
	}
	w.seen[b] = w.stamp
	w.stack = append(w.stack[:0], b)
	for len(w.stack) > 0 {
		c := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		if c == a {
			return true
		}
		if tg, ok := t.tagAt[c]; ok {
			if t.contains(tg, a) {
				return true
			}
			continue
		}
		for _, p := range t.store.parents.of(c) {
			if w.seen[p] != w.stamp && t.gen[p] >= t.gen[a] {
				w.seen[p] = w.stamp
				w.stack = append(w.stack, p)
			}
		}
	}
	return false
}

// contains tells whether release tag tg has commit x: a mainline tag the
// commits of its own and earlier mainline tags, a stable tag those of its
// own and earlier tags of its series and of its base release.
func (t *topology) contains(tg int32, x uint32) bool {
	f := t.firstTag[x]
	if f == none || f > tg {
		return false
	}
	have, tag := t.tags[f], t.tags[tg]
	if !tag.Stable {
		return !have.Stable
	}
	return have.Stable && have.Release == tag.Release || !have.Stable && have.Release <= tag.Release
}
