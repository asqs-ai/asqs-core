package testbootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/teststack"
)

// writeTestStackContract persists the bootstrap → generation contract, best-effort.
//
// A failed write is audited and swallowed. The contract only ever ADDS information for downstream
// steps; failing an otherwise successful bootstrap because a JSON file could not be written would
// trade a working run for a missing optimisation.
//
// Nothing here keeps the file out of the shipped diff, and nothing needs to: the ship path already
// deletes the whole .asqs/ directory before staging (orchestrator.RemoveRepoAsqsDirForShip), keeping
// only the cache paths an operator configured. Writing a .gitignore entry as well was redundant, and
// worse, it silently modified a tracked file in the customer's repository.
func writeTestStackContract(ctx context.Context, audit Auditor, repo string, c teststack.Contract) {
	if err := teststack.Write(repo, c); err != nil {
		logAudit(audit, ctx, "test_bootstrap.contract_write_failed", map[string]interface{}{
			"message": fmt.Sprintf("Could not write %s: %v. Generation falls back to reading raw manifests, exactly as before this file existed.", teststack.RelPath, err),
			"path":    teststack.RelPath,
			"error":   err.Error(),
		})
		return
	}
	logAudit(audit, ctx, "test_bootstrap.contract_written", map[string]interface{}{
		"message": fmt.Sprintf("Wrote %s: %s/%s, %d import root(s), verified=%v.",
			teststack.RelPath, c.Framework, c.Runner, len(c.AvailableImports), c.Verified),
		"path":              teststack.RelPath,
		"framework":         c.Framework,
		"runner":            c.Runner,
		"stack":             c.Stack,
		"verified":          c.Verified,
		"smoke_status":      string(c.Smoke.Status),
		"available_imports": c.AvailableImports,
	})
}

// smokeFromRun maps a framework-smoke outcome onto the contract's vocabulary.
func smokeFromRun(kind string, staged, passed bool, note string) teststack.Smoke {
	switch {
	case kind == "":
		return teststack.Smoke{Status: teststack.SmokeNone}
	case !staged:
		return teststack.Smoke{Kind: kind, Status: teststack.SmokeSkipped, Detail: strings.TrimSpace(note)}
	case passed:
		return teststack.Smoke{Kind: kind, Status: teststack.SmokePassed}
	default:
		return teststack.Smoke{Kind: kind, Status: teststack.SmokeFailed, Detail: strings.TrimSpace(note)}
	}
}

// writeSkipContract records what detection found on the path where bootstrap changed nothing because
// the stack was already complete.
//
// This is the case the contract would otherwise miss entirely, and it is a common one: a repository
// that already has its test tooling never reaches an apply step, yet detection has just computed the
// full profile. Verified is false — the packages are really there, they simply were not exercised.
func writeSkipContract(ctx context.Context, audit Auditor, repo, lang string, v existingStackVerification) {
	c, ok := contractForCompleteStack(repo, lang)
	if !ok {
		return
	}
	c.Smoke = teststack.Smoke{Status: teststack.SmokeNotRun}
	switch {
	case v.Attempted && v.OK:
		// Bootstrap changed nothing, but it did run a test with the repository's own stack — which is
		// exactly the claim `verified` makes.
		c.Verified = true
		c.Notes = append(c.Notes,
			"Bootstrap made no changes: the repository already carried the full stack. A throwaway smoke test was run with the repository's own runner to confirm it works.")
	case v.Attempted:
		c.Verified = false
		c.Notes = append(c.Notes,
			"The repository declares a complete test stack, but a trivial smoke test could not be run with it.")
	default:
		c.Verified = false
		c.Notes = append(c.Notes,
			"Bootstrap made no changes: the repository already carried the full stack. Nothing was executed to confirm it ("+v.Reason+").")
	}
	writeTestStackContract(ctx, audit, repo, c)
}

// verifiedSuffix renders the stderr tail for the skip path.
func verifiedSuffix(v existingStackVerification) string {
	switch {
	case v.Attempted && v.OK:
		return " — verified: a smoke test ran with it"
	case v.Attempted:
		return " — NOT verified: a smoke test could not run"
	default:
		return " — not verified (" + v.Reason + ")"
	}
}

// contractForCompleteStack re-derives the profile for a repo bootstrap decided to skip.
//
// Re-resolving costs only file reads, and it keeps the skip path from having to thread a
// language-specific profile through Run's language-agnostic control flow.
func contractForCompleteStack(repo, lang string) (teststack.Contract, bool) {
	switch {
	case lang == "java":
		prof, jbf, err := resolveJavaTestProfile(repo)
		if err != nil || jbf.Abs == "" || prof.Declined {
			return teststack.Contract{}, false
		}
		return javaContract(prof), true
	case isCSharpLang(lang):
		prof, err := resolveCSharpTestProfile(repo, "")
		if err != nil || prof.Declined {
			return teststack.Contract{}, false
		}
		return csharpContract(prof), true
	case isJSLang(lang):
		prof, _, err := resolveJSTestProfile(repo, lang)
		if err != nil || prof.Declined {
			return teststack.Contract{}, false
		}
		return jsContract(prof, lang), true
	}
	return teststack.Contract{}, false
}
