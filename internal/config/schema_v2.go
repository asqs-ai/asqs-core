package config

// Schema v2: the YAML-facing configuration surface.
//
// v1 grew flat and by accretion — 208 leaf keys after CP36/CP37 pruned it, of which 34 were four
// near-identical `vcs.<provider>` blocks the runtime only ever read one of. v2 is organised by the
// PIPELINE instead: `general` for what more than one step shares, then one section per step in run
// order (bootstrap → indexer → retrieval → generation → fixer).
//
// Three rules hold throughout, and all three exist to kill the "documented, settable, inert" defect
// class this restructure was written to remove:
//
//  1. **One home per knob.** Where v1 had two spellings for one decision, v2 keeps the live one.
//  2. **Positive booleans.** Every toggle is `enabled` (or a positive noun), never `disable_*`. Eight
//     v1 keys were inverted to get here; TranslateV2ToRuntime is where each inversion happens, and
//     schema_v2_translate_test.go pins every one of them.
//  3. **No `env` tags.** Env names are DERIVED from the paths below — `ASQS_` + the dotted path
//     upper-cased, with a leading `general.` stripped (see envNameForPath in env_v2.go). A tag would
//     be a second, drifting source of truth, and that is precisely how v1 came to document env vars
//     for field types its own loader could not decode.
//
// Every field carries a doc comment. That is enforced by a test, not by convention, because CP39
// lifts these comments verbatim into the generated reference — an undocumented key would ship as a
// blank row.
type SchemaV2 struct {
	// SchemaVersion is absent or 2. Anything else is rejected, so a future schema change has an
	// anchor to branch on instead of guessing from the document's shape.
	SchemaVersion int `yaml:"schema_version"`
	// ClientID scopes environment overrides: ASQS_<ClientID>_<KEY> beats ASQS_<KEY>.
	ClientID string `yaml:"client_id"`

	// General holds settings more than one pipeline step reads.
	General GeneralV2 `yaml:"general"`
	// Bootstrap prepares the repository's test tooling before generation.
	Bootstrap BootstrapV2 `yaml:"bootstrap"`
	// Indexer builds the symbol graph and chunk corpus.
	Indexer IndexerV2 `yaml:"indexer"`
	// Retrieval assembles the context each generation call sees.
	Retrieval RetrievalV2 `yaml:"retrieval"`
	// Generation writes tests and documentation.
	Generation GenerationV2 `yaml:"generation"`
	// Fixer owns the evaluate-and-repair loop.
	Fixer FixerV2 `yaml:"fixer"`
}

// ---------- general ----------

// GeneralV2 holds what more than one pipeline step needs. A key belongs here only when at least two
// steps read it; anything a single step owns lives in that step's section.
type GeneralV2 struct {
	// Database is the Postgres metadata store and the pgvector chunk store.
	Database DatabaseV2 `yaml:"database"`
	// Git is the version-control platform: cloning, and shipping results back as a pull request.
	Git GitV2 `yaml:"git"`
	// LLM is the default model configuration every step falls back to.
	LLM LLMV2 `yaml:"llm"`
	// Build describes how to build and test the repository under test.
	Build BuildV2 `yaml:"build"`
	// Sandbox is where compile and test commands actually run.
	Sandbox SandboxV2 `yaml:"sandbox"`
	// Notifications carry the human-in-the-loop email.
	Notifications NotificationsV2 `yaml:"notifications"`
	// Audit records what each step did, for post-mortems and A/B comparison.
	Audit AuditV2 `yaml:"audit"`
	// WebSearch gives the tool loop bounded access to external documentation.
	WebSearch WebSearchV2 `yaml:"websearch"`
}

// DatabaseV2 configures the two Postgres connections.
type DatabaseV2 struct {
	// MetadataURL is the Postgres URL for symbols, edges, files and run records. Required.
	MetadataURL string `yaml:"metadata_url"`
	// EmbeddingsURL is the pgvector database for chunks. Empty = the same database as MetadataURL.
	EmbeddingsURL string `yaml:"embeddings_url"`
	// MaxOpenConns caps both connection pools. 0 = the pgxpool default.
	MaxOpenConns int `yaml:"max_open_conns"`
}

// GitV2 replaces v1's four near-identical vcs.<provider> blocks.
//
// The runtime only ever read the ACTIVE provider's block, through the Active*() accessors, so 13
// keys × 4 providers were 52 duplicates that could disagree with one another without anyone
// noticing — and did, since nothing validated that the block you filled in matched the provider you
// selected.
type GitV2 struct {
	// Provider selects which platform's API and clone URLs are used: github, gitlab, bitbucket or
	// azure_devops.
	Provider string `yaml:"provider"`
	// Token authenticates the ACTIVE provider. v1 had four provider-specific variables; a single
	// active provider needs one.
	Token string `yaml:"token"`
	// BaseURL points at a self-hosted instance. Empty = the provider's cloud host.
	BaseURL string `yaml:"base_url"`
	// DefaultOwner names the owning account when it cannot be inferred from the clone URL. Each
	// provider's own word for it — GitLab namespace, Bitbucket workspace, Azure organization — maps
	// onto this single key.
	DefaultOwner string `yaml:"default_owner"`
	// DefaultRepo is the repository slug when it cannot be inferred from the clone URL.
	DefaultRepo string `yaml:"default_repo"`
	// AzureDevOps carries the extras Azure needs beyond owner/repo, and its own token.
	AzureDevOps GitAzureDevOpsV2 `yaml:"azure_devops"`
	// Ship is one block for the active provider — v1 declared it four times and read one.
	Ship ShipConfig `yaml:"ship"`
}

