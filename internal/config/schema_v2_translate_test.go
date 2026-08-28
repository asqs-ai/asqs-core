package config

import (
	"strings"
	"testing"
)

func ptrBool(b bool) *bool { return &b }

// The INVERSIONS are the one-line silent-bug surface of the whole restructure: a v2 key reads
// `enabled`, the runtime field reads `disable_*`, and a missing `!` flips a feature for every
// deployment without changing a single visible key. Each is pinned in BOTH directions, because a
// translator that hard-coded either constant would pass a one-directional test.
func TestTranslate_everyInversion(t *testing.T) {
	cases := []struct {
		name string
		// set writes the v2 side; read returns the runtime side.
		set  func(*SchemaV2, bool)
		read func(*Config) bool
	}{
		{
			name: "general.llm.embeddings.cache.enabled -> DisableEmbeddingCache",
			set:  func(s *SchemaV2, v bool) { s.General.LLM.Embeddings.Cache.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.LLM.DisableEmbeddingCache },
		},
		{
			name: "general.sandbox.docker.offline_test -> DockerDisableOfflineTest",
			set:  func(s *SchemaV2, v bool) { s.General.Sandbox.Docker.OfflineTest = ptrBool(v) },
			read: func(c *Config) bool { return c.Runner.DockerDisableOfflineTest },
		},
		{
			name: "retrieval.policy.abstention.enabled -> AbstentionDisabled",
			set:  func(s *SchemaV2, v bool) { s.Retrieval.Policy.Abstention.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Retrieval.AbstentionDisabled },
		},
		{
			name: "retrieval.policy.hybrid_module_filter.enabled -> DisableHybridModuleFilter",
			set:  func(s *SchemaV2, v bool) { s.Retrieval.Policy.HybridModuleFilter.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Retrieval.DisableHybridModuleFilter },
		},
		{
			name: "generation.policy.structured_output.enabled -> DisableStructuredGenerateOutput",
			set:  func(s *SchemaV2, v bool) { s.Generation.Policy.StructuredOutput.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Runner.DisableStructuredGenerateOutput },
		},
		{
			name: "generation.docs.overview.enabled -> DisableOverviewDocGeneration",
			set:  func(s *SchemaV2, v bool) { s.Generation.Docs.Overview.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Indexer.DisableOverviewDocGeneration },
		},
		{
			name: "fixer.policy.structured_output.enabled -> DisableStructuredFixOutput",
			set:  func(s *SchemaV2, v bool) { s.Fixer.Policy.StructuredOutput.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Runner.DisableStructuredFixOutput },
		},
		{
			name: "fixer.policy.error_log_llm_summary.enabled -> DisableErrorLogLLMSummary",
			set:  func(s *SchemaV2, v bool) { s.Fixer.Policy.ErrorLogLLMSummary.Enabled = ptrBool(v) },
			read: func(c *Config) bool { return c.Runner.DisableErrorLogLLMSummary },
		},
	}

	if len(cases) != 8 {
		t.Fatalf("the schema documents 8 inversions; this table has %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, enabled := range []bool{true, false} {
				var s SchemaV2
				tc.set(&s, enabled)
				got := tc.read(TranslateV2ToRuntime(&s))
				if got != !enabled {
					t.Errorf("enabled=%v produced disable=%v; the inversion is missing or doubled", enabled, got)
				}
			}
		})
	}
}

