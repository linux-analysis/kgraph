package kgraph

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/linux-analysis/kgraph/kernel"
)

// store is what a build reads from git: the commits, their committer dates
// and parents and the facts their messages state, with the tips that reach
// every commit. It is what the cache keeps.
type store struct {
	hashes  []hash   // in the order git log emitted them
	order   []uint32 // indexes sorted by hash
	keys    []uint64 // the leading eight bytes of each hash of order, big-endian
	fanout  []uint32 // where in order each value of the leading two bytes starts, and n
	date    []int64  // committer date
	parents parents
	facts   facts
	tips    []hash
	// pending are the parent ids of the commits index has not seen yet,
	// those of each commit ending at its pendingEnd.
	pending    []hash
	pendingEnd []uint32
	malformed  int    // records of git log that did not parse
	mapped     []byte // the cache file the columns refer into, or nil
}

func newStore() *store {
	return &store{parents: parents{start: []uint32{0}}}
}

// update reads the commits tips reach that s lacks and indexes them. A
// commit of s that tips no longer reach, as one of a rewound branch, is an
// error: the graph has only what its tips reach. It returns how many
// commits it read.
func (s *store) update(ctx context.Context, dir string, tips []hash) (streamed int, err error) {
	before := len(s.hashes)
	if err := s.stream(ctx, dir, tips, s.tips); err != nil {
		return 0, err
	}
	streamed = len(s.hashes) - before
	if streamed > 0 || len(s.order) != len(s.hashes) {
		s.index()
	}
	// The tips of s reach every commit of s, so the same tips leave none.
	if before > 0 && (streamed > 0 || !slices.Equal(tips, s.tips)) {
		lost := 0
		for _, r := range s.reached(tips) {
			if !r {
				lost++
			}
		}
		if lost > 0 {
			return 0, fmt.Errorf("%d cached commits are on no branch or tag any more", lost)
		}
	}
	s.tips = tips
	return streamed, nil
}

// readLines reads the lines that each of commits changes and s lacks, for
// findCopies to compare, and returns how many it read.
func (s *store) readLines(ctx context.Context, dir string, commits []uint32) (int, error) {
	have := map[uint32]bool{}
	for _, l := range s.facts.lines {
		have[l.commit] = true
	}
	var want []uint32
	var ids []string
	for _, c := range commits {
		if !have[c] {
			have[c] = true
			want, ids = append(want, c), append(ids, s.hashes[c].String())
		}
	}
	if len(want) == 0 {
		return 0, nil
	}
	lines, err := kernel.ChangedLines(ctx, dir, ids)
	if err != nil {
		return 0, err
	}
	for k, c := range want {
		s.facts.lines = append(s.facts.lines, namedRef{c, sha1.Sum([]byte(lines[ids[k]]))})
	}
	return len(want), nil
}

// span returns the positions of order whose keys lie between lo and hi,
// both included: the fanout gives the positions of their leading two bytes,
// about forty keys for a tree of two million commits, and a binary search
// over those the rest.
func (s *store) span(lo, hi uint64) (a, b int) {
	if len(s.fanout) == 0 {
		return 0, 0
	}
	f, l := int(s.fanout[lo>>48]), int(s.fanout[hi>>48+1])
	keys := s.keys[f:l]
	a, _ = slices.BinarySearch(keys, lo)
	b = len(keys)
	if hi < math.MaxUint64 {
		b, _ = slices.BinarySearch(keys[a:], hi+1)
		b += a
	}
	return f + a, f + b
}

// idx finds a commit by hash.
func (s *store) idx(h hash) (int32, bool) {
	key := binary.BigEndian.Uint64(h[:])
	a, b := s.span(key, key)
	for _, i := range s.order[a:b] {
		if s.hashes[i] == h {
			return int32(i), true
		}
	}
	return none, false
}