// GitAzureDevOpsV2 holds Azure DevOps' extra coordinates.
//
// Its Token is NOT redundant with git.token: CloneAuthTokenForURL uses the Azure PAT for
// dev.azure.com clone URLs and for the NuGet feed envelope even when the active provider is
// something else, so a single active-provider token cannot express a repository that lives on Azure
// while the run's provider is GitHub.
type GitAzureDevOpsV2 struct {
	// Organization is the Azure DevOps organization name.
	Organization string `yaml:"organization"`
	// Project is the team project containing the repository.
	Project string `yaml:"project"`
	// Repository is the git repository name within the project.
	Repository string `yaml:"repository"`
	// Token is the Azure PAT, honoured for dev.azure.com remotes regardless of git.provider.
	Token string `yaml:"token"`
}

// LLMV2 is the default model configuration. Each step may override provider, model and credential in
// its own section; an empty field there falls back here.
type LLMV2 struct {
	// Provider names the API to talk to: openai, anthropic, azure_openai, ollama.
	Provider string `yaml:"provider"`
	// Model is the chat model id. It also determines the context window the prompt budget derives
	// from, so an unknown model leaves the budget unbounded rather than guessed.
	Model string `yaml:"model"`
	// APIKey is the credential in clear text. There is no ${VAR} interpolation anywhere in the
	// loader — a literal "${MY_KEY}" here is sent verbatim. Use APIKeyFromEnv instead.
	APIKey string `yaml:"api_key"`
	// APIKeyFromEnv names an environment variable to read the credential from, so a config can be
	// committed without a secret in it. It wins over APIKey when both are set.
	APIKeyFromEnv string `yaml:"api_key_from_env"`
	// BaseURL overrides the provider endpoint — Azure, a corporate proxy, or an Ollama gateway.
	BaseURL string `yaml:"base_url"`
	// MaxConcurrent bounds in-flight completions across the whole run. This is the lever for a
	// provider rate limit, and for a single local Ollama process it is the difference between a
	// working run and a thrashing one.
	MaxConcurrent int `yaml:"max_concurrent"`
	// OllamaNumCtx sets options.num_ctx on Ollama chat calls. 0 = the model's own default. Native
	// tool calling requires it, so leaving it unset disables tools on Ollama.
	OllamaNumCtx int `yaml:"ollama_num_ctx"`
	// HTTP tunes the transport. Client proxies are a documented deployment constraint, which is why
	// these stay configurable rather than joining the constants freeze.
	HTTP LLMHTTPV2 `yaml:"http"`
	// Embeddings configures the vector model, separately from the chat model.
	Embeddings EmbeddingsV2 `yaml:"embeddings"`
}

// LLMHTTPV2 tunes the HTTP transport shared by every LLM client.
type LLMHTTPV2 struct {
	// Timeout bounds one whole completion request, e.g. "10m".
	Timeout string `yaml:"timeout"`
	// ResponseHeaderTimeout bounds the wait for the first response header, separately from the body —
	// the distinction that tells a hung proxy apart from a slow model.
	ResponseHeaderTimeout string `yaml:"response_header_timeout"`
	// DisableKeepAlives forces a new connection per request; some proxies mishandle pooled ones.
	DisableKeepAlives bool `yaml:"disable_keep_alives"`
}

// EmbeddingsV2 configures the vector model used for chunk search.
type EmbeddingsV2 struct {
	// Provider defaults to general.llm.provider.
	Provider string `yaml:"provider"`
	// Model is the embedding model id. Its output width must match Dimension below.
	Model string `yaml:"model"`
	// Dimension must match the model's output width. It lived under database.* in v1, which put it
	// arbitrarily far from the model that determines it. Changing it fails closed on a populated
	// table rather than silently mixing widths.
	Dimension int `yaml:"dimension"`
	// APIKey authenticates the embedding provider when it differs from the chat one. Empty falls
	// back to the general credential.
	APIKey string `yaml:"api_key"`
	// APIKeyFromEnv reads that credential from an environment variable instead.
	APIKeyFromEnv string `yaml:"api_key_from_env"`
	// Fallback is "", "auto", or an explicit Ollama model used when the primary embedder fails.
	Fallback string `yaml:"fallback"`
	// Cache memoises embeddings by content hash so a re-run over unchanged code pays nothing.
	Cache EmbCacheV2 `yaml:"cache"`
}

