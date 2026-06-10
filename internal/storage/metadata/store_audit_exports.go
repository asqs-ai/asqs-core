package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AuditExportRow tracks an async audit export job.
type AuditExportRow struct {
	ID           string
	RunID        string
	Format       string
	Status       string
	LineCount    int64
	ErrorMessage string
	FileName     string
	CreatedAt    string // RFC3339Nano UTC
	CompletedAt  string // empty if not completed
	CompletedAtT sql.NullTime
}

// InsertAuditExport enqueues a pending export. format is "json" or "ndjson".
func (s *Store) InsertAuditExport(ctx context.Context, runID, format string) (id string, err error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("run_id required")
	}
	format = strings.TrimSpace(strings.ToLower(format))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "ndjson" {
		return "", fmt.Errorf("format must be json or ndjson")
	}
	err = s.db.QueryRowContext(ctx, `
INSERT INTO audit_exports (run_id, format, status) VALUES ($1, $2, 'pending')
RETURNING id::text`, runID, format).Scan(&id)
	return id, err
}

// ClaimPendingAuditExport locks one pending job and sets status to processing. Returns (nil, nil) if none.
func (s *Store) ClaimPendingAuditExport(ctx context.Context) (*AuditExportRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var id, runID, format string
	err = tx.QueryRowContext(ctx, `
UPDATE audit_exports e
SET status = 'processing'
FROM (
  SELECT id FROM audit_exports
  WHERE status = 'pending'
  ORDER BY created_at ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED
) sub
WHERE e.id = sub.id
RETURNING e.id::text, e.run_id, e.format
`).Scan(&id, &runID, &format)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &AuditExportRow{ID: id, RunID: runID, Format: format, Status: "processing"}, nil
}

// CompleteAuditExportReady marks the job done and stores the artifact file name (basename under export dir).
func (s *Store) CompleteAuditExportReady(ctx context.Context, id string, fileName string, lineCount int64) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE audit_exports
SET status = 'ready', file_name = $2, line_count = $3, completed_at = NOW(), error_message = ''
WHERE id = $1::uuid AND status = 'processing'`,
		id, strings.TrimSpace(fileName), lineCount)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("complete audit export: no processing row matched id %s", id)
	}
	return nil
}

// CompleteAuditExportFailed marks the job failed with a message.
func (s *Store) CompleteAuditExportFailed(ctx context.Context, id, errMsg string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE audit_exports
SET status = 'failed', error_message = $2, completed_at = NOW(), file_name = '', line_count = 0
WHERE id = $1::uuid AND status = 'processing'`,
		id, strings.TrimSpace(errMsg))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("fail audit export: no processing row matched id %s", id)
	}
	return nil
}

// GetAuditExport returns the job row or (nil, nil) if missing.
func (s *Store) GetAuditExport(ctx context.Context, id string) (*AuditExportRow, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	var r AuditExportRow
	var createdAt, completedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT id::text, run_id, format, status, line_count, COALESCE(error_message, ''), COALESCE(file_name, ''),
       created_at, completed_at
FROM audit_exports WHERE id = $1::uuid`, id).Scan(
		&r.ID, &r.RunID, &r.Format, &r.Status, &r.LineCount, &r.ErrorMessage, &r.FileName,
		&createdAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if createdAt.Valid {
		r.CreatedAt = createdAt.Time.UTC().Format(time.RFC3339Nano)
	}
	r.CompletedAtT = completedAt
	if completedAt.Valid {
		r.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return &r, nil
}
