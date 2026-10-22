// Command kgraph answers questions about the commits of a Linux kernel
// tree from its commit graph: the first release that contains a commit,
// the stable backports of a mainline commit, the commits a Fixes: tag
// names and the later fixes of a commit, the commits it needs first, its
// twins and its reverts, and whether each stable series has it. It also
// reads the release tags and the stable series kernel.org supports. It
// never changes the tree: fetching is up to its owner.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/linux-analysis/kgraph"
	"github.com/linux-analysis/kgraph/internal/git"
	"github.com/linux-analysis/kgraph/kernel"
)

const usage = `usage: kgraph [flags] command [args]

Flags may also follow the command; after --, every argument is one.

commands:
  build                  build the graph and write its cache
  lookup HASH...         tree, tag, release, series, upstream and date
  where HASH...          whether each stable series and vendor branch has it,
                         and since which release
  followups HASH...      its later fixes that a series or branch with it lacks
  prereqs HASH...        the commits it needs first that a series or branch lacks;
                         with --deep, theirs too
  fixes HASH...          the commits its Fixes: tags name
  fixed-by HASH...       the later commits whose Fixes: tags name it, in any tree
  deps HASH...           the commits it needs first, by its Depends-on: tags, its Cc:
                         stable prerequisites and the Stable-dep-of: tags that name it
  needed-by HASH...      the commits that need it first
  backports HASH...      its stable and vendor backports; with --adapted, marked when
                         they add or remove other lines than it
  copies HASH...         the mainline commits with its author, date and subject,
                         or with its subject and cherry-picked to or from it
  reverts HASH...        what it reverts, and whether it was reverted
  release TAG...         the mainline release a tag or version belongs to
  tags                   the mainline release tags of the tree
  remotes                the mainline, stable, linux-next and vendor remotes
  series                 the stable series kernel.org supports

A HASH may be -, which reads hashes from standard input in its place,
separated by blanks or line breaks. Any hash may be abbreviated. Each
line of an answer starts with the hash asked about, and for a hash the
graph has no commit for, says why: it is not a commit id, names several
commits, names a commit that no branch or tag the graph reads reaches,
or names none. But for lookup and reverts, the answers are about the
commit's patch: a backport stands for its upstream, a copy for every
copy, and a patch goes by its copy in the earliest release.

flags:
`

// version is what the build stamps with -X main.version.
var version = "dev"

