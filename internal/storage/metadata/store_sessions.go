package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// RunSessionRow mirrors one row of the run_sessions table.
type RunSessionRow struct {
	ID               string
	RunID            string
	ProjectID        sql.NullString
	RepoID           string
	CommitSHA        string
	TaskKind         string
	Goal             string
	State            string
	CurrentIteration int
	MaxIteration     int
	ScheduledRerunAt sql.NullInt64
	BootstrapJSON    sql.NullString
	IndexDeltaJSON   sql.NullString
	PlanSummaryJSON  sql.NullString
	SeamNotesJSON    sql.NullString
	OverviewPath     string
	DiscardPathsJSON sql.NullString
	StartedAt        int64
	UpdatedAt        int64
	FinishedAt       int64
}

// GapSessionRow mirrors one row of the gap_sessions table.
type GapSessionRow struct {
	ID               string
	RunSessionID     string
	SymbolFQName     string
	SourceFile       string
	Layer            string
	Kind             string
	RetrievalProfile string
	State            string
	// CurrentStep is the engine `Step` constant for the in-flight or last-completed step
	// (retrieve, generate, per_gap_write, compile, test, lint, fix_compile, …). Empty when
	// the gap has not yet started any tool.
	CurrentStep string
	// LastError is the most recent failure summary (`tool error`, `Summary` field, or a
	// derived "<step>: failed" fallback). Cleared by the runner on the next successful
	// attempt; persists at terminal state for unstable / aborted gaps.
	LastError          string
	IterationsUsed     int
	IterationBudget    int
	ArtifactPathsJSON  sql.NullString
	DiscardedPathsJSON sql.NullString
	Fingerprint        string
	AbstainReason      string
	StartedAt          int64
	UpdatedAt          int64
	FinishedAt         int64
}

// SessionAttemptRow mirrors one row of the session_attempts table.
type SessionAttemptRow struct {
	ID                int64
	RunSessionID      string
	GapSessionID      sql.NullString
	Idx               int
	Tool              string
	Step              string
	InputSummaryJSON  sql.NullString
	OutputSummaryJSON sql.NullString
	DurationMs        int64
	OK                bool
	CreatedAt         int64
}

// SessionFeedbackRow mirrors one row of the session_feedback table.
type SessionFeedbackRow struct {
	ID                int64
	RunSessionID      string
	GapSessionID      sql.NullString
	Kind              string
	Subkind           string
	OwnerArtifactPath string
	File              string
	Symbol            string
	Line              int
	Message           string
	SuggestedAction   string
	RawExcerpt        string
	PayloadJSON       sql.NullString
	CreatedAt         int64
}

// UpsertRunSession writes or updates a run_sessions row (PK = id).
func (s *Store) UpsertRunSession(ctx context.Context, row RunSessionRow) error {
	const q = `
INSERT INTO run_sessions (
    id, run_id, project_id, repo_id, commit_sha, task_kind, goal, state,
    current_iteration, max_iteration, scheduled_rerun_at,
    bootstrap, index_delta, plan_summary, seam_notes, overview_path, discard_paths,
    started_at, updated_at, finished_at
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20
) ON CONFLICT (id) DO UPDATE SET
    state = EXCLUDED.state,
    current_iteration = EXCLUDED.current_iteration,
    max_iteration = EXCLUDED.max_iteration,
    scheduled_rerun_at = EXCLUDED.scheduled_rerun_at,
    bootstrap = EXCLUDED.bootstrap,
    index_delta = EXCLUDED.index_delta,
    plan_summary = EXCLUDED.plan_summary,
    seam_notes = COALESCE(EXCLUDED.seam_notes, '{}'::jsonb),
    overview_path = EXCLUDED.overview_path,
    discard_paths = EXCLUDED.discard_paths,
    updated_at = EXCLUDED.updated_at,
    finished_at = EXCLUDED.finished_at`
	_, err := s.db.ExecContext(ctx, q,
		row.ID, row.RunID, nullOrStr(row.ProjectID), row.RepoID, row.CommitSHA,
		row.TaskKind, row.Goal, row.State,
		row.CurrentIteration, row.MaxIteration, nullOrInt64(row.ScheduledRerunAt),
		nullOrJSON(row.BootstrapJSON), nullOrJSON(row.IndexDeltaJSON), nullOrJSON(row.PlanSummaryJSON),
		jsonOrDefault(row.SeamNotesJSON, "{}"), row.OverviewPath, nullOrJSON(row.DiscardPathsJSON),
		row.StartedAt, row.UpdatedAt, row.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert run_session: %w", err)
	}
	return nil
}

