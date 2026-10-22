package kgraph

import (
	"bytes"
	"iter"
	"regexp"
)

// The message parsers return the ids they find as written; link resolves
// them once every commit is known. The cache keeps what they return as
// facts, and TestParserDigest ties that to cacheVersion.

// tagRe matches a tag such as Fixes:, also with the word commit before
// the id and also where a missing line break left it after other text; the
// rest of the line may quote the subject.
func tagRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?im)\b` + tag + `:[ \t]*(?:commit[ \t]+)?([0-9a-f]{6,40})\b[ \t]*(.*)$`)
}

// revertRe matches "This reverts commit X" and its variants, the id
// possibly on the next line or after "upstream", and "partially reverts
// commit X", which reverts no commit as a whole. ccStableRe matches the
// comment of a Cc: stable line, where the ids of the commits the patch
// needs first may follow the version, as "# 3.3.x: a1f84a3: sctp: Fix
// ipv6"; dependsOnRe matches "depends on commit X" in running text.
var (
	fixesRe     = tagRe("fixes")
	dependsRe   = tagRe("depends-on")
	depOfRe     = tagRe("stable-dep-of")
	quotedRe    = regexp.MustCompile(`^\(?\s*"(.*)"\s*\)?`)
	revertRe    = regexp.MustCompile(`(?i)\b(partial(?:ly)?\s+|partly\s+)?reverts\s+(?:(?:upstream|mainline)\s+)?(?:commit\s+)?([0-9a-f]{7,40})\b`)
	ccStableRe  = regexp.MustCompile(`(?im)^[ \t]*cc:[^\n#]*stable@(?:vger\.)?kernel\.org[^\n#]*#(.*)$`)
	hexRe       = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	dependsOnRe = regexp.MustCompile(`(?i)\bdepends? (?:on|upon) (?:the )?commit ([0-9a-f]{7,40})\b`)
)

// linkRe matches the message id a Link: line names on lore.kernel.org or
// patch.msgid.link. seriesRe takes the ids git send-email, old and new,
// b4 and format-patch --thread give the patches of one series apart from
// the patch number: what is left names the series.
var (
	linkRe   = regexp.MustCompile(`(?im)^[ \t]*link:[ \t]*https?://(?:lore\.kernel\.org|lkml\.kernel\.org|patch\.msgid\.link|msgid\.link)/(?:[\w.-]+/)?([^\s/]+)`)
	seriesRe = regexp.MustCompile(`^(?:(\d{8}-.+-v\d+)-\d+-([0-9a-f]{6,}@.+)|(\d{14}\.\d+)-\d+-(.+@.+)|(\d{9,10}-\d+)-\d+-(git-send-email-.+)|[0-9a-f]{40}\.(\d{10}\.git\..+))$`)
)

// rawFix is one Fixes: or dependency tag, or a commit's revert: the id as
// written and, when a subject is quoted, its hash.
type rawFix struct {
	id      []byte
	subject uint64
	quoted  bool
}

// parseMessage runs the parsers whose keyword msg contains; lower is msg in
// lower case, as the substring tests are far cheaper than the regexes.
func parseMessage(r *record, msg, lower []byte) {
	if bytes.Contains(lower, []byte("upstream")) || bytes.Contains(lower, []byte("cherry picked")) {
		if id := namedUpstream(msg); id != "" {
			r.named, r.hasNamed = parseHash([]byte(id))
		}
	}
	// Red Hat's header starts a line with "commit ", far rarer than the
	// letters of "author:", which a substring search has to try one by one.
	if bytes.Contains(lower, []byte("\ncommit ")) && bytes.Contains(lower, []byte("author:")) || bytes.Contains(lower, []byte("backported from")) {
		for _, m := range weakRe.FindAllSubmatch(msg, -1) {
			if h, ok := parseHash(bytes.Join(m[1:], nil)); ok {
				r.weak = append(r.weak, h)
			}
		}
	}
	if i := bytes.Index(lower, []byte("\ncommit list:\n")); i >= 0 {
		for _, m := range commitListRe.FindAllSubmatch(msg[i:], -1) {
			if h, ok := parseHash(m[1]); ok {
				r.weak = append(r.weak, h)
			}
		}
	}
	if bytes.Contains(lower, []byte("reverts")) {
		partial := false
		for _, m := range revertRe.FindAllSubmatch(msg, -1) {
			if partial = m[1] != nil; !partial {
				r.revert.id = bytes.ToLower(m[2])
				break
			}
		}
		if partial {
			r.revert = rawFix{} // not even the subject it quotes, then
		}
	}
	r.fixes = tagLines(msg, lower, "fixes:", fixesRe)
	r.deps = tagLines(msg, lower, "depends-on:", dependsRe)
	r.depOf = tagLines(msg, lower, "stable-dep-of:", depOfRe)
	for line := range keyLines(msg, lower, "stable@") {
		m := ccStableRe.FindSubmatch(line)
		if m == nil {
			continue
		}
		r.deps = append(r.deps, ccStableIDs(m[1])...)
	}
	for line := range keyLines(msg, lower, "depend") {
		for _, m := range dependsOnRe.FindAllSubmatch(line, -1) {
			r.deps = append(r.deps, rawFix{id: bytes.ToLower(m[1])})
		}
	}
	// The last Link: of a series names the commit's: the patch's own link
	// comes after those to reports and earlier versions. The empty groups
	// of the other shapes keep the keys of the shapes apart.
	for line := range keyLines(msg, lower, "link:") {
		if m := linkRe.FindSubmatch(line); m != nil {
			if s := seriesRe.FindSubmatch(m[1]); s != nil {
				r.patchset = sum(s[1:]...)
			}
		}
	}
}

