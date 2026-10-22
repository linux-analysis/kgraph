package kgraph

import (
	"bytes"
	"iter"
	"regexp"
)

// The message parsers return the ids they find as written; link resolves
// them once every commit is known. The cache keeps what they return as
// facts, and TestParserDigest ties that to cacheVersion.

// fixesRe matches a Fixes: tag, also with the word commit before the id
// and also where a missing line break left it after other text; the rest of
// the line may quote the subject. revertRe matches "This reverts commit X"
// and its variants, the id possibly on the next line or after "upstream",
// and "partially reverts commit X", which reverts no commit as a whole.
var (
	fixesRe  = regexp.MustCompile(`(?im)\bfixes:[ \t]*(?:commit[ \t]+)?([0-9a-f]{6,40})\b[ \t]*(.*)$`)
	quotedRe = regexp.MustCompile(`^\(?\s*"(.*)"\s*\)?`)
	revertRe = regexp.MustCompile(`(?i)\b(partial(?:ly)?\s+|partly\s+)?reverts\s+(?:(?:upstream|mainline)\s+)?(?:commit\s+)?([0-9a-f]{7,40})\b`)
)

// rawFix is one Fixes: tag, or a commit's revert: the id as
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
		for _, m := range revertRe.FindAllSubmatch(msg, -1) {
			if m[1] == nil {
				r.revert.id = bytes.ToLower(m[2])
				break
			}
		}
	}
	r.fixes = tagLines(msg, lower, "fixes:", fixesRe)
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
// in lower case, and returns what they name.
func tagLines(msg, lower []byte, key string, re *regexp.Regexp) []rawFix {
	var refs []rawFix
	for line := range keyLines(msg, lower, key) {
		for _, m := range re.FindAllSubmatch(line, -1) {
			fx := rawFix{id: bytes.ToLower(m[1])}
			if q := quotedRe.FindSubmatch(m[2]); q != nil {
				fx.subject, fx.quoted = sum(plainSubject(bytes.TrimSpace(q[1]))), true
			}
			refs = append(refs, fx)
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