// UpsertGapSession writes or updates a gap_sessions row (PK = id).
func (s *Store) UpsertGapSession(ctx context.Context, row GapSessionRow) error {
	const q = `
INSERT INTO gap_sessions (
    id, run_session_id, symbol_fq_name, source_file, layer, kind, retrieval_profile,
    state, current_step, last_error, iterations_used, iteration_budget, artifact_paths, discarded_paths,
    fingerprint, abstain_reason, started_at, updated_at, finished_at
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19
) ON CONFLICT (id) DO UPDATE SET
    state = EXCLUDED.state,
    current_step = EXCLUDED.current_step,
    last_error = EXCLUDED.last_error,
    iterations_used = EXCLUDED.iterations_used,
    iteration_budget = EXCLUDED.iteration_budget,
    artifact_paths = EXCLUDED.artifact_paths,
    discarded_paths = EXCLUDED.discarded_paths,
    fingerprint = EXCLUDED.fingerprint,
    abstain_reason = EXCLUDED.abstain_reason,
    updated_at = EXCLUDED.updated_at,
    finished_at = EXCLUDED.finished_at`
	_, err := s.db.ExecContext(ctx, q,
		row.ID, row.RunSessionID, row.SymbolFQName, row.SourceFile, row.Layer, row.Kind, row.RetrievalProfile,
		row.State, row.CurrentStep, row.LastError, row.IterationsUsed, row.IterationBudget,
		nullOrJSON(row.ArtifactPathsJSON), nullOrJSON(row.DiscardedPathsJSON),
		row.Fingerprint, row.AbstainReason,
		row.StartedAt, row.UpdatedAt, row.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert gap_session: %w", err)
	}
	return nil
}

