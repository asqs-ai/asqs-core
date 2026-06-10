package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RunJobRow is one claimed or pending scheduled run job.
type RunJobRow struct {
	ID               string
	ConfigRevisionID string
	ProjectID        string // projects.id (required on row)
	CronExpression   string // non-empty when recurring
}

// ClaimDueRunJob atomically picks the oldest due pending job and sets status to 'running'.
// Returns (nil, nil) when no job is available. Uses FOR UPDATE SKIP LOCKED for safe concurrency.
func (s *Store) ClaimDueRunJob(ctx context.Context) (*RunJobRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, revID, projID string
	var cronExpr sql.NullString
	err = tx.QueryRowContext(ctx, `
UPDATE run_jobs j
SET status = 'running'
FROM (
  SELECT id FROM run_jobs
  WHERE status = 'pending' AND run_at <= NOW()
  ORDER BY run_at ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED
) sub
WHERE j.id = sub.id
RETURNING j.id::text, j.config_revision_id::text, j.project_id::text, j.cron_expression
`).Scan(&id, &revID, &projID, &cronExpr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row := &RunJobRow{ID: id, ConfigRevisionID: revID, ProjectID: projID}
	if cronExpr.Valid {
		row.CronExpression = cronExpr.String
	}
	return row, nil
}

// ProjectHasActiveRun reports whether the project has an index_runs row with status running.
func (s *Store) ProjectHasActiveRun(ctx context.Context, projectID string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, fmt.Errorf("project_id required")
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM index_runs
  WHERE project_id = $1::uuid AND status = 'running'
)`, projectID).Scan(&exists)
	return exists, err
}

// RescheduleRecurringRunJob resets a claimed recurring job to pending with the next run_at.
func (s *Store) RescheduleRecurringRunJob(ctx context.Context, jobID string, nextRunAt time.Time, createdRunID *string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job id required")
	}
	var link sql.NullString
	if createdRunID != nil && strings.TrimSpace(*createdRunID) != "" {
		runID := strings.TrimSpace(*createdRunID)
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM index_runs WHERE run_id = $1 LIMIT 1`, runID).Scan(&one)
		if err == nil {
			link = sql.NullString{String: runID, Valid: true}
		}
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE run_jobs
SET status = 'pending', run_at = $2, created_run_id = COALESCE($3, created_run_id)
WHERE id = $1::uuid AND status = 'running'`,
		jobID, nextRunAt.UTC(), link)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("reschedule job: no running row matched id %s", jobID)
	}
	return nil
}

// CompleteRunJob marks a job finished. created_run_id is set only when runID exists in index_runs (FK-safe).
func (s *Store) CompleteRunJob(ctx context.Context, jobID, runID string, runErr error) error {
	st := "completed"
	if runErr != nil {
		st = "failed"
	}
	var link sql.NullString
	if strings.TrimSpace(runID) != "" {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM index_runs WHERE run_id = $1 LIMIT 1`, runID).Scan(&one)
		if err == nil {
			link = sql.NullString{String: runID, Valid: true}
		}
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE run_jobs SET status = $1, created_run_id = $2 WHERE id = $3::uuid AND status = 'running'`,
		st, link, jobID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("complete job: no running row matched id %s", jobID)
	}
	return nil
}
