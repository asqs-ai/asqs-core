package embeddings

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pgvector/pgvector-go"
)

// pgErrUndefinedColumn is Postgres SQLSTATE 42703. It is the one lexical-search failure that means
// "this corpus predates the content_tsv column", as opposed to "the query failed".
const pgErrUndefinedColumn = "42703"

// ErrLexicalIndexUnavailable reports that the corpus has no content_tsv column yet, so the lexical
// channel cannot contribute. Callers fall back to dense-only retrieval; it is not a run failure.
var ErrLexicalIndexUnavailable = errors.New("embeddings: lexical index unavailable (run asqs-core migrate)")

// SearchLexical ranks chunks by term overlap with query, using the same filter set as Search.
//
// Dense similarity is good at "code that does a similar thing" and poor at "code that mentions
// exactly this identifier". For a system whose central task is "find the existing test for
// OrderService.place and the collaborators it mocks", exact-identifier matching is first-order —
// and nothing in the system ranked a chunk higher because it literally contained the token
// `OrderRepository`.
//
// The embedding column IS selected unless opts.OmitEmbedding says otherwise. This function used to
// omit it unconditionally and ignore the flag, which made `OmitEmbedding: true` at the only caller
// decorative — and left every lexical-only hit entering the MMR pool with a nil embedding.
// cosineSimilarity(nil, …) is 0, so MMR read those chunks as maximally diverse and preferred them
// over dense hits: on the PetClinic corpus 26–32 of a 66–72 chunk pool had no embedding. See
// SearchOptions.OmitEmbedding, which already documented that omitting is unsafe for anything
// reaching MMR. Selecting the vector costs ~6 KB/row and the fusion consumer needs it; callers that
// genuinely want ranks only can still opt out.
//
// The ORDER BY carries a deterministic tie-break, and it is load-bearing rather than tidy-minded.
// ts_rank_cd is coarse: on an indexed Spring PetClinic, one query matched 74 chunks with only 16
// distinct scores. `ORDER BY score DESC` alone is therefore not a total order over the result set,
// and SQL does not promise any particular resolution of the remainder — it falls out of whatever
// order the executor happens to emit rows in.
//
// This is not theoretical. The ireval golden suite scored nDCG@10 0.3968 under `fusion: rrf`, then
// 0.2543 on a later run with the same binary, the same corpus and the same suite; each value was
// stable across repeated runs at the time. Ranks feed RRF, so an unordered tail reshuffles the
// fused result. The trigger was not isolated (the likely candidate is a planner switch after the
// table rewrite that added content_tsv changed the statistics), but the defect does not depend on
// knowing it: without a total order the result is unreproducible, and an A/B built on an
// unreproducible measurement means nothing.
//
// Sorting by (score, file, start_line, id) is total — id is the primary key — and the leading
// file/start_line keys keep the resolution semantically stable rather than UUID-arbitrary. Same
// defect class as the gap-ordering tie-break, one layer down.
func (s *Store) SearchLexical(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	q := orTSQuery(query)
	if q == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	var args []interface{}
	var where []string
	argNum := 0

	argNum++
	args = append(args, q)
	queryArg := argNum

	if opts.File != "" {
		argNum++
		where = append(where, fmt.Sprintf("file = $%d", argNum))
		args = append(args, opts.File)
	} else if opts.FilePrefix != "" {
		argNum++
		where = append(where, fmt.Sprintf("file LIKE $%d || '%%'", argNum))
		args = append(args, opts.FilePrefix)
	}
	if opts.SymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("symbol_id = $%d", argNum))
		args = append(args, opts.SymbolID)
	}
	if opts.ParentSymbolID != "" {
		argNum++
		where = append(where, fmt.Sprintf("parent_symbol_id = $%d", argNum))
		args = append(args, opts.ParentSymbolID)
	}
	if opts.RepoID != "" {
		argNum++
		where = append(where, fmt.Sprintf("repo_id = $%d", argNum))
		args = append(args, opts.RepoID)
	}
	if opts.ChunkType == "" && opts.ExcludeChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type <> $%d", argNum))
		args = append(args, opts.ExcludeChunkType)
	}
	if opts.ChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type = $%d", argNum))
		args = append(args, opts.ChunkType)
	}
	if opts.Lang != "" {
		argNum++
		where = append(where, fmt.Sprintf("lang = $%d", argNum))
		args = append(args, opts.Lang)
	}
	if strings.TrimSpace(opts.Module) != "" {
		argNum++
		where = append(where, fmt.Sprintf("COALESCE(chunk_metadata->>'module','') = $%d", argNum))
		args = append(args, strings.TrimSpace(opts.Module))
	}
	where = append(where, fmt.Sprintf("content_tsv @@ to_tsquery('simple', $%d)", queryArg))

	argNum++
	args = append(args, limit)

	embeddingCol := ""
	if !opts.OmitEmbedding {
		embeddingCol = "embedding, "
	}
	sql := fmt.Sprintf(`
		SELECT id, content, %ssymbol_id, file, lang, chunk_type, start_line, end_line, repo_id,
		       chunk_metadata, parent_symbol_id,
		       ts_rank_cd(content_tsv, to_tsquery('simple', $%d)) AS score
		FROM chunks
		WHERE %s
		ORDER BY score DESC, file, start_line, id
		LIMIT $%d`, embeddingCol, queryArg, strings.Join(where, " AND "), argNum)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		// Only ONE failure is expected and recoverable: a corpus indexed before content_tsv existed
		// has no such column, and the caller should fall back to dense-only.
		//
		// This used to `return nil, nil` for every error. That is the same silent-nothing failure
		// mode that made the lexical channel inert for months (plainto_tsquery conjoined its terms
		// and matched 0 of 387 chunks, so `fusion: rrf` was byte-identical to `fusion: dense` and
		// B09's A/B could not have shown a difference in either direction). Swallowing statement
		// timeouts, dropped connections, permission errors and tsquery syntax errors reproduces it
		// exactly, so they are surfaced.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgErrUndefinedColumn {
			return nil, ErrLexicalIndexUnavailable
		}
		return nil, fmt.Errorf("embeddings: lexical search: %w", err)
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var symbolID, parentID *string
		var meta []byte
		var score float64
		var vec pgvector.Vector
		scanTargets := []any{&r.ID, &r.Content}
		if !opts.OmitEmbedding {
			scanTargets = append(scanTargets, &vec)
		}
		scanTargets = append(scanTargets, &symbolID, &r.File, &r.Lang, &r.ChunkType,
			&r.StartLine, &r.EndLine, &r.RepoID, &meta, &parentID, &score)
		// A scan failure used to return (out, nil): a silently truncated result set, which for a
		// ranked list means a silently truncated ranking.
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, fmt.Errorf("embeddings: scan lexical row: %w", err)
		}
		if !opts.OmitEmbedding {
			r.Embedding = vec.Slice()
		}
		if symbolID != nil {
			r.SymbolID = *symbolID
		}
		if parentID != nil {
			r.ParentSymbolID = *parentID
		}
		if len(meta) > 0 {
			r.MetadataJSON = append([]byte(nil), meta...)
		}
		// Distance is unused for lexical hits; the fusion step ranks, it does not compare scores
		// across channels (that comparison is exactly what RRF exists to avoid).
		out = append(out, r)
	}
	return out, rows.Err()
}