// InsertSessionAttempts batch-inserts attempts (idempotency is expected to be handled by the caller:
// attempts are append-only; idx + run/gap pair is unique in practice).
func (s *Store) InsertSessionAttempts(ctx context.Context, rows []SessionAttemptRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		const q = `
INSERT INTO session_attempts (
    run_session_id, gap_session_id, idx, tool, step,
    input_summary, output_summary, duration_ms, ok, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
		_, err := s.db.ExecContext(ctx, q,
			r.RunSessionID, nullOrStr(r.GapSessionID), r.Idx, r.Tool, r.Step,
			nullOrJSON(r.InputSummaryJSON), nullOrJSON(r.OutputSummaryJSON),
			r.DurationMs, r.OK, r.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert session_attempt: %w", err)
		}
	}
	return nil
}

// InsertSessionFeedback batch-inserts feedback rows.
func (s *Store) InsertSessionFeedback(ctx context.Context, rows []SessionFeedbackRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		const q = `
INSERT INTO session_feedback (
    run_session_id, gap_session_id, kind, subkind, owner_artifact_path, file, symbol, line,
    message, suggested_action, raw_excerpt, payload, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`
		_, err := s.db.ExecContext(ctx, q,
			r.RunSessionID, nullOrStr(r.GapSessionID), r.Kind, r.Subkind,
			r.OwnerArtifactPath, r.File, r.Symbol, r.Line,
			r.Message, r.SuggestedAction, r.RawExcerpt,
			nullOrJSON(r.PayloadJSON), r.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert session_feedback: %w", err)
		}
	}
	return nil
}

// ListRunSessionsOptions are the query filters for ListRunSessions.
type ListRunSessionsOptions struct {
	RunID     string
	ProjectID string
	State     string
	Limit     int
	Offset    int
}

// ListRunSessions returns run_sessions rows matching opts (limit capped at 200) and the total count.
func (s *Store) ListRunSessions(ctx context.Context, opts ListRunSessionsOptions) ([]RunSessionRow, int64, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []interface{}{}
	n := 0
	if strings.TrimSpace(opts.RunID) != "" {
		n++
		where = append(where, fmt.Sprintf("run_id = $%d", n))
		args = append(args, opts.RunID)
	}
	if strings.TrimSpace(opts.ProjectID) != "" {
		// run_sessions.project_id is only set when WorkflowInput had ProjectID; many runs (CLI,
		// webhooks) attach the project on index_runs only. Match either column so the UI list is
		// not empty when filtering by ?projectId=.
		n++
		pid := strings.TrimSpace(opts.ProjectID)
		where = append(where, fmt.Sprintf(
			"(run_sessions.project_id::text = $%[1]d OR run_sessions.run_id IN (SELECT run_id FROM index_runs WHERE project_id::text = $%[1]d))",
			n,
		))
		args = append(args, pid)
	}
	if strings.TrimSpace(opts.State) != "" {
		n++
		where = append(where, fmt.Sprintf("state = $%d", n))
		args = append(args, opts.State)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM run_sessions WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count run_sessions: %w", err)
	}
	q := fmt.Sprintf(`
SELECT id, run_id, project_id, repo_id, commit_sha, task_kind, goal, state,
       current_iteration, max_iteration, scheduled_rerun_at,
       bootstrap::text, index_delta::text, plan_summary::text, seam_notes::text, overview_path,
       discard_paths::text, started_at, updated_at, finished_at
FROM run_sessions WHERE %s ORDER BY started_at DESC LIMIT $%d OFFSET $%d`, whereSQL, n+1, n+2)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query run_sessions: %w", err)
	}
	defer rows.Close()
	out := []RunSessionRow{}
	for rows.Next() {
		var r RunSessionRow
		if err := rows.Scan(
			&r.ID, &r.RunID, &r.ProjectID, &r.RepoID, &r.CommitSHA, &r.TaskKind, &r.Goal, &r.State,
			&r.CurrentIteration, &r.MaxIteration, &r.ScheduledRerunAt,
			&r.BootstrapJSON, &r.IndexDeltaJSON, &r.PlanSummaryJSON, &r.SeamNotesJSON, &r.OverviewPath,
			&r.DiscardPathsJSON, &r.StartedAt, &r.UpdatedAt, &r.FinishedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan run_sessions: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetRunSession returns one run_sessions row by id (nil, sql.ErrNoRows when missing).
func (s *Store) GetRunSession(ctx context.Context, id string) (*RunSessionRow, error) {
	const q = `
SELECT id, run_id, project_id, repo_id, commit_sha, task_kind, goal, state,
       current_iteration, max_iteration, scheduled_rerun_at,
       bootstrap::text, index_delta::text, plan_summary::text, seam_notes::text, overview_path,
       discard_paths::text, started_at, updated_at, finished_at
FROM run_sessions WHERE id = $1`
	var r RunSessionRow
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&r.ID, &r.RunID, &r.ProjectID, &r.RepoID, &r.CommitSHA, &r.TaskKind, &r.Goal, &r.State,
		&r.CurrentIteration, &r.MaxIteration, &r.ScheduledRerunAt,
		&r.BootstrapJSON, &r.IndexDeltaJSON, &r.PlanSummaryJSON, &r.SeamNotesJSON, &r.OverviewPath,
		&r.DiscardPathsJSON, &r.StartedAt, &r.UpdatedAt, &r.FinishedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListGapSessions returns all gap_sessions for a run_session, ordered by started_at.
func (s *Store) ListGapSessions(ctx context.Context, runSessionID string) ([]GapSessionRow, error) {
	const q = `
SELECT id, run_session_id, symbol_fq_name, source_file, layer, kind, retrieval_profile,
       state, current_step, last_error, iterations_used, iteration_budget,
       artifact_paths::text, discarded_paths::text,
       fingerprint, abstain_reason, started_at, updated_at, finished_at
FROM gap_sessions WHERE run_session_id = $1 ORDER BY started_at ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, q, runSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GapSessionRow{}
	for rows.Next() {
		var r GapSessionRow
		if err := rows.Scan(
			&r.ID, &r.RunSessionID, &r.SymbolFQName, &r.SourceFile, &r.Layer, &r.Kind, &r.RetrievalProfile,
			&r.State, &r.CurrentStep, &r.LastError, &r.IterationsUsed, &r.IterationBudget,
			&r.ArtifactPathsJSON, &r.DiscardedPathsJSON,
			&r.Fingerprint, &r.AbstainReason, &r.StartedAt, &r.UpdatedAt, &r.FinishedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetGapSession returns one gap_sessions row by id.
func (s *Store) GetGapSession(ctx context.Context, id string) (*GapSessionRow, error) {
	const q = `
SELECT id, run_session_id, symbol_fq_name, source_file, layer, kind, retrieval_profile,
       state, current_step, last_error, iterations_used, iteration_budget,
       artifact_paths::text, discarded_paths::text,
       fingerprint, abstain_reason, started_at, updated_at, finished_at
FROM gap_sessions WHERE id = $1`
	var r GapSessionRow
	if err := s.db.QueryRowContext(ctx, q, id).Scan(
		&r.ID, &r.RunSessionID, &r.SymbolFQName, &r.SourceFile, &r.Layer, &r.Kind, &r.RetrievalProfile,
		&r.State, &r.CurrentStep, &r.LastError, &r.IterationsUsed, &r.IterationBudget,
		&r.ArtifactPathsJSON, &r.DiscardedPathsJSON,
		&r.Fingerprint, &r.AbstainReason, &r.StartedAt, &r.UpdatedAt, &r.FinishedAt,
	); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListSessionAttempts returns the ordered attempt log for a run or gap session.
// When gapSessionID is non-empty, results are filtered to that gap; otherwise all run-scope
// attempts are returned (gap_session_id IS NULL).
func (s *Store) ListSessionAttempts(ctx context.Context, runSessionID, gapSessionID string) ([]SessionAttemptRow, error) {
	var q string
	var args []interface{}
	if strings.TrimSpace(gapSessionID) == "" {
		q = `
SELECT id, run_session_id, gap_session_id, idx, tool, step,
       input_summary::text, output_summary::text, duration_ms, ok, created_at
FROM session_attempts WHERE run_session_id = $1 AND gap_session_id IS NULL
ORDER BY created_at ASC, id ASC`
		args = []interface{}{runSessionID}
	} else {
		q = `
SELECT id, run_session_id, gap_session_id, idx, tool, step,
       input_summary::text, output_summary::text, duration_ms, ok, created_at
FROM session_attempts WHERE run_session_id = $1 AND gap_session_id = $2
ORDER BY created_at ASC, id ASC`
		args = []interface{}{runSessionID, gapSessionID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionAttemptRow{}
	for rows.Next() {
		var r SessionAttemptRow
		if err := rows.Scan(
			&r.ID, &r.RunSessionID, &r.GapSessionID, &r.Idx, &r.Tool, &r.Step,
			&r.InputSummaryJSON, &r.OutputSummaryJSON, &r.DurationMs, &r.OK, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionFeedback returns the feedback log for a run or gap session with identical filter
// semantics to ListSessionAttempts.
func (s *Store) ListSessionFeedback(ctx context.Context, runSessionID, gapSessionID string) ([]SessionFeedbackRow, error) {
	var q string
	var args []interface{}
	if strings.TrimSpace(gapSessionID) == "" {
		q = `
SELECT id, run_session_id, gap_session_id, kind, subkind, owner_artifact_path, file, symbol, line,
       message, suggested_action, raw_excerpt, payload::text, created_at
FROM session_feedback WHERE run_session_id = $1 AND gap_session_id IS NULL
ORDER BY created_at ASC, id ASC`
		args = []interface{}{runSessionID}
	} else {
		q = `
SELECT id, run_session_id, gap_session_id, kind, subkind, owner_artifact_path, file, symbol, line,
       message, suggested_action, raw_excerpt, payload::text, created_at
FROM session_feedback WHERE run_session_id = $1 AND gap_session_id = $2
ORDER BY created_at ASC, id ASC`
		args = []interface{}{runSessionID, gapSessionID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionFeedbackRow{}
	for rows.Next() {
		var r SessionFeedbackRow
		if err := rows.Scan(
			&r.ID, &r.RunSessionID, &r.GapSessionID, &r.Kind, &r.Subkind, &r.OwnerArtifactPath,
			&r.File, &r.Symbol, &r.Line, &r.Message, &r.SuggestedAction, &r.RawExcerpt,
			&r.PayloadJSON, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullOrStr(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}

func nullOrInt64(n sql.NullInt64) interface{} {
	if n.Valid {
		return n.Int64
	}
	return nil
}

// nullOrJSON returns nil for empty/invalid so the JSONB column gets NULL instead of 'null'::jsonb.
// When the wrapped string is non-empty it is returned as-is; the pgx driver sends it to the server
// which parses it as JSONB.
func nullOrJSON(ns sql.NullString) interface{} {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return nil
	}
	return ns.String
}

func jsonOrDefault(ns sql.NullString, fallback string) interface{} {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return fallback
	}
	return ns.String
}

// MarshalJSON is a helper for adapters that need to store a map as JSON into a NullString column.
func MarshalSessionPayload(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{Valid: true, String: string(b)}, nil
}

// GetLatestGapOutcomesForRepo returns the gap_sessions rows from the single most-recent terminal
// run_session for the given repoID (optionally scoped to projectID when non-empty). Returns an
// empty slice and a nil error when no prior run exists (cold start).
//
// Cross-run consumers translate these rows into session.GapOutcome and attach them to the next
// RunSession.PriorOutcomes so phase-1 policies can deprioritize chronic-unstable gaps and feed
// per-gap failure excerpts into applyFailureLocalizedRetrieval.
func (s *Store) GetLatestGapOutcomesForRepo(ctx context.Context, repoID, projectID string) ([]GapSessionRow, error) {
	if strings.TrimSpace(repoID) == "" {
		return nil, nil
	}
	// Find the most-recent terminal RunSession.
	args := []interface{}{repoID}
	where := "rs.repo_id = $1"
	if strings.TrimSpace(projectID) != "" {
		args = append(args, projectID)
		where += " AND rs.project_id::text = $2"
	}
	q := `SELECT rs.id FROM run_sessions rs WHERE ` + where + ` ORDER BY rs.started_at DESC LIMIT 1`
	var runSessionID string
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&runSessionID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("find latest run_session: %w", err)
	}
	return s.ListGapSessions(ctx, runSessionID)
}
