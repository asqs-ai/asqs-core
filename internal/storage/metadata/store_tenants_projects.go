package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TenantRow is one tenants row.
type TenantRow struct {
	ID          string
	Name        string
	MaxProjects int
	CreatedAt   time.Time
}

// ProjectRow is one projects row.
type ProjectRow struct {
	ID                     string
	TenantID               string
	Name                   string
	RepoURL                string
	DisplayName            string
	CreatedBy              string
	ConfigID               string
	PinnedConfigRevisionID sql.NullString
	CreatedAt              time.Time
}

// InsertTenant creates a tenant. maxProjects <= 0 defaults to 3.
func (s *Store) InsertTenant(ctx context.Context, name string, maxProjects int) (tenantID string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("tenant name required")
	}
	if maxProjects <= 0 {
		maxProjects = 3
	}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO tenants (name, max_projects) VALUES ($1, $2) RETURNING id::text`,
		name, maxProjects,
	).Scan(&tenantID)
	return tenantID, err
}

// GetTenantByID returns one tenant or (nil, nil).
func (s *Store) GetTenantByID(ctx context.Context, tenantID string) (*TenantRow, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, nil
	}
	var r TenantRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id::text, name, max_projects, created_at FROM tenants WHERE id = $1::uuid`,
		tenantID,
	).Scan(&r.ID, &r.Name, &r.MaxProjects, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListTenants returns tenants newest first.
func (s *Store) ListTenants(ctx context.Context, limit, offset int) ([]TenantRow, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id::text, name, max_projects, created_at FROM tenants ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var r TenantRow
		if err := rows.Scan(&r.ID, &r.Name, &r.MaxProjects, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// UpdateTenant updates name and/or max_projects. maxProjects nil = leave unchanged.
// Returns false if tenant not found. Rejects max below current project count.
func (s *Store) UpdateTenant(ctx context.Context, tenantID, name string, maxProjects *int) (ok bool, err error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return false, fmt.Errorf("tenant id required")
	}
	name = strings.TrimSpace(name)
	if name == "" && maxProjects == nil {
		return false, fmt.Errorf("nothing to update")
	}
	var n int
	if maxProjects != nil {
		if *maxProjects < 1 {
			return false, fmt.Errorf("max_projects must be >= 1")
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE tenant_id = $1::uuid`, tenantID).Scan(&n); err != nil {
			return false, err
		}
		if *maxProjects < n {
			return false, fmt.Errorf("max_projects cannot be below current project count (%d)", n)
		}
	}
	if maxProjects == nil {
		if name == "" {
			return false, fmt.Errorf("name required when not updating max_projects")
		}
		res, err := s.db.ExecContext(ctx, `UPDATE tenants SET name = $1 WHERE id = $2::uuid`, name, tenantID)
		if err != nil {
			return false, err
		}
		aff, _ := res.RowsAffected()
		return aff > 0, nil
	}
	if name == "" {
		res, err := s.db.ExecContext(ctx, `UPDATE tenants SET max_projects = $1 WHERE id = $2::uuid`, *maxProjects, tenantID)
		if err != nil {
			return false, err
		}
		aff, _ := res.RowsAffected()
		return aff > 0, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tenants SET name = $1, max_projects = $2 WHERE id = $3::uuid`, name, *maxProjects, tenantID)
	if err != nil {
		return false, err
	}
	aff, _ := res.RowsAffected()
	return aff > 0, nil
}

// CountProjectsForTenant returns how many projects belong to the tenant.
func (s *Store) CountProjectsForTenant(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE tenant_id = $1::uuid`, strings.TrimSpace(tenantID)).Scan(&n)
	return n, err
}

// InsertProject creates a project if the tenant is under max_projects.
// displayName and createdBy are optional labels (former standalone repos registry fields).
func (s *Store) InsertProject(ctx context.Context, tenantID, name, repoURL, displayName, createdBy, configID, pinnedConfigRevisionID string) (projectID string, err error) {
	tenantID = strings.TrimSpace(tenantID)
	name = strings.TrimSpace(name)
	repoURL = strings.TrimSpace(repoURL)
	displayName = strings.TrimSpace(displayName)
	createdBy = strings.TrimSpace(createdBy)
	configID = strings.TrimSpace(configID)
	pinnedConfigRevisionID = strings.TrimSpace(pinnedConfigRevisionID)
	if tenantID == "" || name == "" || repoURL == "" || configID == "" {
		return "", fmt.Errorf("tenant_id, name, repo_url, and config_id required")
	}
	t, err := s.GetTenantByID(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("tenant not found")
	}
	n, err := s.CountProjectsForTenant(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if n >= t.MaxProjects {
		return "", fmt.Errorf("tenant project limit reached (%d)", t.MaxProjects)
	}
	var pinned sql.NullString
	if pinnedConfigRevisionID != "" {
		var cfgFromRev string
		err := s.db.QueryRowContext(ctx,
			`SELECT config_id::text FROM config_revisions WHERE id = $1::uuid`, pinnedConfigRevisionID,
		).Scan(&cfgFromRev)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("pinned config revision not found")
		}
		if err != nil {
			return "", err
		}
		if cfgFromRev != configID {
			return "", fmt.Errorf("pinned revision does not belong to config_id")
		}
		pinned = sql.NullString{String: pinnedConfigRevisionID, Valid: true}
	}
	err = s.db.QueryRowContext(ctx, `
INSERT INTO projects (tenant_id, name, repo_url, display_name, created_by, config_id, pinned_config_revision_id)
VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7)
RETURNING id::text`,
		tenantID, name, repoURL, displayName, createdBy, configID, pinned,
	).Scan(&projectID)
	return projectID, err
}

// GetProjectByID returns a project or (nil, nil).
func (s *Store) GetProjectByID(ctx context.Context, projectID string) (*ProjectRow, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, nil
	}
	var r ProjectRow
	err := s.db.QueryRowContext(ctx, `
SELECT id::text, tenant_id::text, name, repo_url, display_name, created_by, config_id::text, pinned_config_revision_id, created_at
FROM projects WHERE id = $1::uuid`, projectID,
	).Scan(&r.ID, &r.TenantID, &r.Name, &r.RepoURL, &r.DisplayName, &r.CreatedBy, &r.ConfigID, &r.PinnedConfigRevisionID, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListProjectsByTenant returns projects for a tenant (newest first).
func (s *Store) ListProjectsByTenant(ctx context.Context, tenantID string, limit, offset int) ([]ProjectRow, int64, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, 0, fmt.Errorf("tenant id required")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE tenant_id = $1::uuid`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id::text, tenant_id::text, name, repo_url, display_name, created_by, config_id::text, pinned_config_revision_id, created_at
FROM projects WHERE tenant_id = $1::uuid
ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		tenantID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		var r ProjectRow
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Name, &r.RepoURL, &r.DisplayName, &r.CreatedBy, &r.ConfigID, &r.PinnedConfigRevisionID, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// DeleteProject removes a project. index_runs.project_id becomes NULL (ON DELETE SET NULL).
func (s *Store) DeleteProject(ctx context.Context, projectID string) (deleted bool, err error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, fmt.Errorf("project id required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1::uuid`, projectID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateProject updates mutable fields. Empty strings mean "leave unchanged" for name/repo_url; pinned empty string clears pin; omit pin with a pointer — use UpdateProjectPatch.
func (s *Store) UpdateProject(ctx context.Context, projectID string, name, repoURL string, clearPinned bool, newPinnedRevisionID string) (ok bool, err error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, fmt.Errorf("project id required")
	}
	p, err := s.GetProjectByID(ctx, projectID)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	newName := p.Name
	if s := strings.TrimSpace(name); s != "" {
		newName = s
	}
	newURL := p.RepoURL
	if s := strings.TrimSpace(repoURL); s != "" {
		newURL = s
	}
	var pin sql.NullString
	if clearPinned {
		pin = sql.NullString{}
	} else if rev := strings.TrimSpace(newPinnedRevisionID); rev != "" {
		var cfgID string
		err := s.db.QueryRowContext(ctx,
			`SELECT config_id::text FROM config_revisions WHERE id = $1::uuid`, rev,
		).Scan(&cfgID)
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("pinned config revision not found")
		}
		if err != nil {
			return false, err
		}
		if cfgID != p.ConfigID {
			return false, fmt.Errorf("pinned revision does not belong to project's config")
		}
		pin = sql.NullString{String: rev, Valid: true}
	} else {
		pin = p.PinnedConfigRevisionID
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE projects SET name = $1, repo_url = $2, pinned_config_revision_id = $3 WHERE id = $4::uuid`,
		newName, newURL, pin, projectID,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResolveProjectForRun returns clone URL and config_revisions.id to use for this project.
// When pinned_config_revision_id is set it must belong to the project's config_id; otherwise the latest revision for that config is used.
func (s *Store) ResolveProjectForRun(ctx context.Context, projectID string) (repoURL, configRevisionID string, err error) {
	p, err := s.GetProjectByID(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if p == nil {
		return "", "", fmt.Errorf("project not found")
	}
	repoURL = strings.TrimSpace(p.RepoURL)
	if repoURL == "" {
		return "", "", fmt.Errorf("project has empty repo_url")
	}
	if p.PinnedConfigRevisionID.Valid {
		rid := strings.TrimSpace(p.PinnedConfigRevisionID.String)
		var cfgID string
		err := s.db.QueryRowContext(ctx,
			`SELECT config_id::text FROM config_revisions WHERE id = $1::uuid`, rid,
		).Scan(&cfgID)
		if err == sql.ErrNoRows {
			return "", "", fmt.Errorf("pinned config revision not found")
		}
		if err != nil {
			return "", "", err
		}
		if cfgID != p.ConfigID {
			return "", "", fmt.Errorf("pinned revision does not belong to project's config")
		}
		return repoURL, rid, nil
	}
	err = s.db.QueryRowContext(ctx, `
SELECT id::text FROM config_revisions WHERE config_id = $1::uuid ORDER BY version DESC LIMIT 1`,
		p.ConfigID,
	).Scan(&configRevisionID)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("no config revision for project's config")
	}
	if err != nil {
		return "", "", err
	}
	return repoURL, configRevisionID, nil
}

// ProjectAPIBundle is one read-only transaction snapshot: project row, config catalog fields,
// the revision used for execute (pinned when valid, else latest), and the true latest revision.
type ProjectAPIBundle struct {
	Project         ProjectRow
	ConfigName      string
	ConfigDesc      string
	ConfigUpdatedAt string
	EffectiveRev    *Revision
	LatestRev       *Revision
}

func scanLatestRevisionTx(ctx context.Context, tx *sql.Tx, configID string) (*Revision, error) {
	query := `
SELECT r.id, r.version, r.created_at, r.created_by, r.yaml_body
FROM config_revisions r
WHERE r.config_id = $1::uuid
ORDER BY r.version DESC
LIMIT 1`
	var rev Revision
	var at time.Time
	err := tx.QueryRowContext(ctx, query, strings.TrimSpace(configID)).Scan(
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

// GetProjectAPIBundle loads project + config + revisions in a single read-only transaction.
// Returns (nil, nil) when the project id is missing or unknown.
func (s *Store) GetProjectAPIBundle(ctx context.Context, projectID string) (*ProjectAPIBundle, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var r ProjectRow
	err = tx.QueryRowContext(ctx, `
SELECT id::text, tenant_id::text, name, repo_url, display_name, created_by, config_id::text, pinned_config_revision_id, created_at
FROM projects WHERE id = $1::uuid`, projectID,
	).Scan(&r.ID, &r.TenantID, &r.Name, &r.RepoURL, &r.DisplayName, &r.CreatedBy, &r.ConfigID, &r.PinnedConfigRevisionID, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	out := &ProjectAPIBundle{Project: r}
	cfgID := strings.TrimSpace(r.ConfigID)
	if cfgID == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return out, nil
	}

	var cfgUpdated time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT name, description, updated_at FROM configs WHERE id = $1::uuid`,
		cfgID,
	).Scan(&out.ConfigName, &out.ConfigDesc, &cfgUpdated)
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.ConfigUpdatedAt = cfgUpdated.UTC().Format(time.RFC3339Nano)

	latest, err := scanLatestRevisionTx(ctx, tx, cfgID)
	if err != nil {
		return nil, err
	}
	out.LatestRev = latest

	var effective *Revision
	if r.PinnedConfigRevisionID.Valid {
		rid := strings.TrimSpace(r.PinnedConfigRevisionID.String)
		if rid != "" {
			var rev Revision
			var cfgFromRev string
			var at time.Time
			err = tx.QueryRowContext(ctx, `
SELECT r.id, r.version, r.created_at, r.created_by, r.yaml_body, r.config_id::text
FROM config_revisions r
WHERE r.id = $1::uuid`, rid,
			).Scan(&rev.ID, &rev.Version, &at, &rev.CreatedBy, &rev.YAMLBody, &cfgFromRev)
			if err == nil && cfgFromRev == cfgID {
				rev.CreatedAt = at.UTC().Format(time.RFC3339Nano)
				effective = &rev
			} else if err != nil && err != sql.ErrNoRows {
				return nil, err
			}
		}
	}
	if effective == nil {
		effective = latest
	}
	out.EffectiveRev = effective

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
