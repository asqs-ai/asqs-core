package metadata

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaFS embed.FS

// Store provides access to metadata tables (symbols, edges, files).
type Store struct {
	db *sql.DB
}

// Open opens a Postgres connection and returns a Store. connString must be a valid libpq connection string.
func Open(connString string) (*Store, error) {
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, fmt.Errorf("metadata open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("metadata ping: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// InitSchema runs the embedded schema.sql to create tables and indexes if they do not exist.
func (s *Store) InitSchema(ctx context.Context) error {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	statements := splitSQL(string(b))
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec schema %q: %w", truncate(stmt, 60), err)
		}
	}
	return nil
}

func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
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
		INSERT INTO symbols (lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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
	err = s.db.QueryRowContext(ctx, query,
		sym.Lang, sym.Kind, sym.FQName, sym.File, sym.StartLine, sym.EndLine, startCol, endCol, sig,
	).Scan(&id)
	return id, err
}

// DeleteSymbolsByFile deletes all symbols (and their edges via cascade) for the given file. Use before reindexing.
func (s *Store) DeleteSymbolsByFile(ctx context.Context, file string) (deleted int64, err error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM symbols WHERE file = $1", file)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

// DeleteFile deletes the file row. Call after DeleteSymbolsByFile when removing a file from the index.
func (s *Store) DeleteFile(ctx context.Context, file string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM files WHERE file = $1", file)
	return err
}

// GetSymbolByID returns the symbol with the given ID, or nil if not found.
func (s *Store) GetSymbolByID(ctx context.Context, id string) (*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE id = $1`
	var sym Symbol
	var sig sql.Null[[]byte]
	var startCol, endCol sql.NullInt32
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&sym.ID, &sym.Lang, &sym.Kind, &sym.FQName, &sym.File,
		&sym.StartLine, &sym.EndLine, &startCol, &endCol, &sig,
	)
	if err == sql.ErrNoRows {
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
func (s *Store) ListSymbolsByFile(ctx context.Context, file string) ([]*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE file = $1 ORDER BY start_line`
	rows, err := s.db.QueryContext(ctx, query, file)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByFQSubstring returns symbols whose fq_name contains needle (case-insensitive), ordered by fq_name, capped.
func (s *Store) ListSymbolsByFQSubstring(ctx context.Context, needle string, limit int) ([]*Symbol, error) {
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
		WHERE strpos(lower(fq_name), lower($1)) > 0
		ORDER BY fq_name
		LIMIT $2`
	rows, err := s.db.QueryContext(ctx, query, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByFQName returns all symbols with the given fully qualified name (may be multiple overloads/locations).
func (s *Store) ListSymbolsByFQName(ctx context.Context, fqName string) ([]*Symbol, error) {
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE fq_name = $1 ORDER BY file, start_line`
	rows, err := s.db.QueryContext(ctx, query, fqName)
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
func (s *Store) ListSymbolsByTypeSimpleName(ctx context.Context, simpleName string, limit int) ([]*Symbol, error) {
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
		WHERE lower(kind) IN ('class','interface','struct','record','enum','type','type_alias','object')
		  AND (fq_name = $1 OR fq_name LIKE '%.' || $1)
		ORDER BY length(fq_name), fq_name
		LIMIT $2`
	rows, err := s.db.QueryContext(ctx, query, simpleName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsByLang returns symbols for the given language, optionally filtered by kind.
func (s *Store) ListSymbolsByLang(ctx context.Context, lang string, kind string) ([]*Symbol, error) {
	if kind != "" {
		query := `
			SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
			FROM symbols WHERE lang = $1 AND kind = $2 ORDER BY file, start_line`
		rows, err := s.db.QueryContext(ctx, query, lang, kind)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanSymbols(rows)
	}
	query := `
		SELECT id, lang, kind, fq_name, file, start_line, end_line, start_column, end_column, signature_json
		FROM symbols WHERE lang = $1 ORDER BY file, start_line`
	rows, err := s.db.QueryContext(ctx, query, lang)
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

func scanSymbols(rows *sql.Rows) ([]*Symbol, error) {
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
func (s *Store) ListSymbolsInNonTestFiles(ctx context.Context, lang, kind string) ([]*Symbol, error) {
	query := `
		SELECT s.id, s.lang, s.kind, s.fq_name, s.file, s.start_line, s.end_line, s.start_column, s.end_column, s.signature_json
		FROM symbols s
		INNER JOIN files f ON s.file = f.file
		WHERE f.is_test = false AND LOWER(s.lang) = LOWER($1) AND s.kind = $2
		  AND LOWER(s.file) NOT LIKE '%.d.ts'
		ORDER BY s.file, s.start_line`
	rows, err := s.db.QueryContext(ctx, query, lang, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListSymbolsInTestFiles returns symbols of the given kind from files where is_test = true (e.g. E2E specs).
func (s *Store) ListSymbolsInTestFiles(ctx context.Context, lang, kind string) ([]*Symbol, error) {
	query := `
		SELECT s.id, s.lang, s.kind, s.fq_name, s.file, s.start_line, s.end_line, s.start_column, s.end_column, s.signature_json
		FROM symbols s
		INNER JOIN files f ON s.file = f.file
		WHERE f.is_test = true AND LOWER(s.lang) = LOWER($1) AND s.kind = $2
		ORDER BY s.file, s.start_line`
	rows, err := s.db.QueryContext(ctx, query, lang, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// --- Edges ---

// InsertEdge inserts an edge. Idempotent if (caller, callee, type) already exists.
func (s *Store) InsertEdge(ctx context.Context, e *Edge) error {
	query := `
		INSERT INTO edges (caller_symbol_id, callee_symbol_id, edge_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (caller_symbol_id, callee_symbol_id, edge_type) DO NOTHING`
	_, err := s.db.ExecContext(ctx, query, e.CallerSymbolID, e.CalleeSymbolID, e.EdgeType)
	return err
}

// GetEdgesFrom returns all edges whose caller is the given symbol ID.
func (s *Store) GetEdgesFrom(ctx context.Context, callerSymbolID string) ([]*Edge, error) {
	query := `
		SELECT caller_symbol_id, callee_symbol_id, edge_type
		FROM edges WHERE caller_symbol_id = $1`
	rows, err := s.db.QueryContext(ctx, query, callerSymbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// GetEdgesTo returns all edges whose callee is the given symbol ID (inbound: who references this symbol).
// Uses idx_edges_callee. Use for “who targets this route/DTO?” style expansion.
func (s *Store) GetEdgesTo(ctx context.Context, calleeSymbolID string) ([]*Edge, error) {
	query := `
		SELECT caller_symbol_id, callee_symbol_id, edge_type
		FROM edges WHERE callee_symbol_id = $1`
	rows, err := s.db.QueryContext(ctx, query, calleeSymbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

func scanEdges(rows *sql.Rows) ([]*Edge, error) {
	var list []*Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.CallerSymbolID, &e.CalleeSymbolID, &e.EdgeType); err != nil {
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
func (s *Store) ListEdgeFiles(ctx context.Context, lang string) ([]*EdgeFile, error) {
	lang = strings.TrimSpace(lang)
	var (
		query string
		rows  *sql.Rows
		err   error
	)
	if lang == "" {
		query = `
		SELECT s1.file AS caller_file, s2.file AS callee_file, e.edge_type
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller_symbol_id
		JOIN symbols s2 ON s2.id = e.callee_symbol_id
		WHERE s1.lang = s2.lang`
		rows, err = s.db.QueryContext(ctx, query)
	} else {
		query = `
		SELECT s1.file AS caller_file, s2.file AS callee_file, e.edge_type
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller_symbol_id
		JOIN symbols s2 ON s2.id = e.callee_symbol_id
		WHERE s1.lang = $1 AND s2.lang = $1`
		rows, err = s.db.QueryContext(ctx, query, lang)
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

// UpsertFile inserts or updates a file row (by path).
func (s *Store) UpsertFile(ctx context.Context, f *File) error {
	query := `
		INSERT INTO files (file, sha, lang, module, is_test)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (file) DO UPDATE SET sha = $2, lang = $3, module = $4, is_test = $5`
	_, err := s.db.ExecContext(ctx, query, f.File, f.SHA, f.Lang, f.Module, f.IsTest)
	return err
}

// GetFile returns the file row for the given path, or nil if not found.
func (s *Store) GetFile(ctx context.Context, file string) (*File, error) {
	query := `SELECT file, sha, lang, module, is_test FROM files WHERE file = $1`
	var f File
	err := s.db.QueryRowContext(ctx, query, file).Scan(&f.File, &f.SHA, &f.Lang, &f.Module, &f.IsTest)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ListFiles returns all files, optionally filtered by lang and is_test.
func (s *Store) ListFiles(ctx context.Context, lang string, isTest *bool) ([]*File, error) {
	if lang != "" && isTest != nil {
		query := `SELECT file, sha, lang, module, is_test FROM files WHERE lang = $1 AND is_test = $2 ORDER BY file`
		rows, err := s.db.QueryContext(ctx, query, lang, *isTest)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	if lang != "" {
		query := `SELECT file, sha, lang, module, is_test FROM files WHERE lang = $1 ORDER BY file`
		rows, err := s.db.QueryContext(ctx, query, lang)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	if isTest != nil {
		query := `SELECT file, sha, lang, module, is_test FROM files WHERE is_test = $1 ORDER BY file`
		rows, err := s.db.QueryContext(ctx, query, *isTest)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanFiles(rows)
	}
	query := `SELECT file, sha, lang, module, is_test FROM files ORDER BY file`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func scanFiles(rows *sql.Rows) ([]*File, error) {
	var list []*File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.File, &f.SHA, &f.Lang, &f.Module, &f.IsTest); err != nil {
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO index_runs (run_id, repo_id, commit_sha, started_at, last_heartbeat_at, current_iteration, status, stable, trigger_source, repo_url, repo_local_path, config_revision_id, project_id) VALUES ($1, $2, $3, $4, $4, $5, 'running', NULL, $6, $7, $8, $9, $10)
		 ON CONFLICT (run_id) DO UPDATE SET started_at = EXCLUDED.started_at, last_heartbeat_at = EXCLUDED.last_heartbeat_at, finished_at = 0, scheduled_rerun_at = NULL, status = 'running', first_wave_metrics = NULL, trigger_source = EXCLUDED.trigger_source, repo_url = EXCLUDED.repo_url, repo_local_path = EXCLUDED.repo_local_path, config_revision_id = EXCLUDED.config_revision_id, project_id = EXCLUDED.project_id`,
		runID, repoID, commitSHA, startedAt, currentIteration, ts, repoURL, repoLocalPath, configRev, projectID)
	return err
}

// SetIndexRunFirstWaveMetrics writes first-wave quality metrics for the run (JSONB). Nil m clears the column.
func (s *Store) SetIndexRunFirstWaveMetrics(ctx context.Context, runID string, m *FirstWaveRunMetrics) error {
	if m == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE index_runs SET first_wave_metrics = NULL WHERE run_id = $1`, runID)
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE index_runs SET first_wave_metrics = $1 WHERE run_id = $2`, b, runID)
	return err
}

// GetIndexRunFirstWaveMetrics returns stored metrics or (nil, nil) when the column is NULL or missing row.
func (s *Store) GetIndexRunFirstWaveMetrics(ctx context.Context, runID string) (*FirstWaveRunMetrics, error) {
	var ns sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT first_wave_metrics::text FROM index_runs WHERE run_id = $1`, runID).Scan(&ns)
	if err == sql.ErrNoRows {
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
		_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET status = 'completed' WHERE run_id = $1 AND status = 'running'", runID)
		return err
	}
	if stable != nil && iterations != nil {
		_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET status = 'completed', stable = $1, iterations = $2 WHERE run_id = $3 AND status = 'running'", *stable, *iterations, runID)
		return err
	}
	if stable != nil {
		_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET status = 'completed', stable = $1 WHERE run_id = $2 AND status = 'running'", *stable, runID)
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET status = 'completed', iterations = $1 WHERE run_id = $2 AND status = 'running'", *iterations, runID)
	return err
}

// GetRunStatus returns status and stable for the run. status is "running", "completed", or "failed"; stable is nil if not set.
func (s *Store) GetRunStatus(ctx context.Context, runID string) (status string, stable *bool, err error) {
	var st string
	var sval sql.NullBool
	err = s.db.QueryRowContext(ctx, "SELECT status, stable FROM index_runs WHERE run_id = $1", runID).Scan(&st, &sval)
	if err == sql.ErrNoRows {
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
	err := s.db.QueryRowContext(ctx, "SELECT current_iteration FROM index_runs WHERE run_id = $1", runID).Scan(&cur)
	if err == sql.ErrNoRows {
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
		_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET current_iteration = $1, scheduled_rerun_at = NULL WHERE run_id = $2", currentIteration, runID)
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET current_iteration = $1, scheduled_rerun_at = $2 WHERE run_id = $3", currentIteration, *scheduledRerunAt, runID)
	return err
}

// ScheduledRerun identifies a run that is due for rerun (scheduled_rerun_at <= now).
type ScheduledRerun struct {
	RunID  string
	RepoID string
}

// ListRunsDueForRerun returns runs where scheduled_rerun_at is set and <= nowMs (unix milliseconds). Used by the scheduler to trigger reruns.
func (s *Store) ListRunsDueForRerun(ctx context.Context, nowMs int64) ([]ScheduledRerun, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT run_id, repo_id FROM index_runs WHERE scheduled_rerun_at IS NOT NULL AND scheduled_rerun_at <= $1", nowMs)
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
	_, err := s.db.ExecContext(ctx, "UPDATE index_runs SET finished_at = $1 WHERE run_id = $2", finishedAt, runID)
	return err
}

// CountIndexRuns returns the number of index runs for the given repo_id (for first-run detection).
func (s *Store) CountIndexRuns(ctx context.Context, repoID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM index_runs WHERE repo_id = $1", repoID).Scan(&n)
	return n, err
}

// CountSymbols returns the total number of symbols currently stored across all repos. The
// symbols table is intentionally repo-agnostic (see schema.sql) so the count is global. Used by
// the indexer after a run finishes to populate IndexPhaseResult.SymbolsTotal so the
// session_feedback "index_delta" payload reports e.g. "now 678 symbols" alongside the per-run
// delta (A.7).
func (s *Store) CountSymbols(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols`).Scan(&n)
	return n, err
}

// CountEdges returns the total number of edges currently stored across all repos. As with
// CountSymbols the edges table is repo-agnostic, so the count is global. Populates
// IndexPhaseResult.EdgesTotal (A.7).
func (s *Store) CountEdges(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&n)
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
	_, err := s.db.ExecContext(ctx,
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

	rows, err := s.db.QueryContext(ctx, query, args...)
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

	rows, err := s.db.QueryContext(ctx, query, args...)
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