// keyLines yields the lines of msg that hold key, in lower case. The
// regexes run on those lines alone: case folding leaves them no literal to
// skip ahead to, so on the whole message they would try every offset.
// Their matches never span lines.
func keyLines(msg, lower []byte, key string) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for off := 0; ; {
			i := bytes.Index(lower[off:], []byte(key))
			if i < 0 {
				return
			}
			i += off
			start, end := bytes.LastIndexByte(msg[:i], '\n')+1, len(msg)
			if j := bytes.IndexByte(msg[i:], '\n'); j >= 0 {
				end = i + j
			}
			if !yield(msg[start:end]) {
				return
			}
			off = end
		}
	}
}

// tagLines runs re, a tagRe, over the lines of msg that hold key, the tag
// in lower case, and returns what they name. A message id is no commit id,
// though it may start with one: a date before a dash, a number before an
// @.
func tagLines(msg, lower []byte, key string, re *regexp.Regexp) []rawFix {
	var refs []rawFix
	for line := range keyLines(msg, lower, key) {
		for _, m := range re.FindAllSubmatchIndex(line, -1) {
			if end := m[3]; end < len(line) && (line[end] == '-' || line[end] == '@') {
				continue
			}
			fx := rawFix{id: bytes.ToLower(line[m[2]:m[3]])}
			if q := quotedRe.FindSubmatch(line[m[4]:m[5]]); q != nil {
				fx.subject, fx.quoted = sum(plainSubject(bytes.TrimSpace(q[1]))), true
			}
			refs = append(refs, fx)
		}
	}
	return refs
}

// ccStableIDs returns the ids that the comment of a Cc: stable line names,
// each with the subject that follows it after a colon, or quoted, up to the
// next id: "# 5.10.x: a1f84a3: sctp: Fix ipv6". A version or a bug number
// is digits alone, a word letters alone.
func ccStableIDs(comment []byte) []rawFix {
	var ids [][]int
	for _, at := range hexRe.FindAllIndex(bytes.ToLower(comment), -1) {
		if id := comment[at[0]:at[1]]; bytes.ContainsAny(id, "0123456789") && bytes.ContainsAny(bytes.ToLower(id), "abcdef") {
			ids = append(ids, at)
		}
	}
	refs := make([]rawFix, len(ids))
	for k, at := range ids {
		refs[k].id = bytes.ToLower(comment[at[0]:at[1]])
		end := len(comment)
		if k+1 < len(ids) {
			end = ids[k+1][0]
		}
		rest := bytes.TrimSpace(comment[at[1]:end])
		var subject []byte
		if q := quotedRe.FindSubmatch(rest); q != nil {
			subject = q[1]
		} else if s, ok := bytes.CutPrefix(rest, []byte(":")); ok {
			subject = bytes.TrimRight(s, " \t,;")
		}
		if subject = bytes.TrimSpace(subject); len(subject) > 0 {
			refs[k].subject, refs[k].quoted = sum(plainSubject(subject)), true
		}
	}
	return refs
}

