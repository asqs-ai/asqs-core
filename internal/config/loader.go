package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/workspace"
)

// LoadOptions control how configuration is loaded.
type LoadOptions struct {
	// ConfigPath is the path to a YAML config file. If empty, only env and defaults are used.
	ConfigPath string
	// EnvPrefix is prepended to env var names (e.g. "ASQS_" -> ASQS_DATABASE_METADATA_URL).
	EnvPrefix string
	// ClientID enables per-client env overrides: EnvPrefix + ClientID + "_" + tag (e.g. ASQS_ACME_DATABASE_METADATA_URL).
	ClientID string
	// ValidateMode: "full" (default) = require metadata_url and vcs.github.token; "audit" = require only metadata_url (for audit CLI).
	ValidateMode string
}

const (
	defaultEnvPrefix = "ASQS_"
	envConfigPath    = "ASQS_CONFIG_PATH"
	envClientID      = "ASQS_CLIENT_ID"
)

// LoadFromEnv loads config from environment variables only (no file). Uses default prefix ASQS_.
// Useful for containers or when all settings are in env. For per-client, set ASQS_CLIENT_ID.
func LoadFromEnv() (*Config, error) {
	return Load(LoadOptions{})
}

// Load builds Config from a v2 YAML file (when ConfigPath or ASQS_CONFIG_PATH is set), overlays
// environment variables, applies defaults, translates to the runtime shape, and validates.
//
// **This reads schema v2 only.** A v1 file fails the load with a message naming the sections that
// moved, rather than being partially understood — which is what a lenient parser did to it before:
// yaml.Unmarshal with no KnownFields silently ignored every key it did not recognise, so a typo or a
// stale section produced no error, no warning, and no effect. That is the same failure shape as the
// inert keys this restructure removed, except self-inflicted and invisible.
//
// ClientID in opts is overridden by ASQS_CLIENT_ID when set.
func Load(opts LoadOptions) (*Config, error) {
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = os.Getenv(envConfigPath)
	}
	clientID := opts.ClientID
	if clientID == "" {
		clientID = os.Getenv(envClientID)
	}
	opts.ConfigPath = configPath
	opts.ClientID = clientID

	prefix := opts.EnvPrefix
	if prefix == "" {
		prefix = defaultEnvPrefix
	}
	getenv := os.Getenv
	if prefix != defaultEnvPrefix {
		// A non-default prefix is only used by tests and embedders; honour it by rewriting the
		// derived name's prefix on the way through.
		getenv = func(name string) string {
			return os.Getenv(prefix + strings.TrimPrefix(name, defaultEnvPrefix))
		}
	}

	data, sourcePath, err := readConfigFile(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	var c *Config
	if len(data) == 0 {
		// No file anywhere: every value comes from the environment. Containers that inject settings
		// as variables use this path, and it must still get defaults and translation.
		c, _, err = LoadSchemaV2FromEnvOnly(getenv, opts.ClientID)
		if err != nil {
			return nil, err
		}
	} else {
		if err := rejectSurvivingRedactedPlaceholders(data); err != nil {
			return nil, err
		}
		c, _, err = LoadSchemaV2(data, getenv, opts.ClientID)
		if err != nil {
			return nil, err
		}
		c.SourcePath = sourcePath
	}

	if err := normaliseAndValidateRunnerType(c); err != nil {
		return nil, err
	}
	warnInertDockerKeysForLocalRunner(c)
	warnDeprecatedBuildToolWrapperAlias(c)
	if opts.ClientID != "" {
		c.ClientID = opts.ClientID
	}
	mode := opts.ValidateMode
	if mode == "" {
		mode = "full"
	}
	if err := Validate(c, mode); err != nil {
		return nil, err
	}
	return c, nil
}

// readConfigFile returns the config bytes and the path they came from. An explicit path that cannot
// be read is an error; the implicit search is best-effort, because "no config file" is a supported
// way to run.
func readConfigFile(explicit string) (data []byte, path string, err error) {
	if explicit != "" {
		b, rerr := os.ReadFile(explicit)
		if rerr != nil {
			return nil, "", fmt.Errorf("config: read file: %w", rerr)
		}
		return b, explicit, nil
	}
	for _, name := range []string{"config.yaml", "config.yml"} {
		if b, rerr := os.ReadFile(name); rerr == nil {
			return b, name, nil
		}
	}
	return nil, "", nil
}

