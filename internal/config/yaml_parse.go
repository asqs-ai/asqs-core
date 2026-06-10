package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// seedRunnerBoolDefaultsBeforeYAML matches config.Load: for bool fields that default to true when
// the YAML key is omitted, set them before Unmarshal so absent keys do not become the Go zero
// (false). API-stored revision YAML must behave like file-based config for
// runner.two_phase_test_generation.
func seedRunnerBoolDefaultsBeforeYAML(c *Config) {
	c.Runner.TwoPhaseTestGeneration = true
}

// UnmarshalYAMLBytes parses YAML into Config without reading files, env, or running Validate.
// Use for API-stored config revisions; call Validate separately when executing a run if full checks are required.
func UnmarshalYAMLBytes(data []byte) (*Config, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, fmt.Errorf("config: empty yaml")
	}
	var c Config
	seedRunnerBoolDefaultsBeforeYAML(&c)
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse yaml: %w", err)
	}
	warnDeprecatedConfigYAML(data, "yaml")
	// B.3 — runner.policy overrides are validated eagerly so unknown keys / wrong types fail
	// the load rather than silently disabling a hook at run time. Validation is a thin pass
	// over the policy block; nothing else in Config is touched.
	if err := c.Runner.Policy.ValidatePolicy(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}