// Absence must resolve to the DOCUMENTED default, not to the zero value. This is the failure the
// *bool rule exists to prevent: a default-true toggle whose absence silently reads as false turns a
// feature off for every config that simply does not mention it.
func TestTranslate_absenceUsesTheDocumentedDefault(t *testing.T) {
	var s SchemaV2
	ApplyV2Defaults(&s)
	c := TranslateV2ToRuntime(&s)

	offByDefault := map[string]bool{
		"DisableEmbeddingCache":           c.LLM.DisableEmbeddingCache,
		"DockerDisableOfflineTest":        c.Runner.DockerDisableOfflineTest,
		"AbstentionDisabled":              c.Retrieval.AbstentionDisabled,
		"DisableHybridModuleFilter":       c.Retrieval.DisableHybridModuleFilter,
		"DisableStructuredGenerateOutput": c.Runner.DisableStructuredGenerateOutput,
		"DisableOverviewDocGeneration":    c.Indexer.DisableOverviewDocGeneration,
		"DisableStructuredFixOutput":      c.Runner.DisableStructuredFixOutput,
		"DisableErrorLogLLMSummary":       c.Runner.DisableErrorLogLLMSummary,
	}
	for name, disabled := range offByDefault {
		if disabled {
			t.Errorf("%s is true on an empty config; the feature defaults ON, so absence must not disable it", name)
		}
	}
	if !c.Runner.TwoPhaseTestGeneration {
		t.Error("two-phase generation defaults on; an empty config turned it off")
	}
	// Tools default OFF, and that must survive the same path — a blanket "default everything true"
	// would be just as wrong in the other direction.
	if c.Generation.ToolsEnabled || c.Generation.FixerToolsEnabled {
		t.Error("tool calling defaults off; an empty config turned it on")
	}
}

// The VCS merge writes ONE v2 block into whichever provider block the runtime will read. Writing it
// into all four would be simpler and wrong: CloneAuthTokenForURL falls back across providers by URL,
// so a token meant for GitHub would start authenticating Bitbucket clones.
func TestTranslate_gitMergeTargetsOnlyTheActiveProvider(t *testing.T) {
	cases := map[string]func(*Config) string{
		"github":       func(c *Config) string { return c.VCS.GitHub.Token },
		"gitlab":       func(c *Config) string { return c.VCS.GitLab.Token },
		"bitbucket":    func(c *Config) string { return c.VCS.Bitbucket.Token },
		"azure_devops": func(c *Config) string { return c.VCS.AzureDevOps.Token },
	}
	for provider, read := range cases {
		t.Run(provider, func(t *testing.T) {
			var s SchemaV2
			s.General.Git.Provider = provider
			s.General.Git.Token = "tok-" + provider
			s.General.Git.DefaultOwner = "acme"
			s.General.Git.DefaultRepo = "widgets"
			c := TranslateV2ToRuntime(&s)

			if got := read(c); got != "tok-"+provider {
				t.Errorf("active provider %s did not receive the token: %q", provider, got)
			}
			var others int
			for other, otherRead := range cases {
				if other == provider {
					continue
				}
				if otherRead(c) != "" {
					t.Errorf("token leaked into inactive provider %s", other)
					others++
				}
			}
			if others > 0 {
				t.Errorf("a token written to %d inactive provider block(s) would authenticate the wrong host", others)
			}
		})
	}
}

// Each provider spells owner/repo differently, and v2 collapses all three spellings onto one pair.
func TestTranslate_gitOwnerRepoReachesEachProvidersOwnFieldNames(t *testing.T) {
	mk := func(provider string) *Config {
		var s SchemaV2
		s.General.Git.Provider = provider
		s.General.Git.DefaultOwner = "acme"
		s.General.Git.DefaultRepo = "widgets"
		return TranslateV2ToRuntime(&s)
	}
	if c := mk("gitlab"); c.VCS.GitLab.DefaultNamespace != "acme" || c.VCS.GitLab.DefaultProject != "widgets" {
		t.Errorf("gitlab namespace/project = %q/%q", c.VCS.GitLab.DefaultNamespace, c.VCS.GitLab.DefaultProject)
	}
	if c := mk("bitbucket"); c.VCS.Bitbucket.DefaultWorkspace != "acme" || c.VCS.Bitbucket.DefaultRepo != "widgets" {
		t.Errorf("bitbucket workspace/repo = %q/%q", c.VCS.Bitbucket.DefaultWorkspace, c.VCS.Bitbucket.DefaultRepo)
	}
	if c := mk("github"); c.VCS.GitHub.DefaultOwner != "acme" || c.VCS.GitHub.DefaultRepo != "widgets" {
		t.Errorf("github owner/repo = %q/%q", c.VCS.GitHub.DefaultOwner, c.VCS.GitHub.DefaultRepo)
	}
}

