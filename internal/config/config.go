// Package config provides a robust, per-client configuration system for QualityBot.
//
// All communication with external systems (Postgres, VCS platforms, LLM APIs) is driven by Config.
// Load from file (YAML), environment variables, or both; use ClientID and EnvPrefix for
// multi-tenant or per-environment overrides.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Config is the root configuration for the quality pipeline.
type Config struct {
	// SourcePath is the YAML file this config was loaded from ("" when env-only). Set by Load,
	// never by YAML: the pipeline records the file's content as a config revision per run so the
	// A/B report can join runs to the exact configuration that produced them.
	SourcePath string `yaml:"-"`

	// ClientID identifies the client/tenant; used for env overrides (e.g. ASQS_<ClientID>_*).
	ClientID string `yaml:"client_id" env:"CLIENT_ID"`

	// Database holds Postgres connection settings for metadata and embeddings.
	Database DatabaseConfig `yaml:"database"`

	// VCS holds version control (e.g. GitHub) connection and defaults.
	VCS VCSConfig `yaml:"vcs"`

	// LLM holds the language model API (embeddings, completions) settings.
	LLM LLMConfig `yaml:"llm"`

	// Runner holds sandbox/CI runner settings (e.g. Docker).
	Runner RunnerConfig `yaml:"runner"`

	// Indexer holds indexer run schedule and first-run behaviour.
	Indexer IndexerConfig `yaml:"indexer"`

	// Retrieval configures symbol-aware context when building the test plan (graph expansion + similar chunks).
	Retrieval RetrievalConfig `yaml:"retrieval"`

	// Generation configures the tool-calling generation loop.
	Generation GenerationConfig `yaml:"generation"`

	// WebSearch configures the external documentation tools. Off by default; the only feature that
	// sends data out of the process.
	WebSearch WebSearchConfig `yaml:"websearch"`

	// Audit holds audit log settings (run-scoped step logging for debugging and improvement).
	Audit AuditConfig `yaml:"audit"`
}

// RetrievalProfileBudget caps context sections for one RetrievalProfile (unit and E2E plans resolve separately using each plan’s active profile).
// Zero values in a field mean “do not override” when merging onto global defaults.
type RetrievalProfileBudget struct {
	MaxSimilarTests     int `yaml:"max_similar_tests"`
	MaxDependencyChunks int `yaml:"max_dependency_chunks"`
	MaxFixtures         int `yaml:"max_fixtures"`
}

// ContextCompactConfig switches deterministic shrinking of retrieved chunks on or off before LLM
// context assembly.
//
// The on/off switch is ALL that remains configurable. CP17 wired compaction and passed only Enabled
// through, so the rune caps and the merge/dedupe behaviours were already reaching their consumers as
// zero — the four keys parsed and did nothing. CP37 deleted them: the caps are
// retrieval.DefaultCompactMaxNonTargetChunkRunes (4096) and DefaultCompactBoilerplateScanRunes
// (2048), and the two behaviours stay off until a measurement says otherwise, which is a code change
// rather than an operator's.
type ContextCompactConfig struct {
	// Enabled: nil or true = compaction on (default when the key is omitted). Explicit false disables. Env: RETRIEVAL_CONTEXT_COMPACT_ENABLED (true/false/1/0).
	Enabled *bool `yaml:"enabled" env:"RETRIEVAL_CONTEXT_COMPACT_ENABLED"`
}

// ProjectIntelConfig configures repo doc / agent-skill scanning for generation context.
// YAML lives under runner.policy.project_intel (not retrieval). Env tags remain RETRIEVAL_PROJECT_INTEL_* for compatibility.
type ProjectIntelConfig struct {
	// Enabled when nil or true turns on project intel (still gated by run_hooks project_intel.enabled). Env: RETRIEVAL_PROJECT_INTEL_ENABLED.
	Enabled *bool `yaml:"enabled" env:"RETRIEVAL_PROJECT_INTEL_ENABLED"`
	// UseEmbeddingsRank reserved for future embedding rerank (default false).
	UseEmbeddingsRank bool `yaml:"use_embeddings_rank" env:"RETRIEVAL_PROJECT_INTEL_USE_EMBEDDINGS_RANK"`
	// ExtraDocGlobs optional additional glob patterns (repo-relative) for markdown docs.
	ExtraDocGlobs []string `yaml:"extra_doc_globs" env:"RETRIEVAL_PROJECT_INTEL_EXTRA_DOC_GLOBS"`
	// ExtraSkillGlobs optional additional globs for skill files.
	ExtraSkillGlobs []string `yaml:"extra_skill_globs" env:"RETRIEVAL_PROJECT_INTEL_EXTRA_SKILL_GLOBS"`
}

// EffectiveEnabled reports whether project intel is enabled at config layer.
func (c ProjectIntelConfig) EffectiveEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// The project-intel scan's shape is FROZEN (CP37). These were nine YAML keys with no plausible
// per-deployment value: an operator has no basis on which to prefer 11 doc files to 12, and the
// repository's own discipline is that a default earns a change through measurement.
//
// They stay as accessor METHODS rather than becoming bare constants because ConfigFingerprintHash
// folds their values into the project-intel cache key. Keeping the call shape keeps that hash
// byte-identical to its pre-freeze value, so no existing .asqs/project-intel-cache.json is
// invalidated by this bundle. Turning them into a shorter expression would silently cold-start
// every cache in the field.
const (
	projectIntelMaxTotalRunes       = 12000
	projectIntelMaxDocFiles         = 12
	projectIntelMaxSkillFiles       = 8
	projectIntelMinRelevanceScore   = 0.08
	projectIntelSummarizeAboveRunes = 6000
	projectIntelFingerprintMode     = "stat"
	// ProjectIntelCachePath is the committed-into-the-repository cache location. It is exported
	// because the pre-ship preserve list names the same path, and two spellings of one path is how
	// a cache silently stops surviving a ship.
	ProjectIntelCachePath = ".asqs/project-intel-cache.json"
)

// EffectiveMaxTotalRunes returns cap runes for injected block.
func (c ProjectIntelConfig) EffectiveMaxTotalRunes() int { return projectIntelMaxTotalRunes }

// EffectiveMaxDocFiles is frozen at 12.
func (c ProjectIntelConfig) EffectiveMaxDocFiles() int { return projectIntelMaxDocFiles }

// EffectiveMaxSkillFiles is frozen at 8.
func (c ProjectIntelConfig) EffectiveMaxSkillFiles() int { return projectIntelMaxSkillFiles }

// EffectiveMinRelevanceScore is frozen at 0.08.
func (c ProjectIntelConfig) EffectiveMinRelevanceScore() float64 {
	return projectIntelMinRelevanceScore
}

// EffectiveSummarizeAboveRunes is frozen at 6000.
func (c ProjectIntelConfig) EffectiveSummarizeAboveRunes() int {
	return projectIntelSummarizeAboveRunes
}

// EffectiveCacheEnabled is frozen on. The cache is what makes a second run cheap, and it is
// invalidated by the fingerprint rather than by a switch.
func (c ProjectIntelConfig) EffectiveCacheEnabled() bool { return true }

// EffectiveCachePath is frozen at the repo-relative committed location.
func (c ProjectIntelConfig) EffectiveCachePath() string { return ProjectIntelCachePath }

// EffectiveFingerprintMode is frozen at "stat" — mtime+size rather than content hashing. The
// "content" alternative cost a full re-read of every candidate doc on every run to catch a case the
// stat pair already catches.
func (c ProjectIntelConfig) EffectiveFingerprintMode() string { return projectIntelFingerprintMode }

// ConfigFingerprintHash returns a stable hash string of options that affect ranking/selection.
func (c ProjectIntelConfig) ConfigFingerprintHash() string {
	h := sha256.New()
	fmt.Fprintf(h, "enabled=%v", c.EffectiveEnabled())
	fmt.Fprintf(h, "max_total=%d", c.EffectiveMaxTotalRunes())
	fmt.Fprintf(h, "max_doc=%d", c.EffectiveMaxDocFiles())
	fmt.Fprintf(h, "max_skill=%d", c.EffectiveMaxSkillFiles())
	fmt.Fprintf(h, "min_rel=%g", c.EffectiveMinRelevanceScore())
	fmt.Fprintf(h, "sum_above=%d", c.EffectiveSummarizeAboveRunes())
	fmt.Fprintf(h, "emb=%v", c.UseEmbeddingsRank)
	for _, g := range c.ExtraDocGlobs {
		fmt.Fprintf(h, "docglob=%s|", g)
	}
	for _, g := range c.ExtraSkillGlobs {
		fmt.Fprintf(h, "skillglob=%s|", g)
	}
	fmt.Fprintf(h, "fp=%s", c.EffectiveFingerprintMode())
	return hex.EncodeToString(h.Sum(nil))
}

