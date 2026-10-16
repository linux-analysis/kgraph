package kernel

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
)

// adaptedWorkers bounds the chunks of diffs read at once; each is a git
// process with its own object caches.
const adaptedWorkers = 4

// Adapted tells, for each stable backport of pairs, which maps it to its
// upstream commit, whether it adds or removes other lines than the upstream
// commit: it was reworked for the older code, not only applied at other
// line numbers or among other lines. A backport whose upstream diff the
// repository at dir lacks is left out.
func Adapted(ctx context.Context, dir string, pairs map[string]string) (map[string]bool, error) {
	// The diffs are read a chunk at a time, as all of them take hundreds of
	// megabytes. Sorted by upstream, the backports of one fix share a chunk.
	backports := slices.SortedFunc(maps.Keys(pairs), func(a, b string) int {
		return cmp.Or(strings.Compare(pairs[a], pairs[b]), strings.Compare(a, b))
	})
	adapted := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, adaptedWorkers)
	for start := 0; start < len(backports); start += 1000 {
		chunk := backports[start:min(start+1000, len(backports))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if cctx.Err() != nil {
				return
			}
			want := slices.Clone(chunk)
			for _, h := range chunk {
				want = append(want, pairs[h])
			}
			diffs, err := Diffs(cctx, dir, want)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// The first error ends the other chunks; theirs then
				// say only that the context ended.
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			for _, h := range chunk {
				if up, ok := diffs[pairs[h]]; ok {
					adapted[h] = withoutPaths(changedLines(diffs[h])) != withoutPaths(changedLines(up))
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return adapted, nil
}

// ChangedLines returns the lines each commit adds and removes, as
// changedLines gives them; a commit without a diff, or that the repository
// at dir lacks, has none. A part of the commits goes to each worker.
func ChangedLines(ctx context.Context, dir string, hashes []string) (map[string]string, error) {
	lines := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, adaptedWorkers)
	size := (len(hashes) + adaptedWorkers - 1) / adaptedWorkers
	for w := range adaptedWorkers {
		part := hashes[min(w*size, len(hashes)):min((w+1)*size, len(hashes))]
		wg.Go(func() {
			diffs, err := Diffs(ctx, dir, part)
			errs[w] = err
			mu.Lock()
			defer mu.Unlock()
			for h, d := range diffs {
				lines[h] = changedLines(d)
			}
		})
	}
	wg.Wait()
	return lines, errors.Join(errs...)
}

// changedLines returns the lines diff adds and removes, without whitespace,
// in an order that does not depend on where the hunks fall: by file, then
// sorted.
func changedLines(diff string) string {
	byFile := map[string][]string{}
	file, inHunk := "", false
	for line := range strings.Lines(diff) {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, inHunk = strings.TrimSpace(line[strings.LastIndex(line, " b/")+1:]), false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && (line[0] == '+' || line[0] == '-'):
			byFile[file] = append(byFile[file], line[:1]+strings.Join(strings.Fields(line[1:]), ""))
		}
	}
	var b strings.Builder
	for _, f := range slices.Sorted(maps.Keys(byFile)) {
		lines := byFile[f]
		slices.Sort(lines)
		b.WriteString(f + "\n" + strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}

// withoutPaths drops the paths of the files from what changedLines
// returns, so that a backport to a file that moved since changes the same
// lines. Copies keep them: one patch applied to two copies of a file is
// two patches.
func withoutPaths(lines string) string {
	var out []string
	for line := range strings.Lines(lines) {
		if line[0] == '+' || line[0] == '-' {
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}
