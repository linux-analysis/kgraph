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
		{"Commit: " + h + " upstream", h},
		{"comit " + h + " upstream.", h},
		{"[ upstream commmit " + h + " ]", h},
		{"This is upstream commit " + h + ".", h},
		{"[ Backport of upstream commit " + h + " ]", h},
		{h + " upstream.", ""},
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

// The weak forms name an upstream commit in a vendor tree: Red Hat's
// quoted header and Ubuntu's backported-from line; not the strong forms,
// which namedUpstream takes first.
func TestWeakUpstream(t *testing.T) {
	const h = "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct{ msg, want string }{
		{"x: fix\n\nJIRA: https://issues.redhat.com/browse/RHEL-1\n\ncommit " + h + "\nAuthor: A <a@example.com>\nDate:   Mon Jan 1 00:00:00 2024 +0000\n\n    x: fix", h},
		{"x: fix\n\n(backported from commit " + h + ")", h},
		{"x: fix\n\n(backported from commit " + h + " linux-6.6.y)", h},
		{"commit " + h + " broke it", ""},
		{"x: fix\n\n(cherry picked from commit " + h + ")", ""},
	} {
		if got := matchedID(weakRe, []byte(tt.msg)); got != tt.want {
			t.Errorf("weak upstream of %q = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

// plainSubject strips Android's prefixes and SUSE's reference suffix,
// nothing else.
func TestPlainSubject(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"net: fix foo", "net: fix foo"},
		{"UPSTREAM: net: fix foo", "net: fix foo"},
		{"BACKPORT: FROMGIT: net: fix foo", "net: fix foo"},
		{"ANDROID: GKI: add symbols", "GKI: add symbols"},
		{"android: binder: fix", "android: binder: fix"},
		{"net: fix foo (bsc#1234567).", "net: fix foo"},
		{"net: fix foo (git-fixes).", "net: fix foo"},
		{"net: fix foo (CVE-2024-1 bsc#2).", "net: fix foo"},
		{"net: fix foo (part 2)", "net: fix foo (part 2)"},
		{"net: fix (foo) bar.", "net: fix (foo) bar."},
	} {
		if got := string(plainSubject([]byte(tt.in))); got != tt.want {
			t.Errorf("plainSubject(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A Revert "..." subject quotes what it reverts; other subjects quote
// nothing.
func TestRevertedSubject(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{`Revert "net: fix foo"`, "net: fix foo"},
		{`Revert "Revert "net: fix foo""`, `Revert "net: fix foo"`},
		{`Revert "net: fix foo" (1/6)`, ""},
		{`Revert ""`, ""},
		{"net: fix foo", ""},
	} {
		got, ok := revertedSubject([]byte(tt.in))
		if string(got) != tt.want || ok != (tt.want != "") {
			t.Errorf("revertedSubject(%q) = %q, %v", tt.in, got, ok)
		}
	}
}

// A revert names what it reverts by id, or by the subject it quotes; a
// partial revert reverts no commit as a whole.
func TestRevertID(t *testing.T) {
	for _, tt := range []struct {
		msg, id string
		quoted  bool
	}{
		{"x: undo\n\nThis reverts commit 0123456789ab.", "0123456789ab", false},
		{"Revert \"x: add\"\n\nThis reverts commit\n0123456789AB.", "0123456789ab", true},
		{"Revert \"x: add\"\n\nIt broke y.", "", true},
		{"x: tidy\n\nThis partially reverts commit 0123456789ab.", "", false},
		{"Revert \"x: add\"\n\nThis partly reverts 0123456789ab.", "", false},
		{"x: tidy\n\nThis partially reverts 0123456789ab and reverts\ncommit 89abcdef0123.", "89abcdef0123", false},
		{"Revert \"x: add\"\n\nThis reverts upstream commit 0123456789ab\nfrom stable.", "0123456789ab", true},
		{"x: undo\n\nThis reverts mainline commit 0123456789ab.", "0123456789ab", false},
	} {
		var lower []byte
		r := parseRecord([]byte(strings.Repeat("1", 40)+"\x1f\x1f1\x1fa@b\x1f1\x1f"+tt.msg), &lower)
		if string(r.revert.id) != tt.id || r.revert.quoted != tt.quoted {
			t.Errorf("revert of %q = %q, quoted %v; want %q, %v", tt.msg, r.revert.id, r.revert.quoted, tt.id, tt.quoted)
		}
	}
}
