package kgraph

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"runtime"
	"strconv"
	"sync"

	"github.com/linux-analysis/kgraph/internal/git"
)

// logFormat gives each commit its hash, parents, committer date, author
// email, author date and raw message, separated by 0x1f; the subject is
// taken from the message, which may hold 0x1f itself.
const logFormat = "--format=%H%x1f%P%x1f%ct%x1f%ae%x1f%at%x1f%B"

var (
	// parseWorkers bounds the goroutines parsing records; git log itself
	// takes one core.
	parseWorkers = max(2, runtime.NumCPU()-1)
	// batchSize is the number of records a worker parses at once.
	batchSize = 2000
	// maxRecord bounds one commit's record; the largest kernel commit
	// message is far below it.
	maxRecord = 64 << 20
)

// record is one parsed git log entry.
type record struct {
	hash     hash
	parents  []hash
	date     int64
	ident    uint64
	twin     uint64
	subject  uint64
	named    hash
	hasNamed bool
	weak     []hash
	revert   rawFix
	fixes    []rawFix
	bad      bool
}

// sum hashes parts, joined by NULs, with 64-bit FNV-1a, so equal subjects
// and identities get equal sums in every process.
func sum(parts ...[]byte) uint64 {
	h := fnv.New64a()
	for i, b := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write(b)
	}
	return h.Sum64()
}

// parseRecord splits one record and runs the message parsers; lower is a
// scratch buffer it reuses.
func parseRecord(rec []byte, lower *[]byte) record {
	var r record
	f := bytes.SplitN(rec, []byte{0x1f}, 6)
	if len(f) != 6 {
		r.bad = true
		return r
	}
	var ok bool
	if r.hash, ok = parseHash(f[0]); !ok {
		r.bad = true
		return r
	}
	for p := range bytes.FieldsSeq(f[1]) {
		if ph, ok := parseHash(p); ok {
			r.parents = append(r.parents, ph)
		}
	}
	r.date, _ = strconv.ParseInt(string(f[2]), 10, 64)
	msg := f[5]
	subject := plainSubject(subjectOf(msg))
	r.subject = sum(subject)
	// One author writes their email address in more than one case.
	email := bytes.ToLower(f[3])
	r.ident = sum(email, subject)
	r.twin = sum(email, f[4], subject)
	*lower = append((*lower)[:0], msg...)
	for i, c := range *lower {
		if 'A' <= c && c <= 'Z' {
			(*lower)[i] = c + 'a' - 'A'
		}
	}
	parseMessage(&r, msg, *lower)
	return r
}

// batch is a run of records, kept in one buffer, and their parse.
type batch struct {
	seq  int
	buf  []byte
	ends []int
	out  []record
}

// stream runs one git log over the commits tips reach and seen does not,
// and parses the records in a pool of workers, adding them to s in stream
// order.
func (s *store) stream(ctx context.Context, dir string, tips, seen []hash) error {
	var stdin bytes.Buffer
	for _, t := range tips {
		stdin.WriteString(t.String() + "\n")
	}
	for _, t := range seen {
		stdin.WriteString("^" + t.String() + "\n")
	}
	read := func(r io.Reader) error {
		in := make(chan *batch, parseWorkers)
		done := make(chan *batch, parseWorkers)
		var wg sync.WaitGroup
		for range parseWorkers {
			wg.Go(func() {
				var lower []byte
				for b := range in {
					b.out = make([]record, len(b.ends))
					start := 0
					for i, end := range b.ends {
						b.out[i] = parseRecord(b.buf[start:end], &lower)
						start = end
					}
					b.buf = nil
					done <- b
				}
			})
		}
		collected := make(chan struct{})
		go func() {
			defer close(collected)
			pending := map[int]*batch{}
			next := 0
			for b := range done {
				pending[b.seq] = b
				for pending[next] != nil {
					for _, rec := range pending[next].out {
						s.add(rec)
					}
					delete(pending, next)
					next++
				}
			}
		}()

		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, min(1<<20, maxRecord)), maxRecord)
		sc.Split(splitNUL)
		cur := &batch{}
		for sc.Scan() {
			cur.buf = append(cur.buf, sc.Bytes()...)
			cur.ends = append(cur.ends, len(cur.buf))
			if len(cur.ends) == batchSize {
				next := &batch{seq: cur.seq + 1, buf: make([]byte, 0, cap(cur.buf))}
				in <- cur
				cur = next
			}
		}
		if len(cur.ends) > 0 {
			in <- cur
		}
		close(in)
		wg.Wait()
		close(done)
		<-collected
		if err := sc.Err(); err != nil {
			return fmt.Errorf("read git log: %w", err)
		}
		return nil
	}
	return git.Stream(ctx, dir, stdin.Bytes(), read,
		"--no-replace-objects", "-c", "log.showSignature=false", "log", "-z", "--stdin", "--no-use-mailmap", logFormat)
}

// add appends one parsed record, unless s has its commit.
func (s *store) add(r record) {
	if r.bad {
		s.malformed++
		return
	}
	if _, known := s.idx(r.hash); known {
		return
	}
	i := uint32(len(s.hashes))
	s.hashes = append(s.hashes, r.hash)
	s.date = append(s.date, r.date)
	s.pending = append(s.pending, r.parents...)
	s.pendingEnd = append(s.pendingEnd, uint32(len(s.pending)))
	f := &s.facts
	f.ident = append(f.ident, r.ident)
	f.twin = append(f.twin, r.twin)
	f.subject = append(f.subject, r.subject)
	if r.hasNamed {
		f.named = append(f.named, namedRef{i, r.named})
	}
	for _, w := range r.weak {
		f.weak = append(f.weak, namedRef{i, w})
	}
	if r.revert.id != nil {
		f.reverts = append(f.reverts, fixRef{i, r.revert})
	}
	for _, fx := range r.fixes {
		f.fixes = append(f.fixes, fixRef{i, fx})
	}
}

func splitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