// subjectOf is the subject of a commit message as git log's %s gives it:
// the first paragraph, its lines joined by a space and stripped of trailing
// blanks, after any leading blank lines.
func subjectOf(msg []byte) []byte {
	var subject []byte
	lines := 0
	for len(msg) > 0 {
		line, rest, _ := bytes.Cut(msg, []byte("\n"))
		msg = rest
		line = bytes.TrimRight(line, " \t\r\v\f")
		if len(line) == 0 {
			if lines > 0 {
				break
			}
			continue
		}
		if lines == 0 {
			subject = line
		} else {
			if lines == 1 {
				subject = bytes.Clone(subject)
			}
			subject = append(append(subject, ' '), line...)
		}
		lines++
	}
	return subject
}

// revertedSubject returns the subject that a Revert "..." subject quotes:
// what the revert reverts when its message names no id.
func revertedSubject(subject []byte) ([]byte, bool) {
	inner, ok := bytes.CutPrefix(subject, []byte(`Revert "`))
	if !ok || len(inner) < 2 || inner[len(inner)-1] != '"' {
		return nil, false
	}
	return inner[:len(inner)-1], true
}

// upstreamRe matches the line by which a stable backport names the mainline
// commit it was made from: mostly "commit <hash> upstream." or "[ Upstream
// commit <hash> ]", but also such forms as "Upstream commit: <hash>",
// "(cherry picked from commit <hash>)" or "This is upstream commit <hash>",
// and the word commit with a colon or misspelt. A bare "<hash> upstream"
// line is not one: reverts and reapplies quote it.
var upstreamRe = regexp.MustCompile(`(?mi)^[\[( ]*(?:com+(?:it|in):?[\s\x{a0}]+([0-9a-f]{40})[\s\x{a0}]+upstream|upstream (?:com+its?:? )?([0-9a-f]{40})|cherry picked from (?:commit|upstream) ([0-9a-f]{40})|(?:this is|backport of) (?:the )?upstream commit ([0-9a-f]{40}))`)

// namedUpstream returns the upstream commit that a backport's message
// names, or "" when it names none.
func namedUpstream(msg []byte) string { return matchedID(upstreamRe, msg) }

// matchedID returns the id of re's first match in msg, which one of its
// groups holds, or "".
func matchedID(re *regexp.Regexp, msg []byte) string {
	m := re.FindSubmatch(msg)
	if m == nil {
		return ""
	}
	return string(bytes.Join(m[1:], nil))
}

// weakRe matches the forms by which a vendor tree names an upstream commit
// and that mean something else elsewhere: the "commit <hash>" header block
// Red Hat quotes, which stable commits use to quote some other commit, and
// Ubuntu's "(backported from commit <hash>)". findNamed takes them for a
// commit of a vendor branch alone, over an upstreamRe form when they name a
// mainline commit: Red Hat's quoted message keeps the upstream's own
// lines, which name other commits. Red Hat quotes a header per upstream
// commit, so a message may hold several.
var weakRe = regexp.MustCompile(`(?mi)^commit ([0-9a-f]{40})\n\s*author:|\(backported from commit ([0-9a-f]{40})`)

// commitListRe matches an entry of the "Commit list:" by which Red Hat's
// DRM rebases name the upstream commits they bring, a weak form too.
var commitListRe = regexp.MustCompile(`(?m)^commit ([0-9a-f]{40})$`)

// Vendor trees decorate the subject of a patch: Android with prefixes such
// as "UPSTREAM: " or "BACKPORT: FROMGIT: ", SUSE with a trailing reference
// such as " (bsc#1234567)." or " (git-fixes).". plainSubject strips them,
// so that the subject, and the author with it, still name the patch.
var (
	prefixRe = regexp.MustCompile(`^(?:(?:UPSTREAM|BACKPORT|FROMGIT|FROMLIST|ANDROID|NOUPSTREAM): )+`)
	suffixRe = regexp.MustCompile(` \([^()]*\)\.$`)
)

func plainSubject(subject []byte) []byte {
	if m := prefixRe.FindIndex(subject); m != nil {
		subject = subject[m[1]:]
	}
	if bytes.HasSuffix(subject, []byte(").")) {
		if m := suffixRe.FindIndex(subject); m != nil {
			subject = subject[:m[0]]
		}
	}
	return subject
}
