package config

import (
	"fmt"
	"strings"
)

// Canonical retrieval profile names. The typed constants live in
// internal/intelligence/retrieval (RetrievalProfile), which imports this package — so the
// alias table has to live here to avoid an import cycle, and this is where it belongs anyway:
// mapping user-written YAML to a canonical value is config parsing.
const (
	RetrievalProfileJavaUnit      = "java_unit"
	RetrievalProfileHTTPAPI       = "http_api"
	RetrievalProfileE2EPlaywright = "e2e_playwright"
	RetrievalProfileReactFeature  = "react_feature"
	RetrievalProfileNestModule    = "nest_module"
	RetrievalProfileFullStack     = "full_stack"
)

// ValidRetrievalProfiles lists the canonical names, for error messages and documentation.
var ValidRetrievalProfiles = []string{
	RetrievalProfileJavaUnit,
	RetrievalProfileHTTPAPI,
	RetrievalProfileE2EPlaywright,
	RetrievalProfileReactFeature,
	RetrievalProfileNestModule,
	RetrievalProfileFullStack,
}

// NormalizeRetrievalProfileName maps an alias to its canonical profile name.
//
// Empty is the documented default (java_unit) and is not an error. Anything unrecognized IS an
// error: silently falling back to java_unit — the most restrictive profile, outgoing edges only and
// `test` chunks only — is the largest quality regression reachable through a config typo, and it
// was delivered without a warning. `http-api` for `http_api` cost a run's worth of retrieval
// quality and looked like nothing had happened.
func NormalizeRetrievalProfileName(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "java", "java_unit", "java-unit", "unit":
		return RetrievalProfileJavaUnit, nil
	case "http_api", "http-api", "api", "backend", "nest_api", "spring":
		return RetrievalProfileHTTPAPI, nil
	case "e2e_playwright", "e2e-playwright", "e2e", "playwright", "ui_test":
		return RetrievalProfileE2EPlaywright, nil
	case "react_feature", "react-feature", "react", "frontend":
		return RetrievalProfileReactFeature, nil
	case "nest_module", "nest-module", "nest", "wiring":
		return RetrievalProfileNestModule, nil
	case "full_stack", "full-stack", "fullstack", "react_http_api", "react-http-api", "ui_and_api":
		return RetrievalProfileFullStack, nil
	default:
		return "", fmt.Errorf("unknown retrieval profile %q; valid values: %s",
			strings.TrimSpace(p), strings.Join(ValidRetrievalProfiles, ", "))
	}
}

// validateRetrievalProfiles checks retrieval.profile and retrieval.profile_e2e, returning one
// message per invalid value. Called from Validate in every mode: a typo'd profile degrades every
// run regardless of which command is running, so gating it behind full mode would hide it from
// exactly the audit and e2e paths that surface problems.
func validateRetrievalProfiles(c *Config) []string {
	if c == nil {
		return nil
	}
	var errs []string
	if _, err := NormalizeRetrievalProfileName(c.Retrieval.Profile); err != nil {
		errs = append(errs, "retrieval.profile: "+err.Error())
	}
	if _, err := NormalizeRetrievalProfileName(c.Retrieval.ProfileE2E); err != nil {
		errs = append(errs, "retrieval.profile_e2e: "+err.Error())
	}
	for name := range c.Retrieval.ProfileBudgets {
		if _, err := NormalizeRetrievalProfileName(name); err != nil {
			errs = append(errs, "retrieval.profile_budgets: "+err.Error())
		}
	}
	return errs
}
