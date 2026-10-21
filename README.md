# kgraph

kgraph answers questions about the commits of a Linux kernel tree from
its commit graph: the release that first contains a commit, the stable
backports of a mainline commit and whether each stable series has it,
the commits a Fixes: tag names and the later fixes of a commit, the same
patch merged twice, and reverts. It is a Go library with a command line,
depends on the standard library and git only, and reads the whole tree,
about two million commits, in about 20 seconds and 2 GB of memory, or in
about half a second from its cache.

## Install

    go install github.com/linux-analysis/kgraph/cmd/kgraph@latest

Or `make build` in a checkout, for `bin/kgraph`.

## The tree

kgraph reads an existing git repository of the kernel, a checkout or a
bare clone, that has the mainline and the stable tree as remotes: it
finds them by URL (torvalds/linux and stable/linux) or by the names
`mainline` and `stable`, and `kgraph remotes` shows which it found. It
never clones or fetches: the tree is yours to keep up to date. One way
to make a tree, about 12 GB of disk:

    git init --bare linux.git
    cd linux.git
    git remote add mainline -t master https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git
    git remote add stable https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git
    git fetch --multiple mainline stable

Run the same fetch to bring it up to date. Without a stable remote only
the release tags reach stable commits, and without a mainline remote
HEAD stands for mainline.

A linux-next remote, found by its URL, adds its `master` as a tip:

    git remote add next --no-tags -t master https://git.kernel.org/pub/scm/linux/kernel/git/next/linux-next.git

linux-next is rebuilt daily: the first build after a fetch reads its new
commits and drops the old ones from the cache. `--next REF` reads
another ref of it instead, such as a `next-20261001` tag (fetch them
with `git fetch next 'refs/tags/next-*:refs/tags/next-*'`) or
`next/pending-fixes`, and fails when the remote or the ref is missing.

Vendor trees join the same way. A remote with the URL of Oracle UEK
(github.com/oracle/linux-uek), SUSE (github.com/SUSE/kernel), CentOS
Stream (gitlab.com/redhat/centos-stream/src/kernel/centos-stream-N), an
Ubuntu series
(git.launchpad.net/~ubuntu-kernel/ubuntu/+source/linux/+git/SERIES) or
Android common (android.googlesource.com/kernel/common) is read, every
branch fetched from it as a tip named after the remote and the branch,
such as `uek/uek8/u2`. Choose the branches when adding the remote; the
vendors' release tags are of no use to kgraph:

    git remote add uek --no-tags -t uek8/u2 https://github.com/oracle/linux-uek.git
    git remote add c9s --no-tags -t main https://gitlab.com/redhat/centos-stream/src/kernel/centos-stream-9.git

The graph is built from the mainline branch, the `linux-*.y` branches of
the stable remote, the `v*` release tags, linux-next and the vendor
branches. The commits read are kept in a cache file, one per tree under
`~/.cache/kgraph/` (or `$XDG_CACHE_HOME/kgraph`), that the next build
maps into memory and adds only the new commits to, dropping those no
branch or tag reaches any more, as after a rebase; `--cache FILE` names
the file instead. `kgraph build` builds the graph and the cache and
nothing else. `--no-stable` leaves the stable branches and releases out,
and the vendor branches, which bring stable commits along, for a quicker
build of mainline alone; it keeps a cache file of its own, and `where`
and `followups` refuse it.

## Questions

`--tree` is the current directory by default, so from inside the tree:

    $ kgraph lookup dae8dda341d2
    dae8dda341d2d9034a90d59e8a7d502e1263813f	mainline	tag v7.3-rc2	release v7.3

The second column is the tree: `mainline`, `stable`, `next` or a vendor
branch. Hashes may be abbreviated.

The other questions are about the commit's patch, the change itself: the
mainline and linux-next commits that are the same patch merged more than
once, its copies, with the stable and vendor backports of any of them. A
backport stands for its upstream, a copy for every copy, and an answer
names each patch by its canonical copy, the one in the earliest release.
`where` tells which stable series and vendor branches have a commit,
through the series' base release or a backport, and since which release;
a revert the series has takes out what it reverts, and a revert of that
revert brings it back. A series that picked a patch twice and reverted
the second pick as a duplicate still has it, while copies a merge made
one change go out together, and a backport of a mainline revert undoes
what the revert undoes, whatever its message names. `--series` names the
series and branches, and without it they are the series kernel.org
supports and every vendor branch of the tree. A vendor branch has no
releases to kgraph, so its tag column is `-`:

    $ kgraph --series 6.12,6.18,7.2 where dae8dda341d2
    dae8dda341d2d9034a90d59e8a7d502e1263813f	mainline	tag v7.3-rc2	release v7.3
    dae8dda341d2d9034a90d59e8a7d502e1263813f	6.12	-	-
    dae8dda341d2d9034a90d59e8a7d502e1263813f	6.18	v6.18.57	3f30b2cd4eba45c05f822f03f1b0f4077d6d4dc3
    dae8dda341d2d9034a90d59e8a7d502e1263813f	7.2	v7.2.7	75b37b8369778b69ce9738d1c97685a1615cb252