// EmbCacheV2 controls the content-addressed embedding memo.
type EmbCacheV2 struct {
	// Enabled inverts v1's llm.disable_embedding_cache. Default true: a cache miss is exactly the
	// pre-cache behaviour, so there is nothing to lose by leaving it on.
	Enabled *bool `yaml:"enabled"`
	// RetentionDays prunes rows unused for this long. 0 = 30 days.
	RetentionDays int `yaml:"retention_days"`
}

// BuildV2 describes how to build and test the repository under test. Bootstrap, the evaluator and
// the fixer all read it, which is why it is general rather than owned by one step.
type BuildV2 struct {
	// Toolchain pins the evaluation image family (java-maven, java-gradle, typescript, csharp).
	// "auto" or empty detects from the repository. v1 spelled it runner.eval_profile.
	Toolchain string `yaml:"toolchain"`
	// BuildTool forces mvn or gradle for Java. "auto" or empty detects.
	BuildTool string `yaml:"build_tool"`
	// CompileCommand overrides the detected compile step, run from the repository root.
	CompileCommand string `yaml:"compile_command"`
	// TestCommand overrides the detected whole-suite test step.
	TestCommand string `yaml:"test_command"`
	// UnitTestCommand overrides the unit-only test step when it differs from the whole suite.
	UnitTestCommand string `yaml:"unit_test_command"`
	// E2ETestCommand overrides the end-to-end test step.
	E2ETestCommand string `yaml:"e2e_test_command"`
	// FormatCommand runs after generation and after each fix, before evaluation.
	FormatCommand string `yaml:"format_command"`
	// DotNetFallbackTargetFramework is used when a .csproj names no target framework.
	DotNetFallbackTargetFramework string `yaml:"dotnet_fallback_target_framework"`
	// Workspace narrows a mono-repo to one project.
	Workspace WorkspaceV2 `yaml:"workspace"`
}

// WorkspaceV2 narrows a mono-repo to the sub-project under test.
type WorkspaceV2 struct {
	// Path is the repo-relative directory holding the project to index and generate for. Empty =
	// the whole repository. No "..".
	Path string `yaml:"path"`
	// TestPath is where generated tests go when the test tree does not sit under Path. Empty = Path.
	TestPath string `yaml:"test_path"`
}

// SandboxV2 is where compile and test commands run.
type SandboxV2 struct {
	// Type is "docker" or "local". An unrecognised value fails at startup rather than silently
	// falling back — a typo used to green-light a run that compiled nothing and shipped.
	Type string `yaml:"type"`
	// Timeout bounds one sandbox step, e.g. "30m".
	Timeout string `yaml:"timeout"`
	// Docker holds settings that only apply to the docker target.
	Docker SandboxDockerV2 `yaml:"docker"`
	// Images pins the container image per toolchain.
	Images SandboxImagesV2 `yaml:"images"`
	// Resources caps what one container may consume.
	Resources SandboxResourcesV2 `yaml:"resources"`
	// Network controls whether the restore and test phases may reach the network.
	Network SandboxNetworkV2 `yaml:"network"`
	// Caches mount host package caches so restore does not re-download every run.
	Caches SandboxCachesV2 `yaml:"caches"`
	// Registries configures access to private package feeds.
	Registries SandboxRegistriesV2 `yaml:"registries"`
}

// SandboxDockerV2 holds docker-target settings.
type SandboxDockerV2 struct {
	// Binary is the docker CLI to invoke. Empty = "docker" on PATH; set it for podman or a wrapper.
	Binary string `yaml:"binary"`
	// OfflineTest runs the test phase with networking disabled once restore has populated the
	// caches. Inverts v1's runner.docker_disable_offline_test. Default true — an offline test phase
	// is what makes a run reproducible.
	OfflineTest *bool `yaml:"offline_test"`
	// RequireBootstrap fails the run when the image's toolchain bootstrap did not complete, instead
	// of proceeding to compile against a half-provisioned container.
	RequireBootstrap bool `yaml:"require_bootstrap"`
}

// SandboxImagesV2 pins one container image per toolchain.
type SandboxImagesV2 struct {
	// Java is the default JDK image when neither Maven nor Gradle is detected.
	Java string `yaml:"java"`
	// JavaMaven is the image for Maven projects.
	JavaMaven string `yaml:"java_maven"`
	// JavaGradle is the image for Gradle projects.
	JavaGradle string `yaml:"java_gradle"`
	// Node is the image for JavaScript and TypeScript projects.
	Node string `yaml:"node"`
	// DotNet is the image for C# projects.
	DotNet string `yaml:"dotnet"`
	// Playwright is the image for JS/TS end-to-end runs, which need browsers.
	Playwright string `yaml:"playwright"`
	// PlaywrightJava is the Playwright image with a JDK.
	PlaywrightJava string `yaml:"playwright_java"`
	// PlaywrightDotNet is the Playwright image with the .NET SDK.
	PlaywrightDotNet string `yaml:"playwright_dotnet"`
}

