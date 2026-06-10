-- symbols: indexed code symbols (classes, methods, fields)
CREATE TABLE IF NOT EXISTS symbols (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lang           TEXT NOT NULL,
    kind           TEXT NOT NULL,
    fq_name        TEXT NOT NULL,
    file           TEXT NOT NULL,
    start_line     INTEGER NOT NULL,
    end_line       INTEGER NOT NULL,
    signature_json JSONB
);

CREATE INDEX IF NOT EXISTS idx_symbols_lang ON symbols (lang);
CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols (file);
CREATE INDEX IF NOT EXISTS idx_symbols_fq_name ON symbols (fq_name);
CREATE INDEX IF NOT EXISTS idx_symbols_kind ON symbols (kind);

-- Optional precise span (see docs/DOCUMENTATION.md — Symbol line/column spans). NULL when unknown or line-only indexer.
ALTER TABLE symbols ADD COLUMN IF NOT EXISTS start_column INTEGER;
ALTER TABLE symbols ADD COLUMN IF NOT EXISTS end_column INTEGER;

-- edges: directed relationships between symbols (caller -> callee)
CREATE TABLE IF NOT EXISTS edges (
    caller_symbol_id UUID NOT NULL REFERENCES symbols (id) ON DELETE CASCADE,
    callee_symbol_id UUID NOT NULL REFERENCES symbols (id) ON DELETE CASCADE,
    edge_type       TEXT NOT NULL,
    PRIMARY KEY (caller_symbol_id, callee_symbol_id, edge_type)
);

CREATE INDEX IF NOT EXISTS idx_edges_caller ON edges (caller_symbol_id);
CREATE INDEX IF NOT EXISTS idx_edges_callee ON edges (callee_symbol_id);
CREATE INDEX IF NOT EXISTS idx_edges_type ON edges (edge_type);

-- files: tracked source and test files
CREATE TABLE IF NOT EXISTS files (
    file    TEXT PRIMARY KEY,
    sha     TEXT NOT NULL,
    lang    TEXT NOT NULL,
    module  TEXT NOT NULL DEFAULT '',
    is_test BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_files_lang ON files (lang);
CREATE INDEX IF NOT EXISTS idx_files_module ON files (module);
CREATE INDEX IF NOT EXISTS idx_files_is_test ON files (is_test);

-- index_runs: versioning and scheduling of indexing (incremental updates)
-- repo_id: required stable key for symbols/chunks/edges (derived from clone URL, explicit repo_id, or local path). Not a FK, separate from control-plane linkage below.
-- project_id: optional FK to projects (API/scheduler runs scoped to a tenant project).
-- repo_url: optional clone URL for this run (request body or resolved from project when cloning).
-- repo_local_path: optional absolute filesystem path when the workspace was a local tree (with or without project_id). NULL when the run cloned repo_url only.
-- current_iteration: max evaluation fix-iteration budget for this run (starts at start_max_iteration, incremented when unstable until max_iteration).
-- scheduled_rerun_at: unix ms when this run should be re-run after unstable evaluation (NULL if not scheduled).
-- status: 'running' | 'completed' | 'failed' — running, finished successfully, or terminal failure (workflow_error set).
-- stable: when status='completed' and evaluation ran, true/false. NULL while running or if evaluate was skipped.
-- iterations: actual fix-loop iterations used for this run (e.g. 4). NULL when evaluate was skipped.
CREATE TABLE IF NOT EXISTS index_runs (
    run_id             TEXT PRIMARY KEY,
    repo_id            TEXT NOT NULL DEFAULT '',
    commit_sha         TEXT NOT NULL DEFAULT '',
    started_at         BIGINT NOT NULL,
    finished_at        BIGINT NOT NULL DEFAULT 0,
    current_iteration  INTEGER NOT NULL DEFAULT 3,
    iterations         INTEGER,
    scheduled_rerun_at BIGINT,
    status             TEXT NOT NULL DEFAULT 'running',
    stable             BOOLEAN
);

-- Migration: add evaluation-stabilization and status columns (no-op if already present). Run before indexes that use these columns.
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS current_iteration INTEGER NOT NULL DEFAULT 3;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS iterations INTEGER;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS scheduled_rerun_at BIGINT;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'running';
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS stable BOOLEAN;
-- first_wave_metrics: JSONB snapshot of first-wave quality metrics after evaluation (see docs/DOCUMENTATION.md — First-wave quality metrics). NULL while running, skipped eval, or before completion.
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS first_wave_metrics JSONB;

CREATE INDEX IF NOT EXISTS idx_index_runs_repo ON index_runs (repo_id);
CREATE INDEX IF NOT EXISTS idx_index_runs_scheduled_rerun ON index_runs (scheduled_rerun_at) WHERE scheduled_rerun_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_index_runs_status ON index_runs (status) WHERE status = 'running';

-- audit_log: persisted step log for each run (index, retrieve, generate, etc.) for debugging and improvement
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    run_id     TEXT NOT NULL,
    at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    step       TEXT NOT NULL,
    payload    JSONB,
    level      TEXT NOT NULL DEFAULT 'info'
);

