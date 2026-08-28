package metadata

import (
	"context"
	"strings"
	"sync"
)

// simpleNameProbe caches whether symbols.simple_name exists.
//
// The column is a STORED generated column added by `asqs-core migrate` (0002), not by
// InitSchema — ADD COLUMN ... GENERATED ... STORED rewrites the table on PG 12+, which must be a
// deliberate operator action rather than a restart side effect. So the fast queries below are
// conditional: a database that has not been migrated keeps the old, correct-but-slow predicates.
type simpleNameProbe struct {
	once sync.Once
	has  bool
}

func (s *Store) hasSimpleNameColumn(ctx context.Context) bool {
	s.simpleName.once.Do(func() {
		const q = `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'symbols' AND column_name = 'simple_name'
			)`
		var ok bool
		if err := s.db.QueryRow(ctx, q).Scan(&ok); err != nil {
			s.simpleName.has = false
			return
		}
		s.simpleName.has = ok
	})
	return s.simpleName.has
}

// hasTrigramIndex reports whether pg_trgm is installed, which decides whether ListSymbolsByFQSubstring
// can use similarity() ordering. Cached like simple_name.
func (s *Store) hasTrigram(ctx context.Context) bool {
	s.trigram.once.Do(func() {
		const q = `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')`
		var ok bool
		if err := s.db.QueryRow(ctx, q).Scan(&ok); err != nil {
			s.trigram.has = false
			return
		}
		s.trigram.has = ok
	})
	return s.trigram.has
}

// ListSymbolsByBareFQName resolves the PRE-B25 (parameterless, generic-free) form of a C# name
// via signature_json.bare_fq_name — a model that read "OrderService#GetOrder" in prose can still
// get_symbol it even though the stored FQName is "OrderService#GetOrder(string)". Only C# symbols
// carry the key, so other languages can never false-hit. Returns overloads in deterministic order;
// the caller's existing ambiguity handling applies.
func (s *Store) ListSymbolsByBareFQName(ctx context.Context, repoID, bareFQ string) ([]*Symbol, error) {
	bareFQ = strings.TrimSpace(bareFQ)
	if bareFQ == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json, in_degree, out_degree, in_degree_non_test
		FROM symbols
		WHERE repo_id = $1 AND signature_json->>'bare_fq_name' = $2
		ORDER BY fq_name, file, start_line, id`, repoID, bareFQ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// CountLegacyCSharpFQNames counts C# callable symbols still in the pre-B25 FQName format (a '#'
// member with no parameter list). Any positive count means the stored graph predates the format
// change, and the indexer forces a full reindex — mixed formats would make overload gaps, edge
// binding and lookups silently inconsistent (Spec 4(b): "reindex requirement is enforced").
func (s *Store) CountLegacyCSharpFQNames(ctx context.Context, repoID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM symbols
		WHERE repo_id = $1
		  AND lower(lang) IN ('csharp', 'cs')
		  AND lower(kind) IN ('method', 'constructor')
		  AND fq_name LIKE '%#%'
		  AND fq_name NOT LIKE '%(%'`, repoID).Scan(&n)
	return n, err
}