// RetrievalConfig selects how Retrieve expands the dependency graph for each test gap.
type RetrievalConfig struct {
	// Profile: java_unit (default), http_api, e2e_playwright, react_feature, nest_module. See internal/intelligence/retrieval/profiles.go.
	Profile string `yaml:"profile"`
	// ProfileE2E is used for the E2E test plan when indexer.max_gaps_e2e > 0. Empty: inherits retrieval.profile when set; else http_api for Java and C# and e2e_playwright for JS/TS (orchestrator.DefaultRetrievalProfileE2E).
	ProfileE2E string `yaml:"profile_e2e" env:"RETRIEVAL_PROFILE_E2E"`
	// FailureHintFile is an optional repo-relative UTF-8 file (compiler/test stderr or CI log). When WorkflowInput.RetrievalFailureHint is empty, the orchestrator reads this path before planning. Must not contain ".." (stay under repo root). Env: RETRIEVAL_FAILURE_HINT_FILE.
	FailureHintFile string `yaml:"failure_hint_file" env:"RETRIEVAL_FAILURE_HINT_FILE"`
	// PersistLastEvalFailure when true, after evaluation removes this file on success or writes failing compile/test/e2e step output for the next run (same path as failure_hint_file, or default .asqs/last-eval-failure.log when failure_hint_file is empty).
	PersistLastEvalFailure bool `yaml:"persist_last_eval_failure" env:"RETRIEVAL_PERSIST_LAST_EVAL_FAILURE"`
	// DisableHybridModuleFilter when true, similar-chunk vector search does not constrain chunk_metadata.module (disables hybrid structured filter). Default false. Env: RETRIEVAL_DISABLE_HYBRID_MODULE_FILTER.
	DisableHybridModuleFilter bool `yaml:"disable_hybrid_module_filter" env:"RETRIEVAL_DISABLE_HYBRID_MODULE_FILTER"`
	// DependencyMaxDepth is how many edge hops the dependency-graph walk expands per gap
	// (0 = built-in 2). Env: RETRIEVAL_DEPENDENCY_MAX_DEPTH.
	DependencyMaxDepth int `yaml:"dependency_max_depth" env:"RETRIEVAL_DEPENDENCY_MAX_DEPTH"`
	// Fusion selects how retrieval candidate lists are combined: "dense" (default, previous
	// behaviour) or "rrf" — dense per-chunk_type lists plus a lexical (tsvector) list, fused by
	// reciprocal rank. RRF also removes the invalid comparison of raw cosines across chunk_type
	// sub-corpora. Requires `asqs-core migrate` for the content_tsv index. Upstream measured rrf
	// as a REGRESSION on its golden suite (nDCG@10 0.4792 → 0.2736); leave it on dense unless an
	// ab-report comparison on your corpus says otherwise. Env: RETRIEVAL_FUSION.
	Fusion string `yaml:"fusion" env:"RETRIEVAL_FUSION"`
	// MaxContextTokens caps the assembled generation prompt in TOKENS (0 = derive from the model's
	// context window, or unbounded when the model is unknown). Sections are allocated shares of
	// this budget and unspent allowance flows to later sections; the target symbol and the output
	// contract are never truncated. Env: RETRIEVAL_MAX_CONTEXT_TOKENS.
	MaxContextTokens int `yaml:"max_context_tokens" env:"RETRIEVAL_MAX_CONTEXT_TOKENS"`
	// ProfileBudgets per RetrievalProfile (keys may be aliases; normalized to canonical names at load). Non-zero fields override globals for that profile only.
	ProfileBudgets map[string]RetrievalProfileBudget `yaml:"profile_budgets"`
	// AbstentionDisabled when true: turns off retrieval sufficiency checks (no abstention). Default false. Env: RETRIEVAL_ABSTENTION_DISABLED.
	AbstentionDisabled bool `yaml:"abstention_disabled" env:"RETRIEVAL_ABSTENTION_DISABLED"`
	// MinSimilarTestsForGeneration: when abstention_disabled is false, 0 applies retrieval.DefaultAbstentionMinSimilarTests (0 = no count floor for greenfield); -1 disables only the count axis; ≥1 sets an explicit minimum. Ignored when abstention_disabled. Env: RETRIEVAL_MIN_SIMILAR_TESTS_FOR_GENERATION.
	MinSimilarTestsForGeneration int `yaml:"min_similar_tests_for_generation" env:"RETRIEVAL_MIN_SIMILAR_TESTS_FOR_GENERATION"`
	// MinSimilarityCosine: when abstention_disabled is false, 0 applies retrieval.DefaultAbstentionMinSimilarityCosine; any negative value disables only the cosine axis; >0 sets an explicit threshold (values >1 clamped in retrieval). If the target chunk has no embedding, cosine check is skipped. Ignored when abstention_disabled. Env: RETRIEVAL_MIN_SIMILARITY_COSINE.
	MinSimilarityCosine float64 `yaml:"min_similarity_cosine" env:"RETRIEVAL_MIN_SIMILARITY_COSINE"`
	// ContextCompact optional deterministic compaction before BuildLLMContext (orchestrator Phase 3 pre-pass).
	ContextCompact ContextCompactConfig `yaml:"context_compact"`
}

// EffectiveProjectIntel returns runner.project_intel with built-in defaults applied.
func (c *Config) EffectiveProjectIntel() ProjectIntelConfig {
	return c.Runner.ProjectIntel
}

// AuditConfig configures where audit entries are persisted (structured JSONL file).
type AuditConfig struct {
	// FilePath is an optional file path to append audit entries (one JSON line per step). Empty =
	// no audit file (stderr step lines only). The --audit-log flag overrides it for one run.
	FilePath string `yaml:"file_path" env:"AUDIT_FILE_PATH"`
	// DumpPrompts restores full prompt/completion text in audit payloads. Off by default: prompt
	// bodies carry repository source code, extracted configuration and compiler/test output, so
	// the sink stores {sha256, len} for such fields instead. Enable for post-mortems.
	// Env: ASQS_AUDIT_DUMP_PROMPTS.
	DumpPrompts bool `yaml:"dump_prompts" env:"AUDIT_DUMP_PROMPTS"`
}

// DependencyDocsConfig configures local-only ingestion of documentation for the repository's DIRECT
// dependencies — Maven sources-jars, NuGet XML documentation, node_modules .d.ts — so the model can
// retrieve third-party signatures without a network call.
//
// No network and no subprocess: it reads artifacts that are already on disk. Gradle is NOT
// supported; a Gradle project ingests nothing rather than half-working.
//
// Dependency chunks are stored under their own chunk type with its own retrieval budget, so a large
// dependency corpus cannot crowd repository chunks out of a result — that is a structural guard, not
// a tuned weight.
type DependencyDocsConfig struct {
	// Enabled turns dependency doc ingestion on. Default false: with it off, chunk counts are
	// identical to before this existed. Env: INDEXER_DEPENDENCY_DOCS_ENABLED.
	Enabled bool `yaml:"enabled" env:"INDEXER_DEPENDENCY_DOCS_ENABLED"`
	// MavenRepoDir overrides the local Maven repository root. Empty = ~/.m2/repository.
	// Env: INDEXER_DEPENDENCY_DOCS_MAVEN_REPO_DIR.
	MavenRepoDir string `yaml:"maven_repo_dir" env:"INDEXER_DEPENDENCY_DOCS_MAVEN_REPO_DIR"`
	// NuGetPackagesDir overrides the NuGet global packages folder. Empty = NUGET_PACKAGES or
	// ~/.nuget/packages. Env: INDEXER_DEPENDENCY_DOCS_NUGET_PACKAGES_DIR.
	NuGetPackagesDir string `yaml:"nuget_packages_dir" env:"INDEXER_DEPENDENCY_DOCS_NUGET_PACKAGES_DIR"`
}

