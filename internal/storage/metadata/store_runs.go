package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RecordWorkflowRunFailureParams inserts a failed index run row or transitions an existing **running** row to **failed**.
// Used when the workflow exits with an error before or after the normal indexer insert (bootstrap, plan, generate, evaluate, API invoker).
type RecordWorkflowRunFailureParams struct {
	RunID            string
	RepoID           string
	CommitSHA        string
	StartedAtMs      int64 // 0 means use FinishedAtMs
	FinishedAtMs     int64 // 0 means use current time (Unix ms)
	CurrentIteration int   // 0 means 3
	Extras           *IndexRunStartExtras
	ErrMsg           string
}

// ListIndexRuns returns runs with total count for pagination.
// Default order is started_at DESC; when opts.ScheduledRerunOnly, order is scheduled_rerun_at ASC.
func (s *Store) ListIndexRuns(ctx context.Context, opts ListRunsOptions) ([]IndexRunRow, int64, error) {
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
	if strings.TrimSpace(opts.RepoID) != "" {
		n++
		where = append(where, fmt.Sprintf("r.repo_id = $%d", n))
		args = append(args, opts.RepoID)
	}
	if strings.TrimSpace(opts.Status) != "" {
		n++
		where = append(where, fmt.Sprintf("r.status = $%d", n))
		args = append(args, opts.Status)
	}
	if opts.SinceMs != nil {
		n++
		where = append(where, fmt.Sprintf("r.started_at >= $%d", n))
		args = append(args, *opts.SinceMs)
	}
	if opts.UntilMs != nil {
		n++
		where = append(where, fmt.Sprintf("r.started_at <= $%d", n))
		args = append(args, *opts.UntilMs)
	}
	if strings.TrimSpace(opts.ProjectID) != "" {
		n++
		where = append(where, fmt.Sprintf("r.project_id = $%d::uuid", n))
		args = append(args, strings.TrimSpace(opts.ProjectID))
	}
	if strings.TrimSpace(opts.TenantID) != "" {
		n++
		where = append(where, fmt.Sprintf(`r.project_id IS NOT NULL AND r.project_id IN (SELECT id FROM projects WHERE tenant_id = $%d::uuid)`, n))
		args = append(args, strings.TrimSpace(opts.TenantID))
	}
	if opts.ScheduledRerunOnly {
		where = append(where, "r.scheduled_rerun_at IS NOT NULL")
	}
	whereSQL := strings.Join(where, " AND ")

	orderBy := "r.started_at DESC"
	if opts.ScheduledRerunOnly {
		orderBy = "r.scheduled_rerun_at ASC"
	}

	countQuery := "SELECT COUNT(*) FROM index_runs r WHERE " + whereSQL
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	n++
	limitArg := n
	n++
	offsetArg := n
	argsWithPage := append(append([]interface{}{}, args...), limit, offset)

	query := fmt.Sprintf(`
SELECT
  r.run_id, r.repo_id, r.commit_sha, r.started_at, COALESCE(r.last_heartbeat_at, 0), r.finished_at, r.current_iteration,
  r.iterations, r.scheduled_rerun_at, r.status, r.stable,
  COALESCE(r.workflow_error, ''), COALESCE(r.trigger_source, 'unknown'), COALESCE(r.repo_url, ''),
  COALESCE(r.repo_local_path, ''),
  r.config_revision_id, r.project_id,
  EXISTS(SELECT 1 FROM audit_log a WHERE a.run_id = r.run_id LIMIT 1) AS has_audit,
  (r.first_wave_metrics IS NOT NULL) AS has_metrics
FROM index_runs r
WHERE %s
ORDER BY %s
LIMIT $%d OFFSET $%d`, whereSQL, orderBy, limitArg, offsetArg)

	rows, err := s.db.QueryContext(ctx, query, argsWithPage...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []IndexRunRow
	for rows.Next() {
		var row IndexRunRow
		err := rows.Scan(
			&row.RunID, &row.RepoID, &row.CommitSHA, &row.StartedAt, &row.LastHeartbeatAt, &row.FinishedAt, &row.CurrentIteration,
			&row.Iterations, &row.ScheduledRerunAt, &row.Status, &row.Stable,
			&row.WorkflowError, &row.TriggerSource, &row.RepoURL, &row.RepoLocalPath, &row.ConfigRevisionID, &row.ProjectID,
			&row.HasAudit, &row.HasMetrics,
		)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// GetIndexRun returns one run row with has_audit, has_metrics, and raw first_wave_metrics JSON text when present.
func (s *Store) GetIndexRun(ctx context.Context, runID string) (*IndexRunRow, error) {
	query := `
SELECT
  r.run_id, r.repo_id, r.commit_sha, r.started_at, COALESCE(r.last_heartbeat_at, 0), r.finished_at, r.current_iteration,
  r.iterations, r.scheduled_rerun_at, r.status, r.stable,
  COALESCE(r.workflow_error, ''), COALESCE(r.trigger_source, 'unknown'), COALESCE(r.repo_url, ''),
  COALESCE(r.repo_local_path, ''),
  r.config_revision_id, r.project_id,
  EXISTS(SELECT 1 FROM audit_log a WHERE a.run_id = r.run_id LIMIT 1) AS has_audit,
  (r.first_wave_metrics IS NOT NULL) AS has_metrics,
  r.first_wave_metrics::text
FROM index_runs r
WHERE r.run_id = $1`
	var row IndexRunRow
	var metrics sql.NullString
	err := s.db.QueryRowContext(ctx, query, runID).Scan(
		&row.RunID, &row.RepoID, &row.CommitSHA, &row.StartedAt, &row.LastHeartbeatAt, &row.FinishedAt, &row.CurrentIteration,
		&row.Iterations, &row.ScheduledRerunAt, &row.Status, &row.Stable,
		&row.WorkflowError, &row.TriggerSource, &row.RepoURL, &row.RepoLocalPath, &row.ConfigRevisionID, &row.ProjectID,
		&row.HasAudit, &row.HasMetrics, &metrics,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.FirstWaveMetricsRaw = metrics
	return &row, nil
}

// SetIndexRunWorkflowError sets workflow_error and marks the run **failed** (status, finished_at).
// Only updates when status is still 'running' so late errors do not overwrite a completed or reaped row.
func (s *Store) SetIndexRunWorkflowError(ctx context.Context, runID, msg string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	if strings.TrimSpace(msg) == "" {
		msg = "workflow failed"
	}
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`UPDATE index_runs SET workflow_error = $1, status = 'failed', finished_at = $2 WHERE run_id = $3 AND status = 'running'`,
		msg, now, runID)
	return err
}

// RecordWorkflowRunFailure inserts a new **failed** index run or updates an existing **running** row to **failed**.
// Rows already **completed** or **failed** are left unchanged (idempotent second call on a failed row is a no-op).
func (s *Store) RecordWorkflowRunFailure(ctx context.Context, p *RecordWorkflowRunFailureParams) error {
	if p == nil {
		return fmt.Errorf("params required")
	}
	runID := strings.TrimSpace(p.RunID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	finished := p.FinishedAtMs
	if finished <= 0 {
		finished = time.Now().UnixMilli()
	}
	started := p.StartedAtMs
	if started <= 0 {
		started = finished
	}
	ci := p.CurrentIteration
	if ci <= 0 {
		ci = 3
	}
	msg := strings.TrimSpace(p.ErrMsg)
	if msg == "" {
		msg = "workflow failed"
	}
	repoID := strings.TrimSpace(p.RepoID)
	if repoID == "" {
		repoID = "unknown"
	}
	commitSHA := strings.TrimSpace(p.CommitSHA)

	ts := "unknown"
	var repoURL sql.NullString
	var repoLocalPath sql.NullString
	var configRev sql.NullString
	var projectID sql.NullString
	if p.Extras != nil {
		if t := strings.TrimSpace(p.Extras.TriggerSource); t != "" {
			ts = t
		}
		if u := strings.TrimSpace(p.Extras.RepoURL); u != "" {
			repoURL = sql.NullString{String: u, Valid: true}
		}
		if lp := strings.TrimSpace(p.Extras.RepoLocalPath); lp != "" {
			repoLocalPath = sql.NullString{String: lp, Valid: true}
		}
		if id := strings.TrimSpace(p.Extras.ConfigRevisionID); id != "" {
			configRev = sql.NullString{String: id, Valid: true}
		}
		if id := strings.TrimSpace(p.Extras.ProjectID); id != "" {
			projectID = sql.NullString{String: id, Valid: true}
		}
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO index_runs (
  run_id, repo_id, commit_sha, started_at, last_heartbeat_at, finished_at,
  current_iteration, status, stable,
  trigger_source, repo_url, repo_local_path, config_revision_id, project_id,
  workflow_error
) VALUES (
  $1, $2, $3, $4, $4, $5,
  $6, 'failed', NULL,
  $7, $8, $9, $10, $11,
  $12
)
ON CONFLICT (run_id) DO UPDATE SET
  workflow_error = EXCLUDED.workflow_error,
  status = 'failed',
  finished_at = EXCLUDED.finished_at,
  last_heartbeat_at = EXCLUDED.last_heartbeat_at
WHERE index_runs.status = 'running'`,
		runID, repoID, commitSHA, started, finished, ci,
		ts, repoURL, repoLocalPath, configRev, projectID,
		msg,
	)
	return err
}

// IndexRunExists returns true when a row exists in index_runs for run_id. Used before writing
// run_sessions (FK to index_runs.run_id) to surface a clear error when the row is missing.
func (s *Store) IndexRunExists(ctx context.Context, runID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM index_runs WHERE run_id = $1 LIMIT 1`, runID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
