package kgraph

import "github.com/linux-analysis/kgraph/kernel"

// The tests name commits by their hex ids; these helpers ask the graph's
// handles and answer in ids, a patch by its canonical copy.

// info is what the handle of a commit tells, for comparing.
type info struct {
	Hash          string
	Mainline      bool
	Tree, Tag     string
	Release       kernel.Release
	SeriesRelease kernel.Release
	Upstream      string
	Date          int64
}

func lookupOf(g *Graph, h string) (info, bool) {
	c, ok := g.Commit(h)
	if !ok {
		return info{}, false
	}
	i := info{Hash: c.Hash(), Mainline: c.Mainline(), Tree: c.Tree(), Tag: c.Tag(), Release: c.Release(), SeriesRelease: c.SeriesRelease(), Date: c.Date()}
	if u, ok := c.Upstream(); ok {
		i.Upstream = u.Hash()
	}
	return i, true
}

func resolveOf(g *Graph, h string) (string, bool) {
	c, ok := g.Commit(h)
	if !ok {
		return "", false
	}
	return c.Hash(), true
}

func patchOf(g *Graph, h string) (Patch, bool) {
	c, ok := g.Commit(h)
	if !ok {
		return Patch{}, false
	}
	return c.Patch(), true
}

func ids(cs []Commit) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Hash())
	}
	return out
}

func pids(ps []Patch) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Commit().Hash())
	}
	return out
}

// related asks a question of the patch of commit h, nil for none.
func related[T any](g *Graph, h string, ask func(Patch) T) T {
	p, ok := patchOf(g, h)
	if !ok {
		var zero T
		return zero
	}
	return ask(p)
}

func fixesOf(g *Graph, h string) []string {
	return related(g, h, func(p Patch) []string { return pids(p.Fixes()) })
}
func fixedByOf(g *Graph, h string) []string {
	return related(g, h, func(p Patch) []string { return pids(p.FixedBy()) })
}
func copiesOf(g *Graph, h string) []string {
	return related(g, h, func(p Patch) []string { return ids(p.Copies()) })
}
func backportsOf(g *Graph, h string) []string {
	return related(g, h, func(p Patch) []string { return ids(p.Backports()) })
}

func revertsOf(g *Graph, h string) string {
	c, ok := g.Commit(h)
	if !ok {
		return ""
	}
	if r, ok := c.Reverts(); ok {
		return r.Hash()
	}
	return ""
}

func revertedOf(g *Graph, h string) bool {
	c, ok := g.Commit(h)
	return ok && len(c.RevertedBy()) > 0
}

func inSeriesOf(g *Graph, h string, ts []Target) []Presence {
	if p, ok := patchOf(g, h); ok {
		return p.In(ts)
	}
	out := make([]Presence, len(ts))
	for i, t := range ts {
		out[i].Series = t.Name
	}
	return out
}

// followup is a Related by its canonical copy.
type followup struct {
	Hash   string
	Series []string
}

func followupsOf(g *Graph, h string, ts []Target) []followup {
	return related(g, h, func(p Patch) []followup { return asFollowups(p.Followups(ts)) })
}

func asFollowups(rs []Related) []followup {
	var out []followup
	for _, r := range rs {
		out = append(out, followup{r.Patch.Commit().Hash(), r.Series})
	}
	return out
}