// SandboxResourcesV2 caps what one container may consume.
type SandboxResourcesV2 struct {
	// CPUs limits container CPU, e.g. 2. 0 = no limit.
	CPUs float64 `yaml:"cpus"`
	// Memory limits container memory, e.g. "4g". Empty = no limit.
	Memory string `yaml:"memory"`
	// PidsLimit caps processes in the container, bounding a fork bomb in a repository's own build.
	PidsLimit int `yaml:"pids_limit"`
	// ReadonlyRootfs mounts the container root read-only, writing only to the mounted workspace.
	ReadonlyRootfs bool `yaml:"readonly_rootfs"`
}

// SandboxNetworkV2 controls network access per phase.
type SandboxNetworkV2 struct {
	// Restore is the docker --network value for the dependency-restore phase, which legitimately
	// needs a registry. Empty = the default for the target.
	Restore string `yaml:"restore"`
	// Test is the --network value for compile and test. Empty = the default, which is isolated.
	Test string `yaml:"test"`
}

// SandboxCachesV2 mounts host package caches so restore does not re-download on every run.
type SandboxCachesV2 struct {
	// Maven is the host path mounted as the Maven repository.
	Maven string `yaml:"maven"`
	// Gradle is the host Gradle user home.
	Gradle string `yaml:"gradle"`
	// Npm is the host npm cache directory.
	Npm string `yaml:"npm"`
	// Pnpm is the host pnpm store directory.
	Pnpm string `yaml:"pnpm"`
	// NuGet is the host NuGet packages directory.
	NuGet string `yaml:"nuget"`
	// Cypress is the host Cypress binary cache.
	Cypress string `yaml:"cypress"`
}

// SandboxRegistriesV2 configures private package feeds.
type SandboxRegistriesV2 struct {
	// AzureDevOpsNuGetFeedEndpoints lists feed URLs for the Azure artifacts credential envelope.
	AzureDevOpsNuGetFeedEndpoints []string `yaml:"azure_devops_nuget_feed_endpoints"`
	// Credentials are per-registry credentials for restore inside the sandbox.
	Credentials []PrivateRegistryCredential `yaml:"credentials"`
}

// NotificationsV2 configures the human-in-the-loop email sent when a run ends unstable.
type NotificationsV2 struct {
	// HumanInTheLoopEmail receives the notification. Empty disables it.
	HumanInTheLoopEmail string `yaml:"human_in_the_loop_email"`
	// SMTP is the relay used to send it.
	SMTP SMTPV2 `yaml:"smtp"`
}

// SMTPV2 is the mail relay.
type SMTPV2 struct {
	// Host is the SMTP server hostname. Empty disables mail entirely.
	Host string `yaml:"host"`
	// Port is the SMTP port.
	Port int `yaml:"port"`
	// User authenticates to the relay.
	User string `yaml:"user"`
	// Password authenticates to the relay.
	Password string `yaml:"password"`
	// From is the envelope sender address.
	From string `yaml:"from"`
}

// AuditV2 configures the structured record of what each step did.
type AuditV2 struct {
	// FilePath appends one JSON object per step to this file. Empty = stderr step lines only.
	FilePath string `yaml:"file_path"`
	// DumpPrompts restores full prompt and completion text in audit payloads. Off by default:
	// prompt bodies carry repository source, extracted configuration and compiler output, so the
	// sink stores {sha256, len} instead. Turn it on for a post-mortem, not for normal running.
	DumpPrompts bool `yaml:"dump_prompts"`
}

// WebSearchV2 gives the tool loop bounded, auditable access to external documentation. It is the one
// component that sends data out of the process, which is why every field here is a brake.
type WebSearchV2 struct {
	// Enabled turns the search and fetch tools on. Off by default, and off means the tools are not
	// registered at all rather than registered and refusing.
	Enabled bool `yaml:"enabled"`
	// Offline serves only from the replay cache and never egresses. A cache miss becomes an answer,
	// not a network call.
	Offline bool `yaml:"offline"`
	// Provider selects the backend: "searxng" (self-hosted, queries stay inside your boundary) or
	// "brave" (hosted, needs a key).
	Provider string `yaml:"provider"`
	// Endpoint is the SearXNG base URL. Operator-configured infrastructure, so cluster-internal
	// http is legitimate here — the https-only rule guards model-chosen URLs, not this.
	Endpoint string `yaml:"endpoint"`
	// APIKey authenticates a hosted provider.
	APIKey string `yaml:"api_key"`
	// APIKeyFromEnv reads that credential from an environment variable instead.
	APIKeyFromEnv string `yaml:"api_key_from_env"`
	// AllowedHosts gates which hosts may be fetched. Exact names or "*.example.org". EMPTY DISABLES
	// FETCH — an empty allow-list fails closed, never open.
	AllowedHosts []string `yaml:"allowed_hosts"`
}

// ---------- bootstrap ----------

// BootstrapV2 prepares the repository's test tooling before generation, so the fix loop does not
// spend its whole budget discovering that the repository could never have run a generated test.
type BootstrapV2 struct {
	// TestFramework installs and smoke-verifies the unit-test stack.
	TestFramework FrameworkBootstrapV2 `yaml:"test_framework"`
	// E2EFramework installs the end-to-end stack when E2E gaps are enabled.
	E2EFramework FrameworkBootstrapV2 `yaml:"e2e_framework"`
}

