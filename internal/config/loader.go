package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/asqs/asqs-core/internal/workspace"
	"gopkg.in/yaml.v3"
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

// Load builds Config from file (if ConfigPath is set or ASQS_CONFIG_PATH is set), then applies environment overrides, then validates.
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

	var c Config
	// Defaults before YAML merge: omitted keys keep these values (yaml.v3 does not zero unset fields).
	seedRunnerBoolDefaultsBeforeYAML(&c)
	if opts.ConfigPath != "" {
		data, err := os.ReadFile(opts.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("config: read file: %w", err)
		}
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("config: parse yaml: %w", err)
		}
		warnDeprecatedConfigYAML(data, opts.ConfigPath)
	} else {
		// Try default paths so "config.yaml" in cwd is used when -config and ASQS_CONFIG_PATH are unset
		for _, name := range []string{"config.yaml", "config.yml"} {
			data, err := os.ReadFile(name)
			if err == nil {
				if err := yaml.Unmarshal(data, &c); err != nil {
					return nil, fmt.Errorf("config: parse %s: %w", name, err)
				}
				warnDeprecatedConfigYAML(data, name)
				break
			}
		}
	}
	prefix := opts.EnvPrefix
	if prefix == "" {
		prefix = defaultEnvPrefix
	}
	applyEnv(&c, prefix, opts.ClientID)
	warnDeprecatedConfigEnv(prefix, opts.ClientID)
	// Slice fields are not set by applyEnv; support INDEXER_SKIP_PATH_PREFIXES (comma-separated)
	if s := os.Getenv(prefix + "INDEXER_SKIP_PATH_PREFIXES"); s != "" {
		var list []string
		for _, p := range strings.Split(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				list = append(list, t)
			}
		}
		c.Indexer.SkipPathPrefixes = list
	}
	if s := os.Getenv(prefix + "RUNNER_AZURE_DEVOPS_NUGET_FEED_ENDPOINTS"); s != "" {
		var list []string
		for _, p := range strings.Split(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				list = append(list, t)
			}
		}
		c.Runner.AzureDevOpsNuGetFeedEndpoints = list
	}
	if opts.ClientID != "" {
		c.ClientID = opts.ClientID
	}
	mode := opts.ValidateMode
	if mode == "" {
		mode = "full"
	}
	if err := Validate(&c, mode); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyEnv sets struct fields from environment variables using env tags.
// For each field with tag env:"NAME", sets from os.Getenv(prefix + clientPrefix + NAME).
func applyEnv(c *Config, prefix, clientID string) {
	clientPrefix := ""
	if clientID != "" {
		clientPrefix = clientID + "_"
	}
	ensureRunnerPolicyForProjectIntelEnv(c, prefix, clientID)
	applyEnvStruct(reflect.ValueOf(c).Elem(), prefix, clientPrefix)
}

// ensureRunnerPolicyForProjectIntelEnv allocates runner.policy when any RETRIEVAL_PROJECT_INTEL_*
// env var is set so nested env tags on policy.project_intel are applied (applyEnvStruct skips nil pointers).
func ensureRunnerPolicyForProjectIntelEnv(c *Config, prefix, clientID string) {
	if c == nil {
		return
	}
	clientPrefix := ""
	if strings.TrimSpace(clientID) != "" {
		clientPrefix = strings.TrimSpace(clientID) + "_"
	}
	needle := "RETRIEVAL_PROJECT_INTEL_"
	for _, e := range os.Environ() {
		name := strings.SplitN(e, "=", 2)[0]
		if !strings.Contains(name, needle) {
			continue
		}
		if strings.HasPrefix(name, prefix+clientPrefix) || (clientPrefix != "" && strings.HasPrefix(name, prefix)) {
			if c.Runner.Policy == nil {
				c.Runner.Policy = &PolicyConfig{}
			}
			return
		}
	}
}

func applyEnvStruct(v reflect.Value, prefix, clientPrefix string) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fv := v.Field(i)
		ft := t.Field(i)
		if !fv.CanSet() {
			continue
		}
		tag := ft.Tag.Get("env")
		if fv.Kind() == reflect.Struct {
			applyEnvStruct(fv, prefix, clientPrefix)
		} else if fv.Kind() == reflect.Ptr && !fv.IsNil() && fv.Elem().Kind() == reflect.Struct {
			applyEnvStruct(fv.Elem(), prefix, clientPrefix)
		}
		if tag == "" {
			continue
		}
		key := prefix + clientPrefix + tag
		s := os.Getenv(key)
		if s == "" && clientPrefix != "" {
			s = os.Getenv(prefix + tag)
		}
		if s == "" {
			continue
		}
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(s)
		case reflect.Int, reflect.Int32, reflect.Int64:
			var n int64
			if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
				fv.SetInt(n)
			}
		case reflect.Bool:
			fv.SetBool(isTruthy(s))
		case reflect.Ptr:
			if fv.Type().Elem().Kind() != reflect.Bool {
				break
			}
			if b, ok := parseEnvBool(s); ok {
				bp := reflect.New(fv.Type().Elem())
				bp.Elem().SetBool(b)
				fv.Set(bp)
			}
		}
	}
}

