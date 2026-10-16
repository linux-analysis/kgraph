package kernel

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Release is a mainline kernel release ordered by number: v2.6.32 is
// 2006032 and v5.10 is 5010000. The zero value is no release.
type Release int

// Unreleased stands for commits that no release tag contains yet.
const Unreleased Release = math.MaxInt32

// ParseRelease reads a tag or version such as v6.12, v6.12-rc3, 6.12.107 or
// v2.6.32; stable and -rc suffixes are dropped. It returns 0 for anything
// else, a 2.x release other than 2.6 too, as ParseTag does.
func ParseRelease(s string) Release {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	n := make([]int, 0, 3)
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 999 {
			return 0
		}
		n = append(n, v)
	}
	switch {
	case len(n) >= 3 && n[0] == 2 && n[1] == 6:
		return Release(2*1000000 + n[1]*1000 + n[2])
	case len(n) >= 2 && n[0] >= 3:
		return Release(n[0]*1000000 + n[1]*1000)
	}
	return 0
}

func (r Release) String() string {
	switch {
	case r == Unreleased:
		return "unreleased"
	case r == 0:
		return ""
	case r/1000000 == 2:
		return fmt.Sprintf("v2.%d.%d", int(r)/1000%1000, int(r)%1000)
	}
	return fmt.Sprintf("v%d.%d", int(r)/1000000, int(r)/1000%1000)
}

// Tag is a release tag: a mainline release or release candidate, such as
// v6.6, v6.6-rc1 or v2.6.32, or a stable release, a patch level on top of
// a mainline release, such as v6.6.8 or v2.6.32.5.
type Tag struct {
	Name    string
	Release Release // the mainline release: v6.6 for v6.6-rc1 and for v6.6.8
	Stable  bool
	RC      int // the release candidate of a mainline -rc tag, else 0
	patch   int // the patch level of a stable tag
}

var tagRe = regexp.MustCompile(`^v(\d+)\.(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-rc(\d+))?$`)

// ParseTag reads a release tag; stable -rc tags and other names are none.
func ParseTag(name string) (Tag, bool) {
	m := tagRe.FindStringSubmatch(name)
	if m == nil {
		return Tag{}, false
	}
	var n []int
	for _, d := range m[1:5] {
		if d == "" {
			break
		}
		v, err := strconv.Atoi(d)
		if err != nil || v > 999 {
			return Tag{}, false
		}
		n = append(n, v)
	}
	t := Tag{Name: name}
	t.RC, _ = strconv.Atoi(m[5])
	switch {
	case n[0] == 2 && n[1] == 6 && len(n) == 3:
	case n[0] == 2 && n[1] == 6 && len(n) == 4 && t.RC == 0:
		t.Stable, t.patch = true, n[3]
	case n[0] >= 3 && len(n) == 2:
	case n[0] >= 3 && len(n) == 3 && t.RC == 0:
		t.Stable, t.patch = true, n[2]
	default:
		return Tag{}, false
	}
	if n[0] == 2 {
		t.Release = Release(2*1000000 + n[1]*1000 + n[2])
	} else {
		t.Release = Release(n[0]*1000000 + n[1]*1000)
	}
	return t, true
}

// Compare orders tags by release, a mainline release after its -rc tags
// and the stable releases on top of it after both, by patch level.
func (t Tag) Compare(u Tag) int {
	final := func(t Tag) int {
		if t.RC == 0 {
			return math.MaxInt
		}
		return t.RC
	}
	return cmp.Or(cmp.Compare(t.Release, u.Release), cmp.Compare(final(t), final(u)), cmp.Compare(t.patch, u.patch))
}

// ReleasesURL lists the releases kernel.org supports.
const ReleasesURL = "https://www.kernel.org/releases.json"

// Series is a stable series kernel.org supports.
type Series struct {
	Name     string `json:"name"` // such as 6.6
	Longterm bool   `json:"longterm"`
}

// ParseReleases reads the series of the stable and long-term releases in
// kernel.org's releases.json that have not reached their end of life. A
// file without any is an error, so that a change of format cannot empty a
// stored list.
func ParseReleases(body []byte) ([]Series, error) {
	var doc struct {
		Releases []struct {
			Moniker string `json:"moniker"`
			Version string `json:"version"`
			EOL     bool   `json:"iseol"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var series []Series
	for _, r := range doc.Releases {
		if s := strings.TrimPrefix(ParseRelease(r.Version).String(), "v"); s != "" && !r.EOL && (r.Moniker == "stable" || r.Moniker == "longterm") {
			series = append(series, Series{Name: s, Longterm: r.Moniker == "longterm"})
		}
	}
	if len(series) == 0 {
		return nil, errors.New("no supported stable series")
	}
	return series, nil
}