// FrameworkBootstrapV2 configures one bootstrap stage. Both stages take identical settings, which is
// why v1's two structurally identical blocks collapse to one type here.
type FrameworkBootstrapV2 struct {
	// Enabled turns the stage on. Off by default: bootstrap writes to the repository under test.
	Enabled bool `yaml:"enabled"`
	// Mode is how far it may go — "detect" reports only, "apply" edits build manifests.
	Mode string `yaml:"mode"`
	// PinVersions writes exact dependency versions rather than ranges, so a later run gets the same
	// stack it was verified against.
	PinVersions bool `yaml:"pin_versions"`
	// AllowLockfileChange permits updating a lockfile. Off keeps the repository's pinned graph
	// intact even when that means bootstrap cannot finish.
	AllowLockfileChange bool `yaml:"allow_lockfile_change"`
	// Execution is "docker" or "local" for the bootstrap's own commands. Empty follows
	// general.sandbox.type.
	Execution string `yaml:"execution"`
}

// ---------- indexer ----------

// IndexerV2 builds the symbol graph and the chunk corpus everything downstream reads.
type IndexerV2 struct {
	// Execution is "local" or "docker" for the external language indexers. Empty = local.
	Execution string `yaml:"execution"`
	// Java configures the Java indexer.
	Java IndexerJavaV2 `yaml:"java"`
	// JSTS configures the JavaScript/TypeScript indexer.
	JSTS IndexerJSTSV2 `yaml:"jsts"`
	// CSharp configures the C# Roslyn indexer.
	CSharp IndexerCSharpV2 `yaml:"csharp"`
	// Docker holds images and limits for indexers run in containers.
	Docker IndexerDockerV2 `yaml:"docker"`
	// DependencyDocs ingests documentation for the repository's direct dependencies.
	DependencyDocs IndexerDependencyDocsV2 `yaml:"dependency_docs"`
	// Policy bounds what the indexer produces.
	Policy IndexerPolicyV2 `yaml:"policy"`
}

// IndexerJavaV2 configures the Java indexer.
type IndexerJavaV2 struct {
	// Mode is auto | advanced | minimal. "auto" (the default) picks the advanced JavaParser indexer
	// when a JAR path is configured and falls back to the line-based one otherwise.
	//
	// v1 required BOTH `indexer.type: advanced` AND a jar path, so a deployment that had built the
	// JAR and set its path but never set the type silently got line-based indexing — full AST and
	// symbol resolution quietly replaced by heuristics, with nothing in the run to say so. Making
	// the path sufficient removes the trap; "minimal" remains available for someone who wants the
	// heuristics deliberately.
	Mode string `yaml:"mode"`
	// JarPath locates the JavaParser indexer JAR. Empty = no advanced indexing.
	JarPath string `yaml:"jar_path"`
}

// IndexerJSTSV2 configures the JavaScript/TypeScript indexer.
type IndexerJSTSV2 struct {
	// IndexerPath is the Node indexer entry point. Empty disables JS/TS indexing.
	IndexerPath string `yaml:"indexer_path"`
}

// IndexerCSharpV2 configures the C# Roslyn indexer.
type IndexerCSharpV2 struct {
	// IndexerDLLPath is the published Roslyn indexer DLL. Empty disables C# indexing. Rebuild it
	// after upgrading — a stale DLL emits the pre-B25 FQName format and forces a full reindex.
	IndexerDLLPath string `yaml:"indexer_dll_path"`
}

// IndexerDockerV2 holds images and limits for indexers run in containers.
type IndexerDockerV2 struct {
	// CLI is the docker binary for indexer containers. Empty = "docker".
	CLI string `yaml:"cli"`
	// Memory limits an indexer container, e.g. "2g".
	Memory string `yaml:"memory"`
	// JavaImage is the image for the advanced Java indexer.
	JavaImage string `yaml:"java_image"`
	// NodeImage is the image for the JS/TS indexer.
	NodeImage string `yaml:"node_image"`
	// DotNetIndexerImage is the image for the C# indexer.
	DotNetIndexerImage string `yaml:"dotnet_indexer_image"`
	// NodeHeapMB sets --max-old-space-size for the Node indexer on large repositories.
	NodeHeapMB int `yaml:"node_heap_mb"`
}

// IndexerDependencyDocsV2 ingests documentation for direct dependencies — Maven sources jars, NuGet
// XML docs, node_modules type declarations — so the model can read an API instead of guessing it.
// Local only: nothing here reaches the network.
type IndexerDependencyDocsV2 struct {
	// Enabled turns ingestion on. Off by default.
	Enabled bool `yaml:"enabled"`
	// MavenRepoDir overrides the local Maven repository location.
	MavenRepoDir string `yaml:"maven_repo_dir"`
	// NuGetPackagesDir overrides the local NuGet packages location.
	NuGetPackagesDir string `yaml:"nuget_packages_dir"`
}

