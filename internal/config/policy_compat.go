package config

// Governance/policy is an enterprise feature and is NOT part of asqs-core. These are inert
// compatibility placeholders so a `runner.policy:` block (or legacy keys) in a config file parses
// without error and is then ignored. There is no policy engine in the open core.

// PolicyConfig is an inert placeholder for the (excluded) governance/policy block.
type PolicyConfig struct{}

// ValidatePolicy is a no-op; the open core has no policy hooks to validate.
func (p *PolicyConfig) ValidatePolicy() error { return nil }

// warnDeprecatedConfigYAML / warnDeprecatedConfigEnv are no-ops in the open core (the legacy
// deprecation-warning machinery is not carried over).
func warnDeprecatedConfigYAML(_ []byte, _ string) {}
func warnDeprecatedConfigEnv(_ string, _ string)  {}
