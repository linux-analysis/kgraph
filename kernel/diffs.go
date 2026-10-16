package kernel

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/linux-analysis/kgraph/internal/git"
)

// initialCommit is the 2005 import whose diff is the whole tree; its diff
// is never extracted.
const initialCommit = "1da177e4c3f41524e886b7f1b8a0c1fc7321cac2"

const endMarker = "---END_COMMIT---"

var fullHashRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Diffs returns the whitespace-normalized diff of each commit. Hashes
// absent from the repository are skipped.
func Diffs(ctx context.Context, dir string, hashes []string) (map[string]string, error) {
	present, err := presentCommits(ctx, dir, hashes)
	if err != nil {
		return nil, err
	}
	diffs := map[string]string{}
	for start := 0; start < len(present); start += 500 {
		batch, err := diffsFor(ctx, dir, present[start:min(start+500, len(present))])
		if err != nil {
			return nil, err
		}
		maps.Copy(diffs, batch)
	}
	return diffs, nil
}

// presentCommits returns the full hashes of the commits hashes name, full
// or abbreviated, sorted and without the ones that are missing, ambiguous
// or not commits.
func presentCommits(ctx context.Context, dir string, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	out, err := git.Run(ctx, dir, lines(hashes), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	var present []string
	for row := range strings.Lines(string(out)) {
		if f := strings.Fields(row); len(f) >= 2 && f[1] == "commit" {
			present = append(present, f[0])
		}
	}
	slices.Sort(present)
	return slices.Compact(present), nil
}

func diffsFor(ctx context.Context, dir string, hashes []string) (map[string]string, error) {
	diffs := map[string]string{}
	var wanted []string
	for _, h := range hashes {
		if h != initialCommit {
			wanted = append(wanted, h)
		}
	}
	if len(wanted) == 0 {
		return diffs, nil
	}
	out, err := git.Run(ctx, dir, lines(wanted), "show", "--stdin", "--no-walk=unsorted", "--format="+endMarker+"%H",
		"-p", "--ignore-all-space", "--ignore-space-change", "--ignore-blank-lines")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(string(out), endMarker) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hash, body, _ := strings.Cut(part, "\n")
		hash = strings.TrimSpace(hash)
		if !fullHashRe.MatchString(hash) {
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		diffs[hash] = validUTF8(body)
	}
	return diffs, nil
}

// lines joins hashes one per line for git's --stdin readers.
func lines(hashes []string) []byte {
	return []byte(strings.Join(hashes, "\n") + "\n")
}

func validUTF8(s string) string {
	return strings.ToValidUTF8(s, "�")
}
