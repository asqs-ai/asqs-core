package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ExpandRow is one reachable symbol from a graph expansion, with how it was reached.
type ExpandRow struct {
	Symbol   *Symbol
	Depth    int
	EdgeType string
	Inbound  bool
	ViaFrom  string
}

// ExpandGraphOptions bounds a traversal. Callees/Callers select direction; at least one must be set.
type ExpandGraphOptions struct {
	Callees   bool
	Callers   bool
	MaxDepth  int
	MaxNodes  int
	EdgeTypes []string // nil or empty = all edge types
}

// ExpandGraph walks the symbol graph from startID in ONE query.
//
// It replaces a Go breadth-first search that issued a GetEdgesFrom/GetEdgesTo per frontier node and
// a GetSymbolByID per discovered node — up to 500 round trips for a single expansion, and two
// independent implementations meant the operator inspecting the graph and the retrieval layer
// reasoning over it saw different neighbourhoods for the same symbol.
//
// # Cycle guard
//
// Each row carries the path taken to reach it, and a step is only taken to a node not already on
// that path. Code graphs contain cycles routinely — mutual recursion, a class whose method calls
// back into the class.
//
// The depth cap alone would make the recursion terminate, so the guard is about correctness rather
// than termination: measured without it on a three-node cycle A->B->C->A, the walk from A returns
// A itself at depth 3. A start symbol reappearing in its own expansion is wrong, and the same
// mechanism inflates every count behind it.
//
// The guard permits a node to be reached by several distinct paths; DISTINCT ON collapses those to
// the shallowest, which is what a breadth-first search would have found. Breadth is not deduplicated
// during recursion, so a dense graph can still expand combinatorially before that collapse — depth
// is bounded by the caller (HardMaxDepth = 10 in graphquery), and raising it is not free.
//
// # Ranking before truncation
//
// The old BFS truncated at whatever the frontier happened to reach first. This orders by depth,
// then by in_degree_non_test descending, so a capped expansion keeps the *important* neighbourhood
// rather than the first-discovered one, and fq_name last so the result is deterministic.
//
// # Repository scoping
//
// The traversal is scoped at three points, not one: the seed row, every edge hop, and the final
// join back to symbols. Scoping only the seed would still walk edges into another repository the
// moment two repositories share a symbol id boundary, and scoping only the join would return an
// empty row for such a node rather than never visiting it.
func (s *Store) ExpandGraph(ctx context.Context, repoID, startID string, opt ExpandGraphOptions) ([]ExpandRow, error) {
	startID = strings.TrimSpace(startID)
	if startID == "" {
		return nil, fmt.Errorf("metadata: ExpandGraph requires a start symbol id")
	}
	if !opt.Callees && !opt.Callers {
		return nil, fmt.Errorf("metadata: ExpandGraph requires at least one direction")
	}
	if opt.MaxDepth <= 0 || opt.MaxNodes <= 0 {
		return nil, fmt.Errorf("metadata: ExpandGraph requires positive MaxDepth and MaxNodes")
	}

	var types any
	if len(opt.EdgeTypes) > 0 {
		up := make([]string, 0, len(opt.EdgeTypes))
		for _, t := range opt.EdgeTypes {
			if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
				up = append(up, t)
			}
		}
		if len(up) > 0 {
			types = pqTextArray(up)
		}
	}

	const q = `
WITH RECURSIVE walk AS (
    SELECT s.id, 0 AS depth, ''::text AS edge_type, false AS inbound,
           NULL::uuid AS via, ARRAY[s.id] AS path
      FROM symbols s
     WHERE s.id = $1::uuid AND s.repo_id = $7
    UNION ALL
    SELECT nxt.id, w.depth + 1, nxt.edge_type, nxt.inbound, w.id, w.path || nxt.id
      FROM walk w
      JOIN LATERAL (
            SELECT e.callee_symbol_id AS id, e.edge_type, false AS inbound
              FROM edges e
             WHERE $2::bool AND e.caller_symbol_id = w.id AND e.repo_id = $7
               AND ($4::text[] IS NULL OR e.edge_type = ANY($4::text[]))
            UNION ALL
            SELECT e.caller_symbol_id AS id, e.edge_type, true AS inbound
              FROM edges e
             WHERE $3::bool AND e.callee_symbol_id = w.id AND e.repo_id = $7
               AND ($4::text[] IS NULL OR e.edge_type = ANY($4::text[]))
      ) nxt ON true
     WHERE w.depth < $5
       AND NOT (nxt.id = ANY(w.path))
),
shallowest AS (
    SELECT DISTINCT ON (w.id) w.id, w.depth, w.edge_type, w.inbound, w.via
      FROM walk w
     WHERE w.depth > 0
     ORDER BY w.id, w.depth, w.edge_type
)
SELECT sh.id, sh.depth, sh.edge_type, sh.inbound, COALESCE(sh.via::text, ''),
       s.lang, s.kind, s.fq_name, s.file, s.start_line, s.end_line,
       s.in_degree, s.out_degree, s.in_degree_non_test, s.repo_id
  FROM shallowest sh
  JOIN symbols s ON s.id = sh.id AND s.repo_id = $7
 ORDER BY sh.depth ASC, s.in_degree_non_test DESC, s.fq_name ASC
 LIMIT $6`

	rows, err := s.db.Query(ctx, q, startID, opt.Callees, opt.Callers, types, opt.MaxDepth, opt.MaxNodes, repoID)
	if err != nil {
		return nil, fmt.Errorf("metadata: expand graph: %w", err)
	}
	defer rows.Close()

	var out []ExpandRow
	for rows.Next() {
		var r ExpandRow
		var sym Symbol
		// repo_id is selected so the returned symbol is self-describing: a caller that fans these
		// rows out to another query must not have to remember which repository it asked about.
		if err := rows.Scan(&sym.ID, &r.Depth, &r.EdgeType, &r.Inbound, &r.ViaFrom,
			&sym.Lang, &sym.Kind, &sym.FQName, &sym.File, &sym.StartLine, &sym.EndLine,
			&sym.InDegree, &sym.OutDegree, &sym.InDegreeNonTest, &sym.RepoID); err != nil {
			return out, err
		}
		r.Symbol = &sym
		out = append(out, r)
	}
	return out, rows.Err()
}

// pqTextArray renders a Go slice as a Postgres text[] literal.
//
// lib/pq's array helpers are not a dependency here, so the literal is built explicitly. Values are
// edge types — already uppercased and trimmed — but quotes and backslashes are escaped anyway
// rather than trusting the caller.
func pqTextArray(vals []string) any {
	if len(vals) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return sql.NullString{String: b.String(), Valid: true}
}