type opts struct {
	tree, cache, fixes, series, next       string
	json, adapted, deep, version, noStable bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o opts
	fs := flag.NewFlagSet("kgraph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.tree, "tree", ".", "git repository of the kernel tree, a checkout or a bare clone")
	fs.StringVar(&o.cache, "cache", "", "file that keeps the commits read, so that the next build reads only the new ones (default: one per tree under the user cache directory)")
	fs.StringVar(&o.fixes, "fixes", "", "file of Fixes: ids that name no commit and the commits they mean, such as fixes.txt of the kgraph source")
	fs.BoolVar(&o.noStable, "no-stable", false, "leave the stable branches and releases, and the vendor branches, out: the graph holds mainline alone")
	fs.StringVar(&o.next, "next", "", "linux-next ref to read instead of the next remote's master, such as next-20261001 or next/pending-fixes")
	fs.BoolVar(&o.adapted, "adapted", false, "backports: compare each backport's diff with its upstream commit's")
	fs.BoolVar(&o.deep, "deep", false, "prereqs: also the prerequisites of the prerequisites a series or branch lacks, until it has what it needs")
	fs.StringVar(&o.series, "series", "", "where, followups, prereqs: the stable series and vendor branches to answer for, comma-separated, such as 6.6,6.1,uek/uek8/u2 (default: the series kernel.org supports and every vendor branch)")
	fs.BoolVar(&o.json, "json", false, "print JSON")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Flags may follow the command too, as in backports --adapted X.
	var cmd string
	var cmdArgs []string
	for rest := fs.Args(); len(rest) > 0; {
		if cmd == "" {
			cmd, rest = rest[0], rest[1:]
		} else {
			cmdArgs, rest = append(cmdArgs, rest[0]), rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if parsed := rest[:len(rest)-fs.NArg()]; len(parsed) > 0 && parsed[len(parsed)-1] == "--" {
			cmdArgs = append(cmdArgs, fs.Args()...)
			break
		}
		rest = fs.Args()
	}
	if o.version {
		fmt.Fprintln(stdout, "kgraph", version)
		return 0
	}
	if cmd == "" {
		fs.Usage()
		return 2
	}
	tree, err := filepath.Abs(o.tree)
	if err != nil {
		fmt.Fprintln(stderr, "kgraph:", err)
		return 1
	}
	o.tree = tree
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log := slog.New(slog.NewTextHandler(stderr, nil))
	err = dispatch(ctx, o, cmd, cmdArgs, stdin, stdout, log)
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, "kgraph:", err)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "kgraph:", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

func dispatch(ctx context.Context, o opts, cmd string, args []string, in io.Reader, out io.Writer, log *slog.Logger) error {
	switch cmd {
	case "release":
		return release(o, args, out)
	case "series":
		return series(ctx, o, out)
	case "tags", "remotes", "build", "lookup", "where", "followups", "prereqs", "reverts":
	default:
		if relations[cmd] == nil {
			return fmt.Errorf("%w: unknown command %q", errUsage, cmd)
		}
	}
	if err := checkTree(ctx, o); err != nil {
		return err
	}
	switch cmd {
	case "tags":
		return tags(ctx, o, out, log)
	case "remotes":
		return remotes(ctx, o, out)
	case "build":
		_, err := build(ctx, o, log)
		return err
	}
	return query(ctx, o, cmd, args, in, out, log)
}

// checkTree tells whether --tree is a git repository and --cache no
// directory, which git and the cache would say less plainly.
func checkTree(ctx context.Context, o opts) error {
	st, err := os.Stat(o.tree)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a git repository", o.tree)
	}
	if _, err := git.Run(ctx, o.tree, nil, "rev-parse", "--git-dir"); err != nil {
		if errors.As(err, new(*exec.ExitError)) {
			return fmt.Errorf("%s is not a git repository", o.tree)
		}
		return err
	}
	if st, err := os.Stat(o.cache); o.cache != "" && err == nil && st.IsDir() {
		return fmt.Errorf("%w: --cache %s is a directory", errUsage, o.cache)
	}
	return nil
}

func print(out io.Writer, o opts, v any, text func()) error {
	if !o.json {
		text()
		return nil
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// discover reads the layout of the tree, its cache file that of --cache or
// else the default one.
func discover(ctx context.Context, o opts, log *slog.Logger) (*kgraph.Layout, error) {
	rem, err := kernel.FindRemotes(ctx, o.tree)
	if err != nil {
		return nil, err
	}
	cache := o.cache
	if cache == "" {
		if cache, err = defaultCache(ctx, o.tree, o.noStable); err != nil {
			log.Warn("no cache directory: every build reads every commit", "err", err)
		}
	}
	return kgraph.Discover(ctx, o.tree, rem, kgraph.Options{Cache: cache, Fixes: o.fixes, NoStable: o.noStable, Next: o.next})
}

func build(ctx context.Context, o opts, log *slog.Logger) (*kgraph.Graph, error) {
	l, err := discover(ctx, o, log)
	if err != nil {
		return nil, err
	}
	return l.Build(ctx, log)
}

// defaultCache is the cache file of the tree at tree: one per repository,
// wherever in it tree is, under the user's cache directory,
// $XDG_CACHE_HOME/kgraph or ~/.cache/kgraph on Linux. A mainline-only
// build has its own, so that the two modes never rebuild each other's.
func defaultCache(ctx context.Context, tree string, mainlineOnly bool) (string, error) {
	gitDir, err := git.Run(ctx, tree, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kgraph")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSpace(gitDir))
	name := hex.EncodeToString(sum[:6])
	if mainlineOnly {
		name += "-mainline"
	}
	return filepath.Join(dir, name+".cache"), nil
}

// asked is a hash asked about: the full one when the graph has its commit,
// else the one asked and why the graph has none.
type asked struct {
	Hash    string `json:"hash"`
	Found   bool   `json:"found"`
	Missing string `json:"missing,omitempty"` // not-an-id, ambiguous, not-in-graph or no-commit
	why     string
}

// String is the line for a hash the graph has no commit for.
func (a asked) String() string { return a.Hash + "\t" + a.why }

// commitRow is what lookup prints for a commit.
type commitRow struct {
	asked

	Tree     string `json:"tree,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Release  string `json:"release,omitempty"`
	Series   string `json:"series_release,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Date     int64  `json:"date,omitempty"`
}

func row(c kgraph.Commit) commitRow {
	r := commitRow{asked: asked{Hash: c.Hash(), Found: true}, Tree: c.Tree(), Tag: c.Tag(), Date: c.Date()}
	if rel := c.Release(); rel != kernel.Unreleased {
		r.Release = rel.String()
	}
	if rel := c.SeriesRelease(); rel != kernel.Unreleased {
		r.Series = rel.String()
	}
	if u, ok := c.Upstream(); ok {
		r.Upstream = u.Hash()
	}
	return r
}

func (r commitRow) String() string {
	if !r.Found {
		return r.asked.String()
	}
	parts := []string{r.Hash, r.Tree}
	if r.Tag != "" {
		parts = append(parts, "tag "+r.Tag)
	}
	if r.Release != "" {
		parts = append(parts, "release "+r.Release)
	}
	if r.Series != "" {
		parts = append(parts, "series "+r.Series)
	}
	if r.Upstream != "" {
		parts = append(parts, "upstream "+r.Upstream)
	}
	return strings.Join(parts, "\t")
}

// whereRow is what where prints for a commit: its lookup row and whether
// each series has it.
type whereRow struct {
	commitRow
	Series []kgraph.Presence `json:"series,omitempty"`
}

// print writes the lookup row and one line per series: the first release
// of the series that has the commit and the commit it has, or dashes.
func (r whereRow) print(out io.Writer) {
	fmt.Fprintln(out, r.commitRow)
	for _, p := range r.Series {
		tag, commit := p.Tag, p.Commit
		if commit == "" {
			commit = "-"
		}
		if tag == "" {
			tag = "-"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", r.Hash, p.Series, tag, commit)
	}
}

// followupsRow is what followups and prereqs print for a commit: its
// follow-up fixes, or the commits it needs first, that some series lack.
type followupsRow struct {
	asked
	Followups []followupRow `json:"followups,omitempty"`
	Prereqs   []followupRow `json:"prereqs,omitempty"`
}

type followupRow struct {
	Hash    string   `json:"hash"`
	Series  []string `json:"series"`
	Tag     string   `json:"tag,omitempty"`
	Tree    string   `json:"tree,omitempty"`
	Date    int64    `json:"date,omitempty"`
	Subject string   `json:"subject,omitempty"`
}

// print writes one line per follow-up or prerequisite: the commit, the
// related commit, the series that lack it, its tag or its tree when no
// release has it yet, and its subject.
func (r followupsRow) print(out io.Writer) {
	if !r.Found {
		fmt.Fprintln(out, r.asked)
		return
	}
	for _, f := range slices.Concat(r.Followups, r.Prereqs) {
		tag := f.Tag
		if tag == "" {
			tag = f.Tree
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", r.Hash, f.Hash, strings.Join(f.Series, ","), tag, f.Subject)
	}
}

// backportRow is one backport with whether it was adapted, when asked.
type backportRow struct {
	commitRow
	Adapted *bool `json:"adapted,omitempty"`
}

// hashes returns args with each - in them replaced by the blank-separated
// words of in, which is read once.
func hashes(args []string, in io.Reader) ([]string, error) {
	if !slices.Contains(args, "-") {
		return args, nil
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return nil, fmt.Errorf("read standard input: %w", err)
	}
	words := strings.Fields(string(b))
	var out []string
	for _, a := range args {
		if a == "-" {
			out, words = append(out, words...), nil
		} else {
			out = append(out, a)
		}
	}
	return out, nil
}

// lookups finds the commit of each hash of args and tells, for those the
// graph has no commit for, why: not a hex id, the prefix of several
// commits, a commit git has and no branch or tag the graph reads reaches,
// or none at all.
func lookups(ctx context.Context, o opts, g *kgraph.Graph, args []string) ([]kgraph.Commit, []asked, error) {
	commits, rows := make([]kgraph.Commit, len(args)), make([]asked, len(args))
	var unknown []int
	for i, h := range args {
		rows[i].Hash = h
		var ok bool
		switch commits[i], ok = g.Commit(h); {
		case ok:
			rows[i] = asked{Hash: commits[i].Hash(), Found: true}
		case len(h) == 0 || len(h) > 40 || strings.Trim(strings.ToLower(h), "0123456789abcdef") != "":
			rows[i].Missing, rows[i].why = "not-an-id", "is not a commit id"
		case g.Ambiguous(h):
			rows[i].Missing, rows[i].why = "ambiguous", "names several commits"
		default:
			unknown = append(unknown, i)
		}
	}
	if len(unknown) == 0 {
		return commits, rows, nil
	}
	var in bytes.Buffer
	for _, i := range unknown {
		in.WriteString(args[i] + "\n")
	}
	out, err := git.Run(ctx, o.tree, in.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(out), "\n")
	notIn := "is not in the graph: no branch or tag it reads reaches it"
	if o.noStable {
		notIn = "is not in the graph, which --no-stable leaves to mainline and linux-next"
	}
	for k, i := range unknown {
		// A commit's line is "<hash> commit <size>", an unknown id's says
		// missing or ambiguous.
		var f []string
		if k < len(lines) {
			f = strings.Fields(lines[k])
		}
		if len(f) == 3 && f[1] == "commit" {
			rows[i].Missing, rows[i].why = "not-in-graph", notIn
		} else {
			rows[i].Missing, rows[i].why = "no-commit", "names no commit"
		}
	}
	return commits, rows, nil
}

// relations are the commands that list the commits related to a commit:
// most of them the canonical copies of the patches related to its patch.
var relations = map[string]func(kgraph.Commit) []kgraph.Commit{
	"fixes":     func(c kgraph.Commit) []kgraph.Commit { return canonical(c.Patch().Fixes()) },
	"fixed-by":  func(c kgraph.Commit) []kgraph.Commit { return canonical(c.Patch().FixedBy()) },
	"deps":      func(c kgraph.Commit) []kgraph.Commit { return canonical(c.Patch().Needs()) },
	"needed-by": func(c kgraph.Commit) []kgraph.Commit { return canonical(c.Patch().NeededBy()) },
	"copies":    func(c kgraph.Commit) []kgraph.Commit { return c.Patch().Copies() },
	"backports": func(c kgraph.Commit) []kgraph.Commit { return c.Patch().Backports() },
}

func canonical(ps []kgraph.Patch) []kgraph.Commit {
	cs := make([]kgraph.Commit, len(ps))
	for i, p := range ps {
		cs[i] = p.Commit()
	}
	return cs
}

// relatedRow is what a relation command prints for a commit: the commits
// related to it.
type relatedRow struct {
	asked
	Commits []backportRow `json:"commits,omitempty"`
}

func query(ctx context.Context, o opts, cmd string, args []string, in io.Reader, out io.Writer, log *slog.Logger) error {
	args, err := hashes(args, in)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("%w: %s takes commit hashes", errUsage, cmd)
	}
	if o.noStable && (cmd == "where" || cmd == "followups" || cmd == "prereqs") {
		return fmt.Errorf("%w: %s answers for stable series and vendor branches, which --no-stable leaves out", errUsage, cmd)
	}
	l, err := discover(ctx, o, log)
	if err != nil {
		return err
	}
	// The series are checked against the layout before the build, which
	// reads every commit when there is no cache.
	var targets []kgraph.Target
	if cmd == "where" || cmd == "followups" || cmd == "prereqs" {
		names, err := seriesNames(ctx, o)
		if err != nil {
			return err
		}
		if o.series == "" {
			names = append(names, l.VendorBranches()...)
		}
		if targets, err = l.Targets(names); err != nil {
			return fmt.Errorf("%w: %v", errUsage, err)
		}
	}
	g, err := l.Build(ctx, log)
	if err != nil {
		return err
	}
	commits, asks, err := lookups(ctx, o, g, args)
	if err != nil {
		return err
	}
	switch cmd {
	case "lookup":
		rows := make([]commitRow, len(args))
		for i, c := range commits {
			rows[i] = commitRow{asked: asks[i]}
			if asks[i].Found {
				rows[i] = row(c)
			}
		}
		return print(out, o, rows, func() {
			for _, r := range rows {
				fmt.Fprintln(out, r)
			}
		})
	case "where":
		rows := make([]whereRow, len(args))
		for i, c := range commits {
			rows[i].commitRow = commitRow{asked: asks[i]}
			if asks[i].Found {
				rows[i] = whereRow{row(c), c.Patch().In(targets)}
			}
		}
		return print(out, o, rows, func() {
			for _, r := range rows {
				r.print(out)
			}
		})
	case "followups", "prereqs":
		return followups(ctx, o, cmd, commits, asks, targets, out)
	case "reverts":
		type revertsRow struct {
			asked
			Reverts  string `json:"reverts,omitempty"`
			Reverted *bool  `json:"reverted,omitempty"`
		}
		rows := make([]revertsRow, len(args))
		for i, c := range commits {
			rows[i].asked = asks[i]
			if asks[i].Found {
				reverted := len(c.RevertedBy()) > 0
				rows[i].Reverted = &reverted
				if r, ok := c.Reverts(); ok {
					rows[i].Reverts = r.Hash()
				}
			}
		}
		return print(out, o, rows, func() {
			for _, r := range rows {
				switch {
				case !r.Found:
					fmt.Fprintln(out, r.asked)
					continue
				case r.Reverts != "":
					fmt.Fprintf(out, "%s\treverts %s\n", r.Hash, r.Reverts)
				}
				fmt.Fprintf(out, "%s\treverted %v\n", r.Hash, *r.Reverted)
			}
		})
	}
	rows := make([]relatedRow, len(args))
	pairs := map[string]string{} // backport to its upstream, for --adapted
	for i, c := range commits {
		rows[i].asked = asks[i]
		if !asks[i].Found {
			continue
		}
		for _, r := range relations[cmd](c) {
			rows[i].Commits = append(rows[i].Commits, backportRow{commitRow: row(r)})
			if u, ok := r.Upstream(); ok {
				pairs[r.Hash()] = u.Hash()
			}
		}
	}
	if cmd == "backports" && o.adapted && len(pairs) > 0 {
		marks, err := kernel.Adapted(ctx, o.tree, pairs)
		if err != nil {
			return err
		}
		for _, r := range rows {
			for j := range r.Commits {
				if a, ok := marks[r.Commits[j].Hash]; ok {
					r.Commits[j].Adapted = &a
				}
			}
		}
	}
	return print(out, o, rows, func() {
		for _, r := range rows {
			if !r.Found {
				fmt.Fprintln(out, r.asked)
			}
			for _, c := range r.Commits {
				line := r.Hash + "\t" + c.commitRow.String()
				if c.Adapted != nil && *c.Adapted {
					line += "\tadapted"
				}
				fmt.Fprintln(out, line)
			}
		}
	})
}

// followups answers followups and prereqs: the later fixes of each commit's
// patch, or the patches it needs first, and with --deep theirs, that some of
// targets lack, each by its canonical copy with the subject of that.
func followups(ctx context.Context, o opts, cmd string, commits []kgraph.Commit, asks []asked, targets []kgraph.Target, out io.Writer) error {
	related := kgraph.Patch.Followups
	switch {
	case cmd == "prereqs" && o.deep:
		related = kgraph.Patch.AllPrereqs
	case cmd == "prereqs":
		related = kgraph.Patch.Prereqs
	}
	rows, lists := make([]followupsRow, len(commits)), make([][]kgraph.Related, len(commits))
	var all []string
	for i, c := range commits {
		rows[i].asked = asks[i]
		if !asks[i].Found {
			continue
		}
		lists[i] = related(c.Patch(), targets)
		for _, f := range lists[i] {
			all = append(all, f.Patch.Commit().Hash())
		}
	}
	subjects, err := kernel.Subjects(ctx, o.tree, all)
	if err != nil {
		return err
	}
	for i, list := range lists {
		var fs []followupRow
		for _, f := range list {
			c := f.Patch.Commit()
			fs = append(fs, followupRow{Hash: c.Hash(), Series: f.Series, Tag: c.Tag(), Tree: c.Tree(), Date: c.Date(), Subject: subjects[c.Hash()]})
		}
		if cmd == "prereqs" {
			rows[i].Prereqs = fs
		} else {
			rows[i].Followups = fs
		}
	}
	return print(out, o, rows, func() {
		for _, r := range rows {
			r.print(out)
		}
	})
}

func release(o opts, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: release takes tags or versions", errUsage)
	}
	type rel struct {
		Input   string `json:"input"`
		Release string `json:"release,omitempty"`
		Number  int    `json:"number,omitempty"`
	}
	var rows []rel
	var bad []string
	for _, a := range args {
		r := kernel.ParseRelease(a)
		if r == 0 {
			bad = append(bad, a)
		}
		rows = append(rows, rel{a, r.String(), int(r)})
	}
	err := print(out, o, rows, func() {
		for _, r := range rows {
			if r.Number == 0 {
				fmt.Fprintf(out, "%s\tnot a release\n", r.Input)
			} else {
				fmt.Fprintf(out, "%s\t%s\t%d\n", r.Input, r.Release, r.Number)
			}
		}
	})
	if err == nil && bad != nil {
		err = fmt.Errorf("not a release: %s", strings.Join(bad, ", "))
	}
	return err
}

// tags lists the mainline releases whose tags the tree has.
func tags(ctx context.Context, o opts, out io.Writer, log *slog.Logger) error {
	l, err := discover(ctx, o, log)
	if err != nil {
		return err
	}
	var names []string
	for _, t := range l.Tags() {
		if !t.Stable && t.RC == 0 {
			names = append(names, t.Release.String())
		}
	}
	names = slices.Compact(names)
	return print(out, o, names, func() {
		for _, n := range names {
			fmt.Fprintln(out, n)
		}
	})
}

func remotes(ctx context.Context, o opts, out io.Writer) error {
	rem, err := kernel.FindRemotes(ctx, o.tree)
	if err != nil {
		return err
	}
	return print(out, o, rem, func() {
		fmt.Fprintf(out, "mainline: %s\nstable: %s\nnext: %s\nvendors: %s\n", rem.Mainline, rem.Stable, rem.Next, strings.Join(rem.Vendors, " "))
	})
}

// seriesNames are the series and trees of --series, or the series
// kernel.org supports; query checks them against the graph once built.
func seriesNames(ctx context.Context, o opts) ([]string, error) {
	var names []string
	if o.series == "" {
		ss, err := supportedSeries(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range ss {
			names = append(names, s.Name)
		}
		return names, nil
	}
	for _, s := range strings.Split(o.series, ",") {
		names = append(names, strings.TrimSpace(s))
	}
	return names, nil
}

func series(ctx context.Context, o opts, out io.Writer) error {
	ss, err := supportedSeries(ctx)
	if err != nil {
		return err
	}
	return print(out, o, ss, func() {
		for _, s := range ss {
			if s.Longterm {
				fmt.Fprintf(out, "%s\tlongterm\n", s.Name)
			} else {
				fmt.Fprintf(out, "%s\tstable\n", s.Name)
			}
		}
	})
}

// supportedSeries fetches the stable series kernel.org supports.
func supportedSeries(ctx context.Context) ([]kernel.Series, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kernel.ReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", kernel.ReleasesURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return kernel.ParseReleases(body)
}
