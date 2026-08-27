package config

import "strings"

// ApplyV2Defaults is the SINGLE defaults mechanism, applied after the parse and after the
// environment overlay.
//
// Only fields whose ABSENCE differs from their zero value appear here, and a `*bool` in the schema
// is the marker for exactly that. A plain `bool` needs no entry: absent and false are the same
// instruction, which is what makes it a plain bool in the first place.
//
// Order matters and is asserted by a test: YAML → env → defaults → translate. Applying defaults
// before the environment overlay would make a default-true toggle impossible to turn off from the
// environment, because the overlay would find a materialised `true` and the operator's `false`
// would be indistinguishable from an unset variable.
func ApplyV2Defaults(s *SchemaV2) {
	if s == nil {
		return
	}
	// Each of these pairs with a v1 field whose effective default was true, so leaving it nil would
	// silently flip behaviour on a config that simply does not mention it.
	defaultTrue(&s.General.LLM.Embeddings.Cache.Enabled)
	defaultTrue(&s.General.Sandbox.Docker.OfflineTest)
	defaultTrue(&s.Retrieval.Context.Compact.Enabled)
	defaultTrue(&s.Retrieval.Policy.Abstention.Enabled)
	defaultTrue(&s.Retrieval.Policy.HybridModuleFilter.Enabled)
	defaultTrue(&s.Generation.Policy.TwoPhase.Enabled)
	defaultTrue(&s.Generation.Policy.StructuredOutput.Enabled)
	defaultTrue(&s.Generation.Policy.ProjectIntel.Enabled)
	defaultTrue(&s.Generation.Docs.Overview.Enabled)
	defaultTrue(&s.Fixer.Policy.StructuredOutput.Enabled)
	defaultTrue(&s.Fixer.Policy.ErrorLogLLMSummary.Enabled)

	// generation.policy.tools.enabled, its prompted fallback and fixer.policy.tools.enabled are
	// deliberately ABSENT from this list. They default OFF, so nil already means what it should, and
	// materialising a false would make "not written" indistinguishable from "written as false" for a
	// reader of the resolved config.
}

func defaultTrue(p **bool) {
	if *p == nil {
		v := true
		*p = &v
	}
}