// IndexerPolicyV2 bounds what the indexer produces.
type IndexerPolicyV2 struct {
	// MaxGaps caps unit-test gaps per run. The --max-gaps flag overrides it.
	MaxGaps int `yaml:"max_gaps"`
	// MaxGapsE2E caps end-to-end gaps per run.
	MaxGapsE2E int `yaml:"max_gaps_e2e"`
	// MaxGapsPerFile stops one large file consuming the whole budget.
	MaxGapsPerFile int `yaml:"max_gaps_per_file"`
	// MaxGapsPerFileE2E is the same cap for end-to-end gaps.
	MaxGapsPerFileE2E int `yaml:"max_gaps_per_file_e2e"`
	// CriticalModulePrefixes raises the priority of gaps under these path prefixes.
	CriticalModulePrefixes []string `yaml:"critical_module_prefixes"`
	// SkipPathPrefixes excludes paths from indexing entirely — generated code, vendored trees.
	SkipPathPrefixes []string `yaml:"skip_path_prefixes"`
}

// ---------- retrieval ----------

// RetrievalV2 assembles the context each generation call sees.
type RetrievalV2 struct {
	// Profile selects the retrieval shape for unit gaps: java_unit, http_api, react_feature,
	// nest_module, full_stack.
	Profile string `yaml:"profile"`
	// ProfileE2E is the profile for end-to-end gaps, usually e2e_playwright.
	ProfileE2E string `yaml:"profile_e2e"`
	// ProfileBudgets overrides the section caps per profile. This is where budget tuning lives; the
	// global caps were frozen into constants because per-profile is the only level at which the
	// numbers mean anything.
	ProfileBudgets map[string]RetrievalProfileBudget `yaml:"profile_budgets"`
	// Fusion selects how dense and lexical channels combine: "dense" or "rrf". Dense is the default
	// because rrf measured as a regression.
	Fusion string `yaml:"fusion"`
	// MaxContextTokens caps the assembled prompt. 0 = derive it from the model's context window.
	MaxContextTokens int `yaml:"max_context_tokens"`
	// Context bounds how far graph expansion walks.
	Context RetrievalContextV2 `yaml:"context"`
	// FailureHint feeds a previous failure into planning so retrieval localises on it.
	FailureHint RetrievalFailureHintV2 `yaml:"failure_hint"`
	// Policy holds the thresholds that decide whether to generate at all.
	Policy RetrievalPolicyV2 `yaml:"policy"`
}

// RetrievalContextV2 bounds graph expansion.
type RetrievalContextV2 struct {
	// DependencyMaxDepth is how many edges out from the target to walk. 0 = the built-in 2.
	DependencyMaxDepth int `yaml:"dependency_max_depth"`
	// Compact shrinks retrieved chunks deterministically before assembly.
	Compact EnabledV2 `yaml:"compact"`
}

// RetrievalFailureHintV2 feeds the previous failure into planning.
type RetrievalFailureHintV2 struct {
	// File is a repo-relative compiler or CI log read before planning, so retrieval weights the code
	// the failure implicates. Must stay under the repository root.
	File string `yaml:"file"`
	// Persist writes this run's failing compile and test output for the next run to read, and
	// removes the file when a run goes green.
	Persist bool `yaml:"persist"`
}

// RetrievalPolicyV2 holds the thresholds that decide whether to generate at all.
type RetrievalPolicyV2 struct {
	// MinSimilarTestsForGeneration abstains when fewer than this many similar tests were found —
	// generating without an example of the repository's conventions produces tests it rejects.
	MinSimilarTestsForGeneration int `yaml:"min_similar_tests_for_generation"`
	// MinSimilarityCosine is the floor below which a retrieved chunk is not considered similar.
	MinSimilarityCosine float64 `yaml:"min_similarity_cosine"`
	// Abstention lets retrieval decline a gap it has too little context for. Inverts v1's
	// retrieval.abstention_disabled. Default on.
	Abstention EnabledV2 `yaml:"abstention"`
	// HybridModuleFilter restricts hybrid search to the target's own module. Inverts v1's
	// retrieval.disable_hybrid_module_filter. Default on.
	HybridModuleFilter EnabledV2 `yaml:"hybrid_module_filter"`
}

// ---------- generation ----------

// GenerationV2 writes tests and documentation.
type GenerationV2 struct {
	// LLM overrides general.llm for test generation. Empty fields fall back.
	LLM StepLLMV2 `yaml:"llm"`
	// Policy shapes what the generator may do and how it is prompted.
	Policy GenerationPolicyV2 `yaml:"policy"`
	// Docs covers the documentation workstream, which runs in this phase.
	Docs GenerationDocsV2 `yaml:"docs"`
}

// StepLLMV2 is a per-step model override. Every field empty = use general.llm.
type StepLLMV2 struct {
	// Provider overrides general.llm.provider for this step. Empty falls back — which is the point
	// of a step override: set only what differs.
	Provider string `yaml:"provider"`
	// Model overrides general.llm.model for this step. Empty falls back.
	Model string `yaml:"model"`
	// APIKey overrides the general credential for this step, in clear text. Empty falls back.
	APIKey string `yaml:"api_key"`
	// APIKeyFromEnv names an environment variable to read that credential from instead.
	APIKeyFromEnv string `yaml:"api_key_from_env"`
}