// IndexerConfig selects which language indexer to use and bounds what it scans.
//
// It used to carry schedule, run_on_first_start and repo_path as well. Those three described a
// long-running serve mode that decided WHEN to index and WHICH checkout to index — a mode the open
// core does not have. The CLI is invoked once, against the repository named on its command line, and
// a cron expression in a file it reads had no scheduler to reach (CP36).
type IndexerConfig struct {
	// DependencyDocs configures local-only ingestion of documentation for DIRECT dependencies.
	DependencyDocs DependencyDocsConfig `yaml:"dependency_docs"`

	// Type selects the Java indexer: "minimal" (default, line-based Go) or "advanced" (JavaParser JAR, AST + symbol resolution).
	Type string `yaml:"type" env:"INDEXER_TYPE"`

	// Execution selects how language indexers run when their backend is external: "local" (default) uses host java / host node; "docker" runs the advanced Java JAR or the JS/TS indexer in ephemeral docker run --rm containers (no JVM/Node on host).
	// For Java, only applies when indexer.type is advanced. For JS/TS, applies when jst_indexer_path is used for the run.
	Execution string `yaml:"execution" env:"INDEXER_EXECUTION"`

	// DockerCLI overrides the docker binary (default "docker"). Used when execution is docker.
	DockerCLI string `yaml:"docker_cli" env:"INDEXER_DOCKER_CLI"`

	// DockerJavaImage is the image for the advanced Java indexer container (e.g. eclipse-temurin:21-jre-jammy). Empty with execution: docker uses a built-in default.
	DockerJavaImage string `yaml:"docker_java_image" env:"INDEXER_DOCKER_JAVA_IMAGE"`

	// DockerMemory caps indexer container memory (e.g. "4g"). Empty = no --memory flag.
	DockerMemory string `yaml:"docker_memory" env:"INDEXER_DOCKER_MEMORY"`

	// DockerNodeImage is the image for the JS/TS Node indexer container (e.g. node:20-bookworm). Empty with execution: docker uses a built-in default.
	DockerNodeImage string `yaml:"docker_node_image" env:"INDEXER_DOCKER_NODE_IMAGE"`

	// DockerNodeHeapMB sets NODE_OPTIONS --max-old-space-size in the JS/TS indexer container. 0 = default (4096).
	DockerNodeHeapMB int `yaml:"docker_node_heap_mb" env:"INDEXER_DOCKER_NODE_HEAP_MB"`

	// AdvancedJarPath is the path to the java-indexer JAR when type is "advanced" (e.g. tools/java-indexer/target/java-indexer-0.1.0.jar).
	AdvancedJarPath string `yaml:"advanced_jar_path" env:"INDEXER_ADVANCED_JAR_PATH"`

	// JSTIndexerPath is the path to the JS/TS indexer entry (e.g. tools/js-ts-indexer/dist/index.js). When set, the run/reindex commands use the Node indexer for repos that contain JS/TS files; build with cd tools/js-ts-indexer && npm run build.
	JSTIndexerPath string `yaml:"jst_indexer_path" env:"INDEXER_JST_INDEXER_PATH"`

	// CSharpIndexerDllPath is the absolute or cwd-relative path to published CSharpIndexer.dll (dotnet publish tools/csharp-indexer). When set and the repo has .cs files (and JS/TS does not win), the Roslyn indexer runs and merges with the Java advanced map when both apply.
	CSharpIndexerDllPath string `yaml:"csharp_indexer_dll_path" env:"INDEXER_CSHARP_DLL_PATH"`

	// DockerDotNetIndexerImage is the SDK image for the C# indexer container when indexer.execution is docker (default mcr.microsoft.com/dotnet/sdk:10.0).
	DockerDotNetIndexerImage string `yaml:"docker_dotnet_indexer_image" env:"INDEXER_DOCKER_DOTNET_IMAGE"`

	// CriticalModulePrefixes mark business-critical path/module substrings (payment, auth, …) for test-gap priority.
	CriticalModulePrefixes []string `yaml:"critical_module_prefixes"`

	// MaxGaps is the maximum number of test-gap candidates to plan per run (0 = use default 10 in retrieval).
	MaxGaps int `yaml:"max_gaps" env:"INDEXER_MAX_GAPS"`

	// MaxGapsPerFile caps how many gaps are selected per source file (0 = use retrieval default 2). Use 0 and set in plan options to disable per-file capping.
	MaxGapsPerFile int `yaml:"max_gaps_per_file" env:"INDEXER_MAX_GAPS_PER_FILE"`

	// MaxGapsE2E caps E2E-oriented plan candidates per run (0 = disable that plan branch and JS E2E eval pass wiring).
	// Java: uncovered API_ROUTE (HTTP entrypoints not targeted from test-scoped API clients) first; else E2E_SPEC / PAGE_OBJECT / USER_FLOW in test files. JS/TS: E2E_SPEC in test files.
	MaxGapsE2E int `yaml:"max_gaps_e2e" env:"INDEXER_MAX_GAPS_E2E"`
	// MaxGapsPerFileE2E caps E2E gaps per file (0 = use retrieval default 2 for E2E).
	MaxGapsPerFileE2E int `yaml:"max_gaps_per_file_e2e" env:"INDEXER_MAX_GAPS_PER_FILE_E2E"`

	// OverviewDocPath is the repo-relative path for the generated overview/workflows document (e.g. docs/documentation.md).
	// Empty = use default "docs/documentation.md". With mono_repo_workspace, set this to e.g. "<workspace>/docs/documentation.md" to keep the overview under the scoped project.
	OverviewDocPath string `yaml:"overview_doc_path" env:"INDEXER_OVERVIEW_DOC_PATH"`
	// DisableOverviewDocGeneration skips overview/workflows document generation in orchestrator phase 3 even when an LLM generator is configured.
	DisableOverviewDocGeneration bool `yaml:"disable_overview_doc_generation" env:"INDEXER_DISABLE_OVERVIEW_DOC_GENERATION"`

	// OverviewFullRewrite when true, regenerates the entire overview narrative from the LLM on each run (replaces prior prose).
	// When false (default), an existing file is kept: only a short dated "Overview updates" section is appended when the index snapshot has material new facts; the Mermaid appendix is always refreshed from the current index.
	OverviewFullRewrite bool `yaml:"overview_full_rewrite" env:"INDEXER_OVERVIEW_FULL_REWRITE"`

	// OverviewMaxFilesPerSlice caps how many repo-relative source file paths go into one index snapshot for one overview LLM call (Plan B batching). Large monorepos are split across multiple calls. 0 = default 400. Env: INDEXER_OVERVIEW_MAX_FILES_PER_SLICE.
	OverviewMaxFilesPerSlice int `yaml:"overview_max_files_per_slice" env:"INDEXER_OVERVIEW_MAX_FILES_PER_SLICE"`
	// OverviewMaxIndexRunesPerSlice caps UTF-8 runes in the built index snapshot for one overview LLM request (after file batching). Dense symbol lists can be large with few files; 0 = default 120000; -1 disables extra split/clamp. Env: INDEXER_OVERVIEW_MAX_INDEX_RUNES_PER_SLICE.
	OverviewMaxIndexRunesPerSlice int `yaml:"overview_max_index_runes_per_slice" env:"INDEXER_OVERVIEW_MAX_INDEX_RUNES_PER_SLICE"`

	// SkipPathPrefixes are repo-relative path prefixes to skip when scanning/indexing (e.g. "app/lib" to skip app/lib in AngularJS).
	// Applied by the Go file scanner and by the JS/TS indexer. Paths use forward slashes; a prefix skips that folder and everything under it.
	SkipPathPrefixes []string `yaml:"skip_path_prefixes"`

	// MonoRepoWorkspace is an optional repo-relative subdirectory (forward slashes, no ".." segments) that scopes scanning, language indexers, planning, and gap filtering to that folder (entire git tree is still the mount root for Docker). Generated tests and eval cwd use mono_repo_test_workspace when set; otherwise they use this workspace too.
	// The folder should contain a project root marker: pom.xml, package.json, build.gradle(.kts), or a .sln / .slnx / .csproj at that directory level. Empty = use the whole repository. Env: INDEXER_MONO_REPO_WORKSPACE.
	MonoRepoWorkspace string `yaml:"mono_repo_workspace" env:"INDEXER_MONO_REPO_WORKSPACE"`
	// MonoRepoTestWorkspace is an optional repo-relative directory (same path rules as mono_repo_workspace) where unit/E2E test framework bootstrap, generated test file paths, and evaluation working directory are rooted, while indexing, planning, gap filtering, and documentation/overview generation stay scoped to mono_repo_workspace.
	// Requires mono_repo_workspace when set. The directory should contain a project root marker at that level. Empty = use mono_repo_workspace for tests and eval (legacy behavior). Env: INDEXER_MONO_REPO_TEST_WORKSPACE.
	MonoRepoTestWorkspace string `yaml:"mono_repo_test_workspace" env:"INDEXER_MONO_REPO_TEST_WORKSPACE"`
}

// DatabaseConfig configures Postgres for metadata and embeddings stores.
type DatabaseConfig struct {
	// MetadataURL is the connection string for the metadata DB (symbols, edges, files).
	// Example: postgres://user:pass@localhost:5432/asqs?sslmode=disable
	MetadataURL string `yaml:"metadata_url" env:"DATABASE_METADATA_URL"`

	// EmbeddingsURL is the connection string for the embeddings/chunks store (pgvector).
	// If empty, MetadataURL is used (same database, different tables).
	EmbeddingsURL string `yaml:"embeddings_url" env:"DATABASE_EMBEDDINGS_URL"`

	// EmbeddingsDimension is the vector dimension (e.g. 1536 for OpenAI). 0 = default 1536.
	EmbeddingsDimension int `yaml:"embeddings_dimension" env:"DATABASE_EMBEDDINGS_DIMENSION"`

	// MaxOpenConns caps each connection pool (metadata and embeddings size from it separately,
	// so two pools per process can hold up to twice this). 0 leaves pgxpool's default of
	// max(4, NumCPU).
	MaxOpenConns int `yaml:"max_open_conns" env:"DATABASE_MAX_OPEN_CONNS"`
}

// VCSConfig configures the version control system. Set provider to github, gitlab, bitbucket, or azure_devops.
// Each provider has its own credentials block; only the block for the active provider is used at runtime.
type VCSConfig struct {
	// Provider selects which platform API + webhook implementation is active: github (default), gitlab, bitbucket, azure_devops.
	Provider string `yaml:"provider" env:"VCS_PROVIDER"`

	GitHub      GitHubConfig         `yaml:"github"`
	GitLab      GitLabVCSConfig      `yaml:"gitlab"`
	Bitbucket   BitbucketVCSConfig   `yaml:"bitbucket"`
	AzureDevOps AzureDevOpsVCSConfig `yaml:"azure_devops"`
}