// parseEnvBool returns (value, true) for explicit true/false tokens; (_, false) for unrecognized empty strings.
func parseEnvBool(s string) (bool, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// Validate checks required fields and returns an error listing what's missing.
// Mode "full" (default) requires metadata_url, vcs.github.token, and llm when llm.provider is set.
// Mode "audit" requires only metadata_url; VCS and LLM are not required.
// Mode "e2e" requires only metadata_url (for E2E tests using config.test.yaml without LLM/VCS).
func Validate(c *Config, mode string) error {
	if mode == "" {
		mode = "full"
	}
	var errs []string
	// runner.policy is gone. It was v1's alternative home for project intel and an inert
	// placeholder for the excluded governance engine; v2 gives project intel exactly one home
	// (generation.policy.project_intel), so there is no second spelling left to validate.
	// Retrieval profile names fail closed in every mode — see validateRetrievalProfiles: a typo'd
	// profile silently degrades to java_unit, the most restrictive profile, and that regression
	// arrives without a warning.
	errs = append(errs, validateRetrievalProfiles(c)...)
	if c.Database.MetadataURL == "" {
		errs = append(errs, "general.database.metadata_url (or ASQS_DATABASE_METADATA_URL)")
	}
	if c.VCS.Provider == "" {
		c.VCS.Provider = "github"
	}
	prov := strings.ToLower(strings.TrimSpace(c.VCS.Provider))
	if mode != "audit" && mode != "e2e" {
		// v2 has ONE token key. The runtime still keeps a block per provider, but naming the
		// per-provider field here would send an operator to a key that no longer parses.
		if c.ActiveVCSToken() == "" {
			errs = append(errs, "general.git.token (or ASQS_GIT_TOKEN)")
		}
		switch prov {
		case "github", "gitlab", "bitbucket", "azure_devops":
		default:
			errs = append(errs, "general.git.provider must be github, gitlab, bitbucket, or azure_devops")
		}
	}
	// Keyless providers (e.g. a local Ollama endpoint) do not need an API key. For everyone else,
	// accept a directly-configured llm.api_key OR a non-empty llm.api_key_from_env env var. (The env
	// var only overrides the direct key when it actually resolves to something — matching the client
	// resolution in internal/llm — so a config-file api_key is not clobbered by an unset env var.)
	if mode != "e2e" && c.LLM.Provider != "" && !strings.EqualFold(strings.TrimSpace(c.LLM.Provider), "ollama") {
		key := c.LLM.APIKey
		if c.LLM.APIKeyFromEnv != "" {
			if v := os.Getenv(c.LLM.APIKeyFromEnv); v != "" {
				key = v
			}
		}
		if key == "" {
			errs = append(errs, "general.llm.api_key or general.llm.api_key_from_env (or ASQS_LLM_API_KEY)")
		}
	}
	if tw, err := workspace.NormalizeMonoRepoWorkspace(c.Indexer.MonoRepoTestWorkspace); err != nil {
		errs = append(errs, fmt.Sprintf("general.build.workspace.test_path: %v", err))
	} else if tw != "" {
		cw, err := workspace.NormalizeMonoRepoWorkspace(c.Indexer.MonoRepoWorkspace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("general.build.workspace.path: %v", err))
		} else if cw == "" {
			errs = append(errs, "general.build.workspace.test_path requires general.build.workspace.path to be set")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation: missing required: %s", strings.Join(errs, ", "))
	}
	// A malformed keep_alive would otherwise surface as a 400 from Ollama on the first completion of
	// the run, after indexing and planning have already been paid for.
	if _, err := OllamaKeepAliveJSON(c.LLM.OllamaKeepAlive); err != nil {
		return fmt.Errorf("config validation: general.llm.ollama_keep_alive: %w", err)
	}
	if c.Database.EmbeddingsURL == "" {
		c.Database.EmbeddingsURL = c.Database.MetadataURL
	}
	if c.Database.EmbeddingsDimension <= 0 {
		c.Database.EmbeddingsDimension = 1536
	}
	// runner.type is NOT defaulted here. normaliseAndValidateRunnerType already lowercased it,
	// defaulted empty to "local" and rejected anything that is neither local nor docker — and it runs
	// far earlier, so this branch was unreachable. CP35 added that validator and left the older
	// default behind; the golden-config mutation check is what proved the leftover dead (CP36).
	return nil
}