// GenerationPolicyV2 shapes what the generator may do.
type GenerationPolicyV2 struct {
	// Tools let the generator query the index while writing a test, instead of working only from
	// the context assembled up front.
	Tools GenerationToolsV2 `yaml:"tools"`
	// TwoPhase plans the test before writing it, in two model calls rather than one.
	TwoPhase EnabledV2 `yaml:"two_phase"`
	// StructuredOutput requests a provider JSON schema for generation. Inverts v1's
	// runner.disable_structured_generate_output. Off falls back to code-fence extraction.
	StructuredOutput EnabledV2 `yaml:"structured_output"`
	// ProjectIntel injects the repository's own documentation and agent skill files into context.
	ProjectIntel GenerationProjectIntelV2 `yaml:"project_intel"`
	// Format controls the post-write formatting pass.
	Format GenerationFormatV2 `yaml:"format"`
	// PreferDefaultTestSuffix always emits the convention default path instead of extending an
	// existing test file that uses a different suffix.
	PreferDefaultTestSuffix bool `yaml:"prefer_default_test_suffix"`
	// ReconcileDuplicateTestArtifacts merges a generated artifact into the existing test file it
	// duplicates, instead of leaving two files that test the same symbol. Report-only by default.
	ReconcileDuplicateTestArtifacts bool `yaml:"reconcile_duplicate_test_artifacts"`
}

// GenerationToolsV2 bounds the model→tool→model loop. Zero means "use the built-in cap"; the caps
// stay configurable because raising them is a real deployment lever.
type GenerationToolsV2 struct {
	// Enabled turns native tool calling on for generation.
	Enabled *bool `yaml:"enabled"`
	// PromptedFallback offers the tools through the prompt when the provider has no native tool API.
	PromptedFallback *bool `yaml:"prompted_fallback"`
	// MaxTurns bounds model→tool→model round trips for one gap. 0 = the built-in cap.
	MaxTurns int `yaml:"max_turns"`
	// MaxCallsPerTurn bounds parallel tool calls accepted in one turn. 0 = the built-in cap.
	MaxCallsPerTurn int `yaml:"max_calls_per_turn"`
	// MaxCallsPerRun bounds total calls for one gap. 0 = the built-in cap.
	MaxCallsPerRun int `yaml:"max_calls_per_run"`
	// MaxResultChars caps one tool result, which draws on the same prompt allowance as the context.
	// 0 = the built-in cap.
	MaxResultChars int `yaml:"max_result_chars"`
}

// GenerationProjectIntelV2 injects the repository's own documentation into generation context. The
// scan's tuning is constant (CP37); what a deployment varies is whether it runs and where it looks.
type GenerationProjectIntelV2 struct {
	// Enabled turns the scan on. Default on — it needs no configuration to be useful.
	Enabled *bool `yaml:"enabled"`
	// UseEmbeddingsRank reranks candidate docs by embedding cosine rather than lexical score alone.
	// Without an embedder the run degrades to lexical ranking rather than failing.
	UseEmbeddingsRank bool `yaml:"use_embeddings_rank"`
	// ExtraDocGlobs adds repo-relative globs to treat as documentation on this repository's layout.
	ExtraDocGlobs []string `yaml:"extra_doc_globs"`
	// ExtraSkillGlobs adds globs to treat as agent skill files.
	ExtraSkillGlobs []string `yaml:"extra_skill_globs"`
}

// GenerationFormatV2 controls the post-write formatting pass.
type GenerationFormatV2 struct {
	// OnlyAdded runs the formatter per written file instead of repository-wide, so a run does not
	// reformat code it did not touch. The command is general.build.format_command.
	OnlyAdded bool `yaml:"only_added"`
}

// GenerationDocsV2 covers the documentation workstream, which runs in the generation phase even
// though v1 filed it under indexer.*.
type GenerationDocsV2 struct {
	// LLM overrides general.llm for documentation generation.
	LLM StepLLMV2 `yaml:"llm"`
	// Overview is the repository-level document, written once per run rather than per symbol.
	Overview GenerationOverviewV2 `yaml:"overview"`
}

// GenerationOverviewV2 configures the repository-level document.
type GenerationOverviewV2 struct {
	// Enabled inverts v1's indexer.disable_overview_doc_generation. Default true.
	Enabled *bool `yaml:"enabled"`
	// Path is where the overview is written, repo-relative.
	Path string `yaml:"path"`
	// FullRewrite regenerates the whole overview instead of applying a delta to the existing one.
	FullRewrite bool `yaml:"full_rewrite"`
	// MaxFilesPerSlice caps files described in one overview request. 0 = the built-in cap.
	MaxFilesPerSlice int `yaml:"max_files_per_slice"`
	// MaxIndexRunesPerSlice caps index text in one overview request. 0 = the built-in cap; -1
	// removes the clamp entirely.
	MaxIndexRunesPerSlice int `yaml:"max_index_runes_per_slice"`
}

// ---------- fixer ----------