// GitHubConfig configures GitHub API and git operations.
type GitHubConfig struct {
	// Token is the GitHub personal access token (or app token) for API and git clone/push.
	Token string `yaml:"token" env:"GITHUB_TOKEN"`

	// BaseURL is the API base URL; set for GitHub Enterprise (e.g. https://github.company.com/api/v3).
	BaseURL string `yaml:"base_url" env:"GITHUB_BASE_URL"`

	// DefaultOwner and DefaultRepo are optional defaults for CreatePullRequest when not specified per run.
	DefaultOwner string `yaml:"default_owner" env:"GITHUB_DEFAULT_OWNER"`
	DefaultRepo  string `yaml:"default_repo" env:"GITHUB_DEFAULT_REPO"`

	// Ship: after a stable run with generated artifacts, commit and push to a stable branch and create/update a single PR (avoids duplicate PRs on recurring runs).
	Ship ShipConfig `yaml:"ship"`
}

// ShipConfig configures "ship after stable" for the run command: one branch per repo, update PR if it exists.
type ShipConfig struct {
	// Enabled: when true, after a stable evaluation with generated artifacts, commit, push, and create a PR (only if none exists for the branch).
	Enabled bool `yaml:"enabled" env:"VCS_SHIP_ENABLED"`
	// Branch is the stable branch name (e.g. "quality-bot"). Same name every run so the PR is updated instead of creating a new one.
	Branch string `yaml:"branch" env:"VCS_SHIP_BRANCH"`
	// BaseBranch is the target branch for the PR (e.g. "main").
	BaseBranch string `yaml:"base_branch" env:"VCS_SHIP_BASE_BRANCH"`
	// DraftPR creates the PR as draft when true.
	DraftPR bool `yaml:"draft_pr" env:"VCS_SHIP_DRAFT_PR"`
}

// WebSearchConfig configures the external documentation tools.
//
// **This is the only feature in asqs-core that sends data out of the process.** It is off by
// default and registers no tools at all when disabled — not a disabled tool the model can see, but
// no tool. Two independent brakes exist on top of that: an allow-list that gates page fetches and
// fails CLOSED when empty, and an offline switch that makes egress structurally impossible by
// serving replies only from the on-disk replay cache.
//
// **The cache lives inside the repository under test** (`.asqs/websearch-cache.json`), because the
// queries belong to that repository rather than to the tool — the same ownership argument the
// project-intel cache follows. Two consequences worth knowing before enabling this: a run against a
// local folder leaves the cache in that folder, and a shipped run can COMMIT the cache, which means
// **search queries and results can reach the repository's pull request**. Set an absolute
// `cache_path` outside the repository if that is not wanted.
type WebSearchConfig struct {
	// Enabled turns the two tools on. False registers neither. Env: WEBSEARCH_ENABLED.
	Enabled bool `yaml:"enabled" env:"WEBSEARCH_ENABLED"`
	// Offline serves ONLY from the replay cache and never egresses: a cache miss is an answer
	// ("offline, not cached"), not a network call. This is the switch that makes egress
	// structurally impossible rather than merely unconfigured. Env: WEBSEARCH_OFFLINE.
	Offline bool `yaml:"offline" env:"WEBSEARCH_OFFLINE"`
	// Provider selects the backend: "brave" (hosted, needs a key, nothing to run) or "searxng"
	// (self-hosted; keeps queries inside your own boundary). Env: WEBSEARCH_PROVIDER.
	Provider string `yaml:"provider" env:"WEBSEARCH_PROVIDER"`
	// Endpoint is the SearXNG base URL, e.g. http://searxng:8080 inside a cluster.
	// Env: WEBSEARCH_ENDPOINT.
	Endpoint string `yaml:"endpoint" env:"WEBSEARCH_ENDPOINT"`
	// APIKey authenticates hosted providers. Prefer APIKeyFromEnv. Env: WEBSEARCH_API_KEY.
	APIKey string `yaml:"api_key" env:"WEBSEARCH_API_KEY"`
	// APIKeyFromEnv names an environment variable holding the key (overrides APIKey when set).
	APIKeyFromEnv string `yaml:"api_key_from_env" env:"WEBSEARCH_API_KEY_FROM_ENV"`
	// AllowedHosts gates web_fetch: exact hosts or "*.example.org" wildcards. **EMPTY DISABLES
	// FETCH** — search still works, pages cannot be read. Empty fails closed, never open.
	AllowedHosts []string `yaml:"allowed_hosts"`
}

// WebSearchCacheFileName is the cache document's name when only a directory is known.
const WebSearchCacheFileName = "websearch-cache.json"

// EffectiveCachePath resolves the replay cache file, repo-relative unless explicitly absolute.
// EffectiveCachePath is FROZEN (CP37) at the repo-relative location inside the clone.
//
// This took a capability with it, deliberately: an ABSOLUTE cache_path used to escape the per-run
// workspace and give cross-run reuse without committing anything. Nothing shipped used it, and the
// supported route to cross-run reuse is the one project-intel already takes — the file lives in the
// repository under test and is preserved through ship, so the next clone brings it along. An
// operator-settable absolute path also pointed the one component that egresses at a location outside
// the sandbox, which is not a knob worth keeping for a capability nothing used.
func (c WebSearchConfig) EffectiveCachePath() string { return ".asqs/" + WebSearchCacheFileName }

// LLMConfig configures the LLM/embedding API used for generation and RAG.
type LLMConfig struct {
	// DisableEmbeddingCache turns off the content-addressed embedding memo. Default false (cache
	// on): a cache miss is exactly the previous behaviour, so enabling costs nothing and re-runs
	// over unchanged content stop paying the provider. Env: LLM_DISABLE_EMBEDDING_CACHE.
	DisableEmbeddingCache bool `yaml:"disable_embedding_cache" env:"LLM_DISABLE_EMBEDDING_CACHE"`
	// EmbeddingCacheRetentionDays prunes cache rows unused for this long. 0 = 30 days.
	// Env: LLM_EMBEDDING_CACHE_RETENTION_DAYS.
	EmbeddingCacheRetentionDays int `yaml:"embedding_cache_retention_days" env:"LLM_EMBEDDING_CACHE_RETENTION_DAYS"`
	// Provider is the provider name: "openai", "anthropic", "azure_openai", etc.
	Provider string `yaml:"provider" env:"LLM_PROVIDER"`

	// APIKey is the API key for the provider (or use APIKeyFromEnv to read from a named env var).
	APIKey string `yaml:"api_key" env:"LLM_API_KEY"`

	// APIKeyFromEnv names an environment variable to read the API key from (overrides APIKey if set).
	APIKeyFromEnv string `yaml:"api_key_from_env" env:"LLM_API_KEY_FROM_ENV"`

	// Model is the default model ID for chat/completions (e.g. gpt-4o, claude-3-5-sonnet). Used for all steps when step-specific model is not set.
	Model string `yaml:"model" env:"LLM_MODEL"`
	// Per-step overrides: provider and optionally model/API key. Empty provider = use Provider above. Example: openai for generation, anthropic for docs and fixer.
	DocProvider             string `yaml:"doc_provider" env:"LLM_DOC_PROVIDER"`
	DocModel                string `yaml:"doc_model" env:"LLM_DOC_MODEL"`
	DocAPIKey               string `yaml:"doc_api_key" env:"LLM_DOC_API_KEY"`
	DocAPIKeyFromEnv        string `yaml:"doc_api_key_from_env" env:"LLM_DOC_API_KEY_FROM_ENV"`
	GenerationProvider      string `yaml:"generation_provider" env:"LLM_GENERATION_PROVIDER"`
	GenerationModel         string `yaml:"generation_model" env:"LLM_GENERATION_MODEL"`
	GenerationAPIKey        string `yaml:"generation_api_key" env:"LLM_GENERATION_API_KEY"`
	GenerationAPIKeyFromEnv string `yaml:"generation_api_key_from_env" env:"LLM_GENERATION_API_KEY_FROM_ENV"`
	FixerProvider           string `yaml:"fixer_provider" env:"LLM_FIXER_PROVIDER"`
	FixerModel              string `yaml:"fixer_model" env:"LLM_FIXER_MODEL"`
	FixerAPIKey             string `yaml:"fixer_api_key" env:"LLM_FIXER_API_KEY"`
	FixerAPIKeyFromEnv      string `yaml:"fixer_api_key_from_env" env:"LLM_FIXER_API_KEY_FROM_ENV"`

	// EmbeddingProvider is the provider used for embeddings when different from Provider (e.g. openai for embeddings while Provider is anthropic for chat). Empty = use Provider. Anthropic does not offer an embeddings API; use openai or azure_openai here when using Anthropic for chat.
	EmbeddingProvider string `yaml:"embedding_provider" env:"LLM_EMBEDDING_PROVIDER"`
	// EmbeddingAPIKey and EmbeddingAPIKeyFromEnv are used when EmbeddingProvider is set (e.g. OpenAI key for embeddings while main API key is Anthropic). If both empty, main APIKey/APIKeyFromEnv are used.
	EmbeddingAPIKey        string `yaml:"embedding_api_key" env:"LLM_EMBEDDING_API_KEY"`
	EmbeddingAPIKeyFromEnv string `yaml:"embedding_api_key_from_env" env:"LLM_EMBEDDING_API_KEY_FROM_ENV"`

	// EmbeddingModel is the model for embeddings (e.g. text-embedding-3-small). If empty, Model may be used.
	EmbeddingModel string `yaml:"embedding_model" env:"LLM_EMBEDDING_MODEL"`

	// EmbeddingFallback enables a fallback embedding model served via Ollama (native
	// /api/embed) when the configured embedding provider cannot produce embeddings —
	// e.g. provider=anthropic, or provider=ollama with a code model (codestral) and no
	// embedding_model. "" = DISABLED (the provider error surfaces as before). "auto"/
	// "default" = nomic-embed-text (768-dim). Any other value = an explicit Ollama
	// embedding model name. When enabled, set database.embeddings_dimension to match the
	// fallback model (nomic-embed-text=768) or chunk inserts fail; a startup warning is
	// printed on mismatch. Env: LLM_EMBEDDING_FALLBACK.
	EmbeddingFallback string `yaml:"embedding_fallback" env:"LLM_EMBEDDING_FALLBACK"`

	// BaseURL is the API base URL; set for Azure or other proxies.
	BaseURL string `yaml:"base_url" env:"LLM_BASE_URL"`

	// HTTPTimeout is the OpenAI (and OpenAI-compatible) HTTP client total timeout per request (Go http.Client.Timeout). Empty = 5m. Increase on slow models or flaky networks. Env: LLM_HTTP_TIMEOUT.
	HTTPTimeout string `yaml:"http_timeout" env:"LLM_HTTP_TIMEOUT"`
	// HTTPDisableKeepAlives when true sets Transport.DisableKeepAlives so each request uses a new TCP connection (reduces stale pooled-connection EOFs behind proxies). Env: LLM_HTTP_DISABLE_KEEP_ALIVES.
	HTTPDisableKeepAlives bool `yaml:"http_disable_keep_alives" env:"LLM_HTTP_DISABLE_KEEP_ALIVES"`
	// HTTPResponseHeaderTimeout is the max time to wait for response headers on HTTP calls (Transport). Empty = 2m for cloud APIs; Ollama chat/embed clients disable this default when unset so slow non-streaming /api/chat is not cut off at ~2m (use llm.http_timeout for the overall deadline). Env: LLM_HTTP_RESPONSE_HEADER_TIMEOUT.
	HTTPResponseHeaderTimeout string `yaml:"http_response_header_timeout" env:"LLM_HTTP_RESPONSE_HEADER_TIMEOUT"`

	// OllamaNumCtx is sent as POST /api/chat JSON options.num_ctx when > 0 (larger context window; subject to VRAM). 0 = omit options.num_ctx. Env: LLM_OLLAMA_NUM_CTX.
	OllamaNumCtx int `yaml:"ollama_num_ctx" env:"LLM_OLLAMA_NUM_CTX"`

	// MaxConcurrent is the max concurrent requests to the API. 0 = default (e.g. 5).
	MaxConcurrent int `yaml:"max_concurrent" env:"LLM_MAX_CONCURRENT"`
}

