package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CreateConfigWithInitialRevision creates configs + version 1 revision in one transaction.
// yamlBody may be empty (revision still created with yaml_body ” — caller should validate non-empty for real configs).
func (s *Store) CreateConfigWithInitialRevision(ctx context.Context, name, description, yamlBody, createdBy string) (configID, revisionID string, version int, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", 0, fmt.Errorf("config name required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var cid string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO configs (name, description, updated_at) VALUES ($1, $2, NOW()) RETURNING id`,
		name, strings.TrimSpace(description),
	).Scan(&cid)
	if err != nil {
		return "", "", 0, err
	}

	var rid string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO config_revisions (config_id, version, yaml_body, created_by) VALUES ($1, 1, $2, $3) RETURNING id`,
		cid, yamlBody, strings.TrimSpace(createdBy),
	).Scan(&rid)
	if err != nil {
		return "", "", 0, err
	}

	if err := tx.Commit(); err != nil {
		return "", "", 0, err
	}
	return cid, rid, 1, nil
}

// AppendConfigRevision appends the next version for an existing config.
func (s *Store) AppendConfigRevision(ctx context.Context, configID, yamlBody, createdBy string) (revisionID string, version int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM config_revisions WHERE config_id = $1`,
		configID,
	).Scan(&next)
	if err != nil {
		return "", 0, err
	}
	if next < 1 {
		next = 1
	}

	var rid string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO config_revisions (config_id, version, yaml_body, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
		configID, next, yamlBody, strings.TrimSpace(createdBy),
	).Scan(&rid)
	if err != nil {
		return "", 0, err
	}

	_, err = tx.ExecContext(ctx, `UPDATE configs SET updated_at = NOW() WHERE id = $1`, configID)
	if err != nil {
		return "", 0, err
	}

	if err := tx.Commit(); err != nil {
		return "", 0, err
	}
	return rid, next, nil
}

// ListConfigs returns all named configs with latest revision number.
func (s *Store) ListConfigs(ctx context.Context) ([]ConfigSummary, error) {
	query := `
SELECT c.id, c.name, c.description, COALESCE(MAX(r.version), 0), c.updated_at
FROM configs c
LEFT JOIN config_revisions r ON r.config_id = c.id
GROUP BY c.id, c.name, c.description, c.updated_at
ORDER BY c.name ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigSummary
	for rows.Next() {
		var srow ConfigSummary
		var updated time.Time
		if err := rows.Scan(&srow.ID, &srow.Name, &srow.Description, &srow.LatestVersion, &updated); err != nil {
			return nil, err
		}
		srow.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		out = append(out, srow)
	}
	return out, rows.Err()
}

// GetConfigRevisionByVersion returns one revision by numeric version.
func (s *Store) GetConfigRevisionByVersion(ctx context.Context, configID string, version int) (*Revision, error) {
	query := `
SELECT r.id, r.version, r.created_at, r.created_by, r.yaml_body
FROM config_revisions r
WHERE r.config_id = $1::uuid AND r.version = $2`
	var rev Revision
	var at time.Time
	err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(configID), version).Scan(
		&rev.ID, &rev.Version, &at, &rev.CreatedBy, &rev.YAMLBody,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rev.CreatedAt = at.UTC().Format(time.RFC3339Nano)
	return &rev, nil
}

// GetConfigRevisionByID returns revision by revision UUID.
func (s *Store) GetConfigRevisionByID(ctx context.Context, revisionID string) (*Revision, string, error) {
	query := `
SELECT r.id, r.version, r.created_at, r.created_by, r.yaml_body, r.config_id::text
FROM config_revisions r
WHERE r.id = $1::uuid`
	var rev Revision
	var at time.Time
	var cfgID string
	err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(revisionID)).Scan(
		&rev.ID, &rev.Version, &at, &rev.CreatedBy, &rev.YAMLBody, &cfgID,
	)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	rev.CreatedAt = at.UTC().Format(time.RFC3339Nano)
	return &rev, cfgID, nil
}

// ListConfigRevisions lists revision metadata for a config (no YAML).
func (s *Store) ListConfigRevisions(ctx context.Context, configID string) ([]RevisionMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, version, created_at, created_by FROM config_revisions WHERE config_id = $1::uuid ORDER BY version DESC`,
		strings.TrimSpace(configID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RevisionMeta
	for rows.Next() {
		var m RevisionMeta
		var at time.Time
		if err := rows.Scan(&m.ID, &m.Version, &at, &m.CreatedBy); err != nil {
			return nil, err
		}
		m.CreatedAt = at.UTC().Format(time.RFC3339Nano)
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetLatestConfigRevision returns the highest-version revision for a config.
func (s *Store) GetLatestConfigRevision(ctx context.Context, configID string) (*Revision, error) {
	query := `
SELECT r.id, r.version, r.created_at, r.created_by, r.yaml_body
FROM config_revisions r
WHERE r.config_id = $1::uuid
ORDER BY r.version DESC
LIMIT 1`
	var rev Revision
	var at time.Time
	err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(configID)).Scan(
		&rev.ID, &rev.Version, &at, &rev.CreatedBy, &rev.YAMLBody,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rev.CreatedAt = at.UTC().Format(time.RFC3339Nano)
	return &rev, nil
}

// GetConfigByID returns config row if exists (id, name, description, updated_at).
func (s *Store) GetConfigByID(ctx context.Context, configID string) (name, description string, updatedAt string, err error) {
	var t time.Time
	err = s.db.QueryRowContext(ctx,
		`SELECT name, description, updated_at FROM configs WHERE id = $1::uuid`,
		strings.TrimSpace(configID),
	).Scan(&name, &description, &t)
	if err == sql.ErrNoRows {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	return name, description, t.UTC().Format(time.RFC3339Nano), nil
}

// UpdateConfigMetadata sets catalog name and description for an existing config.
func (s *Store) UpdateConfigMetadata(ctx context.Context, configID, name, description string) (ok bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("name required")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE configs SET name = $1, description = $2, updated_at = NOW() WHERE id = $3`,
		name, strings.TrimSpace(description), configID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteConfig deletes config and revisions (CASCADE).
func (s *Store) DeleteConfig(ctx context.Context, configID string) (deleted bool, err error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM configs WHERE id = $1`, configID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// InsertRunJob schedules a future run (worker consumes). projectID is required; stores config_revision_id at queue time for audit/list. Apiserver ExecuteScheduledJob re-resolves revision at run time via ResolveProjectForRun; clone URL comes from projects.repo_url.
func (s *Store) InsertRunJob(ctx context.Context, configRevisionID string, runAt time.Time, projectID string) (jobID string, err error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project_id required")
	}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO run_jobs (config_revision_id, project_id, run_at, status) VALUES ($1, $2::uuid, $3, 'pending') RETURNING id::text`,
		configRevisionID, projectID, runAt,
	).Scan(&jobID)
	return jobID, err
}

// InsertRecurringRunJob schedules a recurring run keyed by cron_expression; runAt is the first fire time.
func (s *Store) InsertRecurringRunJob(ctx context.Context, configRevisionID, projectID, cronExpression string, runAt time.Time) (jobID string, err error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project_id required")
	}
	cronExpression = strings.TrimSpace(cronExpression)
	if cronExpression == "" {
		return "", fmt.Errorf("cron_expression required")
	}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO run_jobs (config_revision_id, project_id, run_at, status, cron_expression) VALUES ($1, $2::uuid, $3, 'pending', $4) RETURNING id::text`,
		configRevisionID, projectID, runAt.UTC(), cronExpression,
	).Scan(&jobID)
	return jobID, err
}
