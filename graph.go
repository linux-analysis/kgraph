// Package kgraph builds the commit graph of a kernel tree in memory:
// every commit of mainline, of the stable linux-*.y branches, of the v*
// release tags, of linux-next and of the vendor branches when the tree has
// them, with the first tag that contains it, the mainline commit a
// backport was made from, and the commits its Fixes: tags and revert line
// name.
//
// A build runs in stages, each making what the next one reads:
//
//   - Discover reads the refs into a Layout: the trees, each of a kind,
//     and the release tags.
//   - The store reads the commits the trees reach with one git log, and the
//     facts their messages state, the same in every tree; a cache file
//     keeps it, so that the next build reads only the commits new since.
//   - The topology tells which trees reach each commit, its first tag and
//     which commits are its ancestors.
//   - The passes resolve what the facts name by what the topology tells:
//     the copies of each patch, reverts, upstreams, Fixes: and backports.
//     derive runs them in the order their inputs ask for. Twins that
//     descend from one another are copies when they change the same
//     lines, which the store reads for them alone and the cache keeps.
//   - The questions go through handles: a Commit, and its Patch, the change
//     itself, whose presence in a series or a tree one replay of its
//     timeline tells.
package kgraph

import (
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"time"
)

type hash [20]byte

func (h hash) String() string { return hex.EncodeToString(h[:]) }

func parseHash(s []byte) (hash, bool) {
	var h hash
	if len(s) != 40 {
		return h, false
	}
	_, err := hex.Decode(h[:], s)
	return h, err == nil
}

const none = -1 // no commit, tag or series

// Graph holds every commit the trees of its layout reach. It is read-only
// once built, so it is safe for concurrent use.
type Graph struct {
	*store
	topology
	patches
	upstream  []int32  // the mainline commit each backport was made from, or none
	revert    []int32  // the commit each commit reverts, or none
	fixes     []uint32 // resolved Fixes: targets, grouped by commit
	fstart    []uint32
	reverters []uint32 // commits grouped by the commit they revert, oldest first
	rstart    []uint32
	backports []uint32 // backports under the canonical copy of their patch, in version order
	bstart    []uint32
	fixedBy   []uint32 // patches under the patches their Fixes: tags name, oldest first
	fbstart   []uint32

	layout *Layout
}

// Options tune a build.
type Options struct {
	// Cache is the file that keeps the commits a build read for the next
	// one, or "" for none. A build from it reads only the commits that are
	// new since and drops those no branch or tag reaches any more, as after
	// a branch was rewound; one it does not fit, as another tree's, reads
	// them all again. The build maps the file and the graph refers into it,
	// so the mapping lasts as long as the process.
	Cache string
	// Fixes is a file of Fixes: ids that name no commit and the commits
	// they mean, as verhaal's fixes.txt, or "" for none.
	Fixes string
	// NoStable leaves the stable branches, the stable release tags and the
	// vendor branches, which bring stable commits along, out, so that the
	// graph holds mainline alone, and linux-next when the tree has it.
	NoStable bool
	// Next is the linux-next ref to read instead of the master branch of
	// the next remote, such as next-20261001 or next/pending-fixes; it
	// needs a next remote.
	Next string
}

// Build reads the graph of the trees of l.
func (l *Layout) Build(ctx context.Context, log *slog.Logger) (*Graph, error) {
	if log == nil {
		log = slog.Default()
	}
	start := time.Now()
	fixups, err := readFixups(l.opts.Fixes)
	if err != nil {
		return nil, err
	}
	cache := l.opts.Cache
	if l.shallow {
		log.Warn("the kernel tree is shallow: commits before its cut are not in the commit graph", "dir", l.dir)
		// Deepening the tree would bring commits older than the cached
		// tips, which a build from the cache never reads.
		cache = ""
	}
	tips := l.tips()
	s := newStore()
	if cache != "" {
		switch c, err := loadStore(cache); {
		case err == nil:
			s = c
		case !errors.Is(err, fs.ErrNotExist):
			log.Warn("commit graph cache unusable, reading every commit", "path", cache, "err", err)
		}
	}
	cached := len(s.hashes)
	streamed, dropped, err := s.update(ctx, l.dir, tips)
	if err != nil && cached > 0 && ctx.Err() == nil {
		log.Warn("commit graph cache does not fit the kernel tree, reading every commit", "path", cache, "err", err)
		s.release()
		s, cached = newStore(), 0
		streamed, dropped, err = s.update(ctx, l.dir, tips)
	}
	if err != nil {
		return nil, err
	}
	g := &Graph{store: s, topology: newTopology(s, l), layout: l}
	p := newPass(s, &g.topology, fixups)
	twins := findTwins(p)
	read, err := s.readLines(ctx, l.dir, onALine(twins))
	if err != nil {
		return nil, err
	}
	if cache != "" && (streamed > 0 || dropped > 0 || read > 0) {
		if err := s.save(cache); err != nil {
			log.Warn("write commit graph cache", "path", cache, "err", err)
		}
	}
	st := g.derive(p, twins)
	log.Info("commit graph built", "commits", len(g.hashes), "cached", cached, "streamed", streamed, "dropped", dropped,
		"mainline", st.mainline, "tags", len(g.tags), "series", l.series, "upstream_named", st.upstream.named,
		"upstream_by_author", st.upstream.identity, "upstream_by_subject", st.upstream.subject, "upstream_rejected_reverts", st.upstream.rejected,
		"fixes", st.fixes.fixes, "fixes_by_table", st.fixes.tabled, "fixes_by_subject", st.fixes.rescued, "fixes_unresolved", st.fixes.unresolved,
		"reverts_by_subject", st.revertsBySubject, "twins", st.twins,
		"malformed", s.malformed, "duration", time.Since(start).Round(time.Millisecond))
	return g, nil
}
