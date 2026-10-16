package kgraph

import (
	"strings"
	"testing"
)

// Backports name their upstream commit in several forms; a commit named
// otherwise, as reverted or only partly used, is not one.
func TestNamedUpstream(t *testing.T) {
	const h, h2 = "0123456789abcdef0123456789abcdef01234567", "89abcdef0123456789abcdef0123456789abcdef"
	for _, tt := range []struct{ msg, want string }{
		{"x: fix\n\ncommit " + h + " upstream.\n", h},
		{"x: fix\n\n[ Upstream commit " + h + " ]\n", h},
		{"[ commit " + h + " upstream ]", h},
		{"[ Commit " + h + " upstream ]", h},
		{"commit " + h + " upstream.", h},
		{"commin " + h + " upstream", h},
		{"upstream commit " + h, h},
		{"Upstream commit: " + h, h},
		{"[ Upstream " + h + " commit ]", h},
		{"[ Upstream commits " + h + " and\n  " + h2 + " ]", h},
		{"x: fix\n\nbody\n(cherry picked from commit " + h + ")", h},
		{"(cherry picked from upstream " + h + ")", h},
		{"This reverts commit " + h + ".", ""},
		{"Revert commit " + h2 + " which is commit\n" + h + " upstream.", ""},
		{"Partially based on commit " + h + " upstream.", ""},
		{"commit " + h + " (\"x: y\") broke z", ""},
		{"x: stable only", ""},
	} {
		if got := namedUpstream([]byte(tt.msg)); got != tt.want {
			t.Errorf("namedUpstream(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

// A revert names what it reverts by id; a partial revert reverts no commit
// as a whole.
func TestRevertID(t *testing.T) {
	for _, tt := range []struct{ msg, id string }{
		{"x: undo\n\nThis reverts commit 0123456789ab.", "0123456789ab"},
		{"Revert \"x: add\"\n\nThis reverts commit\n0123456789AB.", "0123456789ab"},
		{"x: tidy\n\nThis partially reverts commit 0123456789ab.", ""},
		{"x: tidy\n\nThis partly reverts 0123456789ab.", ""},
		{"x: tidy\n\nThis partially reverts 0123456789ab and reverts\ncommit 89abcdef0123.", "89abcdef0123"},
		{"Revert \"x: add\"\n\nThis reverts upstream commit 0123456789ab\nfrom stable.", "0123456789ab"},
		{"x: undo\n\nThis reverts mainline commit 0123456789ab.", "0123456789ab"},
	} {
		var lower []byte
		r := parseRecord([]byte(strings.Repeat("1", 40)+"\x1f\x1f1\x1fa@b\x1f1\x1f"+tt.msg), &lower)
		if string(r.revert.id) != tt.id {
			t.Errorf("revert of %q = %q, want %q", tt.msg, r.revert.id, tt.id)
		}
	}
}