// GenerationConfig bounds the tool-calling loop during test generation.
//
// The defaults are deliberately small. Every extra turn is a round trip paid per gap, so an
// unbounded loop is a cost and latency hazard rather than merely a slow one — and against a local
// Ollama, the single-process case this project targets, it is also a queue.
type GenerationConfig struct {
	// ToolsEnabled turns the loop on. False is the previous one-shot behaviour, byte-identical on
	// the wire. Env: GENERATION_TOOLS_ENABLED.
	ToolsEnabled bool `yaml:"tools_enabled" env:"GENERATION_TOOLS_ENABLED"`
	// PromptedToolsEnabled allows the prompted-JSON fallback on models without native tool calling
	// (see tools.ResolveMode). Env: GENERATION_PROMPTED_TOOLS_ENABLED.
	PromptedToolsEnabled bool `yaml:"prompted_tools_enabled" env:"GENERATION_PROMPTED_TOOLS_ENABLED"`
	// MaxToolTurns caps model→tool→model round trips per gap. 0 = tools.DefaultMaxToolTurns (4).
	// Env: GENERATION_MAX_TOOL_TURNS.
	MaxToolTurns int `yaml:"max_tool_turns" env:"GENERATION_MAX_TOOL_TURNS"`
	// MaxToolCallsPerTurn caps parallel calls in one turn. 0 = tools.DefaultMaxToolCallsPerTurn (3).
	// Env: GENERATION_MAX_TOOL_CALLS_PER_TURN.
	MaxToolCallsPerTurn int `yaml:"max_tool_calls_per_turn" env:"GENERATION_MAX_TOOL_CALLS_PER_TURN"`
	// MaxToolCallsPerRun caps total calls for one gap, so a pathological gap cannot starve the
	// shared LLM limiter. 0 = tools.DefaultMaxToolCallsPerRun (12).
	// Env: GENERATION_MAX_TOOL_CALLS_PER_RUN.
	MaxToolCallsPerRun int `yaml:"max_tool_calls_per_run" env:"GENERATION_MAX_TOOL_CALLS_PER_RUN"`
	// MaxToolResultChars is the total characters tool results may add to the conversation across
	// the whole loop, drawn from the same allowance as the prompt. Beyond it results are truncated
	// and the loop stops requesting tools. 0 = tools.DefaultMaxToolResultChars (24000).
	// Env: GENERATION_MAX_TOOL_RESULT_CHARS.
	MaxToolResultChars int `yaml:"max_tool_result_chars" env:"GENERATION_MAX_TOOL_RESULT_CHARS"`

	// FixerToolsEnabled gives the FIXER the same read-only tool suite during compile/test repair.
	// Independent of tools_enabled deliberately: a fix-quality A/B needs to toggle fixer tools
	// while generation stays fixed, and vice versa. False is the previous one-shot fixer,
	// byte-identical on the wire. Env: GENERATION_FIXER_TOOLS_ENABLED.
	FixerToolsEnabled bool `yaml:"fixer_tools_enabled" env:"GENERATION_FIXER_TOOLS_ENABLED"`
	// FixerMaxToolTurns caps the fixer's model→tool→model round trips. 0 =
	// DefaultFixerMaxToolTurns (3) — smaller than generation's 4 because a fix attempt starts from
	// a narrower question (one error log against one artifact) and runs inside the evaluator's
	// iteration budget, so each extra turn multiplies across fix attempts, not just gaps.
	// Env: GENERATION_FIXER_MAX_TOOL_TURNS.
	FixerMaxToolTurns int `yaml:"fixer_max_tool_turns" env:"GENERATION_FIXER_MAX_TOOL_TURNS"`
}