// Azure's own token block is honoured whatever the active provider is, because a repository can live
// on dev.azure.com while the run's provider is something else — that is why v2 kept a second token
// rather than collapsing it into git.token with the rest.
func TestTranslate_azureTokenSurvivesADifferentActiveProvider(t *testing.T) {
	var s SchemaV2
	s.General.Git.Provider = "github"
	s.General.Git.Token = "gh-token"
	s.General.Git.AzureDevOps.Token = "azure-pat"
	c := TranslateV2ToRuntime(&s)

	if c.VCS.AzureDevOps.Token != "azure-pat" {
		t.Errorf("azure PAT = %q; it must survive a github-provider run", c.VCS.AzureDevOps.Token)
	}
	if c.VCS.GitHub.Token != "gh-token" {
		t.Errorf("github token = %q", c.VCS.GitHub.Token)
	}
}

// The MOVES: keys whose home changed. Each is a place an operator's value could silently stop
// arriving, since the v2 name gives no hint of the v1 field it feeds.
func TestTranslate_movedKeysReachTheirRuntimeHome(t *testing.T) {
	var s SchemaV2
	s.General.LLM.Embeddings.Dimension = 768
	s.General.Build.Toolchain = "java-maven"
	s.General.Build.Workspace.Path = "services/api"
	s.General.Build.Workspace.TestPath = "services/api-tests"
	s.Generation.Docs.Overview.Path = "docs/OVERVIEW.md"
	s.Retrieval.Context.DependencyMaxDepth = 4
	s.Retrieval.FailureHint.File = ".asqs/ci.log"
	s.Fixer.Policy.CircuitBreakers.RepeatedTestFailureThreshold = 7
	c := TranslateV2ToRuntime(&s)

	checks := []struct {
		what string
		got  interface{}
		want interface{}
	}{
		{"general.llm.embeddings.dimension → Database.EmbeddingsDimension", c.Database.EmbeddingsDimension, 768},
		{"general.build.toolchain → Runner.EvalProfile", c.Runner.EvalProfile, "java-maven"},
		{"general.build.workspace.path → Indexer.MonoRepoWorkspace", c.Indexer.MonoRepoWorkspace, "services/api"},
		{"general.build.workspace.test_path → Indexer.MonoRepoTestWorkspace", c.Indexer.MonoRepoTestWorkspace, "services/api-tests"},
		{"generation.docs.overview.path → Indexer.OverviewDocPath", c.Indexer.OverviewDocPath, "docs/OVERVIEW.md"},
		{"retrieval.context.dependency_max_depth → Retrieval.DependencyMaxDepth", c.Retrieval.DependencyMaxDepth, 4},
		{"retrieval.failure_hint.file → Retrieval.FailureHintFile", c.Retrieval.FailureHintFile, ".asqs/ci.log"},
		{"fixer…repeated_test_failure_threshold → Runner.RepeatedTestFailureThreshold", c.Runner.RepeatedTestFailureThreshold, 7},
	}
	for _, c2 := range checks {
		if c2.got != c2.want {
			t.Errorf("%s: got %v, want %v", c2.what, c2.got, c2.want)
		}
	}
}