// tsqueryToken keeps only characters that are safe inside a tsquery lexeme. Everything a caller
// synthesizes comes from identifiers, so this discards separators rather than escaping them.
var tsqueryToken = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// orTSQuery turns a space-separated term list into a disjunctive tsquery ("a | b | c").
//
// The terms MUST be OR'd. plainto_tsquery conjoins them, and the queries this store receives are
// synthesized from a symbol — for `OwnerController#processCreationForm` that is thirteen terms
// (the identifier, its camelCase parts, the enclosing type, every signature type name and their
// parts). Requiring all thirteen to co-occur in one chunk matches nothing: measured against an
// indexed Spring PetClinic, the conjunctive form returned 0 rows for that query and the disjunctive
// form returned 119. A channel that always returns zero rows contributes nothing to the fusion, so
// `fusion: rrf` was silently identical to `fusion: dense` — the comparison it exists to enable
// could not have shown a difference.
//
// Disjunction does not flatten ranking: ts_rank_cd scores by how many query lexemes a document
// matches and how close together they are, so a chunk matching six terms still outranks one
// matching one.
func orTSQuery(query string) string {
	seen := map[string]bool{}
	var terms []string
	for _, f := range strings.Fields(query) {
		t := tsqueryToken.ReplaceAllString(f, "")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		terms = append(terms, t)
	}
	return strings.Join(terms, " | ")
}