// FixerV2 owns the evaluate-and-repair loop. Evaluation is part of this step rather than a section
// of its own: what an operator tunes about evaluation is how hard the fixer tries.
type FixerV2 struct {
	// LLM overrides general.llm for repair. Empty fields fall back.
	LLM StepLLMV2 `yaml:"llm"`
	// Iterations bounds how many repair rounds a run may spend.
	Iterations FixerIterationsV2 `yaml:"iterations"`
	// Policy shapes how the fixer works and when it gives up.
	Policy FixerPolicyV2 `yaml:"policy"`
}

// FixerIterationsV2 bounds repair rounds.
type FixerIterationsV2 struct {
	// Max is the ceiling on fix iterations for the run. 0 = the built-in default.
	Max int `yaml:"max"`
	// Start is the budget the first run gets, when a deployment escalates across reruns rather than
	// spending everything at once. 0 = use Max.
	Start int `yaml:"start"`
}

// FixerPolicyV2 shapes how the fixer works and when it gives up.
type FixerPolicyV2 struct {
	// Tools let the fixer look up code while repairing, instead of guessing from the diagnostic.
	Tools FixerToolsV2 `yaml:"tools"`
	// StructuredOutput requests a provider JSON schema for the fixer. Inverts v1's
	// runner.disable_structured_fix_output. The schema also grammar-enforces the targeted-edit
	// response shape; off relies on the prompt and the parser alone.
	StructuredOutput EnabledV2 `yaml:"structured_output"`
	// StructuredUserMessage sends the fix request as structured JSON rather than prose.
	StructuredUserMessage bool `yaml:"structured_user_message"`
	// ErrorLogLLMSummary asks a model to summarise long build output before the repair prompt.
	// Inverts v1's runner.disable_error_log_llm_summary. Default on. The summary is attached
	// BESIDE the raw output, never in place of it.
	ErrorLogLLMSummary EnabledV2 `yaml:"error_log_llm_summary"`
	// DependencySignatureOnly sends dependency signatures rather than whole bodies, so the repair
	// prompt spends its budget on the failing code.
	DependencySignatureOnly bool `yaml:"dependency_signature_only"`
	// CompileOncePerEval compiles the project once per round instead of per artifact.
	CompileOncePerEval bool `yaml:"compile_once_per_eval"`
	// SkipOnInfrastructureFailure gives up immediately when the failure is a missing database or a
	// bad connection string — no repair to the generated test can fix the environment.
	SkipOnInfrastructureFailure bool `yaml:"skip_on_infrastructure_failure"`
	// AbortOnUnrecoverableEnvCompileFailure stops the loop when compilation fails for a reason
	// clearly outside the generated artifact, such as a private feed needing credentials the
	// container cannot supply. Without it every iteration burns identically.
	AbortOnUnrecoverableEnvCompileFailure bool `yaml:"abort_on_unrecoverable_env_compile_failure"`
	// ContextRunesMax caps the repair prompt's file context. 0 = the built-in cap.
	ContextRunesMax int `yaml:"context_runes_max"`
	// Backoff waits between fix attempts, e.g. "5s". Empty = no wait.
	Backoff string `yaml:"backoff"`
	// CircuitBreakers stop a loop that is no longer converging.
	CircuitBreakers FixerCircuitBreakersV2 `yaml:"circuit_breakers"`
}

// FixerToolsV2 bounds the fixer's tool loop, separately from generation's.
type FixerToolsV2 struct {
	// Enabled turns tool calling on for repair.
	Enabled *bool `yaml:"enabled"`
	// MaxTurns bounds model→tool→model round trips per fix attempt. 0 = the built-in cap, which is
	// lower than generation's: a fixer already has the diagnostic.
	MaxTurns int `yaml:"max_turns"`
}

// FixerCircuitBreakersV2 stops a loop that is no longer converging. Non-positive means "unset, use
// the built-in", NOT "disabled" — a threshold of zero would end every loop on its first round.
type FixerCircuitBreakersV2 struct {
	// RepeatStopThreshold stops after this many rounds producing an identical diagnostic.
	RepeatStopThreshold int `yaml:"repeat_stop_threshold"`
	// RecurrenceStopThreshold stops when a previously fixed failure keeps coming back.
	RecurrenceStopThreshold int `yaml:"recurrence_stop_threshold"`
	// NoProgressStopThreshold stops after this many rounds that changed nothing measurable.
	NoProgressStopThreshold int `yaml:"no_progress_stop_threshold"`
	// RepeatedTestFailureThreshold discards a generated test whose failure fingerprint recurs this
	// many times, so one bad artifact cannot hold the whole run unstable.
	RepeatedTestFailureThreshold int `yaml:"repeated_test_failure_threshold"`
}

// EnabledV2 is a single-toggle block. It exists so a knob that is only ever on or off still reads
// as `something.enabled: false` rather than as a bare boolean whose name has to carry the negation.
type EnabledV2 struct {
	// Enabled turns the feature on. Absent means the documented default, which is why it is a
	// pointer: for a default-true toggle, absent and false are different instructions.
	Enabled *bool `yaml:"enabled"`
}