// RunnerConfig configures the test/CI runner (e.g. Docker).
type RunnerConfig struct {
	// Type is the runner type: "docker" (default), "local", "kubernetes" (future).
	Type string `yaml:"type" env:"RUNNER_TYPE"`

	// Timeout is the max duration for a single test run (e.g. 5m).
	Timeout string `yaml:"timeout" env:"RUNNER_TIMEOUT"`

	// ImageJava is the Docker image for Java (mvn/gradle) runs.
	ImageJava string `yaml:"image_java" env:"RUNNER_IMAGE_JAVA"`
	// ImageJavaMaven overrides the Maven docker eval image (defaults: JDK21 maven:3.9-eclipse-temurin-21 for java-maven/java-maven-21; JDK11 maven:3.9-eclipse-temurin-11 for java-maven-11).
	ImageJavaMaven string `yaml:"image_java_maven" env:"RUNNER_IMAGE_JAVA_MAVEN"`
	// ImageJavaGradle overrides the Gradle docker eval image (defaults: JDK21 gradle:8.11-jdk21; JDK11 gradle:8.11-jdk11 for java-gradle-11).
	ImageJavaGradle string `yaml:"image_java_gradle" env:"RUNNER_IMAGE_JAVA_GRADLE"`
	// ImageNode is the Docker image for JS/TS docker eval (default: node:20-bookworm).
	ImageNode string `yaml:"image_node" env:"RUNNER_IMAGE_NODE"`
	// ImagePlaywright is the Docker image for JS/TS e2e_framework_bootstrap when runner uses ephemeral Docker (default: mcr.microsoft.com/playwright matching @playwright/test pin).
	ImagePlaywright string `yaml:"image_playwright" env:"RUNNER_IMAGE_PLAYWRIGHT"`
	// ImagePlaywrightJava is the Docker image for Java e2e_framework_bootstrap in ephemeral Docker and for the Java E2E eval pass (test_e2e) when E2EFramework is playwright-java — same default as bootstrap (mcr.microsoft.com/playwright/java; browsers + OS libs; plain maven:/gradle: images lack Chromium).
	ImagePlaywrightJava string `yaml:"image_playwright_java" env:"RUNNER_IMAGE_PLAYWRIGHT_JAVA"`
	// ImagePlaywrightDotnet is the Docker image for C# e2e_framework_bootstrap in ephemeral Docker and for the Docker E2E eval pass when E2EFramework is playwright-dotnet (default mcr.microsoft.com/playwright/dotnet; browsers + .NET SDK; plain sdk images lack matching browser bundles for Microsoft.Playwright).
	ImagePlaywrightDotnet string `yaml:"image_playwright_dotnet" env:"RUNNER_IMAGE_PLAYWRIGHT_DOTNET"`
	// ImageDotNet is the Docker image for .NET runs. Empty = mcr.microsoft.com/dotnet/sdk:10.0, or sdk:{major}.0 inferred from net{major}.* in repo-root csproj files.
	ImageDotNet string `yaml:"image_dotnet" env:"RUNNER_IMAGE_DOTNET"`
	// DotNetFallbackTargetFramework when set (e.g. net8.0): for dotnet restore/build/test/format argv, append /p:TargetFramework=<value> when the entry .csproj does not declare a non-empty concrete TargetFramework/TargetFrameworks (no file edits). Empty = disabled.
	DotNetFallbackTargetFramework string `yaml:"dotnet_fallback_target_framework" env:"RUNNER_DOTNET_FALLBACK_TARGET_FRAMEWORK"`

	// EvalProfile selects the docker eval toolchain: java-maven, java-maven-11, java-maven-21, java-gradle, java-gradle-11, java-gradle-21, typescript-*, nodejs-lts, csharp-dotnet, or empty/auto.
	// When the evaluation workflow language is csharp/cs, the .NET SDK profile is used regardless of a java-* eval_profile (shared configs often leave java-maven set).
	EvalProfile string `yaml:"eval_profile" env:"RUNNER_EVAL_PROFILE"`
	// DockerBinary path to docker CLI (default: docker).
	DockerBinary string `yaml:"docker_binary" env:"RUNNER_DOCKER_BINARY"`
	// JobMemory Docker memory limit (e.g. 4g).
	JobMemory string `yaml:"job_memory" env:"RUNNER_JOB_MEMORY"`
	// JobCPUs max CPUs for eval container (e.g. 2).
	JobCPUs float64 `yaml:"job_cpus" env:"RUNNER_JOB_CPUS"`
	// JobPidsLimit caps processes in the eval container (0 = default).
	JobPidsLimit int64 `yaml:"job_pids_limit" env:"RUNNER_JOB_PIDS_LIMIT"`
	// JobNetworkRestore is Docker network for dependency-restore phase (default bridge).
	JobNetworkRestore string `yaml:"job_network_restore" env:"RUNNER_JOB_NETWORK_RESTORE"`
	// JobNetworkTest is network for compile/test when offline test is on (default none).
	JobNetworkTest string `yaml:"job_network_test" env:"RUNNER_JOB_NETWORK_TEST"`
	// DockerDisableOfflineTest when true, compile/test use the same network as restore (bridge). Default false = test phase uses isolated network (none) after restore.
	DockerDisableOfflineTest bool `yaml:"docker_disable_offline_test" env:"RUNNER_DOCKER_DISABLE_OFFLINE_TEST"`
	// JobReadonlyRootfs uses read-only root + tmpfs /tmp in eval containers (default false).
	JobReadonlyRootfs bool `yaml:"job_readonly_rootfs" env:"RUNNER_JOB_READONLY_ROOTFS"`
	// CacheMavenHost bind-mount for Maven repo cache (empty = no mount).
	CacheMavenHost string `yaml:"cache_maven_host" env:"RUNNER_CACHE_MAVEN_HOST"`
	// CacheGradleHost for Gradle user home .gradle.
	CacheGradleHost string `yaml:"cache_gradle_host" env:"RUNNER_CACHE_GRADLE_HOST"`
	// CacheNpmHost for npm cache.
	CacheNpmHost string `yaml:"cache_npm_host" env:"RUNNER_CACHE_NPM_HOST"`
	// CachePnpmHost for pnpm store.
	CachePnpmHost string `yaml:"cache_pnpm_host" env:"RUNNER_CACHE_PNPM_HOST"`
	// CacheNuGetHost for NuGet packages.
	CacheNuGetHost string `yaml:"cache_nuget_host" env:"RUNNER_CACHE_NUGET_HOST"`
	// AzureDevOpsNuGetFeedEndpoints: full Azure Artifacts feed index URLs (…/v3/index.json) for private NuGet restores in Docker.
	// Used with vcs.azure_devops.token to set VSS_NUGET_EXTERNAL_FEED_ENDPOINTS on eval and bootstrap containers. Env: comma-separated ASQS_RUNNER_AZURE_DEVOPS_NUGET_FEED_ENDPOINTS.
	AzureDevOpsNuGetFeedEndpoints []string `yaml:"azure_devops_nuget_feed_endpoints"`
	// PrivateRegistryCredentials is the generic, cross-ecosystem credentials list for private package
	// registries used by the build containers (NuGet for C#, Maven for Java, npm/yarn/pnpm for TS/JS).
	// Each entry is the same YAML shape with a `type` discriminator (`nuget` | `maven` | `npm`), and
	// ASQS translates the list into the ecosystem-appropriate auth mechanism:
	//   - **nuget** → merged into the existing `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS` envelope injected
	//     into every docker eval + bootstrap container; the Artifacts Credential Provider inside
	//     `dotnet` honours that envelope for any HTTPS NuGet source.
	//   - **maven** → emitted into a generated `~/.m2/settings.xml` (one `<server>` per entry, with
	//     `id` defaulting to the endpoint hostname when unset); the file is written to a host temp
	//     file at sandbox init (0600 perms) and mounted read-only into every docker eval + bootstrap
	//     container at `/root/.m2/settings.xml`. Maven picks it up automatically — no pom.xml or
	//     command change is required.
	//   - **npm** → emitted into a generated `~/.npmrc` file mounted read-only at `/root/.npmrc`.
	//     Supports both `token` (preferred, rendered as `_authToken`) and `username`/`password`
	//     (rendered as `_auth` basic-auth). `scope` optionally binds the registry to an npm scope
	//     (e.g. `@company` → `@company:registry=<endpoint>`).
	// Precedence: within the same ecosystem, explicit entries override the blanket Azure DevOps PAT
	// path (`vcs.azure_devops.token` + `runner.azure_devops_nuget_feed_endpoints`), so operators can
	// opt individual feeds into a different identity without removing them from the PAT list.
	// Secrets should be injected via env / secret manager rather than committed.
	// YAML shape (per entry): `{type, endpoint, id?, scope?, username?, password?, token?}`.
	PrivateRegistryCredentials []PrivateRegistryCredential `yaml:"private_registry_credentials"`
	// CacheCypressHost bind-mount for Cypress binary cache (~/.cache/Cypress in container as root). Only used for JS/TS docker toolchains (npm/pnpm/yarn). Empty = no mount. Use with Cypress E2E in ephemeral containers so the binary persists across docker run --rm jobs.
	CacheCypressHost string `yaml:"cache_cypress_host" env:"RUNNER_CACHE_CYPRESS_HOST"`

	// RequireDockerBootstrap when true: TS/JS, Java, and C# test_framework_bootstrap / e2e_framework_bootstrap install/verify must run in ephemeral Docker (runner.type: docker and/or *framework_bootstrap.execution: docker). Default false.
	RequireDockerBootstrap bool `yaml:"require_docker_bootstrap" env:"RUNNER_REQUIRE_DOCKER_BOOTSTRAP"`

	// FormatCommand is run in the repo root after writing generated test files, to fix formatting (e.g. so spring-javaformat:validate passes).
	// Example: "mvn spring-javaformat:apply -q" for Maven/Spring; "dotnet format" for C# (also the default when lang is csharp/cs and this is empty). Empty = skip except C# default.
	FormatCommand string `yaml:"format_command" env:"RUNNER_FORMAT_COMMAND"`
	// FormatOnlyAdded when true runs the formatter only on files that were written (generated tests, docs-inserted sources, overview).
	// The command is invoked once per file with the repo-relative path appended (e.g. "google-java-format -i" → "google-java-format -i path/to/File.java"). Use with formatters that accept a single file path. When false, FormatCommand runs once for the whole repo.
	FormatOnlyAdded bool `yaml:"format_only_added" env:"RUNNER_FORMAT_ONLY_ADDED"`

	// BuildTool selects the Java build tool for evaluation (compile/test): "auto" (detect from repo), "mvn", "mvnw", "gradle", "gradlew".
	// When "auto", uses mvnw if pom.xml and mvnw exist, else mvn; uses gradlew if build.gradle and gradlew exist, else gradle.
	BuildTool string `yaml:"build_tool" env:"RUNNER_BUILD_TOOL"`
	// CompileCommand overrides the compile step when set (e.g. "./mvnw compile -q -B"). Run from repo root via sh -c in Docker and local eval. Empty = use BuildTool / toolchain defaults.
	CompileCommand string `yaml:"compile_command" env:"RUNNER_COMPILE_COMMAND"`
	// TestCommand overrides the test step when set; also used for Docker coverage when set (before profile coverage argv). Same sh -c execution as compile_command.
	TestCommand string `yaml:"test_command" env:"RUNNER_TEST_COMMAND"`
	// UnitTestCommand overrides the test step for the unit-test evaluation pass only (empty = use TestCommand for unit pass).
	UnitTestCommand string `yaml:"unit_test_command" env:"RUNNER_UNIT_TEST_COMMAND"`
	// E2ETestCommand overrides the test step for the E2E evaluation pass. When empty, JS/TS resolves from detected E2E framework: playwright → npx playwright test, cypress → npx cypress run; otherwise npm run test:e2e.
	E2ETestCommand string `yaml:"e2e_test_command" env:"RUNNER_E2E_TEST_COMMAND"`
	// CompileOncePerEval when true: after compile succeeds once in an evaluation run, skip compile on later fix iterations (test/lint/coverage still run). Use when compile is slow and only needed once (e.g. npm ci).
	CompileOncePerEval bool `yaml:"compile_once_per_eval" env:"RUNNER_COMPILE_ONCE_PER_EVAL"`
	// DisableStructuredFixOutput when true, the LLM fixer does not request JSON-schema structured completions (OpenAI response_format json_schema); only prompt + parser + repair. Default false = try structured output first when the provider supports it (OpenAI client implements it; others ignore). Env: RUNNER_DISABLE_STRUCTURED_FIX_OUTPUT.
	DisableStructuredFixOutput bool `yaml:"disable_structured_fix_output" env:"RUNNER_DISABLE_STRUCTURED_FIX_OUTPUT"`
	// FixerDependencySignatureOnly (Phase 3 opt-in) when true, dependency/source files the fixer reads solely as *read-only context* (i.e. not in the writable artifact set, not listed in ArtifactDependencies[artifact], and not cited in the error output) are sliced to signatures only via internal/evaluator/fixslice: class/interface headers, public/protected method signatures, field declarations, and class-level doc-comments are kept while method bodies and private members are dropped. Artifacts, their declared ArtifactDependencies, and error-cited sources are always shipped in full. Default false = every file is shipped with bodies (existing behaviour). Env: RUNNER_FIXER_DEPENDENCY_SIGNATURE_ONLY.
	FixerDependencySignatureOnly bool `yaml:"fixer_dependency_signature_only" env:"RUNNER_FIXER_DEPENDENCY_SIGNATURE_ONLY"`
	// FixerStructuredUserMessage (Phase 3 opt-in) when true, the fixer's user message is emitted as tagged <error>/<file role=... writable=...> XML-like blocks instead of the `--- path ---` free-form layout, giving the model explicit section boundaries. Default false = legacy `--- path ---` layout. Env: RUNNER_FIXER_STRUCTURED_USER_MESSAGE.
	FixerStructuredUserMessage bool `yaml:"fixer_structured_user_message" env:"RUNNER_FIXER_STRUCTURED_USER_MESSAGE"`
	// DisableStructuredGenerateOutput when true, first-pass test generation does not request JSON-schema structured completions; uses free-form + code-fence extraction only. Default false = try structured path→content JSON first when supported (OpenAI). Skipped when appending to an existing test file. Env: RUNNER_DISABLE_STRUCTURED_GENERATE_OUTPUT.
	DisableStructuredGenerateOutput bool `yaml:"disable_structured_generate_output" env:"RUNNER_DISABLE_STRUCTURED_GENERATE_OUTPUT"`
	// PreferDefaultTestSuffix (escape hatch, default false) when true the generator always emits the
	// convention default path from SuggestedTestPath (XTest.java, x.test.ts) even if an existing
	// on-disk test file uses a different suffix (XTests.java, x.spec.ts). Default false =
	// redirect+extend the existing file so the repo keeps a single test per source. Set this to true
	// only for legacy callers that rely on always-default-suffix output.
	// Env: RUNNER_PREFER_DEFAULT_TEST_SUFFIX.
	PreferDefaultTestSuffix bool `yaml:"prefer_default_test_suffix" env:"RUNNER_PREFER_DEFAULT_TEST_SUFFIX"`
	// Policy carries optional YAML-driven session.PolicyOverrides (B.3). Empty / omitted keys
	// preserve Legacy defaults; unknown keys are rejected at YAML load time so a typo cannot
	// silently disable a hook in production. (This cited the session engine's reference document for
	// the override syntax; that engine is enterprise-excluded and its document will never exist in
	// the open core, so the allow-lists below are the authority instead.)
	//
	// The supported keys are the unioned allow-lists exported by
	// session.AllowedRunHookKeys() and session.AllowedGapHookKeys(); see
	// internal/session/policy_overrides.go for the per-key contract. The fields are kept loosely
	// typed (map[string]any) at the YAML layer because individual hooks accept different value
	// types (bool, int, string, []float64, duration string, regex slice); the strong-typed
	// validation lives in session.ValidatePolicyOverrides which the loader calls during parse.
	Policy *PolicyConfig `yaml:"policy"`
	// ProjectIntel configures repo doc / agent-skill scanning injected into generation context.
	// Available by default (enabled when the key is absent). Env vars share the RETRIEVAL_PROJECT_INTEL_* prefix.
	ProjectIntel ProjectIntelConfig `yaml:"project_intel"`
	// TwoPhaseTestGeneration when true, unit-test gaps use two LLM completions: (1) compilable skeleton (imports, containers, mock stubs, placeholder bodies), (2) full tests conditioned on that skeleton. Skipped for E2E items, extend-existing mode, empty suggested path, or nil item. Each phase uses structured path→content JSON when DisableStructuredGenerateOutput is false and the provider supports it; otherwise both phases use free-form output. Default true when the key is omitted from YAML (see config.Load); set false in YAML or env to disable. Env: RUNNER_TWO_PHASE_TEST_GENERATION.
	TwoPhaseTestGeneration bool `yaml:"two_phase_test_generation" env:"RUNNER_TWO_PHASE_TEST_GENERATION"`

	// RepeatedTestFailureThreshold stops the evaluation fix loop early when the same failing generated test fingerprint (from unit or E2E output) occurs this many times in a row. 0 = default 5; -1 = disabled (use full iteration budget only). If other generated tests are not among the failing paths, discards failing files and marks stable; otherwise discards failing files, marks unstable, and applies the same reschedule / human-in-the-loop behavior as max-iteration unstable when start_max_iteration / scheduler are configured.
	RepeatedTestFailureThreshold int `yaml:"repeated_test_failure_threshold" env:"RUNNER_REPEATED_TEST_FAILURE_THRESHOLD"`
	// AbortOnUnrecoverableEnvCompileFailure, when true, aborts the evaluator's fix loop early as soon as the
	// compile step fails on an environmental issue that is clearly outside the generated artifact's scope
	// AND a scoped-compile retry has already been attempted (typical case: private NuGet feeds requiring
	// credentials the build container can't provide). Avoids burning every fix iteration on a condition
	// that deterministic retries cannot change. Generated artifacts remain on disk for local execution
	// once the environment is fixed. Default false preserves existing loop-until-budget behaviour; enable
	// in credential-limited CI where the failure will recur identically on every iteration.
	// Surfaces as audit event evaluator.compile_unrecoverable_environment_failure.
	AbortOnUnrecoverableEnvCompileFailure bool `yaml:"abort_on_unrecoverable_env_compile_failure" env:"RUNNER_ABORT_ON_UNRECOVERABLE_ENV_COMPILE_FAILURE"`
	// SkipFixerOnInfrastructureFailure when true skips LLM fix attempts when test output is classified as infrastructure/environment (missing DB, bad connection string). Default false.
	SkipFixerOnInfrastructureFailure bool `yaml:"skip_fixer_on_infrastructure_failure" env:"RUNNER_SKIP_FIXER_ON_INFRASTRUCTURE_FAILURE"`
	// DisableErrorLogLLMSummary when true disables LLM summarization of large error logs in evaluator.fix_request audit payloads. Default false = summarization on when a fixer LLM is configured.
	DisableErrorLogLLMSummary bool `yaml:"disable_error_log_llm_summary" env:"RUNNER_DISABLE_ERROR_LOG_LLM_SUMMARY"`
	// ReconcileDuplicateTestArtifacts turns duplicate reconciliation from report-only into a
	// repair: each redundant member is merged into its canonical file and removed. Default false —
	// report-only — because deletion is the only source-removing action in the system, and it is
	// gated twice over: by this key AND by provenance (only files this tool is recorded as having
	// written are ever removed). Env: RUNNER_RECONCILE_DUPLICATE_TEST_ARTIFACTS.
	ReconcileDuplicateTestArtifacts bool `yaml:"reconcile_duplicate_test_artifacts" env:"RUNNER_RECONCILE_DUPLICATE_TEST_ARTIFACTS"`
	// FixContextRunesMax caps the total size of the fix context handed to the fixer. Read-only
	// dependency files are shed largest-first until it fits; writable artifacts are never shed,
	// because a truncated artifact invites the model to "complete" it from a fragment. 0 =
	// uncapped. Env: RUNNER_FIX_CONTEXT_RUNES_MAX.
	FixContextRunesMax int `yaml:"fix_context_runes_max" env:"RUNNER_FIX_CONTEXT_RUNES_MAX"`
	// FixBackoff waits before every fix attempt after the first (Go duration, e.g. "30s"). The
	// provider clients already back off between retries WITHIN one call; this paces the evaluator's
	// own rounds against a rate-limited provider. Empty = no wait. Env: RUNNER_FIX_BACKOFF.
	FixBackoff string `yaml:"fix_backoff" env:"RUNNER_FIX_BACKOFF"`
	// FixLoopRepeatStopThreshold / FixLoopRecurrenceStopThreshold / FixLoopNoProgressStopThreshold
	// override the circuit-breaker defaults (3 / 2 / 6). A non-positive value means "unset", never
	// "disabled" — a disabled breaker is how a loop burns its whole budget on one unfixable error.
	// Raising them buys more rounds on a stubborn failure at the cost of wall-clock time on one
	// that will never converge.
	// Env: RUNNER_FIX_LOOP_REPEAT_STOP_THRESHOLD, RUNNER_FIX_LOOP_RECURRENCE_STOP_THRESHOLD,
	// RUNNER_FIX_LOOP_NO_PROGRESS_STOP_THRESHOLD.
	FixLoopRepeatStopThreshold     int `yaml:"fix_loop_repeat_stop_threshold" env:"RUNNER_FIX_LOOP_REPEAT_STOP_THRESHOLD"`
	FixLoopRecurrenceStopThreshold int `yaml:"fix_loop_recurrence_stop_threshold" env:"RUNNER_FIX_LOOP_RECURRENCE_STOP_THRESHOLD"`
	FixLoopNoProgressStopThreshold int `yaml:"fix_loop_no_progress_stop_threshold" env:"RUNNER_FIX_LOOP_NO_PROGRESS_STOP_THRESHOLD"`
	// StartMaxIteration is the max evaluation fix-iteration budget for the first run. Stored as current_iteration in the run DB; if evaluation is still unstable after using this budget, current_iteration is increased and the pipeline is scheduled to rerun after SchedulerInterval. 0 = use default 3.
	StartMaxIteration int `yaml:"start_max_iteration" env:"RUNNER_START_MAX_ITERATION"`
	// MaxIteration is the ceiling for current_iteration; we never allow more than this many fix iterations per run. 0 = use default 10.
	MaxIteration int `yaml:"max_iteration" env:"RUNNER_MAX_ITERATION"`
	// HumanInTheLoopEmail is the address to notify when max_iteration is reached and evaluation is still unstable. Empty = do not send.
	HumanInTheLoopEmail string `yaml:"human_in_the_loop_email" env:"RUNNER_HUMAN_IN_THE_LOOP_EMAIL"`
	// SMTP* configure the human-in-the-loop email sender. SMTPHost and SMTPFrom are required to
	// actually send (together with HumanInTheLoopEmail); otherwise the notification is a no-op.
	// SMTPPort defaults to 587. When SMTPUser is set, PLAIN auth (with STARTTLS when offered) is used.
	SMTPHost     string `yaml:"smtp_host" env:"RUNNER_SMTP_HOST"`
	SMTPPort     int    `yaml:"smtp_port" env:"RUNNER_SMTP_PORT"`
	SMTPUser     string `yaml:"smtp_user" env:"RUNNER_SMTP_USER"`
	SMTPPassword string `yaml:"smtp_password" env:"RUNNER_SMTP_PASSWORD"`
	SMTPFrom     string `yaml:"smtp_from" env:"RUNNER_SMTP_FROM"`

	// TestFrameworkBootstrap optionally installs a default test stack when none is detected (Jest / JUnit / xUnit).
	TestFrameworkBootstrap TestFrameworkBootstrapConfig `yaml:"test_framework_bootstrap"`
	// E2EFrameworkBootstrap: when max_gaps_e2e > 0 and no E2E runner is detected, JS/TS installs Playwright/Cypress; Java patches Maven/Gradle + Playwright Java; C# patches Microsoft.Playwright + smoke test. Unsupported languages are audit-only (skip_apply).
	E2EFrameworkBootstrap E2EFrameworkBootstrapConfig `yaml:"e2e_framework_bootstrap"`
}

