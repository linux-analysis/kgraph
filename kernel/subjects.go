package kernel

import (
	"context"
	"strings"

	"github.com/linux-analysis/kgraph/internal/git"
)

// Subjects returns the subject of each commit that exists in the
// repository at dir, as git log's %s gives it: the first paragraph of the
// message on one line.
func Subjects(ctx context.Context, dir string, hashes []string) (map[string]string, error) {
	subjects := map[string]string{}
	present, err := presentCommits(ctx, dir, hashes)
	if err != nil || len(present) == 0 {
		return subjects, err
	}
	out, err := git.Run(ctx, dir, lines(present), "log", "--stdin", "--no-walk=unsorted", "--format=%H%x00%s")
	if err != nil {
		return nil, err
	}
	for line := range strings.Lines(string(out)) {
		if h, s, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "\x00"); ok && len(h) == 40 {
			subjects[h] = validUTF8(s)
		}
	}
	return subjects, nil
}
