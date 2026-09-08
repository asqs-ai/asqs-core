package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// The Java E2E pass runs `mvn failsafe:integration-test failsafe:verify` or `gradle integrationTest`
// (evaluator/e2e_command.go). Neither is implied by a Playwright dependency: Failsafe is a plugin
// the POM should pin, and Gradle has no integrationTest task unless the build declares one. These
// helpers make the build file carry what the pass invokes; each is a no-op when it already does.

// ensureJavaE2ERunnerDeclared declares the runner in the repository's primary Java build file.
// Failures are reported, not returned: a repository whose build file cannot be patched still runs
// its E2E pass the way it did before, and the audit says why nothing changed.
func ensureJavaE2ERunnerDeclared(ctx context.Context, audit Auditor, repo string) {
	jbf, err := primaryJavaBuildFile(repo)
	if err != nil || jbf.Abs == "" {
		return
	}
	var changed bool
	var runner string
	switch jbf.Kind {
	case javaBuildMaven:
		runner = "maven-failsafe-plugin"
		changed, err = ensureMavenFailsafeDeclared(jbf.Abs)
	case javaBuildGradleGroovy:
		runner = "gradle integrationTest"
		changed, err = ensureGradleIntegrationTestTask(jbf.Abs, false)
	case javaBuildGradleKotlin:
		runner = "gradle integrationTest"
		changed, err = ensureGradleIntegrationTestTask(jbf.Abs, true)
	default:
		return
	}
	rel := relPathForBootstrap(repo, jbf.Abs)
	switch {
	case err != nil:
		logAuditError(audit, ctx, "e2e_bootstrap.runner_declare_failed", map[string]interface{}{
			"message":    fmt.Sprintf("Could not declare the E2E runner (%s) in %s: %v. The E2E pass will invoke it anyway.", runner, rel, err),
			"build_file": rel,
			"runner":     runner,
			"error":      err.Error(),
		})
	case changed:
		logAudit(audit, ctx, "e2e_bootstrap.runner_declared", map[string]interface{}{
			"message":    fmt.Sprintf("Declared the E2E runner (%s) in %s so the evaluator's E2E pass runs against a pinned, explicit configuration.", runner, rel),
			"build_file": rel,
			"runner":     runner,
		})
		fmt.Fprintf(os.Stderr, "  e2e_framework_bootstrap: declared %s in %s\n", runner, rel)
	}
}

// ensureMavenFailsafeDeclared adds the Failsafe plugin declaration when the POM lacks one.
func ensureMavenFailsafeDeclared(pomPath string) (bool, error) {
	b, err := os.ReadFile(pomPath)
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, "maven-failsafe-plugin") {
		return false, nil
	}
	out, err := insertMavenFailsafePlugin(s)
	if err != nil {
		return false, err
	}
	return true, atomicWrite(pomPath, []byte(out))
}

// gradleIntegrationTestGroovy / gradleIntegrationTestKotlin register the task the E2E pass runs and
// keep *IT classes out of `test`, mirroring Maven's Surefire/Failsafe split so the unit pass (whose
// image has no browsers) does not execute Playwright tests. %s is the JUnit platform line, present
// only when the build already uses JUnit 5.
const gradleIntegrationTestGroovy = `

// ASQS e2e_framework_bootstrap: *IT classes run through 'gradle integrationTest'; 'gradle test' stays unit-only.
tasks.named('test', Test) {
    exclude '**/*IT.class'
}
tasks.register('integrationTest', Test) {
    description = 'Runs *IT integration/E2E tests (ASQS).'
    group = 'verification'
    testClassesDirs = sourceSets.test.output.classesDirs
    classpath = sourceSets.test.runtimeClasspath
    include '**/*IT.class'%s
}
`

const gradleIntegrationTestKotlin = `

// ASQS e2e_framework_bootstrap: *IT classes run through 'gradle integrationTest'; 'gradle test' stays unit-only.
tasks.named<Test>("test") {
    exclude("**/*IT.class")
}
tasks.register<Test>("integrationTest") {
    description = "Runs *IT integration/E2E tests (ASQS)."
    group = "verification"
    testClassesDirs = sourceSets["test"].output.classesDirs
    classpath = sourceSets["test"].runtimeClasspath
    include("**/*IT.class")%s
}
`

// ensureGradleIntegrationTestTask appends the integrationTest task when the build file has none.
func ensureGradleIntegrationTestTask(path string, kotlinDSL bool) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := string(b)
	if strings.Contains(s, "integrationTest") {
		return false, nil
	}
	platform := ""
	if strings.Contains(s, "useJUnitPlatform") || strings.Contains(s, "junit-jupiter") || strings.Contains(s, "junit.jupiter") {
		platform = "\n    useJUnitPlatform()"
	}
	block := fmt.Sprintf(gradleIntegrationTestGroovy, platform)
	if kotlinDSL {
		block = fmt.Sprintf(gradleIntegrationTestKotlin, platform)
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += strings.TrimLeft(block, "\n")
	return true, atomicWrite(path, []byte(s))
}