// PrivateRegistryType is the ecosystem discriminator on PrivateRegistryCredential. See
// RegistryTypeNuGet / RegistryTypeMaven / RegistryTypeNpm for the supported values.
type PrivateRegistryType string

const (
	// RegistryTypeNuGet authenticates NuGet v3 feeds (C#). Endpoint = v3 index URL exactly as in
	// nuget.config. Emitted into VSS_NUGET_EXTERNAL_FEED_ENDPOINTS; dotnet's Artifacts Credential
	// Provider consumes it for any HTTPS NuGet source.
	RegistryTypeNuGet PrivateRegistryType = "nuget"
	// RegistryTypeMaven authenticates Maven repositories (Java). Endpoint = repository URL. Emitted
	// as a `<server>` entry in a generated ~/.m2/settings.xml mounted into the eval + bootstrap
	// containers. Server id defaults to the endpoint hostname when `id` is empty; override when
	// the project's pom.xml references a specific id.
	RegistryTypeMaven PrivateRegistryType = "maven"
	// RegistryTypeNpm authenticates npm registries (TS/JS; includes yarn/pnpm). Endpoint = registry
	// URL (trailing `/` normalised). Emitted into a generated ~/.npmrc mounted into the eval +
	// bootstrap containers. `token` (preferred) renders as `_authToken`; `username`/`password`
	// renders as base64 `_auth`. `scope` optionally pins the registry to an npm scope.
	RegistryTypeNpm PrivateRegistryType = "npm"
)

