package config

// Environment-only settings: everything asqs-core reads from the environment that is NOT part of the
// YAML schema.
//
// Since CP38 every schema key has a DERIVED environment variable, so this list is deliberately short
// and everything on it is here for a specific reason: it is a break-glass switch that should not be
// easy to leave on in a config file, a variable the platform or a toolchain already defines that
// asqs-core reads rather than reinventing, or a test input.
//
// The cost of env-only is discoverability — nothing else lists these — which is what this registry
// and its appendix in the generated reference pay back.
//
// The list is CHECKED, never trusted: TestEnvOnlySwitchesAreComplete sweeps every direct os.Getenv
// and os.LookupEnv call under cmd/, internal/ and tools/ and fails when this file is stale.

// EnvOnlySwitch is one environment-only setting.
type EnvOnlySwitch struct {
	// Name is the variable, e.g. "ASQS_ALLOW_EMBEDDING_DIM_RESET".
	Name string
	// Component says which package reads it.
	Component string
	// Kind separates asqs-core's own settings from variables it merely INHERITS from the environment
	// (PATH, NUGET_PACKAGES, AWS_REGION …) and from test-only inputs. Without that distinction an
	// operator reading the appendix cannot tell which of these they are expected to set.
	// One of: "asqs", "inherited", "test".
	Kind string
	// Security marks a switch that WEAKENS a protection when set, so the reference can flag it rather
	// than listing it beside ordinary tuning.
	Security bool
	// Doc explains what setting it does.
	Doc string
}

// EnvOnlySwitches is the registry. Its completeness is enforced by
// `go test ./internal/config/ -run TestEnvOnlySwitchesAreComplete`.
var EnvOnlySwitches = []EnvOnlySwitch{
	{Name: "ASQS_ALLOW_EMBEDDING_DIM_RESET", Component: "internal/storage/embeddings", Kind: "asqs", Security: true,
		Doc: "Break glass: permits changing the embedding dimension on a POPULATED table, which drops every stored vector. " +
			"Deliberately NOT a config key — an operator should have to reach for it once, not leave it set in a file where " +
			"a later run silently discards a corpus."},
	{Name: "ASQS_LOG_RESOLVED_LLM_ENDPOINTS", Component: "internal/llm/embeddings", Kind: "asqs",
		Doc: "1/true logs the resolved Ollama chat and embedding URLs when clients are built. A debugging aid for base-URL " +
			"and proxy problems, where the question is always what the client actually resolved."},
	{Name: "AWS_REGION", Component: "internal/llm/embeddings", Kind: "inherited",
		Doc: "AWS region for a Bedrock embedding endpoint. Standard AWS variable; asqs-core reads it rather than adding a key that would disagree with the rest of the toolchain."},
	{Name: "AWS_BEDROCK_MODEL_ID", Component: "internal/llm/embeddings", Kind: "inherited",
		Doc: "Overrides the Bedrock embedding model id."},
	{Name: "GOOGLE_PROJECT_ID", Component: "internal/llm/embeddings", Kind: "inherited",
		Doc: "Google Cloud project for a Vertex AI embedding endpoint. Standard Google variable."},
	{Name: "VERTEXAI_MODEL_ID", Component: "internal/llm/embeddings", Kind: "inherited",
		Doc: "Overrides the Vertex AI embedding model id."},
	{Name: "VERTEXAI_TOKEN", Component: "internal/llm/embeddings", Kind: "inherited",
		Doc: "Bearer token for Vertex AI, when not using application default credentials."},
	{Name: "NUGET_PACKAGES", Component: "internal/evaluator/apisurface", Kind: "inherited",
		Doc: "The .NET SDK's own global packages folder. The API-surface resolver reads it to locate assemblies rather than assuming the default path."},
	{Name: "PATH", Component: "internal/runner", Kind: "inherited",
		Doc: "Used to locate build toolchains for the local sandbox. Its absence is what produces the 'is not on PATH' remediation."},
	{Name: "PLAYWRIGHT_BROWSERS_PATH", Component: "internal/runner", Kind: "inherited",
		Doc: "Playwright's own browser cache location. The E2E preflight reads it to tell 'browsers not installed' apart from 'installed somewhere else'."},
	{Name: "CYPRESS_CACHE_FOLDER", Component: "internal/runner", Kind: "inherited",
		Doc: "Cypress's own binary cache location, read by the E2E preflight for the same reason."},
	{Name: "RUNNER_DOTNET_FALLBACK_TARGET_FRAMEWORK", Component: "internal/testbootstrap", Kind: "asqs",
		Doc: "Target framework for a generated C# test project when no .csproj in the repository names one. " +
			"NOTE: this is read WITHOUT the ASQS_ prefix, unlike every schema-derived variable — a v1 leftover, and the " +
			"schema key general.build.dotnet_fallback_target_framework is the supported spelling."},
	{Name: "ASQS_TEST_METADATA_URL", Component: "internal/storage (tests)", Kind: "test",
		Doc: "Postgres URL for the live-database tests. UNSET MEANS THOSE TESTS SKIP, so a green run does not by itself prove they executed."},
}
