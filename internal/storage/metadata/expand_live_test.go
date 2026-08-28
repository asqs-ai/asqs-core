package metadata

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// graphFixture builds a symbol graph in a scratch database and returns fq_name -> id.
type graphFixture struct {
	t    *testing.T
	st   *Store
	ctx  context.Context
	repo string
	file string
	ids  map[string]string
}

func newGraphFixture(t *testing.T, repo string) *graphFixture {
	t.Helper()
	url, why := ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	file := "src/" + repo + ".java"
	g := &graphFixture{t: t, st: st, ctx: ctx, repo: repo, file: file, ids: map[string]string{}}
	g.clean()
	t.Cleanup(func() { g.clean(); st.Close() })
	if err := st.UpsertFile(ctx, &File{File: file, SHA: "s", Lang: "java"}); err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *graphFixture) clean() {
	_, _ = g.st.DeleteSymbolsByFile(g.ctx, g.repo, g.file)
	_, _ = g.st.DeleteFile(g.ctx, g.repo, g.file)
}

func (g *graphFixture) sym(name string) string {
	if id, ok := g.ids[name]; ok {
		return id
	}
	id, err := g.st.InsertSymbol(g.ctx, &Symbol{
		Lang: "java", Kind: "class", FQName: name, File: g.file, StartLine: 1, EndLine: 2, RepoID: g.repo,
	})
	if err != nil {
		g.t.Fatal(err)
	}
	g.ids[name] = id
	return id
}

func (g *graphFixture) edge(caller, callee, typ string) {
	if err := g.st.InsertEdge(g.ctx, &Edge{
		CallerSymbolID: g.sym(caller), CalleeSymbolID: g.sym(callee), EdgeType: typ, RepoID: g.repo,
	}); err != nil {
		g.t.Fatal(err)
	}
}

// bfs is the Go breadth-first search ExpandGraph replaces, reimplemented here over the same store so
// the two can be compared on identical data. Keeping it in the test is deliberate: the acceptance
// criterion is equivalence with the previous behaviour, and that claim needs the old algorithm
// present to check against rather than trusted from memory.
func (g *graphFixture) bfs(start string, callees, callers bool, maxDepth, maxNodes int, types []string) map[string]int {
	allow := map[string]bool{}
	for _, t := range types {
		allow[strings.ToUpper(t)] = true
	}
	ok := func(t string) bool { return len(allow) == 0 || allow[strings.ToUpper(t)] }

	seen := map[string]bool{start: true}
	depthOf := map[string]int{}
	frontier := []string{start}
	for depth := 0; depth < maxDepth && len(depthOf) < maxNodes && len(frontier) > 0; depth++ {
		var next []string
		for _, u := range frontier {
			if len(depthOf) >= maxNodes {
				break
			}
			var neighbours []string
			if callees {
				es, err := g.st.GetEdgesFrom(g.ctx, g.repo, u)
				if err != nil {
					g.t.Fatal(err)
				}
				for _, e := range es {
					if ok(e.EdgeType) {
						neighbours = append(neighbours, e.CalleeSymbolID)
					}
				}
			}
			if callers {
				es, err := g.st.GetEdgesTo(g.ctx, g.repo, u)
				if err != nil {
					g.t.Fatal(err)
				}
				for _, e := range es {
					if ok(e.EdgeType) {
						neighbours = append(neighbours, e.CallerSymbolID)
					}
				}
			}
			for _, v := range neighbours {
				if v == "" || seen[v] {
					continue
				}
				seen[v] = true
				depthOf[v] = depth + 1
				next = append(next, v)
				if len(depthOf) >= maxNodes {
					break
				}
			}
		}
		frontier = next
	}
	return depthOf
}

func (g *graphFixture) names(ids map[string]int) []string {
	byID := map[string]string{}
	for n, id := range g.ids {
		byID[id] = n
	}
	var out []string
	for id, d := range ids {
		out = append(out, fmt.Sprintf("%s@%d", byID[id], d))
	}
	sort.Strings(out)
	return out
}

func (g *graphFixture) expandNames(rows []ExpandRow) []string {
	byID := map[string]string{}
	for n, id := range g.ids {
		byID[id] = n
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s@%d", byID[r.Symbol.ID], r.Depth))
	}
	sort.Strings(out)
	return out
}