// PrivateRegistryCredential is a single cross-ecosystem private-registry credential entry. A list of
// these under `runner.private_registry_credentials` is translated by ASQS into the ecosystem-specific
// auth mechanism (VSS envelope for nuget, settings.xml for maven, .npmrc for npm) and injected into
// every docker eval + test_framework_bootstrap / e2e_framework_bootstrap container.
//
// All optional fields are ecosystem-specific; unused fields for a given `type` are ignored. A single
// unified shape is intentional so operators configure auth the same way regardless of language.
type PrivateRegistryCredential struct {
	// Type is the ecosystem: "nuget" | "maven" | "npm". Case-insensitive. Unknown types are skipped
	// with a load-time warning (so typos surface quickly).
	Type PrivateRegistryType `yaml:"type"`
	// Endpoint is the registry/feed URL. Must match the URL used by the project's
	// nuget.config / pom.xml / .npmrc exactly (including trailing slash for npm v1 registries and
	// `/v3/index.json` suffix for NuGet v3 feeds).
	Endpoint string `yaml:"endpoint"`
	// ID is a Maven `<server>` id binding this credential to a pom.xml `<repository>` or
	// `<distributionManagement>` entry. Optional — when empty the endpoint hostname is used.
	// Ignored for nuget and npm entries.
	ID string `yaml:"id,omitempty"`
	// Scope pins an npm registry to a scope (must start with `@`, e.g. `@mycompany`). When unset
	// the entry defines the default registry. Ignored for nuget and maven entries.
	Scope string `yaml:"scope,omitempty"`
	// Username is the identity. For Azure DevOps Artifacts NuGet, the credential provider expects
	// `AzureDevOps` (use `runner.azure_devops_nuget_feed_endpoints` + `vcs.azure_devops.token` for
	// that path instead of repeating it here).
	Username string `yaml:"username,omitempty"`
	// Password is the secret (PAT, API key, password). Prefer injection via env / secret manager.
	Password string `yaml:"password,omitempty"`
	// Token, when set for npm entries, is preferred over username/password: ASQS renders it as
	// `//<registry>:_authToken=<token>`. Ignored for nuget and maven entries (which always use
	// username/password).
	Token string `yaml:"token,omitempty"`
}

// E2EFrameworkBootstrapConfig controls auto-setup of E2E runners when gaps are enabled and no E2E stack exists.
type E2EFrameworkBootstrapConfig struct {
	Enabled bool `yaml:"enabled" env:"RUNNER_E2E_FRAMEWORK_BOOTSTRAP_ENABLED"`
	// Mode: auto | playwright | cypress | off. JS/TS: auto/playwright → Playwright, cypress → Cypress. Java/C#: auto/playwright → Playwright stack; cypress falls back to Playwright (audit mode_fallback).
	Mode string `yaml:"mode" env:"RUNNER_E2E_FRAMEWORK_BOOTSTRAP_MODE"`
	// PinVersions use exact semver for @playwright/test in package.json.
	PinVersions bool `yaml:"pin_versions" env:"RUNNER_E2E_FRAMEWORK_BOOTSTRAP_PIN_VERSIONS"`
	// AllowLockfileChange when false uses frozen install when a lockfile exists.
	AllowLockfileChange bool `yaml:"allow_lockfile_change" env:"RUNNER_E2E_FRAMEWORK_BOOTSTRAP_ALLOW_LOCKFILE_CHANGE"`
	// Execution: auto | docker | local. auto uses ephemeral Docker when runner.type is docker (same for C# as JS/Java).
	Execution string `yaml:"execution" env:"RUNNER_E2E_FRAMEWORK_BOOTSTRAP_EXECUTION"`
}

// TestFrameworkBootstrapConfig controls auto-setup of test tooling before indexing/generation.
type TestFrameworkBootstrapConfig struct {
	// Enabled turns on detection + apply for supported languages. Default false.
	Enabled bool `yaml:"enabled" env:"RUNNER_TEST_FRAMEWORK_BOOTSTRAP_ENABLED"`
	// Mode: auto = per-language default (Jest, JUnit 5, xUnit); jest | junit | xunit = force one stack; off = disable.
	Mode string `yaml:"mode" env:"RUNNER_TEST_FRAMEWORK_BOOTSTRAP_MODE"`
	// PinVersions use exact semver strings in package.json for reproducibility.
	PinVersions bool `yaml:"pin_versions" env:"RUNNER_TEST_FRAMEWORK_BOOTSTRAP_PIN_VERSIONS"`
	// AllowLockfileChange when false and a lockfile exists, install uses frozen lockfile (npm ci / --frozen-lockfile); when true, lockfile may be updated.
	AllowLockfileChange bool `yaml:"allow_lockfile_change" env:"RUNNER_TEST_FRAMEWORK_BOOTSTRAP_ALLOW_LOCKFILE_CHANGE"`
	// Execution: auto | docker | local. auto uses ephemeral Docker when runner.type is docker (C# unit bootstrap uses the csharp-dotnet eval image).
	Execution string `yaml:"execution" env:"RUNNER_TEST_FRAMEWORK_BOOTSTRAP_EXECUTION"`
}