func isTruthy(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "true" || s == "yes" || s == "on"
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
	// B.3 — runner.policy validation must run in every mode (including audit/e2e). Unknown
	// hook keys or wrong types are misconfiguration that we want surfaced unconditionally,
	// not gated behind the heavier full-mode checks.
	if err := c.Runner.Policy.ValidatePolicy(); err != nil {
		errs = append(errs, err.Error())
	}
	if c.Database.MetadataURL == "" {
		errs = append(errs, "database.metadata_url (or ASQS_DATABASE_METADATA_URL)")
	}
	if c.VCS.Provider == "" {
		c.VCS.Provider = "github"
	}
	prov := strings.ToLower(strings.TrimSpace(c.VCS.Provider))
	if mode != "audit" && mode != "e2e" {
		switch prov {
		case "github":
			if c.VCS.GitHub.Token == "" {
				errs = append(errs, "vcs.github.token (or ASQS_GITHUB_TOKEN)")
			}
		case "gitlab":
			if c.VCS.GitLab.Token == "" {
				errs = append(errs, "vcs.gitlab.token (or ASQS_GITLAB_TOKEN)")
			}
		case "bitbucket":
			if c.VCS.Bitbucket.Token == "" {
				errs = append(errs, "vcs.bitbucket.token (or ASQS_BITBUCKET_TOKEN)")
			}
		case "azure_devops":
			if c.VCS.AzureDevOps.Token == "" {
				errs = append(errs, "vcs.azure_devops.token (or ASQS_AZURE_DEVOPS_TOKEN)")
			}
		default:
			errs = append(errs, "vcs.provider must be github, gitlab, bitbucket, or azure_devops")
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
			errs = append(errs, "llm.api_key or llm.api_key_from_env (or ASQS_LLM_API_KEY)")
		}
	}
	if tw, err := workspace.NormalizeMonoRepoWorkspace(c.Indexer.MonoRepoTestWorkspace); err != nil {
		errs = append(errs, fmt.Sprintf("indexer.mono_repo_test_workspace: %v", err))
	} else if tw != "" {
		cw, err := workspace.NormalizeMonoRepoWorkspace(c.Indexer.MonoRepoWorkspace)
		if err != nil {
			errs = append(errs, fmt.Sprintf("indexer.mono_repo_workspace: %v", err))
		} else if cw == "" {
			errs = append(errs, "indexer.mono_repo_test_workspace requires indexer.mono_repo_workspace to be set")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation: missing required: %s", strings.Join(errs, ", "))
	}
	if c.Database.EmbeddingsURL == "" {
		c.Database.EmbeddingsURL = c.Database.MetadataURL
	}
	if c.Database.EmbeddingsDimension <= 0 {
		c.Database.EmbeddingsDimension = 1536
	}
	if c.Runner.Type == "" {
		c.Runner.Type = "local"
	}
	// gap_concurrency: 0/1 = sequential; larger values are capped at the bounded ceiling so a
	// typo like "100" cannot surprise-flood an LLM provider rate limit in production. The
	// coercion is silent because the CLI already echoes the resolved config on startup.
	const gapConcurrencyCeiling = 16
	if c.Runner.GapConcurrency < 0 {
		c.Runner.GapConcurrency = 0
	}
	if c.Runner.GapConcurrency > gapConcurrencyCeiling {
		c.Runner.GapConcurrency = gapConcurrencyCeiling
	}
	return nil
}

// MergeEnvFromOS applies ASQS_* (and client-scoped) environment overrides to c without reading a YAML file.
// Matches the env step in Load after unmarshaling YAML.
func MergeEnvFromOS(c *Config) {
	if c == nil {
		return
	}
	clientID := strings.TrimSpace(c.ClientID)
	if clientID == "" {
		clientID = os.Getenv(envClientID)
	}
	applyEnv(c, defaultEnvPrefix, clientID)
	if s := os.Getenv(defaultEnvPrefix + "INDEXER_SKIP_PATH_PREFIXES"); s != "" {
		var list []string
		for _, p := range strings.Split(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				list = append(list, t)
			}
		}
		c.Indexer.SkipPathPrefixes = list
	}
}

// ApplyServerDatabaseURLs overwrites database URLs (e.g. apiserver forces revision YAML to use its opened Postgres).
func ApplyServerDatabaseURLs(c *Config, metadataURL, embeddingsURL string) {
	if c == nil {
		return
	}
	if metadataURL != "" {
		c.Database.MetadataURL = metadataURL
	}
	if strings.TrimSpace(embeddingsURL) != "" {
		c.Database.EmbeddingsURL = strings.TrimSpace(embeddingsURL)
	} else if metadataURL != "" && c.Database.EmbeddingsURL == "" {
		c.Database.EmbeddingsURL = metadataURL
	}
}

// PrepareConfigForWorkflowRun merges OS env, optionally forces server DB URLs, then runs Validate in full mode.
func PrepareConfigForWorkflowRun(c *Config, serverMetadataURL, serverEmbeddingsURL string) error {
	if c == nil {
		return fmt.Errorf("config: nil")
	}
	MergeEnvFromOS(c)
	if serverMetadataURL != "" {
		ApplyServerDatabaseURLs(c, serverMetadataURL, serverEmbeddingsURL)
	}
	return Validate(c, "full")
}

// ValidateStoredRevisionYAML unmarshals revision YAML, merges OS env, applies server DB URLs, and runs the same
// full Validate as a workflow run (parity with qualitybot / API execute). Used when persisting API config revisions.
func ValidateStoredRevisionYAML(yamlBody []byte, serverMetadataURL, serverEmbeddingsURL string) error {
	cfg, err := UnmarshalYAMLBytes(yamlBody)
	if err != nil {
		return err
	}
	return PrepareConfigForWorkflowRun(cfg, serverMetadataURL, serverEmbeddingsURL)
}
