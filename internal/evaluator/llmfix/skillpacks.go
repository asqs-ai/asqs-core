package llmfix

import (
	"embed"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
)

//go:embed skillpacks/*.md
var fixSkillPacksFS embed.FS

func loadFixSkillPack(name string) string {
	b, err := fixSkillPacksFS.ReadFile("skillpacks/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func fixSkillPackBlock(req evaluator.FixRequest) string {
	switch req.Step {
	case evaluator.StepCompile, evaluator.StepTest:
		if s := loadFixSkillPack("unit-v1.md"); s != "" {
			return "### Unit testing skill pack (fixer-aware)\n" + s
		}
	case evaluator.StepTestE2E:
		if s := loadFixSkillPack("e2e-v1.md"); s != "" {
			return "### E2E testing skill pack (fixer-aware)\n" + s
		}
	}
	return ""
}

// accessModifierErrorPattern matches the compiler phrases the access-modifier skill pack should
// condition on: Java `has protected access`, `has private access`, `has package-private access`,
// `is not accessible`; C# `CS0122` ("… is inaccessible due to its protection level") and related
// "no access" phrasing. Case-insensitive to tolerate lowered logs (Maven's `[ERROR]` prefix and
// Gradle's capitalised variants both occur in the wild). Anchored loosely because these phrases
// appear mid-line in file:line:col diagnostics.
var accessModifierErrorPattern = regexp.MustCompile(`(?i)has\s+(?:protected|private|package-private)\s+access|is\s+not\s+accessible|CS0122|no\s+access`)

// errorOutputHasAccessModifierFailure returns true when the build/test log contains a diagnostic
// that indicates the test is trying to reach a member whose visibility forbids it. Used by the
// skill-pack wiring to inject access-modifier remediation patterns (Mockito-spy + verify,
// ReflectionTestUtils / reflection, InternalsVisibleTo) into the system prompt without bloating
// every prompt with those examples when the current error class is unrelated.
func errorOutputHasAccessModifierFailure(errOut string) bool {
	if strings.TrimSpace(errOut) == "" {
		return false
	}
	return accessModifierErrorPattern.MatchString(errOut)
}

// fixAccessModifierSkillPackBlock returns the access-modifier remediation block when the current
// error output carries a visibility failure and the step is compile or test (E2E runs don't fail
// this way). The block is appended after the general unit skill pack so its patterns override
// the generic "fix imports / fix assertions" guidance for this specific failure class.
func fixAccessModifierSkillPackBlock(req evaluator.FixRequest) string {
	switch req.Step {
	case evaluator.StepCompile, evaluator.StepTest:
	default:
		return ""
	}
	if !errorOutputHasAccessModifierFailure(req.ErrorOutput) {
		return ""
	}
	s := loadFixSkillPack("access-modifier-v1.md")
	if s == "" {
		return ""
	}
	return "### Access-modifier failure skill pack (fixer-aware, conditioned on current error)\n" + s
}