CREATE INDEX IF NOT EXISTS idx_audit_log_run_id ON audit_log (run_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_at ON audit_log (at);
CREATE INDEX IF NOT EXISTS idx_audit_log_step ON audit_log (step);

-- Control plane: versioned named configs (see docs/API-IMPLEMENTATION_PLAN.md)
CREATE TABLE IF NOT EXISTS configs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS config_revisions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    config_id    UUID NOT NULL REFERENCES configs (id) ON DELETE CASCADE,
    version      INTEGER NOT NULL,
    yaml_body    TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by   TEXT NOT NULL DEFAULT '',
    UNIQUE (config_id, version)
);

CREATE INDEX IF NOT EXISTS idx_config_revisions_config ON config_revisions (config_id, version DESC);

ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS config_revision_id UUID REFERENCES config_revisions (id) ON DELETE SET NULL;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS trigger_source TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS workflow_error TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_index_runs_started_at ON index_runs (started_at DESC);
CREATE INDEX IF NOT EXISTS idx_index_runs_config_revision ON index_runs (config_revision_id) WHERE config_revision_id IS NOT NULL;

-- Orchestrator heartbeat for stale-run detection (epoch ms, 0 = fall back to started_at in API/reaper).
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS last_heartbeat_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS repo_local_path TEXT NOT NULL DEFAULT '';
-- Optional URL vs local path: NULL means "not used for this run" (API still maps empty to "" via COALESCE on read).
ALTER TABLE index_runs ALTER COLUMN repo_url DROP NOT NULL;
ALTER TABLE index_runs ALTER COLUMN repo_local_path DROP NOT NULL;
UPDATE index_runs SET repo_url = NULL WHERE repo_url = '';
UPDATE index_runs SET repo_local_path = NULL WHERE repo_local_path = '';

