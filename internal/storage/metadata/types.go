package metadata

import (
	"time"
)

// Symbol represents a code symbol (class, method, field, etc.) in the index.
type Symbol struct {
	ID        string // UUID
	Lang      string // e.g. "java", "csharp"
	Kind      string // e.g. "class", "method", "field"
	FQName    string // fully qualified name
	File      string // source file path
	StartLine int    // 1-based
	EndLine   int    // 1-based
	// StartColumn/EndColumn are optional: 1-based column within start/end line (see DOCUMENTATION.md). Nil when unknown.
	StartColumn   *int
	EndColumn     *int
	SignatureJSON []byte // JSON; nil for empty
	// InDegree / OutDegree / InDegreeNonTest are materialized by RecomputeSymbolDegrees at the end
	// of an index run. InDegreeNonTest excludes TESTS_SOURCE edges — see the centrality signal in
	// gap listing, which would otherwise count a symbol's own tests as evidence that it is central
	// and under-tested.
	InDegree        int
	OutDegree       int
	InDegreeNonTest int
	// RepoID scopes the symbol to one repository. Empty means "unscoped", which is what rows
	// written before repo-scoping carry; deletes match it exactly rather than treating it as a
	// wildcard, so a legacy row is never removed by a scoped run.
	RepoID string
}

// EdgeTypeTestsSource is a materialized trace link: caller is a symbol in a **test** file, callee is a symbol in **production** code that the test is inferred to exercise or name-align with (heuristic; see MaterializeTestsSourceEdges). Matches practice in test–code traceability literature (requirements / coverage links as a graph).
const EdgeTypeTestsSource = "TESTS_SOURCE"

// Edge represents a directed relationship between two symbols (e.g. calls, extends).
type Edge struct {
	CallerSymbolID string // FK to symbols.id
	CalleeSymbolID string // FK to symbols.id
	EdgeType       string // e.g. "calls", "extends", "implements", "TESTS_SOURCE"
	// RepoID is denormalized from the caller symbol so traversal can filter without joining
	// symbols. An edge whose endpoints are in different repositories is a mis-binding, not a
	// feature; see migration 0006.
	RepoID string
}

// EdgeFile is a file-level edge (caller file -> callee file) derived from symbol edges.
type EdgeFile struct {
	CallerFile string // path of file containing the caller symbol
	CalleeFile string // path of file containing the callee symbol
	EdgeType   string // e.g. "calls"
}

// File represents a tracked source or test file in the repo.
type File struct {
	File   string // repo-relative path; primary key is (RepoID, File)
	SHA    string // e.g. git blob sha
	Lang   string // e.g. "java", "csharp"
	Module string // e.g. Maven module, .NET project
	IsTest bool   // true if test file
	// RepoID scopes the row. `File` is a repo-relative path, so it is not unique on its own —
	// `pom.xml` exists in every Maven repository indexed into this database.
	RepoID string
}

// AuditEntry is one row from audit_log (step for a run).
type AuditEntry struct {
	ID      int64  // primary key
	RunID   string // run identifier
	At      string // ISO8601 timestamp (as stored/returned)
	Step    string // e.g. "index.start", "retrieve.plan_done"
	Level   string // info, warn, error
	Payload []byte // JSON; nil if empty
}

// ListAuditOptions filters audit log listing.
type ListAuditOptions struct {
	RunID   *string    // if set, only this run
	Since   *time.Time // if set, only entries with at >= Since (inclusive)
	Until   *time.Time // if set, only entries with at <= Until (inclusive)
	AfterID *int64     // if set, only rows with id > *AfterID (stable cursor for streaming)
	Limit   int        // max rows (0 = default, e.g. 10000)
}

// FirstWaveRunMetrics is stored in index_runs.first_wave_metrics (JSONB) when evaluation completes successfully.
// Query remotely with SQL, e.g. first_wave_metrics->>'test_ok_without_fix' = 'true'.
type FirstWaveRunMetrics struct {
	CompileOKAfterGenerate bool  `json:"compile_ok_after_generate"`
	TestOKWithoutFix       bool  `json:"test_ok_without_fix"`
	EvalStable             bool  `json:"eval_stable"`
	EvalIterations         int   `json:"eval_iterations"`
	CompileFixCount        int   `json:"compile_fix_count"`
	TestFixCount           int   `json:"test_fix_count"`
	LlmTotalTokens         int64 `json:"llm_total_tokens"`
	// PromptTokens is the prompt-side share of LlmTotalTokens as the provider reported it — the
	// ground truth the context-budget calibration (CP28's counter) is measured against. Pointer,
	// like TokensToStable: absent means the provider reported no usage, which must stay
	// distinguishable from a genuine zero.
	PromptTokens   *int64 `json:"prompt_tokens,omitempty"`
	TokensToStable *int64 `json:"tokens_to_stable,omitempty"`
}