// The acceptance criterion: the CTE must agree with the Go BFS on the shapes the existing
// graphquery fixtures describe. Equivalence is asserted on (symbol, depth) pairs with the node cap
// slack, because truncation ORDER is deliberately different — the CTE ranks by importance where the
// BFS kept whatever it found first.
func TestExpandGraph_matchesBreadthFirstSearch(t *testing.T) {
	for _, tc := range []struct {
		name             string
		build            func(*graphFixture)
		start            string
		callees, callers bool
		types            []string
	}{
		{
			name: "fan-out callees",
			build: func(g *graphFixture) {
				for _, c := range []string{"b", "c", "d", "e", "f"} {
					g.edge("start", c, "CALLS")
				}
			},
			start: "start", callees: true,
		},
		{
			name: "caller chain",
			build: func(g *graphFixture) {
				g.edge("A", "B", "CALLS")
				g.edge("B", "C", "CALLS")
			},
			start: "C", callers: true,
		},
		{
			name: "edge type filter",
			build: func(g *graphFixture) {
				g.edge("S", "X", "IMPORTS")
				g.edge("S", "Y", "CALLS")
			},
			start: "S", callees: true, types: []string{"CALLS"},
		},
		{
			name: "both directions, diamond",
			build: func(g *graphFixture) {
				g.edge("top", "left", "CALLS")
				g.edge("top", "right", "CALLS")
				g.edge("left", "bottom", "CALLS")
				g.edge("right", "bottom", "CALLS")
			},
			start: "bottom", callees: true, callers: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraphFixture(t, strings.ReplaceAll(tc.name, " ", "_"))
			tc.build(g)

			const maxDepth, maxNodes = 5, 50
			want := g.names(g.bfs(g.sym(tc.start), tc.callees, tc.callers, maxDepth, maxNodes, tc.types))

			got, err := g.st.ExpandGraph(g.ctx, g.repo, g.sym(tc.start), ExpandGraphOptions{
				Callees: tc.callees, Callers: tc.callers,
				MaxDepth: maxDepth, MaxNodes: maxNodes, EdgeTypes: tc.types,
			})
			if err != nil {
				t.Fatal(err)
			}
			gotNames := g.expandNames(got)
			if strings.Join(gotNames, ",") != strings.Join(want, ",") {
				t.Errorf("CTE and BFS disagree:\n CTE: %v\n BFS: %v", gotNames, want)
			}
		})
	}
}

// A cycle must terminate. Code graphs contain them routinely — mutual recursion, a class whose
// method calls back into the class — and a recursive CTE without a path guard does not stop.
func TestExpandGraph_terminatesOnCycles(t *testing.T) {
	g := newGraphFixture(t, "cycles")
	g.edge("A", "B", "CALLS")
	g.edge("B", "C", "CALLS")
	g.edge("C", "A", "CALLS") // closes the loop
	g.edge("B", "B", "CALLS") // self-edge

	rows, err := g.st.ExpandGraph(g.ctx, g.repo, g.sym("A"), ExpandGraphOptions{
		Callees: true, MaxDepth: 10, MaxNodes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	// B and C are reachable; A is the start and must not reappear via the cycle.
	got := g.expandNames(rows)
	if strings.Join(got, ",") != "B@1,C@2" {
		t.Errorf("cycle walk = %v; want B@1,C@2 (start not revisited, no duplicates)", got)
	}
}

// Each node appears once, at its shallowest depth, even when several paths reach it.
func TestExpandGraph_keepsShallowestPath(t *testing.T) {
	g := newGraphFixture(t, "shallowest")
	g.edge("root", "mid", "CALLS")
	g.edge("root", "leaf", "CALLS") // depth 1
	g.edge("mid", "leaf", "CALLS")  // depth 2 via mid

	rows, err := g.st.ExpandGraph(g.ctx, g.repo, g.sym("root"), ExpandGraphOptions{
		Callees: true, MaxDepth: 5, MaxNodes: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.expandNames(rows); strings.Join(got, ",") != "leaf@1,mid@1" {
		t.Errorf("got %v; leaf must appear once at depth 1, not also at 2", got)
	}
}

// The behaviour change the bundle exists for: a capped expansion must keep the IMPORTANT
// neighbourhood, not the first-discovered one. The old BFS truncated at an arbitrary frontier
// boundary with no ordering.
func TestExpandGraph_truncationKeepsHighDegreeNodes(t *testing.T) {
	g := newGraphFixture(t, "ranking")
	// start calls three symbols; "important" is itself called by many others.
	for _, c := range []string{"aaa_first", "important", "zzz_last"} {
		g.edge("start", c, "CALLS")
	}
	for i := 0; i < 5; i++ {
		g.edge(fmt.Sprintf("caller%d", i), "important", "CALLS")
	}
	if err := g.st.RecomputeSymbolDegrees(g.ctx, g.repo); err != nil {
		t.Fatal(err)
	}

	rows, err := g.st.ExpandGraph(g.ctx, g.repo, g.sym("start"), ExpandGraphOptions{
		Callees: true, MaxDepth: 1, MaxNodes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row under the cap, got %d", len(rows))
	}
	if rows[0].Symbol.FQName != "important" {
		t.Errorf("truncation kept %q; a capped expansion must keep the highest-degree neighbour, "+
			"not whichever the walk reached first (alphabetically that would be aaa_first)",
			rows[0].Symbol.FQName)
	}
}

func TestExpandGraph_rejectsBadOptions(t *testing.T) {
	g := newGraphFixture(t, "badopts")
	id := g.sym("only")
	for _, tc := range []struct {
		name string
		opt  ExpandGraphOptions
	}{
		{"no direction", ExpandGraphOptions{MaxDepth: 3, MaxNodes: 10}},
		{"zero depth", ExpandGraphOptions{Callees: true, MaxNodes: 10}},
		{"zero nodes", ExpandGraphOptions{Callees: true, MaxDepth: 3}},
	} {
		if _, err := g.st.ExpandGraph(g.ctx, g.repo, id, tc.opt); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
	if _, err := g.st.ExpandGraph(g.ctx, g.repo, "  ", ExpandGraphOptions{Callees: true, MaxDepth: 3, MaxNodes: 10}); err == nil {
		t.Error("empty start id: expected an error")
	}
}