-- Async full audit export jobs (file on disk under APISERVER_AUDIT_ASYNC_DIR).
CREATE TABLE IF NOT EXISTS audit_exports (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id         TEXT NOT NULL,
    format         TEXT NOT NULL DEFAULT 'json',
    status         TEXT NOT NULL DEFAULT 'pending',
    line_count     BIGINT NOT NULL DEFAULT 0,
    error_message  TEXT NOT NULL DEFAULT '',
    file_name      TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_audit_exports_pending ON audit_exports (created_at) WHERE status = 'pending';

-- Multi-tenant control plane: tenants → projects → runs (see docs/API-IMPLEMENTATION_PLAN.md §13).
CREATE TABLE IF NOT EXISTS tenants (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL,
    max_projects  INTEGER NOT NULL DEFAULT 3 CHECK (max_projects >= 1),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_created_at ON tenants (created_at DESC);

CREATE TABLE IF NOT EXISTS projects (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                  UUID NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name                       TEXT NOT NULL,
    repo_url                   TEXT NOT NULL,
    display_name               TEXT NOT NULL DEFAULT '',
    created_by                 TEXT NOT NULL DEFAULT '',
    config_id                  UUID NOT NULL REFERENCES configs (id) ON DELETE RESTRICT,
    pinned_config_revision_id  UUID REFERENCES config_revisions (id) ON DELETE SET NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_projects_tenant ON projects (tenant_id);
CREATE INDEX IF NOT EXISTS idx_projects_config ON projects (config_id);

ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_index_runs_project ON index_runs (project_id) WHERE project_id IS NOT NULL;

-- Schedule queue: one row per future run, keyed by project (clone URL lives on projects.repo_url).
CREATE TABLE IF NOT EXISTS run_jobs (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    config_revision_id UUID NOT NULL REFERENCES config_revisions (id),
    run_at             TIMESTAMPTZ NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending',
    created_run_id     TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_run_jobs_run FOREIGN KEY (created_run_id) REFERENCES index_runs (run_id) ON DELETE SET NULL
);
ALTER TABLE run_jobs ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects (id) ON DELETE CASCADE;
ALTER TABLE run_jobs DROP COLUMN IF EXISTS repo_url;
ALTER TABLE run_jobs ADD COLUMN IF NOT EXISTS cron_expression TEXT;

CREATE INDEX IF NOT EXISTS idx_run_jobs_due ON run_jobs (run_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_run_jobs_project ON run_jobs (project_id) WHERE project_id IS NOT NULL;

DROP TABLE IF EXISTS repos;

-- Agent-session engine (see docs/SESSIONS.md). Each qualitybot run produces one run_sessions row
-- and N gap_sessions rows (one per plan item). session_attempts logs tool invocations
-- (do not use semicolons inside these line comments: store.go splitSQL splits on semicolon only)
-- session_feedback stores normalized structured observations emitted by normalizers.
-- Writes are best-effort from internal/session/engine. Failing to persist never aborts a run.
CREATE TABLE IF NOT EXISTS run_sessions (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT NOT NULL,
    project_id          UUID REFERENCES projects (id) ON DELETE SET NULL,
    repo_id             TEXT NOT NULL DEFAULT '',
    commit_sha          TEXT NOT NULL DEFAULT '',
    task_kind           TEXT NOT NULL DEFAULT 'test_gen',
    goal                TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL DEFAULT 'planning',
    current_iteration   INTEGER NOT NULL DEFAULT 0,
    max_iteration       INTEGER NOT NULL DEFAULT 0,
    scheduled_rerun_at  BIGINT,
    bootstrap           JSONB,
    index_delta         JSONB,
    plan_summary        JSONB,
    seam_notes          JSONB NOT NULL DEFAULT '{}'::jsonb,
    overview_path       TEXT NOT NULL DEFAULT '',
    discard_paths       JSONB,
    started_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL,
    finished_at         BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT fk_run_sessions_run FOREIGN KEY (run_id) REFERENCES index_runs (run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_sessions_run_id ON run_sessions (run_id);
CREATE INDEX IF NOT EXISTS idx_run_sessions_project ON run_sessions (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_run_sessions_state ON run_sessions (state);
CREATE INDEX IF NOT EXISTS idx_run_sessions_started_at ON run_sessions (started_at DESC);
ALTER TABLE run_sessions ADD COLUMN IF NOT EXISTS seam_notes JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE IF NOT EXISTS gap_sessions (
    id                  TEXT PRIMARY KEY,
    run_session_id      TEXT NOT NULL REFERENCES run_sessions (id) ON DELETE CASCADE,
    symbol_fq_name      TEXT NOT NULL DEFAULT '',
    source_file         TEXT NOT NULL DEFAULT '',
    layer               TEXT NOT NULL DEFAULT 'unit',
    kind                TEXT NOT NULL DEFAULT 'unit',
    retrieval_profile   TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL DEFAULT 'planning',
    current_step        TEXT NOT NULL DEFAULT '',
    last_error          TEXT NOT NULL DEFAULT '',
    iterations_used     INTEGER NOT NULL DEFAULT 0,
    iteration_budget    INTEGER NOT NULL DEFAULT 0,
    artifact_paths      JSONB,
    discarded_paths     JSONB,
    fingerprint         TEXT NOT NULL DEFAULT '',
    abstain_reason      TEXT NOT NULL DEFAULT '',
    started_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL,
    finished_at         BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_gap_sessions_run ON gap_sessions (run_session_id);
CREATE INDEX IF NOT EXISTS idx_gap_sessions_state ON gap_sessions (state);
CREATE INDEX IF NOT EXISTS idx_gap_sessions_symbol ON gap_sessions (symbol_fq_name);
-- B.9 schema additions: fine-grained live progress columns. Older deployments may have the
-- table without these, so add them idempotently. current_step is the engine `Step` constant
-- (retrieve, generate, per_gap_write, compile, test, lint, fix_compile, fix_test, …);
-- last_error is the most recent failure summary surfaced to the UI per-gap pill.
ALTER TABLE gap_sessions ADD COLUMN IF NOT EXISTS current_step TEXT NOT NULL DEFAULT '';
ALTER TABLE gap_sessions ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS session_attempts (
    id                  BIGSERIAL PRIMARY KEY,
    run_session_id      TEXT NOT NULL REFERENCES run_sessions (id) ON DELETE CASCADE,
    gap_session_id      TEXT REFERENCES gap_sessions (id) ON DELETE CASCADE,
    idx                 INTEGER NOT NULL DEFAULT 0,
    tool                TEXT NOT NULL,
    step                TEXT NOT NULL,
    input_summary       JSONB,
    output_summary      JSONB,
    duration_ms         BIGINT NOT NULL DEFAULT 0,
    ok                  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_attempts_run ON session_attempts (run_session_id);
CREATE INDEX IF NOT EXISTS idx_session_attempts_gap ON session_attempts (gap_session_id) WHERE gap_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_attempts_created_at ON session_attempts (created_at DESC);

CREATE TABLE IF NOT EXISTS session_feedback (
    id                    BIGSERIAL PRIMARY KEY,
    run_session_id        TEXT NOT NULL REFERENCES run_sessions (id) ON DELETE CASCADE,
    gap_session_id        TEXT REFERENCES gap_sessions (id) ON DELETE CASCADE,
    kind                  TEXT NOT NULL,
    subkind               TEXT NOT NULL DEFAULT '',
    owner_artifact_path   TEXT NOT NULL DEFAULT '',
    file                  TEXT NOT NULL DEFAULT '',
    symbol                TEXT NOT NULL DEFAULT '',
    line                  INTEGER NOT NULL DEFAULT 0,
    message               TEXT NOT NULL DEFAULT '',
    suggested_action      TEXT NOT NULL DEFAULT '',
    raw_excerpt           TEXT NOT NULL DEFAULT '',
    payload               JSONB,
    created_at            BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_feedback_run ON session_feedback (run_session_id);
CREATE INDEX IF NOT EXISTS idx_session_feedback_gap ON session_feedback (gap_session_id) WHERE gap_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_feedback_kind ON session_feedback (kind);
CREATE INDEX IF NOT EXISTS idx_session_feedback_owner ON session_feedback (owner_artifact_path) WHERE owner_artifact_path <> '';
