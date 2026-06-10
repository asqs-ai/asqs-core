package retrieval

import (
	"strings"

	"github.com/asqs/asqs-core/internal/config"
)

// Built-in defaults when YAML/env do not set global or per-profile caps.
const (
	DefaultMaxSimilarTests     = 5
	DefaultMaxDependencyChunks = 15
	DefaultMaxFixtures         = 5
)

// NormalizeProfileBudgetsMap re-keys profile_budgets using NormalizeRetrievalProfile so YAML aliases
// (e.g. react → react_feature) match the profile used at Retrieve time.
func NormalizeProfileBudgetsMap(m map[string]config.RetrievalProfileBudget) map[string]config.RetrievalProfileBudget {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]config.RetrievalProfileBudget, len(m))
	for k, v := range m {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		canonical := string(NormalizeRetrievalProfile(RetrievalProfile(k)))
		out[canonical] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResolveRetrievalBudgets returns positive caps for similar reference chunks, dependency graph rows, and fixtures.
// Order: built-in defaults → global overrides (when > 0) → per-profile overrides from perProfile (non-zero fields only).
func ResolveRetrievalBudgets(
	profile RetrievalProfile,
	globalSimilar, globalDep, globalFix int,
	perProfile map[string]config.RetrievalProfileBudget,
) (maxSimilar, maxDep, maxFix int) {
	maxSimilar = DefaultMaxSimilarTests
	maxDep = DefaultMaxDependencyChunks
	maxFix = DefaultMaxFixtures
	if globalSimilar > 0 {
		maxSimilar = globalSimilar
	}
	if globalDep > 0 {
		maxDep = globalDep
	}
	if globalFix > 0 {
		maxFix = globalFix
	}
	if len(perProfile) == 0 {
		return maxSimilar, maxDep, maxFix
	}
	key := string(NormalizeRetrievalProfile(profile))
	b, ok := perProfile[key]
	if !ok {
		return maxSimilar, maxDep, maxFix
	}
	if b.MaxSimilarTests > 0 {
		maxSimilar = b.MaxSimilarTests
	}
	if b.MaxDependencyChunks > 0 {
		maxDep = b.MaxDependencyChunks
	}
	if b.MaxFixtures > 0 {
		maxFix = b.MaxFixtures
	}
	return maxSimilar, maxDep, maxFix
}
