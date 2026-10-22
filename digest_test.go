package kgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// parserDigest is the digest of what the parsers read from parserCorpus,
// taken at cache version parserDigestVersion. When the parsers read
// anything else, bump cacheVersion, so that no build mixes commits read
// the old way with new ones, and set both to the new values.
const (
	parserDigest        = "8222f92108e257b9"
	parserDigestVersion = 1
)

// parserCorpus holds a message of each form the parsers read; a new form
// needs one here too.
var parserCorpus = []string{
	"net: fix foo\n\ncommit 0123456789abcdef0123456789abcdef01234567 upstream.\n\nFixes: 89abcdef0123 (\"net: add foo\")\nSigned-off-by: A <a@example.com>\n",
	"net: fix foo\n\n[ Upstream commit 0123456789abcdef0123456789abcdef01234567 ]\n\nFixes: commit 89abcdef0123\n",
	"x: fix\n\nbody\n(cherry picked from commit 0123456789abcdef0123456789abcdef01234567)\nOrabug: 1\n",
	"[ Backport of upstream commit 0123456789abcdef0123456789abcdef01234567 ]",
	"x: fix\n\nJIRA: https://issues.redhat.com/browse/RHEL-1\n\ncommit 0123456789abcdef0123456789abcdef01234567\nAuthor: A <a@example.com>\nDate:   Mon Jan 1 00:00:00 2024 +0000\n\n    x: fix\n\n    commit 89abcdef0123456789abcdef0123456789abcdef upstream.\n",
	"x: fix\n\n(backported from commit 0123456789abcdef0123456789abcdef01234567 linux-6.6.y)\nSigned-off-by: B <b@example.com>\n",
	"x: fix two\n\ncommit 0123456789abcdef0123456789abcdef01234567\nAuthor: A <a@example.com>\n\n    x: one\n\ncommit 89abcdef0123456789abcdef0123456789abcdef\nAuthor: B <b@example.com>\n\n    x: two\n",
	"Merge DRM changes from upstream v6.18..v6.19\n\nJIRA: https://issues.redhat.com/browse/RHEL-1\n\nCommit list:\ncommit 0123456789abcdef0123456789abcdef01234567\ncommit 89abcdef0123456789abcdef0123456789abcdef\n",
	"Revert \"net: fix foo\"\n\nThis reverts commit\n0123456789abcdef0123456789abcdef01234567.\n",
	"x: undo\n\nThis reverts 0123456, which broke y.\n",
	"Revert \"x: add\"\n\nThis reverts upstream commit 0123456789abcdef0123456789abcdef01234567\nfrom stable.\n",
	"Revert \"Revert \"fs: fix again\"\"\n\nThis reverts commit 0123456789ab.\n",
	"x: tidy\n\nThis partially reverts commit 0123456789ab (\"x: add\").\n",
	"Revert commit 89abcdef0123 which is commit\n0123456789abcdef0123456789abcdef01234567 upstream.",
	"mm: fix bar\n\n  Fixes: commit deadbeef1234 (\"net: add foo\")\nLink: https://lore.kernel.org/r/1/Fixes: 0123456789ab (\"Initial import\")\n",
	"UPSTREAM: BACKPORT: net: fix foo\n\nBug: 1\n(cherry picked from commit 0123456789abcdef0123456789abcdef01234567)\n",
	"net: fix foo (bsc#1234567).\n\nsuse-commit: 0123456789abcdef0123456789abcdef01234567\n",
	"a long subject that git log\nfolds into one line\n\nand a body\n",
	"\n\nleading blank lines\n",
	"no id\x1finside",
}

// TestParserDigest ties what the parsers read to cacheVersion.
func TestParserDigest(t *testing.T) {
	h := sha256.New()
	var lower []byte
	for i, msg := range parserCorpus {
		rec := fmt.Sprintf("%040x\x1f%040x %040x\x1f%d\x1fAlice@Example.com\x1f%d\x1f%s", i+1, i+2, i+3, 1700000000+i, 1600000000+i, msg)
		fmt.Fprintf(h, "%+v\n", parseRecord([]byte(rec), &lower))
	}
	got := hex.EncodeToString(h.Sum(nil))[:16]
	if got != parserDigest || cacheVersion != parserDigestVersion {
		t.Errorf("the parsers read %s at cache version %d, not %s at %d: when they changed, bump cacheVersion and set parserDigest and parserDigestVersion",
			got, cacheVersion, parserDigest, parserDigestVersion)
	}
}
