package kgraph

// facts are what the commits state, the same in every tree, so that the
// cache keeps them: what their messages state, as the parsers read them,
// and for the twins that descend from one another, the lines they change.
// The ids are as written; the passes resolve them once every commit is
// known. A change to what the parsers read changes parserDigest, which a
// test ties to cacheVersion.
type facts struct {
	ident   []uint64 // author email in lower case and subject, per commit
	twin    []uint64 // the same and author date, per commit
	subject []uint64 // subject, per commit
	named   []namedRef
	lines   []namedRef // the SHA-1 of the lines a commit changes, as kernel.ChangedLines gives them, for the twins readLines read
	reverts []fixRef   // one per commit, in stream order
	fixes   []fixRef   // grouped by commit in stream order
}

// namedRef is a commit and the full upstream id its message names.
type namedRef struct {
	commit uint32
	id     hash
}

// fixRef is a commit and an id its message names.
type fixRef struct {
	commit uint32
	rawFix
}

// keyCols, namedCols and refCols are the columns of f: with one value per
// commit, with the upstream ids, and with the other ids the messages name,
// each in the order of the cache file.
func (f *facts) keyCols() []*[]uint64 {
	return []*[]uint64{&f.ident, &f.twin, &f.subject}
}

func (f *facts) namedCols() []*[]namedRef {
	return []*[]namedRef{&f.named, &f.lines}
}

func (f *facts) refCols() []*[]fixRef {
	return []*[]fixRef{&f.reverts, &f.fixes}
}
