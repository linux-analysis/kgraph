// Package kernel is the Linux kernel tree as the commit graph reads it:
// an existing clone, bare or not, with its mainline and stable remotes,
// the release tags and the stable series, and the diffs of its commits.
// It never clones or fetches the tree: keeping it up to date is the
// caller's job. It drives the git command line through kgraph/internal/git
// and depends on the standard library only.
package kernel

import (
	"context"
	"regexp"
	"strings"

	"github.com/linux-analysis/kgraph/internal/git"
)

// Remotes names the remotes that track mainline and stable.
type Remotes struct {
	Mainline string `json:"mainline"`
	Stable   string `json:"stable"`
}

// MainlineRef is the ref that defines mainline membership.
func (r Remotes) MainlineRef() string {
	return "refs/remotes/" + r.Mainline + "/master"
}

// Remote URLs of the mainline and stable trees, in URL and scp-style
// ("git@host:path") form; the stable tree also goes by linux-stable.
var (
	mainlineURLRe = regexp.MustCompile(`[/:]torvalds/linux(\.git)?$`)
	stableURLRe   = regexp.MustCompile(`[/:]stable/linux(-stable)?(\.git)?$`)
)

// FindRemotes finds the remotes for torvalds/linux and stable/linux by
// URL, falling back to remotes literally named mainline and stable; a
// remote it does not find is left empty.
func FindRemotes(ctx context.Context, dir string) (Remotes, error) {
	out, err := git.Run(ctx, dir, nil, "remote", "-v")
	if err != nil {
		return Remotes{}, err
	}
	names := map[string]bool{}
	var rem Remotes
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (len(fields) > 2 && fields[2] != "(fetch)") {
			continue
		}
		name, url := fields[0], strings.TrimSuffix(fields[1], "/")
		names[name] = true
		switch {
		case mainlineURLRe.MatchString(url):
			if rem.Mainline == "" {
				rem.Mainline = name
			}
		case stableURLRe.MatchString(url):
			if rem.Stable == "" {
				rem.Stable = name
			}
		}
	}
	if rem.Mainline == "" && names["mainline"] {
		rem.Mainline = "mainline"
	}
	if rem.Stable == "" && names["stable"] {
		rem.Stable = "stable"
	}
	return rem, nil
}