// SearchByPathPattern ranks chunks whose path contains any of the given substrings, ordered by
// vector distance to the query embedding.
//
// It replaces a List(limit:100) + filter-in-Go pattern that fetched 100 chunks ordered
// ALPHABETICALLY BY FILE PATH, with no lang filter, no chunk_type filter and no relevance signal.
// On any repo with more than 100 chunks — i.e. every real repo — that returned whatever sorted
// first alphabetically. For a Java repo that is likely `src/main/java/com/acme/api/...`, so the
// actual fixtures under `src/test/resources/` were never reached.
//
// The consequence was worse than an empty section: two of the ten context sections consumed prompt
// budget with arbitrary content while implying to the model that these ARE the project's fixtures.
func (s *Store) SearchByPathPattern(ctx context.Context, queryEmbedding []float32, opts SearchOptions, pathSubstrings []string) ([]SearchResult, error) {
	if len(pathSubstrings) == 0 {
		return nil, nil
	}
	if len(queryEmbedding) != s.dim {
		return nil, fmt.Errorf("embeddings: query embedding length %d != store dimension %d", len(queryEmbedding), s.dim)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	vec := pgvector.NewVector(queryEmbedding)
	args := []interface{}{vec}
	argNum := 1
	var where []string

	if opts.RepoID != "" {
		argNum++
		where = append(where, fmt.Sprintf("repo_id = $%d", argNum))
		args = append(args, opts.RepoID)
	}
	if opts.Lang != "" {
		argNum++
		where = append(where, fmt.Sprintf("lang = $%d", argNum))
		args = append(args, opts.Lang)
	}
	if opts.ChunkType == "" && opts.ExcludeChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type <> $%d", argNum))
		args = append(args, opts.ExcludeChunkType)
	}
	if opts.ChunkType != "" {
		argNum++
		where = append(where, fmt.Sprintf("chunk_type = $%d", argNum))
		args = append(args, opts.ChunkType)
	}
	var pathOr []string
	for _, sub := range pathSubstrings {
		sub = strings.TrimSpace(strings.ToLower(sub))
		if sub == "" {
			continue
		}
		argNum++
		pathOr = append(pathOr, fmt.Sprintf("lower(file) LIKE '%%' || $%d || '%%'", argNum))
		args = append(args, sub)
	}
	if len(pathOr) == 0 {
		return nil, nil
	}
	where = append(where, "("+strings.Join(pathOr, " OR ")+")")

	argNum++
	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT id, content, symbol_id, file, lang, chunk_type, start_line, end_line, repo_id,
		       chunk_metadata, parent_symbol_id, embedding <-> $1 AS distance
		FROM chunks
		WHERE %s
		ORDER BY embedding <-> $1
		LIMIT $%d`, strings.Join(where, " AND "), argNum)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var symbolID, parentID *string
		var meta []byte
		if err := rows.Scan(&r.ID, &r.Content, &symbolID, &r.File, &r.Lang, &r.ChunkType,
			&r.StartLine, &r.EndLine, &r.RepoID, &meta, &parentID, &r.Distance); err != nil {
			return out, err
		}
		if symbolID != nil {
			r.SymbolID = *symbolID
		}
		if parentID != nil {
			r.ParentSymbolID = *parentID
		}
		if len(meta) > 0 {
			r.MetadataJSON = append([]byte(nil), meta...)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