// prefix finds the commits whose ids start with the lower-case hex p: one
// of them and how many. The first sixteen digits bound the keys; further
// ones are checked against the hashes in that span, which holds next to
// none.
func (s *store) prefix(p []byte) (match int32, found int) {
	if len(p) == 0 || len(p) > 40 {
		return none, 0
	}
	var lo uint64
	for _, c := range p[:min(len(p), 16)] {
		d, ok := unhex(c)
		if !ok {
			return none, 0
		}
		lo = lo<<4 | uint64(d)
	}
	shift := 64 - 4*min(len(p), 16)
	lo <<= shift
	a, b := s.span(lo, lo|(1<<shift-1))
	match = none
	for _, i := range s.order[a:b] {
		if len(p) <= 16 || hasDigits(&s.hashes[i], p[16:], 16) {
			match = int32(i)
			found++
		}
	}
	return match, found
}

// hasDigits tells whether the hex digits of h from the one at from on
// start with the lower-case hex digits.
func hasDigits(h *hash, digits []byte, from int) bool {
	for j, c := range digits {
		d, ok := unhex(c)
		nibble := h[(from+j)/2]
		if (from+j)%2 == 0 {
			nibble >>= 4
		}
		if !ok || nibble&0xf != d {
			return false
		}
	}
	return true
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// fanoutOf returns where in keys the keys of each value of the leading two
// bytes start, and len(keys) last, as the fanout of git's pack index does.
func fanoutOf(keys []uint64) []uint32 {
	f := make([]uint32, 1<<16+1)
	for _, k := range keys {
		f[k>>48+1]++
	}
	for b := range 1 << 16 {
		f[b+1] += f[b]
	}
	return f
}

// resolve turns a full or abbreviated lower-case id into the one commit it
// names; ok is false when no commit or several do.
func (s *store) resolve(id []byte) (int32, bool) {
	if h, ok := parseHash(id); ok {
		return s.idx(h)
	}
	if match, found := s.prefix(id); found == 1 {
		return match, true
	}
	return none, false
}

// parents are the parents of each commit as indexes: those of commit i
// are list[start[i]:start[i+1]].
type parents struct {
	start, list []uint32
}

func (p *parents) of(i uint32) []uint32 { return p.list[p.start[i]:p.start[i+1]] }

// walk visits the commit from and, depth first, the parents of each commit
// visit returns true for.
func (s *store) walk(from hash, visit func(c uint32) bool) {
	i, ok := s.idx(from)
	if !ok {
		return
	}
	stack := []uint32{uint32(i)}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visit(c) {
			stack = append(stack, s.parents.of(c)...)
		}
	}
}

// index sorts the hashes and turns the pending parent ids into commit
// indexes.
func (s *store) index() {
	// The leading eight bytes order as the whole hash does, so after a
	// sort by them only the runs sharing them, next to none, need the rest.
	n := len(s.hashes)
	keys, order := make([]uint64, n), make([]uint32, n)
	for i := range s.hashes {
		keys[i], order[i] = binary.BigEndian.Uint64(s.hashes[i][:]), uint32(i)
	}
	sortByKey(keys, order)
	for lo := 0; lo < n; {
		hi := lo + 1
		for hi < n && keys[hi] == keys[lo] {
			hi++
		}
		if hi-lo > 1 {
			slices.SortFunc(order[lo:hi], func(a, b uint32) int { return bytes.Compare(s.hashes[a][:], s.hashes[b][:]) })
		}
		lo = hi
	}
	s.order, s.keys, s.fanout = order, keys, fanoutOf(keys)
	p := &s.parents
	p.start = slices.Grow(p.start, len(s.pendingEnd))
	p.list = slices.Grow(p.list, len(s.pending))
	from := uint32(0)
	for _, end := range s.pendingEnd {
		for _, ph := range s.pending[from:end] {
			if j, ok := s.idx(ph); ok {
				p.list = append(p.list, uint32(j))
			}
		}
		p.start = append(p.start, uint32(len(p.list)))
		from = end
	}
	s.pending, s.pendingEnd = nil, nil
}

// reached marks the commits that one of tips reaches.
func (s *store) reached(tips []hash) []bool {
	seen := make([]bool, len(s.hashes))
	for _, t := range tips {
		s.walk(t, func(c uint32) bool {
			if seen[c] {
				return false
			}
			seen[c] = true
			return true
		})
	}
	return seen
}