// A v1 file must fail with a message that names what moved. The generic strict-decode error fires on
// whichever section happens to come first and reads like a bug in ASQS rather than a stale file.
func TestLoader_rejectsV1AndNamesTheMovedSections(t *testing.T) {
	v1 := []byte("database:\n  metadata_url: postgres://x\nvcs:\n  provider: github\nrunner:\n  type: local\n")
	_, err := UnmarshalSchemaV2(v1)
	if err == nil {
		t.Fatal("a v1 config loaded successfully; the whole point of the strict loader is that it cannot")
	}
	msg := err.Error()
	for _, want := range []string{"pre-v2 layout", "general.git", "general.database", "config.example.yaml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not mention %q:\n%s", want, msg)
		}
	}
}

// A v2 config that merely declares a section name v1 also used must NOT be reported as v1. That
// message would confidently send an operator to rewrite a file that was already correct.
func TestLoader_v2ConfigIsNotMistakenForV1(t *testing.T) {
	v2 := []byte("general:\n  database:\n    metadata_url: postgres://x\nretrieval:\n  policy:\n    min_similarity_cosine: 0.2\nindexer:\n  policy:\n    max_gaps: 5\n")
	s, err := UnmarshalSchemaV2(v2)
	if err != nil {
		t.Fatalf("a valid v2 config failed to load: %v", err)
	}
	if s.Indexer.Policy.MaxGaps != 5 {
		t.Errorf("max_gaps = %d, want 5", s.Indexer.Policy.MaxGaps)
	}
}

// A typo must fail and name its own path. Under v1 this was silently ignored, which is the same
// invisible-no-op failure the restructure exists to remove — except self-inflicted.
func TestLoader_typoFailsAndNamesThePath(t *testing.T) {
	_, err := UnmarshalSchemaV2([]byte("general:\n  database:\n    metadata_urls: postgres://x\n"))
	if err == nil {
		t.Fatal("a misspelled key loaded silently")
	}
	if !strings.Contains(err.Error(), "metadata_urls") {
		t.Errorf("the error does not name the offending key:\n%v", err)
	}
}

// schema_version is the anchor a future schema change branches on.
func TestLoader_rejectsUnknownSchemaVersion(t *testing.T) {
	if _, err := UnmarshalSchemaV2([]byte("schema_version: 3\n")); err == nil {
		t.Error("schema_version 3 was accepted by a build that reads version 2")
	}
	if _, err := UnmarshalSchemaV2([]byte("schema_version: 2\ngeneral:\n  database:\n    metadata_url: x\n")); err != nil {
		t.Errorf("schema_version 2 rejected: %v", err)
	}
	// Absent means current, so a file need not carry the key at all.
	if _, err := UnmarshalSchemaV2([]byte("general:\n  database:\n    metadata_url: x\n")); err != nil {
		t.Errorf("absent schema_version rejected: %v", err)
	}
}

// v1 required BOTH `indexer.type: advanced` AND a jar path, so a deployment that had built the JAR
// and configured its path but never set the type silently got line-based indexing — full AST and
// symbol resolution quietly replaced by heuristics, with nothing in the run to say so. Setting the
// path is now sufficient, and an explicit mode still wins in both directions.
func TestTranslate_javaIndexerModeClosesTheTwoKeyTrap(t *testing.T) {
	cases := []struct {
		mode, jar, want string
	}{
		{"", "", "minimal"},
		{"", "tools/java-indexer/x.jar", "advanced"}, // the trap: a path alone is now enough
		{"auto", "tools/java-indexer/x.jar", "advanced"},
		{"auto", "", "minimal"},
		{"advanced", "", "advanced"},                       // explicit wins even with no path
		{"minimal", "tools/java-indexer/x.jar", "minimal"}, // asking for heuristics gets heuristics
		{"MINIMAL", "x.jar", "minimal"},                    // case-insensitive
	}
	for _, tc := range cases {
		var s SchemaV2
		s.Indexer.Java.Mode = tc.mode
		s.Indexer.Java.JarPath = tc.jar
		if got := TranslateV2ToRuntime(&s).Indexer.Type; got != tc.want {
			t.Errorf("mode=%q jar=%q -> %q, want %q", tc.mode, tc.jar, got, tc.want)
		}
	}
}
