package kgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/linux-analysis/kgraph/internal/git"
	"github.com/linux-analysis/kgraph/kernel"
)

// Layout is the shape of a kernel tree as a build reads it, found from its
// refs alone: the trees whose commits the graph tells apart and the release
// tags. Discover makes it and Build reads the commits it names.
type Layout struct {
	dir     string
	opts    Options
	trees   []tree // mainline first; a tree's index is its bit of reach
	tags    []tag  // in version order
	series  int    // the stable branches, for the build log
	shallow bool
}

// treeKind is what a tree is to the graph.
type treeKind uint8

const (
	mainlineTree treeKind = iota
	stableTree
)

// tree is a group of tips whose commits share a name: the mainline branch
// and tags, or the stable branches and tags.
type tree struct {
	name string
	kind treeKind
	tips []hash
}

// tag is a release tag that points at a commit.
type tag struct {
	kernel.Tag
	commit hash
}

// Discover reads the layout of the kernel tree at dir. Its tips are the
// mainline ref of rem, the linux-*.y branches of rem's stable remote and
// the v* tags; without a mainline remote HEAD stands for mainline, and
// without a stable remote only the tags reach stable commits. A mainline
// remote whose ref is missing, as when its first fetch failed, is an
// error: HEAD may be a stable branch. A commit that only other refs reach
// is not in the graph.
func Discover(ctx context.Context, dir string, rem kernel.Remotes, o Options) (*Layout, error) {
	mainRef := "HEAD"
	if rem.Mainline != "" {
		mainRef = rem.MainlineRef()
	}
	tip, err := tipOf(ctx, dir, mainRef)
	if err != nil {
		return nil, err
	}
	l := &Layout{dir: dir, opts: o}
	var branches []hash
	if rem.Stable != "" {
		refs, err := listRefs(ctx, dir, "refs/remotes/"+rem.Stable+"/linux-*.y")
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			series := strings.TrimSuffix(strings.TrimPrefix(r.name, rem.Stable+"/linux-"), ".y")
			if t, ok := kernel.ParseTag("v" + series); ok && t.RC == 0 {
				branches = append(branches, r.commit)
			}
		}
	}
	l.series = len(branches)
	if l.tags, err = listTags(ctx, dir); err != nil {
		return nil, err
	}
	// Mainline is what the mainline tip or a mainline tag reaches, so a
	// mainline ref behind the tags fetched with the stable branches does not
	// make mainline commits stable.
	mainline, stable := []hash{tip}, branches
	for _, t := range l.tags {
		if t.Stable {
			stable = append(stable, t.commit)
		} else {
			mainline = append(mainline, t.commit)
		}
	}
	l.trees = []tree{{"mainline", mainlineTree, mainline}, {"stable", stableTree, stable}}
	out, err := git.Run(ctx, dir, nil, "rev-parse", "--is-shallow-repository")
	l.shallow = err == nil && strings.TrimSpace(string(out)) == "true"
	return l, nil
}

// tips are the tips of the trees.
func (l *Layout) tips() []hash {
	var tips []hash
	for _, t := range l.trees {
		tips = append(tips, t.tips...)
	}
	return tips
}

// Tags are the release tags of the layout, in version order.
func (l *Layout) Tags() []kernel.Tag {
	tags := make([]kernel.Tag, len(l.tags))
	for i, t := range l.tags {
		tags[i] = t.Tag
	}
	return tags
}

// Target is what In answers for: a stable series, such as 6.6.
type Target struct {
	Name   string
	series kernel.Release // the release the series branches from
}

// Targets reads names as stable series, such as 6.6 or v6.6, and fails on
// any other name, a release such as 6.6.30 too.
func (l *Layout) Targets(names []string) ([]Target, error) {
	ts := make([]Target, len(names))
	for i, name := range names {
		st, ok := kernel.ParseStable(name)
		if !ok {
			return nil, fmt.Errorf("%q is not a stable series", name)
		}
		ts[i] = Target{Name: st.Series, series: st.Release()}
	}
	return ts, nil
}

// tipOf resolves ref to the commit it names.
func tipOf(ctx context.Context, dir, ref string) (hash, error) {
	out, err := git.Run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if ctx.Err() != nil {
			return hash{}, ctx.Err()
		}
		if errors.As(err, new(*exec.ExitError)) {
			return hash{}, fmt.Errorf("kernel tree %s has no %s", dir, ref) // --quiet: git tells nothing more
		}
		return hash{}, fmt.Errorf("kernel tree %s has no %s: %w", dir, ref, err)
	}
	h, ok := parseHash(bytes.TrimSpace(out))
	if !ok {
		return hash{}, fmt.Errorf("bad %s %q", ref, out)
	}
	return h, nil
}

// ref is a branch or a tag: its name after refs/remotes/ or refs/tags/ and
// its commit.
type ref struct {
	name   string
	commit hash
}

// listRefs reads the refs that match the for-each-ref pattern and point at
// a commit, directly or through an annotated tag: the walks over the graph
// know commits only, and v2.6.11 is a tag of a tree.
func listRefs(ctx context.Context, dir, pattern string) ([]ref, error) {
	out, err := git.Run(ctx, dir, nil, "for-each-ref",
		"--format=%(objecttype) %(objectname) %(*objecttype) %(*objectname) %(refname:lstrip=2)", pattern)
	if err != nil {
		return nil, err
	}
	var refs []ref
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) == 5 && f[0] == "tag" {
			f = f[2:]
		}
		if len(f) != 3 || f[0] != "commit" {
			continue
		}
		if h, ok := parseHash([]byte(f[1])); ok {
			refs = append(refs, ref{f[2], h})
		}
	}
	return refs, nil
}

// listTags reads the v* tags that are releases and point at a commit,
// directly or through an annotated tag, in version order.
func listTags(ctx context.Context, dir string) ([]tag, error) {
	refs, err := listRefs(ctx, dir, "refs/tags/v*")
	if err != nil {
		return nil, err
	}
	var list []tag
	for _, r := range refs {
		if t, ok := kernel.ParseTag(r.name); ok {
			list = append(list, tag{t, r.commit})
		}
	}
	slices.SortFunc(list, func(a, b tag) int { return a.Compare(b.Tag) })
	return list, nil
}
