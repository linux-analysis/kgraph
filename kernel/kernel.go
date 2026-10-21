// Package kernel is the Linux kernel tree as the commit graph reads it:
// an existing clone, bare or not, with its mainline and stable remotes,
// the release tags and the stable series, and the subjects and diffs of
// its commits. It never clones or fetches the tree: keeping it up
// to date is the caller's job. It drives the git command line through
// kgraph/internal/git and depends on the standard library only.
package kernel

import (
	"context"
	"regexp"
	"strings"

	"github.com/linux-analysis/kgraph/internal/git"
)

// Remotes names the remotes that track mainline, stable and linux-next,
// Next being "" when the tree has no linux-next, and the remotes of vendor
// trees, in name order.
type Remotes struct {
	Mainline string   `json:"mainline"`
	Stable   string   `json:"stable"`
	Next     string   `json:"next,omitempty"`
	Vendors  []string `json:"vendors,omitempty"`
}

// MainlineRef is the ref that defines mainline membership.
func (r Remotes) MainlineRef() string {
	return "refs/remotes/" + r.Mainline + "/master"
}

// NextRef is the linux-next branch, rebuilt daily.
func (r Remotes) NextRef() string {
	return "refs/remotes/" + r.Next + "/master"
}

// Remote URLs of the mainline, stable and linux-next trees, in URL and
// scp-style ("git@host:path") form; the stable tree also goes by
// linux-stable. linux-next is found by URL only: a remote merely named
// next may be anything, and a next remote whose branch is missing fails
// the build.
var (
	mainlineURLRe = regexp.MustCompile(`[/:]torvalds/linux(\.git)?$`)
	stableURLRe   = regexp.MustCompile(`[/:]stable/linux(-stable)?(\.git)?$`)
	nextURLRe     = regexp.MustCompile(`[/:]linux-next(\.git)?$`)
	// The vendor trees: Oracle UEK, SUSE, CentOS Stream, Android common
	// and the Ubuntu kernel of one series.
	vendorURLRe = regexp.MustCompile(`(?:github\.com[/:](?:oracle/linux-uek|SUSE/kernel)|gitlab\.com[/:]redhat/centos-stream/src/kernel/centos-stream-\d+|android\.googlesource\.com/kernel/common)(\.git)?$|launchpad\.net/~ubuntu-kernel/ubuntu/\+source/linux[^/]*/\+git/[^/]+$`)
)

// FindRemotes finds the remotes for torvalds/linux, stable/linux,
// linux-next and the vendor trees by URL, falling back to remotes literally
// named mainline and stable; a remote it does not find is left empty.
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
		case nextURLRe.MatchString(url):
			if rem.Next == "" {
				rem.Next = name
			}
		case vendorURLRe.MatchString(url):
			rem.Vendors = append(rem.Vendors, name)
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
