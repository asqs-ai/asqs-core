package metadata

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asqs/asqs-core/internal/sqlsplit"
)

//go:embed schema.sql
var schemaFS embed.FS

// querier is the subset of *pgxpool.Pool this package uses. Production always holds a real pool;
// the interface exists so connection-failure tests (the retry tests arriving with the materialize
// port) can still inject transient errors, which they used to do by registering a fake
// database/sql driver — an option pgxpool does not offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
	Ping(ctx context.Context) error
}

var _ querier = (*pgxpool.Pool)(nil)

// Store provides access to metadata tables (symbols, edges, files).
//
// Nullable columns are still scanned into database/sql's sql.NullX types. That is deliberate and
// not leftover: pgx routes any destination implementing sql.Scanner through the codec's
// DecodeDatabaseSQLValue, which yields the same value database/sql delivered, so null semantics
// and scanned values are unchanged by the pgx migration. Rewriting 60-odd destinations to
// pgtype/pointer equivalents would have been 60 chances to change behaviour in a bundle whose
// whole point is that behaviour does not change.
type Store struct {
	db querier
	// pool is the handle db is backed by, or nil when a test injected a fake. It owns Close and
	// exposes pool statistics; every query goes through db.
	pool *pgxpool.Pool
}

// Config configures the metadata connection pool. Only ConnString is required.
type Config struct {
	// ConnString is a libpq connection string or postgres:// URL.
	ConnString string
	// MaxConns caps the pool. 0 leaves pgxpool's default, which is max(4, NumCPU).
	MaxConns int32
	// MinConns is the pool floor kept warm. 0 leaves pgxpool's default of 0.
	MinConns int32
}

// Open opens a Postgres connection pool with default sizing and returns a Store. connString must be
// a valid libpq connection string. Use OpenWithConfig to size the pool.
func Open(connString string) (*Store, error) {
	return OpenWithConfig(context.Background(), Config{ConnString: connString})
}

// OpenWithConfig opens a Postgres connection pool with explicit sizing and returns a Store.
func OpenWithConfig(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.ConnString) == "" {
		return nil, fmt.Errorf("metadata open: ConnString required")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.ConnString)
	if err != nil {
		return nil, fmt.Errorf("metadata open: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("metadata open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("metadata ping: %w", err)
	}
	return &Store{db: pool, pool: pool}, nil
}

// Close closes the connection pool. It returns an error only to keep the call sites that check one
// compiling; pgxpool.Close cannot fail.
func (s *Store) Close() error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

// PoolStat reports connection pool statistics, or nil when the store is backed by a test fake.
func (s *Store) PoolStat() *pgxpool.Stat {
	if s.pool == nil {
		return nil
	}
	return s.pool.Stat()
}

