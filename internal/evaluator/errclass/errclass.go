// Package errclass classifies test/build outputs for evaluator policy (infrastructure vs code bugs).
package errclass

import (
	"strings"
)

// Execution kinds describe the sandbox failing to RUN the build at all, as opposed to the build
// running and meeting a broken environment. No edit to a generated test can repair any of them.
const (
	// KindToolchainMissing: the build tool is not on the service account's PATH.
	// runner.type: local only — a container image always supplies its own toolchain.
	KindToolchainMissing = "toolchain_missing"
	// KindToolchainNotExecutable: a build wrapper exists but could not be executed.
	// runner.type: local only, for the same reason.
	KindToolchainNotExecutable = "toolchain_not_executable"
	// KindBrowsersMissing: an E2E runner could not find its browsers (or their OS dependencies).
	// Occurs under BOTH runner types: on a host when e2e_framework_bootstrap has not installed
	// them, and under Docker when the Playwright image override did not apply. No edit to a
	// generated test can install a browser.
	KindBrowsersMissing = "browsers_missing"
	// KindStepTimeout: the step was killed at runner.timeout. Occurs under BOTH runner types —
	// the Docker path used to discard its deadline error, so a timed-out container surfaced as an
	// ordinary test failure and matched nothing here (runner.sandboxStepFailure fixed that).
	KindStepTimeout = "step_timeout"
)

// Kind returns a short classifier label when the failure looks like a host-execution problem or
// missing/invalid environment configuration (DB connection string, DB open, etc.), otherwise "".
// Prefer false negatives. Classification is implemented for csharp, java/kotlin/scala (JDBC
// ecosystem), and javascript/typescript; the host-execution kinds are language-agnostic.
func Kind(lang, testOutput string) string {
	if strings.TrimSpace(testOutput) == "" {
		return ""
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	out := testOutput
	lower := strings.ToLower(out)

	// Checked first and language-agnostic: when the build never ran, nothing below — which
	// classifies what the build printed — can apply.
	if k := kindHostExecution(lower); k != "" {
		return k
	}

	// English phrases that appear in JDBC / driver logs regardless of test language tag.
	if k := kindSharedInfra(lower); k != "" {
		return k
	}

	switch lang {
	case "csharp", "cs":
		return kindCSharp(out, lower)
	case "java", "kotlin", "scala":
		return kindJVM(lower)
	case "javascript", "typescript", "js", "ts":
		return kindJS(lower)
	default:
		return ""
	}
}

// IsInfrastructureOrEnvironmentTestFailure reports whether Kind would return non-empty.
func IsInfrastructureOrEnvironmentTestFailure(lang, testOutput string) bool {
	return Kind(lang, testOutput) != ""
}

func kindSharedInfra(lower string) string {
	if strings.Contains(lower, "communications link failure") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "unknown database") {
		return "database_open"
	}
	if strings.Contains(lower, "access denied for user") && strings.Contains(lower, "using password") {
		return "connection_configuration"
	}
	return ""
}

func kindCSharp(out, lower string) string {
	if strings.Contains(out, "SqliteConnectionStringBuilder") ||
		strings.Contains(out, "Microsoft.Data.Sqlite") {
		if strings.Contains(out, "initialization string") ||
			strings.Contains(out, "Format of the initialization string") ||
			strings.Contains(out, "does not conform to specification") {
			return "sqlite_connection_string"
		}
	}
	if strings.Contains(lower, "cannot open database") ||
		strings.Contains(lower, "unable to open the database") {
		return "database_open"
	}
	if strings.Contains(out, "ArgumentException") &&
		(strings.Contains(lower, "connection string") || strings.Contains(lower, "connectionstring")) {
		return "connection_configuration"
	}
	return ""
}

func kindJVM(lower string) string {
	if strings.Contains(lower, "java.sql.sqlnontransientconnectionexception") ||
		strings.Contains(lower, "org.hibernate.exception.jdbcconnectionexception") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "javax.persistence.persistenceexception") &&
		strings.Contains(lower, "unable to build entitymanagerfactory") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "could not open jpa entitymanager for transaction") &&
		strings.Contains(lower, "could not obtain connection") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "could not get jdbc connection") ||
		strings.Contains(lower, "failed to obtain jdbc connection") ||
		strings.Contains(lower, "unable to acquire jdbc connection") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "connection refused") &&
		(strings.Contains(lower, "jdbc") || strings.Contains(lower, "mysql") ||
			strings.Contains(lower, "postgres") || strings.Contains(lower, "5432") ||
			strings.Contains(lower, "3306") || strings.Contains(lower, "1521")) {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "no suitable driver") && strings.Contains(lower, "jdbc") {
		return "connection_configuration"
	}
	if strings.Contains(lower, "the connection attempt failed") && strings.Contains(lower, "postgresql") {
		return "jdbc_connection"
	}
	return ""
}

