package testbootstrap

import (
	"regexp"
	"strings"
)

// mavenLintSkipProps disable format/lint plugins bound to the build for the duration of a bootstrap
// verification.
//
// Bootstrap verification asks one question: does the test classpath resolve and can a test run. A
// repository's formatter has no bearing on that, but plenty of them are bound to `validate` — which
// runs BEFORE compile — so a style violation in the smoke test aborts the build before the question
// is ever asked. A real run hit exactly this on spring-petclinic: spring-javaformat rejected the
// smoke test's javadoc wrapping and the whole run stopped at minute one.
//
// Unknown -D properties are ignored by Maven, so listing plugins a project does not use is free.
var mavenLintSkipProps = []string{
	"-Dspring-javaformat.skip=true",
	"-Dcheckstyle.skip=true",
	"-Dspotless.check.skip=true",
	"-Dspotless.apply.skip=true",
	"-Dpmd.skip=true",
	"-Dcpd.skip=true",
	"-Dformatter.skip=true",
	"-Dimpsort.skip=true",
	"-Dlicense.skip=true",
	"-Denforcer.skip=true",
}

// reStyleViolation matches build failures that are about code style, not about the code working.
//
// Maven and Gradle report these through ordinary build failure, indistinguishable at the exit-code
// level from a compile error — so the output is the only signal available.
var reStyleViolation = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`formatting violations found`,
	`spring-javaformat`,
	`run .spring-javaformat:apply.`,
	`checkstyle.*violation`,
	`checkstyle rule violations were found`,
	`spotless`,
	`run .mvn spotless:apply.`,
	`license header`,
	`pmd failure`,
	`\bimpsort\b`,
}, "|"))

// isStyleViolationFailure reports whether build output is a formatter/linter rejection.
//
// It requires the absence of a real compiler diagnostic: a build can fail for both reasons at once,
// and a genuine compile error must always win, because that is the failure bootstrap exists to catch.
func isStyleViolationFailure(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	if !reStyleViolation.MatchString(output) {
		return false
	}
	return !hasJavaCompilerDiagnostic(output)
}

var reJavaCompilerDiagnostic = regexp.MustCompile(`(?i)(cannot find symbol|package [\w.]+ does not exist|COMPILATION ERROR|incompatible types|<identifier> expected|cannot access|symbol:\s+class)`)

func hasJavaCompilerDiagnostic(output string) bool {
	return reJavaCompilerDiagnostic.MatchString(output)
}

// styleViolationRemediation explains the downgrade for the audit trail.
func styleViolationRemediation(output string) string {
	tool := "a formatter or linter"
	switch {
	case strings.Contains(strings.ToLower(output), "spring-javaformat"):
		tool = "spring-javaformat"
	case strings.Contains(strings.ToLower(output), "checkstyle"):
		tool = "Checkstyle"
	case strings.Contains(strings.ToLower(output), "spotless"):
		tool = "Spotless"
	}
	return "The build failed on " + tool + " rather than on a compiler error, so it says nothing about whether " +
		"the test classpath resolves. Bootstrap does not treat this as fatal: the smoke test is removed and the run " +
		"continues. If generated tests later fail the same check, set general.build.format_command so they are formatted " +
		"before evaluation."
}