// InitSchema runs the embedded schema.sql to create tables and indexes if they do not exist.
func (s *Store) InitSchema(ctx context.Context) error {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	// sqlsplit understands string literals, dollar quotes and comments, so a semicolon inside any of
	// them is no longer a statement boundary. This used to be a naive strings.Split on ';', under
	// which a prose comment containing a semicolon split its statement in half and produced a
	// "syntax error at end of input" naming the statement's opening comment rather than the comment
	// that broke it.
	statements := sqlsplit.Statements(string(b))
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("exec schema %q: %w", truncate(stmt, 60), err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Symbols ---

// InsertSymbol inserts a symbol and returns its generated ID.
func (s *Store) InsertSymbol(ctx context.Context, sym *Symbol) (id string, err error) {
	query := `
		INSERT INTO symbols (lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json, repo_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`
	var sig *[]byte
	if len(sym.SignatureJSON) > 0 {
		sig = &sym.SignatureJSON
	}
	var startCol, endCol interface{}
	if sym.StartColumn != nil {
		startCol = *sym.StartColumn
	}
	if sym.EndColumn != nil {
		endCol = *sym.EndColumn
	}
	err = s.db.QueryRow(ctx, query,
		sym.Lang, sym.Kind, sym.FQName, sym.File, sym.StartLine, sym.EndLine, startCol, endCol, sig, sym.RepoID,
	).Scan(&id)
	return id, err
}

// DeleteSymbolsByFile deletes the repository's symbols (and their edges via cascade) for the given
// file. Use before reindexing. The repo_id predicate is what keeps two repositories sharing a
// relative path — `pom.xml`, `package.json` — from stripping each other's rows mid-run.
func (s *Store) DeleteSymbolsByFile(ctx context.Context, repoID, file string) (deleted int64, err error) {
	res, err := s.db.Exec(ctx, "DELETE FROM symbols WHERE file = $1 AND repo_id = $2",
		file, strings.TrimSpace(repoID))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

// DeleteFile removes the repository's `files` row for a path. Call after DeleteSymbolsByFile when
// removing a file from the index.
//
// This ran as `DELETE FROM files WHERE file = $1` — no repo predicate — while the cross-repo fix
// scoped the symbol and chunk deletes around it. So repos A and B both indexed, both containing
// `package.json`; B's tree drops it; B's run deletes B's chunks and symbols correctly and then
// deletes A's `files` row. A's symbols and chunks survive but lose their `files` join, and with it
// `is_test`, `lang`, `module` and `sha`: MaterializeTestsSourceEdges INNER JOINs `files` and drops
// those symbols, GetFile returns nil in the retrieve path, and A's next DetectChanges reclassifies
// the path as new.
//
// `files` now carries its own repo_id and is keyed (repo_id, file), so ownership is a column
// predicate rather than an inference. An empty repoID still matches the empty repo_id exactly,
// never as a wildcard, so an unscoped run cannot delete a scoped repository's row.
//
// Returns whether the row was actually removed, so a caller can report retained rows rather than
// silently believing it cleaned up.
func (s *Store) DeleteFile(ctx context.Context, repoID, file string) (deleted bool, err error) {
	res, err := s.db.Exec(ctx, `DELETE FROM files WHERE repo_id = $2 AND file = $1`,
		file, strings.TrimSpace(repoID))
	if err != nil {
		return false, err
	}
	return res.RowsAffected() > 0, nil
}

// GetSymbolByID returns a symbol by primary key, scoped to repoID, or nil if not found.
//
// The id is a UUID and therefore already unique across repositories, so the repo_id predicate is
// not needed for correctness of the LOOKUP — it is needed because the id itself can arrive from
// outside. Passing an empty repoID is a programming error and returns nothing rather than
// everything.
func (s *Store) GetSymbolByID(ctx context.Context, repoID, id string) (*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE id = $1 AND repo_id = $2`
	var sym Symbol
	var sig sql.Null[[]byte]
	var startCol, endCol sql.NullInt32
	err := s.db.QueryRow(ctx, query, id, repoID).Scan(
		&sym.ID, &sym.Lang, &sym.Kind, &sym.FQName, &sym.File,
		&sym.StartLine, &sym.EndLine, &startCol, &endCol, &sig,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	applySymbolColumns(&sym, startCol, endCol)
	if sig.Valid {
		sym.SignatureJSON = sig.V
	}
	return &sym, nil
}

// ListSymbolsByFile returns all symbols in the given file, ordered by start_line.
func (s *Store) ListSymbolsByFile(ctx context.Context, repoID, file string) ([]*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE repo_id = $1 AND file = $2 ORDER BY start_line`
	rows, err := s.db.Query(ctx, query, repoID, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByFQSubstring returns symbols whose fq_name contains needle (case-insensitive), ordered by fq_name, capped.
func (s *Store) ListSymbolsByFQSubstring(ctx context.Context, repoID, needle string, limit int) ([]*Symbol, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return nil, nil
	}
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols
		WHERE repo_id = $3 AND strpos(lower(fq_name), lower($1)) > 0
		ORDER BY fq_name
		LIMIT $2`
	rows, err := s.db.Query(ctx, query, needle, limit, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByFQName returns all symbols with the given fully qualified name (may be multiple overloads/locations).
func (s *Store) ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE repo_id = $1 AND fq_name = $2 ORDER BY file, start_line`
	rows, err := s.db.Query(ctx, query, repoID, fqName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByTypeSimpleName returns TYPE symbols (class/interface/struct/record/enum/type) whose
// fully-qualified name's final segment equals simpleName — e.g. "Order" resolves
// "com.example.javatest.model.Order" regardless of which package the caller lives in. The match is
// anchored at the package separator ('.') so "Order" does NOT match "OrderController" or
// "PurchaseOrder". Used by retrieval to resolve cross-package param/return/field types into domain
// models + collaborators (the prior `<module>.<name>` guess only found same-package types). Capped.
func (s *Store) ListSymbolsByTypeSimpleName(ctx context.Context, repoID, simpleName string, limit int) ([]*Symbol, error) {
	simpleName = strings.TrimSpace(simpleName)
	if simpleName == "" {
		return nil, nil
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols
		WHERE repo_id = $3
		  AND lower(kind) IN ('class','interface','struct','record','enum','type','type_alias','object')
		  AND (fq_name = $1 OR fq_name LIKE '%.' || $1)
		ORDER BY length(fq_name), fq_name
		LIMIT $2`
	rows, err := s.db.Query(ctx, query, simpleName, limit, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByLang returns symbols for the given language, optionally filtered by kind.
func (s *Store) ListSymbolsByLang(ctx context.Context, repoID, lang string, kind string) ([]*Symbol, error) {
	if kind != "" {
		query := `
			SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
			FROM symbols WHERE repo_id = $3 AND lang = $1 AND kind = $2 ORDER BY file, start_line`
		rows, err := s.db.Query(ctx, query, lang, kind, repoID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanSymbols(rows)
	}
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE repo_id = $2 AND lang = $1 ORDER BY file, start_line`
	rows, err := s.db.Query(ctx, query, lang, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func applySymbolColumns(sym *Symbol, startCol, endCol sql.NullInt32) {
	if startCol.Valid {
		v := int(startCol.Int32)
		sym.StartColumn = &v
	}
	if endCol.Valid {
		v := int(endCol.Int32)
		sym.EndColumn = &v
	}
}

func scanSymbols(rows pgx.Rows) ([]*Symbol, error) {
	var list []*Symbol
	for rows.Next() {
		var sym Symbol
		var sig sql.Null[[]byte]
		var startCol, endCol sql.NullInt32
		if err := rows.Scan(
			&sym.ID, &sym.Lang, &sym.Kind, &sym.FQName, &sym.File,
			&sym.StartLine, &sym.EndLine, &startCol, &endCol, &sig,
		); err != nil {
			return nil, err
		}
		applySymbolColumns(&sym, startCol, endCol)
		if sig.Valid {
			sym.SignatureJSON = sig.V
		}
		list = append(list, &sym)
	}
	return list, rows.Err()
}

// ListSymbolsInNonTestFiles returns symbols of the given kind (e.g. "method") from files where is_test = false.
// Used for test-gap analysis (find methods that may need tests).
func (s *Store) ListSymbolsInNonTestFiles(ctx context.Context, repoID, lang, kind string) ([]*Symbol, error) {
	// The join carries repo_id as well as file. Without it a symbol joins any repository's row for
	// the same relative path, so `src/index.ts` being a test file in one repository hid the other
	// repository's symbols from gap analysis entirely.
	query := `
		SELECT s.id, s.lang, s.kind, s.fq_name, s.file, s.start_line, s.end_line, s.start_column, s.end_column, s.signature_json
		FROM symbols s
		INNER JOIN files f ON s.file = f.file AND s.repo_id = f.repo_id
		WHERE s.repo_id = $3 AND f.is_test = false AND LOWER(s.lang) = LOWER($1) AND s.kind = $2
		  AND LOWER(s.file) NOT LIKE '%.d.ts'
		ORDER BY s.file, s.start_line`
	rows, err := s.db.Query(ctx, query, lang, kind, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsInTestFiles returns symbols of the given kind from files where is_test = true (e.g. E2E specs).
func (s *Store) ListSymbolsInTestFiles(ctx context.Context, repoID, lang, kind string) ([]*Symbol, error) {
	query := `
		SELECT s.id, s.lang, s.kind, s.fq_name, s.file, s.start_line, s.end_line, s.start_column, s.end_column, s.signature_json
		FROM symbols s
		INNER JOIN files f ON s.file = f.file AND s.repo_id = f.repo_id
		WHERE s.repo_id = $3 AND f.is_test = true AND LOWER(s.lang) = LOWER($1) AND s.kind = $2
		ORDER BY s.file, s.start_line`
	rows, err := s.db.Query(ctx, query, lang, kind, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// --- Edges ---

// edgeInsertQuery writes an edge with its denormalized repo_id.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING so a re-index repairs the repo_id of an edge written
// before repository scoping, instead of silently keeping it unattributed forever.
const edgeInsertQuery = `
		INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type, repo_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO UPDATE SET repo_id = EXCLUDED.repo_id`

// InsertEdge inserts an edge. Idempotent if (caller, callee, type) already exists.
func (s *Store) InsertEdge(ctx context.Context, e *Edge) error {
	_, err := s.db.Exec(ctx, edgeInsertQuery, e.CallerSymbolID, e.CalleeSymbolID, e.EdgeType, e.RepoID)
	return err
}

// GetEdgesFrom returns all edges whose caller is the given symbol ID.
func (s *Store) GetEdgesFrom(ctx context.Context, repoID, callerSymbolID string) ([]*Edge, error) {
	query := `
		SELECT caller_symbol_id, callee_symbol_id, edge_type, repo_id
		FROM edges WHERE repo_id = $1 AND caller_symbol_id = $2`
	rows, err := s.db.Query(ctx, query, repoID, callerSymbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// GetEdgesTo returns all edges whose callee is the given symbol ID (inbound: who references this symbol).
// Uses idx_edges_callee. Use for “who targets this route/DTO?” style expansion.
func (s *Store) GetEdgesTo(ctx context.Context, repoID, calleeSymbolID string) ([]*Edge, error) {
	query := `
		SELECT caller_symbol_id, callee_symbol_id, edge_type, repo_id
		FROM edges WHERE repo_id = $1 AND callee_symbol_id = $2`
	rows, err := s.db.Query(ctx, query, repoID, calleeSymbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

func scanEdges(rows pgx.Rows) ([]*Edge, error) {
	var list []*Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.CallerSymbolID, &e.CalleeSymbolID, &e.EdgeType, &e.RepoID); err != nil {
			return nil, err
		}
		list = append(list, &e)
	}
	return list, rows.Err()
}

// ListEdgeFiles returns edges as file→file pairs (from symbol edges joined to symbols.file).
// If lang is empty, all languages are included (caller and callee always share the same lang on an edge).
// If lang is non-empty, only edges whose symbols are that language (typical: workflow dominant lang).
// Used to build a file-level dependency graph for the overview document.
func (s *Store) ListEdgeFiles(ctx context.Context, repoID, lang string) ([]*EdgeFile, error) {
	lang = strings.TrimSpace(lang)
	var (
		query string
		rows  pgx.Rows
		err   error
	)
	if lang == "" {
		query = `
		SELECT s1.file AS caller_file, s2.file AS callee_file, e.edge_type
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller_symbol_id
		JOIN symbols s2 ON s2.id = e.callee_symbol_id
		WHERE e.repo_id = $1 AND s1.repo_id = $1 AND s2.repo_id = $1 AND s1.lang = s2.lang`
		rows, err = s.db.Query(ctx, query, repoID)
	} else {
		query = `
		SELECT s1.file AS caller_file, s2.file AS callee_file, e.edge_type
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller_symbol_id
		JOIN symbols s2 ON s2.id = e.callee_symbol_id
		WHERE e.repo_id = $2 AND s1.repo_id = $2 AND s2.repo_id = $2 AND s1.lang = $1 AND s2.lang = $1`
		rows, err = s.db.Query(ctx, query, lang, repoID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*EdgeFile
	for rows.Next() {
		var e EdgeFile
		if err := rows.Scan(&e.CallerFile, &e.CalleeFile, &e.EdgeType); err != nil {
			return nil, err
		}
		list = append(list, &e)
	}
	return list, rows.Err()
}

// --- Files ---

// UpsertFile inserts or updates a file row, keyed by (repo_id, file).
//
// The conflict target is the primary key as of the repo-scoping migration. On a database whose key
// is still `file` alone this statement fails loudly with SQLSTATE 42P10 — which is the intended
// failure mode: silently upserting under the old key is how one repository's SHA ended up
// answering another repository's change detection. schema.sql moves the key itself (guarded DO
// block), so any database that has been through InitSchema has the composite key.
func (s *Store) UpsertFile(ctx context.Context, f *File) error {
	query := `
		INSERT INTO files (file, sha, lang, module, is_test, repo_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (repo_id, file) DO UPDATE SET sha = $2, lang = $3, module = $4, is_test = $5`
	_, err := s.db.Exec(ctx, query, f.File, f.SHA, f.Lang, f.Module, f.IsTest, f.RepoID)
	return err
}

// GetFile returns the file row for the given path within repoID, or nil if not found.
func (s *Store) GetFile(ctx context.Context, repoID, file string) (*File, error) {
	query := `SELECT file, sha, lang, module, is_test, repo_id FROM files WHERE repo_id = $1 AND file = $2`
	var f File
	err := s.db.QueryRow(ctx, query, repoID, file).Scan(&f.File, &f.SHA, &f.Lang, &f.Module, &f.IsTest, &f.RepoID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ListFiles returns all files, optionally filtered by lang and is_test.
func (s *Store) ListFiles(ctx context.Context, repoID, lang string, isTest *bool) ([]*File, error) {
	if lang != "" && isTest != nil {
		query := `SELECT file, sha, lang, module, is_test, repo_id FROM files WHERE repo_id = $3 AND lang = $1 AND is_test = $2 ORDER BY file`
		rows, err := s.db.Query(ctx, query, lang, *isTest, repoID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	if lang != "" {
		query := `SELECT file, sha, lang, module, is_test, repo_id FROM files WHERE repo_id = $2 AND lang = $1 ORDER BY file`
		rows, err := s.db.Query(ctx, query, lang, repoID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	if isTest != nil {
		query := `SELECT file, sha, lang, module, is_test, repo_id FROM files WHERE repo_id = $2 AND is_test = $1 ORDER BY file`
		rows, err := s.db.Query(ctx, query, *isTest, repoID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	query := `SELECT file, sha, lang, module, is_test, repo_id FROM files WHERE repo_id = $1 ORDER BY file`
	rows, err := s.db.Query(ctx, query, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func scanFiles(rows pgx.Rows) ([]*File, error) {
	var list []*File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.File, &f.SHA, &f.Lang, &f.Module, &f.IsTest, &f.RepoID); err != nil {
			return nil, err
		}
		list = append(list, &f)
	}
	return list, rows.Err()
}

// --- Index runs (versioning / scheduling) ---

// IndexRunStartExtras optional control-plane fields persisted on index_runs (API / scheduler).
// RepoURL is set when the run clones or records a canonical URL (including resolved from projects.repo_url).
// RepoLocalPath is set when the workspace is a local filesystem tree (optionally alongside RepoURL for project-scoped local checkouts).
// ProjectID links the run to tenants.projects when the trigger was project-scoped.
// RepoID passed to InsertIndexRun remains the stable index key for chunks/symbols (separate from these).
type IndexRunStartExtras struct {
	TriggerSource    string
	RepoURL          string
	RepoLocalPath    string // absolute path when run used local workspace; empty → NULL in DB
	ConfigRevisionID string // UUID text for config_revisions.id; empty = NULL in DB
	ProjectID        string // UUID text for projects.id; empty = NULL in DB
}

// InsertIndexRun records the start of an index run. currentIteration is the max evaluation fix-iteration budget for this run (e.g. start_max_iteration for new runs). On conflict (rerun of same run_id), updates started_at, finished_at, scheduled_rerun_at, status='running'; stable and current_iteration are left unchanged to preserve last values.
// extras may be nil; trigger_source defaults to 'unknown'. Empty repo_url / repo_local_path are stored as NULL (optional columns).
func (s *Store) InsertIndexRun(ctx context.Context, runID, repoID, commitSHA string, startedAt int64, currentIteration int, extras *IndexRunStartExtras) error {
	if currentIteration <= 0 {
		currentIteration = 3
	}
	ts := "unknown"
	var repoURL sql.NullString
	var repoLocalPath sql.NullString
	var configRev sql.NullString
	var projectID sql.NullString
	if extras != nil {
		if t := strings.TrimSpace(extras.TriggerSource); t != "" {
			ts = t
		}
		if u := strings.TrimSpace(extras.RepoURL); u != "" {
			repoURL = sql.NullString{String: u, Valid: true}
		}
		if p := strings.TrimSpace(extras.RepoLocalPath); p != "" {
			repoLocalPath = sql.NullString{String: p, Valid: true}
		}
		if id := strings.TrimSpace(extras.ConfigRevisionID); id != "" {
			configRev = sql.NullString{String: id, Valid: true}
		}
		if id := strings.TrimSpace(extras.ProjectID); id != "" {
			projectID = sql.NullString{String: id, Valid: true}
		}
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO index_runs (run_id, repo_id, commit_sha, started_at, last_heartbeat_at, current_iteration, status, stable, trigger_source, repo_url, repo_local_path, config_revision_id, project_id) VALUES ($1, $2, $3, $4, $4, $5, 'running', NULL, $6, $7, $8, $9, $10)
		 ON CONFLICT (run_id) DO UPDATE SET started_at = EXCLUDED.started_at, last_heartbeat_at = EXCLUDED.last_heartbeat_at, finished_at = 0, scheduled_rerun_at = NULL, status = 'running', first_wave_metrics = NULL, trigger_source = EXCLUDED.trigger_source, repo_url = EXCLUDED.repo_url, repo_local_path = EXCLUDED.repo_local_path, config_revision_id = EXCLUDED.config_revision_id, project_id = EXCLUDED.project_id`,
		runID, repoID, commitSHA, startedAt, currentIteration, ts, repoURL, repoLocalPath, configRev, projectID)
	return err
}

// SetIndexRunFirstWaveMetrics writes first-wave quality metrics for the run (JSONB). Nil m clears the column.
func (s *Store) SetIndexRunFirstWaveMetrics(ctx context.Context, runID string, m *FirstWaveRunMetrics) error {
	if m == nil {
		_, err := s.db.Exec(ctx, `UPDATE index_runs SET first_wave_metrics = NULL WHERE run_id = $1`, runID)
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE index_runs SET first_wave_metrics = $1 WHERE run_id = $2`, b, runID)
	return err
}

// GetIndexRunFirstWaveMetrics returns stored metrics or (nil, nil) when the column is NULL or missing row.
func (s *Store) GetIndexRunFirstWaveMetrics(ctx context.Context, runID string) (*FirstWaveRunMetrics, error) {
	var ns sql.NullString
	err := s.db.QueryRow(ctx, `SELECT first_wave_metrics::text FROM index_runs WHERE run_id = $1`, runID).Scan(&ns)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ns.Valid || ns.String == "" || ns.String == "null" {
		return nil, nil
	}
	var out FirstWaveRunMetrics
	if err := json.Unmarshal([]byte(ns.String), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetRunCompleted marks the run as completed and sets the evaluation outcome (stable, iterations). When stable is nil (evaluate skipped), only status is updated; stable and iterations are left unchanged. iterations is the actual fix-loop iterations used (e.g. 4); pass nil when evaluate was skipped.
// Only rows with status = 'running' are updated so duplicate completion calls are idempotent no-ops.
func (s *Store) SetRunCompleted(ctx context.Context, runID string, stable *bool, iterations *int) error {
	if stable == nil && iterations == nil {
		_, err := s.db.Exec(ctx, "UPDATE index_runs SET status = 'completed' WHERE run_id = $1 AND status = 'running'", runID)
		return err
	}
	if stable != nil && iterations != nil {
		_, err := s.db.Exec(ctx, "UPDATE index_runs SET status = 'completed', stable = $1, iterations = $2 WHERE run_id = $3 AND status = 'running'", *stable, *iterations, runID)
		return err
	}
	if stable != nil {
		_, err := s.db.Exec(ctx, "UPDATE index_runs SET status = 'completed', stable = $1 WHERE run_id = $2 AND status = 'running'", *stable, runID)
		return err
	}
	_, err := s.db.Exec(ctx, "UPDATE index_runs SET status = 'completed', iterations = $1 WHERE run_id = $2 AND status = 'running'", *iterations, runID)
	return err
}

// GetRunStatus returns status and stable for the run. status is "running", "completed", or "failed"; stable is nil if not set.
func (s *Store) GetRunStatus(ctx context.Context, runID string) (status string, stable *bool, err error) {
	var st string
	var sval sql.NullBool
	err = s.db.QueryRow(ctx, "SELECT status, stable FROM index_runs WHERE run_id = $1", runID).Scan(&st, &sval)
	if err == pgx.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if sval.Valid {
		stable = &sval.Bool
	}
	return st, stable, nil
}

// GetCurrentIteration returns the current_iteration for the run (max evaluation fix-iteration budget). Returns 0 if the run does not exist.
func (s *Store) GetCurrentIteration(ctx context.Context, runID string) (int, error) {
	var cur int
	err := s.db.QueryRow(ctx, "SELECT current_iteration FROM index_runs WHERE run_id = $1", runID).Scan(&cur)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return cur, err
}

// UpdateCurrentIterationAndScheduledRerun sets current_iteration and scheduled_rerun_at for the run (e.g. after unstable evaluation: increment budget and schedule rerun).
func (s *Store) UpdateCurrentIterationAndScheduledRerun(ctx context.Context, runID string, currentIteration int, scheduledRerunAt *int64) error {
	if currentIteration <= 0 {
		currentIteration = 3
	}
	if scheduledRerunAt == nil {
		_, err := s.db.Exec(ctx, "UPDATE index_runs SET current_iteration = $1, scheduled_rerun_at = NULL WHERE run_id = $2", currentIteration, runID)
		return err
	}
	_, err := s.db.Exec(ctx, "UPDATE index_runs SET current_iteration = $1, scheduled_rerun_at = $2 WHERE run_id = $3", currentIteration, *scheduledRerunAt, runID)
	return err
}

// ScheduledRerun identifies a run that is due for rerun (scheduled_rerun_at <= now).
type ScheduledRerun struct {
	RunID  string
	RepoID string
}

// ListRunsDueForRerun returns runs where scheduled_rerun_at is set and <= nowMs (unix milliseconds). Used by the scheduler to trigger reruns.
func (s *Store) ListRunsDueForRerun(ctx context.Context, nowMs int64) ([]ScheduledRerun, error) {
	rows, err := s.db.Query(ctx, "SELECT run_id, repo_id FROM index_runs WHERE scheduled_rerun_at IS NOT NULL AND scheduled_rerun_at <= $1", nowMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledRerun
	for rows.Next() {
		var r ScheduledRerun
		if err := rows.Scan(&r.RunID, &r.RepoID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateIndexRunFinished sets the finished_at timestamp for a run.
func (s *Store) UpdateIndexRunFinished(ctx context.Context, runID string, finishedAt int64) error {
	_, err := s.db.Exec(ctx, "UPDATE index_runs SET finished_at = $1 WHERE run_id = $2", finishedAt, runID)
	return err
}

// CountIndexRuns returns the number of index runs for the given repo_id (for first-run detection).
func (s *Store) CountIndexRuns(ctx context.Context, repoID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM index_runs WHERE repo_id = $1", repoID).Scan(&n)
	return n, err
}

// CountSymbols returns the number of symbols stored for the repository. Used by the indexer after
// a run finishes to populate IndexPhaseResult.SymbolsTotal so the session_feedback "index_delta"
// payload reports e.g. "now 678 symbols" alongside the per-run delta (A.7).
func (s *Store) CountSymbols(ctx context.Context, repoID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM symbols WHERE repo_id = $1`, repoID).Scan(&n)
	return n, err
}

// CountEdges returns the number of edges stored for the repository. Populates
// IndexPhaseResult.EdgesTotal (A.7).
func (s *Store) CountEdges(ctx context.Context, repoID string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM edges WHERE repo_id = $1`, repoID).Scan(&n)
	return n, err
}

// --- Audit log ---

// InsertAudit records one audit step for a run. payload is stored as JSONB (use map, struct, or nil).
func (s *Store) InsertAudit(ctx context.Context, runID, step, level string, payload interface{}) error {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	_, err := s.db.Exec(ctx,
		"INSERT INTO audit_log (run_id, step, payload, level) VALUES ($1, $2, $3, $4)",
		runID, step, raw, level)
	return err
}

// ListAuditEntries returns audit log entries matching the given filters. Ordered by id ASC (chronological for serial ids).
func (s *Store) ListAuditEntries(ctx context.Context, opts ListAuditOptions) ([]AuditEntry, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10000
	}
	query := "SELECT id, run_id, at, step, level, COALESCE(payload::text, 'null') FROM audit_log WHERE 1=1"
	args := []interface{}{}
	argNum := 0
	if opts.RunID != nil {
		argNum++
		query += fmt.Sprintf(" AND run_id = $%d", argNum)
		args = append(args, *opts.RunID)
	}
	if opts.Since != nil {
		argNum++
		query += fmt.Sprintf(" AND at >= $%d", argNum)
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		argNum++
		query += fmt.Sprintf(" AND at <= $%d", argNum)
		args = append(args, *opts.Until)
	}
	if opts.AfterID != nil {
		argNum++
		query += fmt.Sprintf(" AND id > $%d", argNum)
		args = append(args, *opts.AfterID)
	}
	argNum++
	query += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", argNum)
	args = append(args, limit)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at time.Time
		var payloadText string
		if err := rows.Scan(&e.ID, &e.RunID, &at, &e.Step, &e.Level, &payloadText); err != nil {
			return nil, err
		}
		e.At = at.Format(time.RFC3339Nano)
		if payloadText != "" && payloadText != "null" {
			e.Payload = []byte(payloadText)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAuditRunIDs returns distinct run_id values that have audit entries in the given time range. Ordered by latest first.
func (s *Store) ListAuditRunIDs(ctx context.Context, since, until *time.Time) ([]string, error) {
	query := "SELECT run_id FROM audit_log WHERE 1=1"
	args := []interface{}{}
	argNum := 0
	if since != nil {
		argNum++
		query += fmt.Sprintf(" AND at >= $%d", argNum)
		args = append(args, *since)
	}
	if until != nil {
		argNum++
		query += fmt.Sprintf(" AND at <= $%d", argNum)
		args = append(args, *until)
	}
	query += " GROUP BY run_id ORDER BY MAX(at) DESC"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListSymbolFilesByRepo answers "which files does THIS repository have symbols for" — the
// repo-scoped way for a reindex to list what to delete, where ListFiles used to enumerate the
// whole database.
func (s *Store) ListSymbolFilesByRepo(ctx context.Context, repoID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT file FROM symbols WHERE repo_id = $1 ORDER BY file`,
		strings.TrimSpace(repoID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return out, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