func kindJS(lower string) string {
	if strings.Contains(lower, "prismaclientinitializationerror") ||
		strings.Contains(lower, "error code: p1001") ||
		strings.Contains(lower, "can't reach database server") ||
		strings.Contains(lower, "cannot reach database server") {
		return "database_open"
	}
	if strings.Contains(lower, "sequelizeconnectionerror") ||
		strings.Contains(lower, "sequelizehosterror") ||
		strings.Contains(lower, "mongoserverselectionerror") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "connect econnrefused") &&
		(strings.Contains(lower, "5432") || strings.Contains(lower, "3306") ||
			strings.Contains(lower, "27017") || strings.Contains(lower, "6379") ||
			strings.Contains(lower, "postgres") || strings.Contains(lower, "mysql") ||
			strings.Contains(lower, "mongodb") || strings.Contains(lower, "redis")) {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "password authentication failed") && strings.Contains(lower, "postgres") {
		return "connection_configuration"
	}
	return ""
}

// IsHostExecutionKind reports whether kind is one of the host-execution kinds above. Callers use
// it to stop a fix loop that cannot converge: retrying a deterministic host failure re-runs the
// same broken step, and the LLM fixer only writes test files, which is not where the fault is.
func IsHostExecutionKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case KindToolchainMissing, KindToolchainNotExecutable, KindStepTimeout, KindBrowsersMissing:
		return true
	}
	return false
}

// Remediation returns a one-line operator action for a host-execution kind, "" for every other
// kind (including ""). Audited alongside the classification so the log says what to do, not just
// what happened.
func Remediation(kind string) string {
	switch strings.TrimSpace(kind) {
	case KindToolchainMissing:
		return "Install the build toolchain on the host running ASQS and make it available on the service account's PATH, or switch runner.type to docker. (Repository build wrappers are no longer invoked, so shipping a ./mvnw does not help.)"
	case KindToolchainNotExecutable:
		return "The build tool could not be executed. Check that the binary on PATH is executable by the account ASQS runs as, or switch runner.type to docker."
	case KindBrowsersMissing:
		return "The E2E runner has no browsers. Enable runner.e2e_framework_bootstrap.enabled so ASQS installs them (it runs `playwright install` for you), install them by hand for the account ASQS runs as, or switch runner.type to docker, whose Playwright image ships them."
	case KindStepTimeout:
		return "The step was killed at runner.timeout. Raise it, warm the dependency cache before the run, or narrow the build with runner.compile_command / test_command."
	}
	return ""
}

// kindHostExecution matches only strings ASQS itself or Go's os/exec produce, so ordinary build
// output cannot trip it. Anything looser (a bare "permission denied", a bare "command not found")
// would misclassify a test that legitimately prints those, and this package prefers false
// negatives.
func kindHostExecution(lower string) string {
	// os/exec LookPath: exec: "mvn": executable file not found in $PATH
	if strings.Contains(lower, "executable file not found in $path") {
		return KindToolchainMissing
	}
	// os/exec Start: fork/exec ./mvnw: permission denied
	if strings.Contains(lower, "fork/exec ") && strings.Contains(lower, "permission denied") {
		return KindToolchainNotExecutable
	}
	// runner.sandboxStepFailure: "<step> step timed out after 30m0s (runner.timeout)"
	if strings.Contains(lower, "step timed out after ") && strings.Contains(lower, "(runner.timeout)") {
		return KindStepTimeout
	}
	// Playwright and Cypress reporting that their browsers are absent. Every marker below is
	// emitted verbatim by those tools, so an ordinary test that merely mentions a browser cannot
	// match: "download new browsers" and "host system is missing dependencies to run browsers" are
	// whole sentences from Playwright's own installer prompt.
	if strings.Contains(lower, "executable doesn't exist at") && strings.Contains(lower, "ms-playwright") {
		return KindBrowsersMissing
	}
	if strings.Contains(lower, "download new browsers") ||
		strings.Contains(lower, "host system is missing dependencies to run browsers") ||
		strings.Contains(lower, "cypress binary is missing") {
		return KindBrowsersMissing
	}
	// runner.requireLocalToolchain, raised before the process is spawned.
	if strings.Contains(lower, "is not on path") && strings.Contains(lower, "runner.type") {
		return KindToolchainMissing
	}
	return ""
}
