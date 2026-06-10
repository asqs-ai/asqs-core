package metadata

import (
	"context"
)

const staleNoHeartbeatMsg = "stale: no heartbeat"

// UpdateIndexRunHeartbeat sets last_heartbeat_at (epoch ms) for a running workflow row.
func (s *Store) UpdateIndexRunHeartbeat(ctx context.Context, runID string, atMs int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE index_runs SET last_heartbeat_at = $1 WHERE run_id = $2 AND status = 'running'`,
		atMs, runID)
	return err
}

// ReapStaleRunningRuns sets status=completed, finished_at, and workflow_error for rows that are still
// "running" with no workflow_error and whose effective heartbeat (last_heartbeat_at if non-zero, else started_at)
// is strictly before heartbeatBeforeMs. Returns the number of rows updated.
func (s *Store) ReapStaleRunningRuns(ctx context.Context, finishedAtMs, heartbeatBeforeMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE index_runs
SET status = 'completed',
    finished_at = $1,
    workflow_error = $2
WHERE status = 'running'
  AND COALESCE(workflow_error, '') = ''
  AND COALESCE(NULLIF(last_heartbeat_at, 0), started_at) < $3`,
		finishedAtMs, staleNoHeartbeatMsg, heartbeatBeforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountAuditLines returns the number of audit_log rows for run_id.
func (s *Store) CountAuditLines(ctx context.Context, runID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE run_id = $1`, runID).Scan(&n)
	return n, err
}
