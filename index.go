package kgraph

import (
	"bytes"
	"cmp"
	"slices"
)

// keyed indexes commits by a key, such as an identity or subject hash or
// another commit: the keys in order, each with its commit at the same
// position.
type keyed struct {
	keys    []uint64
	commits []uint32
}

// keyIndex lists the commits in marks by their keys.
func keyIndex(keys []uint64, in []bool) keyed {
	n := 0
	for _, m := range in {
		if m {
			n++
		}
	}
	idx := keyed{make([]uint64, 0, n), make([]uint32, 0, n)}
	for i, m := range in {
		if m {
			idx.keys, idx.commits = append(idx.keys, keys[i]), append(idx.commits, uint32(i))
		}
	}
	sortByKey(idx.keys, idx.commits)
	return idx
}

// withKey returns the commits of idx under key.
func withKey(idx keyed, key uint64) []uint32 {
	lo, _ := slices.BinarySearch(idx.keys, key)
	hi := lo
	for hi < len(idx.keys) && idx.keys[hi] == key {
		hi++
	}
	return idx.commits[lo:hi]
}

// sortByKey orders keys and vals alongside, a radix sort over the four
// 16-bit digits: the sorts of the standard library call a comparator per
// step, which over two million keys is most of a build. Four passes land
// the result back in keys and vals.
func sortByKey(keys []uint64, vals []uint32) {
	k2, v2 := make([]uint64, len(keys)), make([]uint32, len(vals))
	var count [1 << 16]uint32
	for shift := 0; shift < 64; shift += 16 {
		clear(count[:])
		for _, k := range keys {
			count[uint16(k>>shift)]++
		}
		pos := uint32(0)
		for d, c := range count {
			count[d], pos = pos, pos+c
		}
		for i, k := range keys {
			d := uint16(k >> shift)
			k2[count[d]], v2[count[d]] = k, vals[i]
			count[d]++
		}
		keys, k2, vals, v2 = k2, keys, v2, vals
	}
}

// group collects the values each pair of edges emits under its key: those
// of key k are list[start[k]:start[k+1]], ordered by less.
func group(n int, edges func(emit func(key, v uint32)), less func(a, b uint32) int) (list, start []uint32) {
	start = make([]uint32, n+1)
	edges(func(key, _ uint32) { start[key+1]++ })
	for k := range n {
		start[k+1] += start[k]
	}
	list = make([]uint32, start[n])
	fill := slices.Clone(start[:n])
	edges(func(key, v uint32) {
		list[fill[key]] = v
		fill[key]++
	})
	for k := range n {
		slices.SortFunc(list[start[k]:start[k+1]], less)
	}
	return list, start
}

// compactGroups drops the repeats of a value within each group that group
// made, in place: those of a group lie together, as its order sorts them.
func compactGroups(list, start []uint32) ([]uint32, []uint32) {
	out := list[:0]
	from := start[0]
	for k := range len(start) - 1 {
		run := list[from:start[k+1]]
		from = start[k+1]
		start[k] = uint32(len(out))
		for j, v := range run {
			if j == 0 || v != run[j-1] {
				out = append(out, v)
			}
		}
	}
	start[len(start)-1] = uint32(len(out))
	return out, start
}

func filled(n int) []int32 {
	s := make([]int32, n)
	for i := range s {
		s[i] = none
	}
	return s
}

// byDate orders commits by committer date, then by hash.
func (s *store) byDate(a, b uint32) int {
	return cmp.Or(cmp.Compare(s.date[a], s.date[b]), bytes.Compare(s.hashes[a][:], s.hashes[b][:]))
}