// boolOr resolves a pointer bool against a fallback.
func boolOr(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

// TranslateV2ToRuntime projects the v2 YAML surface onto the v1-shaped runtime Config.
//
// The runtime struct is retained on purpose. Every consumer in the tree reads `config.Config` and
// every helper hangs off it — ActiveShip, CloneAuthTokenForURL, poolMaxConns, EffectiveProjectIntel —
// so rewriting the operator-facing schema and the internal runtime shape in one step would make a
// schema change indistinguishable from a consumer regression. This function is the seam: v2 is what
// an operator writes, Config is what the pipeline reads, and everything that moved, merged or
// inverted between them happens HERE and nowhere else.
//
// Three classes of change live in this function, each covered by a table in the translate test:
//
//   - RENAMES: a key whose home moved (runner.eval_profile → general.build.toolchain).
//   - MERGES: four VCS provider blocks collapsing onto one; per-step LLM prefixes becoming blocks.
//   - INVERSIONS: eight positive v2 toggles feeding negative v1 fields. These are the one-line
//     silent-bug surface of the whole restructure, which is why every one is enumerated rather than
//     left to review.
func TranslateV2ToRuntime(s *SchemaV2) *Config {
	c := &Config{}
	if s == nil {
		return c
	}
	c.ClientID = s.ClientID

	translateGeneral(s, c)
	translateBootstrap(s, c)
	translateIndexer(s, c)
	translateRetrieval(s, c)
	translateGeneration(s, c)
	translateFixer(s, c)
	return c
}

func translateGeneral(s *SchemaV2, c *Config) {
	g := s.General

	c.Database.MetadataURL = g.Database.MetadataURL
	c.Database.EmbeddingsURL = g.Database.EmbeddingsURL
	c.Database.MaxOpenConns = g.Database.MaxOpenConns
	// MOVED: database.embeddings_dimension → general.llm.embeddings.dimension, next to the model
	// that determines it. v1 filed it under database, arbitrarily far from anything that could tell
	// you what value it should have.
	c.Database.EmbeddingsDimension = g.LLM.Embeddings.Dimension

	translateGit(g.Git, c)

	c.LLM.Provider = g.LLM.Provider
	c.LLM.Model = g.LLM.Model
	c.LLM.APIKey = g.LLM.APIKey
	c.LLM.APIKeyFromEnv = g.LLM.APIKeyFromEnv
	c.LLM.BaseURL = g.LLM.BaseURL
	c.LLM.MaxConcurrent = g.LLM.MaxConcurrent
	c.LLM.OllamaNumCtx = g.LLM.OllamaNumCtx
	c.LLM.HTTPTimeout = g.LLM.HTTP.Timeout
	c.LLM.HTTPResponseHeaderTimeout = g.LLM.HTTP.ResponseHeaderTimeout
	c.LLM.HTTPDisableKeepAlives = g.LLM.HTTP.DisableKeepAlives
	c.LLM.EmbeddingProvider = g.LLM.Embeddings.Provider
	c.LLM.EmbeddingModel = g.LLM.Embeddings.Model
	c.LLM.EmbeddingAPIKey = g.LLM.Embeddings.APIKey
	c.LLM.EmbeddingAPIKeyFromEnv = g.LLM.Embeddings.APIKeyFromEnv
	c.LLM.EmbeddingFallback = g.LLM.Embeddings.Fallback
	c.LLM.EmbeddingCacheRetentionDays = g.LLM.Embeddings.Cache.RetentionDays
	// INVERSION 1/8: embeddings.cache.enabled → disable_embedding_cache.
	c.LLM.DisableEmbeddingCache = !boolOr(g.LLM.Embeddings.Cache.Enabled, true)

	c.Runner.EvalProfile = g.Build.Toolchain
	c.Runner.BuildTool = g.Build.BuildTool
	c.Runner.CompileCommand = g.Build.CompileCommand
	c.Runner.TestCommand = g.Build.TestCommand
	c.Runner.UnitTestCommand = g.Build.UnitTestCommand
	c.Runner.E2ETestCommand = g.Build.E2ETestCommand
	c.Runner.FormatCommand = g.Build.FormatCommand
	c.Runner.DotNetFallbackTargetFramework = g.Build.DotNetFallbackTargetFramework
	// MOVED: the mono-repo workspace pair lived under indexer.* in v1, but it scopes the build and
	// the test tree as much as the scan, so it belongs with the build description.
	c.Indexer.MonoRepoWorkspace = g.Build.Workspace.Path
	c.Indexer.MonoRepoTestWorkspace = g.Build.Workspace.TestPath

	translateSandbox(g.Sandbox, c)

	c.Runner.HumanInTheLoopEmail = g.Notifications.HumanInTheLoopEmail
	c.Runner.SMTPHost = g.Notifications.SMTP.Host
	c.Runner.SMTPPort = g.Notifications.SMTP.Port
	c.Runner.SMTPUser = g.Notifications.SMTP.User
	c.Runner.SMTPPassword = g.Notifications.SMTP.Password
	c.Runner.SMTPFrom = g.Notifications.SMTP.From

	c.Audit.FilePath = g.Audit.FilePath
	c.Audit.DumpPrompts = g.Audit.DumpPrompts

	c.WebSearch.Enabled = g.WebSearch.Enabled
	c.WebSearch.Offline = g.WebSearch.Offline
	c.WebSearch.Provider = g.WebSearch.Provider
	c.WebSearch.Endpoint = g.WebSearch.Endpoint
	c.WebSearch.APIKey = g.WebSearch.APIKey
	c.WebSearch.APIKeyFromEnv = g.WebSearch.APIKeyFromEnv
	c.WebSearch.AllowedHosts = g.WebSearch.AllowedHosts
}

// translateGit is the MERGE that motivated the whole section: v1's four provider blocks become one.
//
// The runtime keeps its four-block shape because ActiveVCSToken, ActiveShip and
// ActiveDefaultOwnerRepo select among them, so the single v2 block is written into whichever block
// the active provider names. Writing it into ALL four would be simpler and wrong: CloneAuthTokenForURL
// falls back across providers by URL, so a token meant for GitHub would start authenticating
// Bitbucket clones.
func translateGit(g GitV2, c *Config) {
	c.VCS.Provider = g.Provider
	// Azure's coordinates and PAT are honoured regardless of the active provider, because a
	// repository can live on dev.azure.com while the run's provider is something else.
	c.VCS.AzureDevOps.Organization = g.AzureDevOps.Organization
	c.VCS.AzureDevOps.Project = g.AzureDevOps.Project
	c.VCS.AzureDevOps.Repository = g.AzureDevOps.Repository
	c.VCS.AzureDevOps.Token = g.AzureDevOps.Token

	switch strings.ToLower(strings.TrimSpace(g.Provider)) {
	case "gitlab":
		c.VCS.GitLab.Token = g.Token
		c.VCS.GitLab.BaseURL = g.BaseURL
		c.VCS.GitLab.DefaultNamespace = g.DefaultOwner
		c.VCS.GitLab.DefaultProject = g.DefaultRepo
		c.VCS.GitLab.Ship = g.Ship
	case "bitbucket":
		c.VCS.Bitbucket.Token = g.Token
		c.VCS.Bitbucket.BaseURL = g.BaseURL
		c.VCS.Bitbucket.DefaultWorkspace = g.DefaultOwner
		c.VCS.Bitbucket.DefaultRepo = g.DefaultRepo
		c.VCS.Bitbucket.Ship = g.Ship
	case "azure_devops", "azuredevops":
		if strings.TrimSpace(c.VCS.AzureDevOps.Token) == "" {
			c.VCS.AzureDevOps.Token = g.Token
		}
		c.VCS.AzureDevOps.BaseURL = g.BaseURL
		if strings.TrimSpace(c.VCS.AzureDevOps.Organization) == "" {
			c.VCS.AzureDevOps.Organization = g.DefaultOwner
		}
		if strings.TrimSpace(c.VCS.AzureDevOps.Repository) == "" {
			c.VCS.AzureDevOps.Repository = g.DefaultRepo
		}
		c.VCS.AzureDevOps.Ship = g.Ship
	default:
		// GitHub is the default provider, matching v1's Active*() accessors, whose default arm is
		// GitHub rather than an error.
		c.VCS.GitHub.Token = g.Token
		c.VCS.GitHub.BaseURL = g.BaseURL
		c.VCS.GitHub.DefaultOwner = g.DefaultOwner
		c.VCS.GitHub.DefaultRepo = g.DefaultRepo
		c.VCS.GitHub.Ship = g.Ship
	}
}

func translateSandbox(sb SandboxV2, c *Config) {
	c.Runner.Type = sb.Type
	c.Runner.Timeout = sb.Timeout
	c.Runner.DockerBinary = sb.Docker.Binary
	// INVERSION 2/8: sandbox.docker.offline_test.enabled → docker_disable_offline_test.
	c.Runner.DockerDisableOfflineTest = !boolOr(sb.Docker.OfflineTest, true)
	c.Runner.RequireDockerBootstrap = sb.Docker.RequireBootstrap

	c.Runner.ImageJava = sb.Images.Java
	c.Runner.ImageJavaMaven = sb.Images.JavaMaven
	c.Runner.ImageJavaGradle = sb.Images.JavaGradle
	c.Runner.ImageNode = sb.Images.Node
	c.Runner.ImageDotNet = sb.Images.DotNet
	c.Runner.ImagePlaywright = sb.Images.Playwright
	c.Runner.ImagePlaywrightJava = sb.Images.PlaywrightJava
	c.Runner.ImagePlaywrightDotnet = sb.Images.PlaywrightDotNet

	c.Runner.JobCPUs = sb.Resources.CPUs
	c.Runner.JobMemory = sb.Resources.Memory
	c.Runner.JobPidsLimit = int64(sb.Resources.PidsLimit)
	c.Runner.JobReadonlyRootfs = sb.Resources.ReadonlyRootfs

	c.Runner.JobNetworkRestore = sb.Network.Restore
	c.Runner.JobNetworkTest = sb.Network.Test

	c.Runner.CacheMavenHost = sb.Caches.Maven
	c.Runner.CacheGradleHost = sb.Caches.Gradle
	c.Runner.CacheNpmHost = sb.Caches.Npm
	c.Runner.CachePnpmHost = sb.Caches.Pnpm
	c.Runner.CacheNuGetHost = sb.Caches.NuGet
	c.Runner.CacheCypressHost = sb.Caches.Cypress

	c.Runner.AzureDevOpsNuGetFeedEndpoints = sb.Registries.AzureDevOpsNuGetFeedEndpoints
	c.Runner.PrivateRegistryCredentials = sb.Registries.Credentials
}

func translateBootstrap(s *SchemaV2, c *Config) {
	c.Runner.TestFrameworkBootstrap.Enabled = s.Bootstrap.TestFramework.Enabled
	c.Runner.TestFrameworkBootstrap.Mode = s.Bootstrap.TestFramework.Mode
	c.Runner.TestFrameworkBootstrap.PinVersions = s.Bootstrap.TestFramework.PinVersions
	c.Runner.TestFrameworkBootstrap.AllowLockfileChange = s.Bootstrap.TestFramework.AllowLockfileChange
	c.Runner.TestFrameworkBootstrap.Execution = s.Bootstrap.TestFramework.Execution

	c.Runner.E2EFrameworkBootstrap.Enabled = s.Bootstrap.E2EFramework.Enabled
	c.Runner.E2EFrameworkBootstrap.Mode = s.Bootstrap.E2EFramework.Mode
	c.Runner.E2EFrameworkBootstrap.PinVersions = s.Bootstrap.E2EFramework.PinVersions
	c.Runner.E2EFrameworkBootstrap.AllowLockfileChange = s.Bootstrap.E2EFramework.AllowLockfileChange
	c.Runner.E2EFrameworkBootstrap.Execution = s.Bootstrap.E2EFramework.Execution
}

func translateIndexer(s *SchemaV2, c *Config) {
	ix := s.Indexer
	c.Indexer.Execution = ix.Execution
	c.Indexer.AdvancedJarPath = ix.Java.JarPath
	c.Indexer.Type = resolveJavaIndexerType(ix.Java)
	c.Indexer.JSTIndexerPath = ix.JSTS.IndexerPath
	c.Indexer.CSharpIndexerDllPath = ix.CSharp.IndexerDLLPath

	c.Indexer.DockerCLI = ix.Docker.CLI
	c.Indexer.DockerMemory = ix.Docker.Memory
	c.Indexer.DockerJavaImage = ix.Docker.JavaImage
	c.Indexer.DockerNodeImage = ix.Docker.NodeImage
	c.Indexer.DockerDotNetIndexerImage = ix.Docker.DotNetIndexerImage
	c.Indexer.DockerNodeHeapMB = ix.Docker.NodeHeapMB

	c.Indexer.DependencyDocs.Enabled = ix.DependencyDocs.Enabled
	c.Indexer.DependencyDocs.MavenRepoDir = ix.DependencyDocs.MavenRepoDir
	c.Indexer.DependencyDocs.NuGetPackagesDir = ix.DependencyDocs.NuGetPackagesDir

	c.Indexer.MaxGaps = ix.Policy.MaxGaps
	c.Indexer.MaxGapsE2E = ix.Policy.MaxGapsE2E
	c.Indexer.MaxGapsPerFile = ix.Policy.MaxGapsPerFile
	c.Indexer.MaxGapsPerFileE2E = ix.Policy.MaxGapsPerFileE2E
	c.Indexer.CriticalModulePrefixes = ix.Policy.CriticalModulePrefixes
	c.Indexer.SkipPathPrefixes = ix.Policy.SkipPathPrefixes
}

func translateRetrieval(s *SchemaV2, c *Config) {
	r := s.Retrieval
	c.Retrieval.Profile = r.Profile
	c.Retrieval.ProfileE2E = r.ProfileE2E
	c.Retrieval.ProfileBudgets = r.ProfileBudgets
	c.Retrieval.Fusion = r.Fusion
	c.Retrieval.MaxContextTokens = r.MaxContextTokens
	c.Retrieval.DependencyMaxDepth = r.Context.DependencyMaxDepth
	c.Retrieval.FailureHintFile = r.FailureHint.File
	c.Retrieval.PersistLastEvalFailure = r.FailureHint.Persist
	c.Retrieval.MinSimilarTestsForGeneration = r.Policy.MinSimilarTestsForGeneration
	c.Retrieval.MinSimilarityCosine = r.Policy.MinSimilarityCosine

	// INVERSION 3/8: retrieval.policy.abstention.enabled → abstention_disabled.
	c.Retrieval.AbstentionDisabled = !boolOr(r.Policy.Abstention.Enabled, true)
	// INVERSION 4/8: retrieval.policy.hybrid_module_filter.enabled → disable_hybrid_module_filter.
	c.Retrieval.DisableHybridModuleFilter = !boolOr(r.Policy.HybridModuleFilter.Enabled, true)
	// NOT an inversion: context_compact.enabled was already positive in v1, and stays a *bool
	// because absent means on.
	c.Retrieval.ContextCompact.Enabled = r.Context.Compact.Enabled
}

func translateGeneration(s *SchemaV2, c *Config) {
	g := s.Generation
	c.LLM.GenerationProvider = g.LLM.Provider
	c.LLM.GenerationModel = g.LLM.Model
	c.LLM.GenerationAPIKey = g.LLM.APIKey
	c.LLM.GenerationAPIKeyFromEnv = g.LLM.APIKeyFromEnv

	c.LLM.DocProvider = g.Docs.LLM.Provider
	c.LLM.DocModel = g.Docs.LLM.Model
	c.LLM.DocAPIKey = g.Docs.LLM.APIKey
	c.LLM.DocAPIKeyFromEnv = g.Docs.LLM.APIKeyFromEnv

	c.Generation.ToolsEnabled = boolOr(g.Policy.Tools.Enabled, false)
	c.Generation.PromptedToolsEnabled = boolOr(g.Policy.Tools.PromptedFallback, false)
	c.Generation.MaxToolTurns = g.Policy.Tools.MaxTurns
	c.Generation.MaxToolCallsPerTurn = g.Policy.Tools.MaxCallsPerTurn
	c.Generation.MaxToolCallsPerRun = g.Policy.Tools.MaxCallsPerRun
	c.Generation.MaxToolResultChars = g.Policy.Tools.MaxResultChars

	c.Runner.TwoPhaseTestGeneration = boolOr(g.Policy.TwoPhase.Enabled, true)
	// INVERSION 5/8: generation.policy.structured_output.enabled → disable_structured_generate_output.
	c.Runner.DisableStructuredGenerateOutput = !boolOr(g.Policy.StructuredOutput.Enabled, true)
	c.Runner.FormatOnlyAdded = g.Policy.Format.OnlyAdded
	c.Runner.PreferDefaultTestSuffix = g.Policy.PreferDefaultTestSuffix
	c.Runner.ReconcileDuplicateTestArtifacts = g.Policy.ReconcileDuplicateTestArtifacts

	pi := ProjectIntelConfig{
		Enabled:           g.Policy.ProjectIntel.Enabled,
		UseEmbeddingsRank: g.Policy.ProjectIntel.UseEmbeddingsRank,
		ExtraDocGlobs:     g.Policy.ProjectIntel.ExtraDocGlobs,
		ExtraSkillGlobs:   g.Policy.ProjectIntel.ExtraSkillGlobs,
	}
	c.Runner.ProjectIntel = pi

	// INVERSION 6/8: generation.docs.overview.enabled → disable_overview_doc_generation.
	c.Indexer.DisableOverviewDocGeneration = !boolOr(g.Docs.Overview.Enabled, true)
	c.Indexer.OverviewDocPath = g.Docs.Overview.Path
	c.Indexer.OverviewFullRewrite = g.Docs.Overview.FullRewrite
	c.Indexer.OverviewMaxFilesPerSlice = g.Docs.Overview.MaxFilesPerSlice
	c.Indexer.OverviewMaxIndexRunesPerSlice = g.Docs.Overview.MaxIndexRunesPerSlice
}

func translateFixer(s *SchemaV2, c *Config) {
	f := s.Fixer
	c.LLM.FixerProvider = f.LLM.Provider
	c.LLM.FixerModel = f.LLM.Model
	c.LLM.FixerAPIKey = f.LLM.APIKey
	c.LLM.FixerAPIKeyFromEnv = f.LLM.APIKeyFromEnv

	c.Runner.MaxIteration = f.Iterations.Max
	c.Runner.StartMaxIteration = f.Iterations.Start

	c.Generation.FixerToolsEnabled = boolOr(f.Policy.Tools.Enabled, false)
	c.Generation.FixerMaxToolTurns = f.Policy.Tools.MaxTurns

	// INVERSION 7/8: fixer.policy.structured_output.enabled → disable_structured_fix_output.
	c.Runner.DisableStructuredFixOutput = !boolOr(f.Policy.StructuredOutput.Enabled, true)
	// INVERSION 8/8: fixer.policy.error_log_llm_summary.enabled → disable_error_log_llm_summary.
	c.Runner.DisableErrorLogLLMSummary = !boolOr(f.Policy.ErrorLogLLMSummary.Enabled, true)

	c.Runner.FixerStructuredUserMessage = f.Policy.StructuredUserMessage
	c.Runner.FixerDependencySignatureOnly = f.Policy.DependencySignatureOnly
	c.Runner.CompileOncePerEval = f.Policy.CompileOncePerEval
	c.Runner.SkipFixerOnInfrastructureFailure = f.Policy.SkipOnInfrastructureFailure
	c.Runner.AbortOnUnrecoverableEnvCompileFailure = f.Policy.AbortOnUnrecoverableEnvCompileFailure
	c.Runner.FixContextRunesMax = f.Policy.ContextRunesMax
	c.Runner.FixBackoff = f.Policy.Backoff

	c.Runner.FixLoopRepeatStopThreshold = f.Policy.CircuitBreakers.RepeatStopThreshold
	c.Runner.FixLoopRecurrenceStopThreshold = f.Policy.CircuitBreakers.RecurrenceStopThreshold
	c.Runner.FixLoopNoProgressStopThreshold = f.Policy.CircuitBreakers.NoProgressStopThreshold
	c.Runner.RepeatedTestFailureThreshold = f.Policy.CircuitBreakers.RepeatedTestFailureThreshold
}

// resolveJavaIndexerType turns indexer.java.mode into the runtime's indexer type.
//
// "auto" and the empty string resolve from the JAR path, which is what closes v1's two-key trap: a
// configured path is now sufficient to get advanced indexing. An explicit mode always wins, so
// "minimal" with a path configured stays minimal — someone who asks for the heuristics gets them.
func resolveJavaIndexerType(j IndexerJavaV2) string {
	switch strings.ToLower(strings.TrimSpace(j.Mode)) {
	case "advanced":
		return "advanced"
	case "minimal":
		return "minimal"
	}
	if strings.TrimSpace(j.JarPath) != "" {
		return "advanced"
	}
	return "minimal"
}