`followups` lists the later fixes whose Fixes: tags name the patch and
that a series with the patch never had, with their tag and subject; a
fix mainline reverted is none, nor is a vendor branch's own fix, which
no other tree can take, nor a merge, which quotes the tags of the
commits it brings; a series that reverted a fix does not lack it, and a
fix still in linux-next shows `next` in place of a tag:

    $ kgraph --series 6.18,7.2 followups dae8dda341d2
    dae8dda341d2d9034a90d59e8a7d502e1263813f	5cbea500775dd1944995f23320af030b9b24b24b	6.18	v7.3-rc2	tracing: Fix comment in tracing_buffers_splice_read()
    dae8dda341d2d9034a90d59e8a7d502e1263813f	7e645147dfba67edb3ed3090a1ed1d89df77fc27	6.18	v7.3-rc3	tracing: Fix ring_buffer_read_page_size() kernel-doc

`backports` lists the stable and vendor backports of a patch, the stable
ones as the kernel CNA's dyad counts them, those in a release, and none
that its own series or branch reverted; `--adapted` marks the ones that
change other lines than their upstream commit does, in whichever files:
a backport to a file that moved since is no adapted one:

    $ kgraph --adapted backports dae8dda341d2
    dae8dda341d2d9034a90d59e8a7d502e1263813f	3f30b2cd4eba45c05f822f03f1b0f4077d6d4dc3	stable	tag v6.18.57	series v6.18	upstream dae8dda341d2d9034a90d59e8a7d502e1263813f	adapted
    dae8dda341d2d9034a90d59e8a7d502e1263813f	75b37b8369778b69ce9738d1c97685a1615cb252	stable	tag v7.2.7	series v7.2	upstream dae8dda341d2d9034a90d59e8a7d502e1263813f

`fixes` are the patches the Fixes: tags of a patch's copies name,
`fixed-by` the later patches whose Fixes: tags name it, `copies` its
copies, the mainline commits with one author, author date and subject,
and that change the same lines when one descends from the other, as a
fix applied again after its revert, or with one subject and
cherry-picked one from another, and `reverts` what the commit itself
reverts and whether it was reverted. A Fixes: id that names no commit,
or several, stands for the only mainline patch with the subject the tag
quotes; with `--fixes fixes.txt` it is first looked up in that table of
corrected ids, which comes from
[verhaal](https://git.sr.ht/~gregkh/verhaal). A stable or vendor
branch's own revert of the upstream or of another series' backport, as
its message names them, reverts the branch's own backport of that patch,
the newest before it.

Every command on commits takes several hashes, and `-` among them reads
more from standard input, so one run answers for a whole backport queue;
each line of an answer starts with the hash asked about, and for a hash
the graph has no commit for says why: it is no commit id, names several
commits, names one that no branch or tag the graph reads reaches, or
names none:

    git log --format=%H v6.18.56..v6.18.57 | kgraph --series 6.12 where -

Every command prints JSON with `--json`, and flags may follow the
command too. `release`, `tags`, `remotes` and `series` (the stable
series kernel.org supports) need no graph; `release` fails on an
argument that names no release.

## Library

```go
import (
	"github.com/linux-analysis/kgraph"
	"github.com/linux-analysis/kgraph/kernel"
)

rem, err := kernel.FindRemotes(ctx, dir)
l, err := kgraph.Discover(ctx, dir, rem, kgraph.Options{Cache: cacheFile, Fixes: fixesFile})
ts, err := l.Targets([]string{"6.6", "6.12", "uek/uek8/u2"}) // stable series and trees
g, err := l.Build(ctx, logger)
c, ok := g.Commit(id)          // a full or abbreviated id
g.Ambiguous(id)                // whether several commits have the abbreviated id
c.Hash(); c.Tree(); c.Tag(); c.Release(); c.Date()
c.Upstream()                   // the mainline commit a backport was made from
c.Reverts(); c.RevertedBy()
p := c.Patch()                 // the change: its copies and backports
p.Commit()                     // its canonical copy
p.Copies(); p.Backports()      // the backports by dyad's rules, then vendor ones
p.Fixes(); p.FixedBy()         // by Fixes: tags
p.In(ts)                       // whether each series and tree has it
p.Followups(ts)                // the later fixes they lack
```

A `Graph` is read-only once built and safe for concurrent use. The
`kernel` package finds the remotes (`kernel.FindRemotes`), parses
releases and stable series, reads the series kernel.org supports
(`kernel.ParseReleases`) and the subjects and diffs of commits
(`kernel.Subjects`, `kernel.Diffs`, `kernel.Adapted`).

## Development

    make lint   # gofmt and go vet
    make test   # go test -race ./...
    make build  # bin/kgraph

The module depends on the standard library only; `deps_test.go` checks
it. The tests build small repositories with git.

## License

MIT, see LICENSE, except fixes.txt: it comes from verhaal and is
GPL-2.0-only, as its header says.
