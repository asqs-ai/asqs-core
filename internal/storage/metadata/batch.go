package metadata

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// symbolInsertQuery is shared by InsertSymbol and InsertSymbols so the two paths cannot drift. A
// column added to one and not the other would be a silent difference between indexing a file with
// one symbol and indexing it with two.
//
// This is a plain insert: the upsert on the natural key (with dup_ordinal), which makes symbol ids
// stable across reindexes, arrives with the stable-identity bundle (CP13) and upgrades this
// constant in place.
const symbolInsertQuery = `
		INSERT INTO symbols (lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json, repo_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`

// symbolInsertArgs normalizes one symbol into the bind parameters symbolInsertQuery expects.
//
// lang/kind are lowercased at write time: queries used to compare LOWER(s.lang) = LOWER($1), which
// defeats idx_symbols_lang because there is no matching expression index, so the gap-listing hot
// path could not use an index at all.
func symbolInsertArgs(sym *Symbol) []any {
	var sig *[]byte
	if len(sym.SignatureJSON) > 0 {
		sig = &sym.SignatureJSON
	}
	var startCol, endCol any
	if sym.StartColumn != nil {
		startCol = *sym.StartColumn
	}
	if sym.EndColumn != nil {
		endCol = *sym.EndColumn
	}
	return []any{
		strings.ToLower(strings.TrimSpace(sym.Lang)), strings.ToLower(strings.TrimSpace(sym.Kind)),
		sym.FQName, sym.File, sym.StartLine, sym.EndLine, startCol, endCol, sig,
		strings.TrimSpace(sym.RepoID),
	}
}

// InsertSymbols inserts many symbols in ONE round trip and returns their generated ids, in the same
// order as the input.
//
// Indexing issued one `INSERT … RETURNING id` per symbol. A 200k-symbol repository therefore paid
// 200k sequential round trips — around a minute of pure latency at 0.3 ms on localhost, and far
// worse against a networked Postgres. The work is round-trip bound, not CPU bound, which is why
// batching is the whole fix.
//
// RETURNING id is kept: the caller needs the ids immediately to resolve edges within the same file.
//
// # Atomicity
//
// The batch runs inside a transaction, so a failure anywhere leaves NO symbols for the file rather
// than a prefix of them. That matters more here than it looks: the caller deletes a file's existing
// symbols before re-inserting, so a partially applied batch would leave the file with fewer symbols
// than it had before the run — silent, and only visible as retrieval quality that quietly dropped.
// pgx sends the whole batch in one flush; without the transaction, statements before the failing
// one would already have committed.
func (s *Store) InsertSymbols(ctx context.Context, syms []*Symbol) ([]string, error) {
	if len(syms) == 0 {
		return nil, nil
	}
	// One statement is atomic on its own, so wrapping it costs a BEGIN and a COMMIT — two round
	// trips to protect a single one. Measured upstream: files with one symbol indexed ~16% SLOWER
	// through the transactional path than through the plain insert it replaced. Files that small
	// are common (interfaces, enums, barrel files), so the fast path is not a micro-optimisation.
	if len(syms) == 1 {
		if syms[0] == nil {
			return nil, fmt.Errorf("metadata: insert symbols: nil symbol at index 0")
		}
		id, err := s.InsertSymbol(ctx, syms[0])
		if err != nil {
			return nil, err
		}
		return []string{id}, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("metadata: insert symbols: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, sym := range syms {
		if sym == nil {
			return nil, fmt.Errorf("metadata: insert symbols: nil symbol at index %d", len(batch.QueuedQueries))
		}
		batch.Queue(symbolInsertQuery, symbolInsertArgs(sym)...)
	}

	br := tx.SendBatch(ctx, batch)
	ids := make([]string, 0, len(syms))
	for i := range syms {
		var id string
		if err := br.QueryRow().Scan(&id); err != nil {
			// Close before returning: the batch result must be drained or the connection is left
			// mid-protocol and every later statement on it fails with a confusing error.
			_ = br.Close()
			return nil, fmt.Errorf("metadata: insert symbol %s: %w", syms[i].FQName, err)
		}
		ids = append(ids, id)
	}
	if err := br.Close(); err != nil {
		return nil, fmt.Errorf("metadata: insert symbols: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("metadata: insert symbols: commit: %w", err)
	}
	return ids, nil
}

// InsertEdges inserts many edges in one round trip, ignoring duplicates.
//
// Same shape and the same reasoning as InsertSymbols. Edges outnumber symbols on most corpora, so
// this is the larger half of the saving.
func (s *Store) InsertEdges(ctx context.Context, edges []*Edge) error {
	if len(edges) == 0 {
		return nil
	}
	// Same single-statement fast path as InsertSymbols.
	if len(edges) == 1 {
		if edges[0] == nil {
			return nil
		}
		return s.InsertEdge(ctx, edges[0])
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("metadata: insert edges: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, e := range edges {
		if e == nil {
			continue
		}
		batch.Queue(edgeInsertQuery, e.CallerSymbolID, e.CalleeSymbolID, e.EdgeType, e.RepoID)
	}
	if batch.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("metadata: insert edges: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("metadata: insert edges: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("metadata: insert edges: commit: %w", err)
	}
	return nil
}

// ListSymbolsByFQNames resolves many fully-qualified names in ONE query.
//
// Edge resolution asked the database once per unresolved callee name. On a file with 200 imports
// and calls that is 200 sequential round trips to answer a question one `= ANY(...)` covers, and it
// ran per file across the whole repository.
//
// The result is keyed by fq_name; names with no rows are simply absent, so a caller can tell "not
// indexed" from "indexed once" without a second query. Row order within a name matches
// ListSymbolsByFQName's (file, start_line), which the disambiguation in the indexer depends on:
// with no file hint it takes the first row, and an unstable order there would make edge resolution
// irreproducible between runs.
func (s *Store) ListSymbolsByFQNames(ctx context.Context, repoID string, fqNames []string) (map[string][]*Symbol, error) {
	if len(fqNames) == 0 {
		return nil, nil
	}
	// Deduplicate: callers collect names per file and the same import appears many times.
	seen := make(map[string]struct{}, len(fqNames))
	uniq := make([]string, 0, len(fqNames))
	for _, n := range fqNames {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		uniq = append(uniq, n)
	}
	if len(uniq) == 0 {
		return nil, nil
	}

	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json, in_degree, out_degree, in_degree_non_test
		FROM symbols WHERE repo_id = $1 AND fq_name = ANY($2) ORDER BY fq_name, file, start_line`
	rows, err := s.db.Query(ctx, query, repoID, uniq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanSymbols(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]*Symbol, len(uniq))
	for _, sym := range list {
		if sym == nil {
			continue
		}
		out[sym.FQName] = append(out[sym.FQName], sym)
	}
	return out, nil
}
