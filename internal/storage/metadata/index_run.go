package metadata

import (
	"database/sql"
)

// IndexRunRow is one row from index_runs plus derived flags for API listing/detail.
// RepoID is the stable index key for chunks/symbols; ProjectID, RepoURL, RepoLocalPath are optional control-plane fields (DB may use NULL; reads COALESCE URL/path to "").
type IndexRunRow struct {
	RunID     string
	RepoID    string // partition key for indexed data; always set for a meaningful run
	CommitSHA string
	StartedAt int64
	// LastHeartbeatAt is epoch ms when the orchestrator last touched the run; 0 means use StartedAt for staleness.
	LastHeartbeatAt     int64
	FinishedAt          int64
	CurrentIteration    int
	Iterations          sql.NullInt64
	ScheduledRerunAt    sql.NullInt64
	Status              string
	Stable              sql.NullBool
	WorkflowError       string
	TriggerSource       string
	RepoURL             string
	RepoLocalPath       string // absolute local workspace path when run used filesystem instead of clone URL
	ConfigRevisionID    sql.NullString
	ProjectID           sql.NullString // projects.id when run was scoped to a project
	HasAudit            bool
	HasMetrics          bool
	FirstWaveMetricsRaw sql.NullString // set on detail fetch only; JSON text
}

// ListRunsOptions filters and pagination for ListIndexRuns.
type ListRunsOptions struct {
	RepoID    string
	ProjectID string // filter index_runs.project_id (UUID text)
	TenantID  string // filter runs whose project belongs to this tenant (UUID text)
	Status    string // DB status: running | completed | failed; empty = all
	SinceMs   *int64 // started_at >= *SinceMs
	UntilMs   *int64 // started_at <= *UntilMs
	// ScheduledRerunOnly when true restricts to rows with scheduled_rerun_at IS NOT NULL (evaluation-scheduled reruns).
	// Results are ordered by scheduled_rerun_at ascending (soonest first).
	ScheduledRerunOnly bool
	Limit              int
	Offset             int
}

// ConfigSummary is one named config with latest revision number (for list).
type ConfigSummary struct {
	ID            string
	Name          string
	Description   string
	LatestVersion int
	UpdatedAt     string // RFC3339 from configs.updated_at
}

// RevisionMeta is metadata for one revision (no YAML body).
type RevisionMeta struct {
	ID        string
	Version   int
	CreatedAt string
	CreatedBy string
}

// Revision includes full YAML body for a config revision.
type Revision struct {
	RevisionMeta
	YAMLBody string
}
