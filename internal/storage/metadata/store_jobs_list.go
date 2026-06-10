package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RunJobRecord is a full run_jobs row for list/detail APIs (repo_url resolved from projects.repo_url).
type RunJobRecord struct {
	ID               string
	ConfigRevisionID string
	RepoURL          string // from projects.repo_url at read time
	ProjectID        string
	RunAt            time.Time
	Status           string
	CreatedRunID     string
	CreatedAt        time.Time
	CronExpression   string
}

// ListRunJobsOptions filters scheduled jobs (order: run_at DESC).
type ListRunJobsOptions struct {
	Status string // pending | running | completed | failed | cancelled; empty = all
	Since  *time.Time
	Until  *time.Time // inclusive upper bound on run_at
	Limit  int
	Offset int
}

// ListRunJobs returns scheduled jobs and total matching count (before limit/offset).
func (s *Store) ListRunJobs(ctx context.Context, opts ListRunJobsOptions) ([]RunJobRecord, int64, error) {
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
	if st := strings.TrimSpace(opts.Status); st != "" {
		n++
		where = append(where, fmt.Sprintf("j.status = $%d", n))
		args = append(args, st)
	}
	if opts.Since != nil {
		n++
		where = append(where, fmt.Sprintf("j.run_at >= $%d", n))
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		n++
		where = append(where, fmt.Sprintf("j.run_at <= $%d", n))
		args = append(args, *opts.Until)
	}
	whereSQL := strings.Join(where, " AND ")

	countQuery := "SELECT COUNT(*) FROM run_jobs j WHERE " + whereSQL
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
SELECT j.id::text, j.config_revision_id::text, COALESCE(p.repo_url, ''), j.project_id::text, j.run_at, j.status, j.created_run_id, j.created_at, j.cron_expression
FROM run_jobs j
LEFT JOIN projects p ON p.id = j.project_id
WHERE %s
ORDER BY j.run_at DESC
LIMIT $%d OFFSET $%d`, whereSQL, limitArg, offsetArg)

	rows, err := s.db.QueryContext(ctx, query, argsWithPage...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []RunJobRecord
	for rows.Next() {
		var r RunJobRecord
		var createdRun, cronExpr sql.NullString
		if err := rows.Scan(&r.ID, &r.ConfigRevisionID, &r.RepoURL, &r.ProjectID, &r.RunAt, &r.Status, &createdRun, &r.CreatedAt, &cronExpr); err != nil {
			return nil, 0, err
		}
		if createdRun.Valid {
			r.CreatedRunID = createdRun.String
		}
		if cronExpr.Valid {
			r.CronExpression = cronExpr.String
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetRunJobByID returns one job or (nil, nil) if not found.
func (s *Store) GetRunJobByID(ctx context.Context, jobID string) (*RunJobRecord, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, nil
	}
	var r RunJobRecord
	var createdRun, cronExpr sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT j.id::text, j.config_revision_id::text, COALESCE(p.repo_url, ''), j.project_id::text, j.run_at, j.status, j.created_run_id, j.created_at, j.cron_expression
FROM run_jobs j
LEFT JOIN projects p ON p.id = j.project_id
WHERE j.id = $1::uuid`, jobID).Scan(
		&r.ID, &r.ConfigRevisionID, &r.RepoURL, &r.ProjectID, &r.RunAt, &r.Status, &createdRun, &r.CreatedAt, &cronExpr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if createdRun.Valid {
		r.CreatedRunID = createdRun.String
	}
	if cronExpr.Valid {
		r.CronExpression = cronExpr.String
	}
	return &r, nil
}

// CancelRunJobResult is the outcome of CancelRunJob.
type CancelRunJobResult int

const (
	// CancelRunJobNotFound: no row with that id.
	CancelRunJobNotFound CancelRunJobResult = iota
	// CancelRunJobUpdated: pending row was set to cancelled.
	CancelRunJobUpdated
	// CancelRunJobAlreadyCancelled: row exists and is already cancelled (idempotent delete).
	CancelRunJobAlreadyCancelled
	// CancelRunJobNotPending: row exists but is running, completed, or failed (cannot cancel).
	CancelRunJobNotPending
)

// CancelRunJob sets status to cancelled when the job is still pending.
// Returns CancelRunJobAlreadyCancelled when the job was already cancelled (safe to treat as success).
// Caller should validate UUID format before calling to avoid database invalid-uuid errors.
func (s *Store) CancelRunJob(ctx context.Context, jobID string) (CancelRunJobResult, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return CancelRunJobNotFound, fmt.Errorf("job id required")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE run_jobs SET status = 'cancelled' WHERE id = $1::uuid AND status = 'pending'`, jobID)
	if err != nil {
		return CancelRunJobNotFound, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return CancelRunJobNotFound, err
	}
	if n > 0 {
		return CancelRunJobUpdated, nil
	}
	var st string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM run_jobs WHERE id = $1::uuid`, jobID).Scan(&st)
	if err == sql.ErrNoRows {
		return CancelRunJobNotFound, nil
	}
	if err != nil {
		return CancelRunJobNotFound, err
	}
	if st == "cancelled" {
		return CancelRunJobAlreadyCancelled, nil
	}
	return CancelRunJobNotPending, nil
}
