package evaluator

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveE2ETestCommand returns the shell command for the E2E pass when RunE2ETestPass is enabled.
func resolveE2ETestCommand(opts EvalOptions) string {
	if s := strings.TrimSpace(opts.E2ETestCommand); s != "" {
		return s
	}
	lang := strings.ToLower(strings.TrimSpace(opts.Lang))
	fw := strings.ToLower(strings.TrimSpace(opts.E2EFramework))
	switch lang {
	case "java":
		return defaultJavaE2EShellCommand(opts.RepoPath, opts.BuildTool, fw)
	case "csharp", "cs":
		return defaultCSharpE2EShellCommand(fw)
	case "javascript", "typescript", "js", "ts":
		switch fw {
		case "playwright":
			return "npx playwright test"
		case "cypress":
			return "npx cypress run"
		default:
			return "npm run test:e2e"
		}
	default:
		return ""
	}
}

func defaultJavaE2EShellCommand(repoPath, buildTool, fw string) string {
	switch fw {
	case "playwright-java", "selenium", "selenide":
	default:
		return ""
	}
	repoPath = filepath.Clean(strings.TrimSpace(repoPath))
	tool := strings.ToLower(strings.TrimSpace(buildTool))
	if tool == "" {
		tool = "auto"
	}
	hasPom := fileExists(filepath.Join(repoPath, "pom.xml"))
	hasMvnw := fileExists(filepath.Join(repoPath, "mvnw")) || fileExists(filepath.Join(repoPath, "mvnw.cmd"))
	hasGradle := fileExists(filepath.Join(repoPath, "build.gradle")) || fileExists(filepath.Join(repoPath, "build.gradle.kts"))
	hasGradlew := fileExists(filepath.Join(repoPath, "gradlew")) || fileExists(filepath.Join(repoPath, "gradlew.bat"))

	if tool == "auto" {
		if hasPom {
			if hasMvnw {
				tool = "mvnw"
			} else {
				tool = "mvn"
			}
		} else if hasGradle {
			if hasGradlew {
				tool = "gradlew"
			} else {
				tool = "gradle"
			}
		} else {
			return ""
		}
	}

	switch tool {
	case "mvn", "mvnw":
		if !hasPom {
			return ""
		}
		if tool == "mvnw" && hasMvnw {
			if runtime.GOOS == "windows" && fileExists(filepath.Join(repoPath, "mvnw.cmd")) {
				return "mvnw.cmd -q -B failsafe:integration-test"
			}
			return "./mvnw -q -B failsafe:integration-test"
		}
		return "mvn -q -B failsafe:integration-test"
	case "gradle", "gradlew":
		if !hasGradle {
			return ""
		}
		var name string
		if tool == "gradlew" && hasGradlew {
			if runtime.GOOS == "windows" && fileExists(filepath.Join(repoPath, "gradlew.bat")) {
				name = "gradlew.bat"
			} else {
				name = "./gradlew"
			}
		} else if tool == "gradlew" {
			return ""
		} else {
			name = "gradle"
		}
		return name + " --no-daemon -q integrationTest"
	default:
		return ""
	}
}

func defaultCSharpE2EShellCommand(fw string) string {
	switch fw {
	case "playwright":
		// Node @playwright/test at repo root while gaps/lang are C# (e.g. dotnet/eShop).
		return "npx playwright test"
	case "playwright-dotnet", "selenium":
		// Heuristic: run tests whose FQN contains "E2E" (common naming); override with runner.e2e_test_command when needed.
		return `dotnet test -c Release --filter "FullyQualifiedName~E2E"`
	default:
		return ""
	}
}

// csharpUnitExcludeE2EFilter mirrors the `FullyQualifiedName~E2E` heuristic used for the C# E2E pass:
// when the E2E pass will run in a browser-capable image (mcr.microsoft.com/playwright/dotnet), the unit
// pass runs in a plain dotnet SDK image that lacks Playwright browsers. Without a symmetric `!~E2E`
// filter the C# bootstrap's AsqsPlaywrightSmokeE2E (and any other `*E2E*`-named test) would execute
// twice — once in the unit image where Chromium.LaunchAsync fails with `Executable doesn't exist …`,
// triggering the fixer to add `[Fact(Skip = "…")]` to a perfectly healthy test. See
// `defaultCSharpE2EShellCommand` for the mirror filter; both should move together when the naming
// heuristic is ever generalized (e.g. to trait-based selectors).
const csharpUnitExcludeE2EFilter = `FullyQualifiedName!~E2E`

// csharpUnitExcludeE2EFilterArg is the `--filter "FullyQualifiedName!~E2E"` flag appended to unit
// `dotnet test` invocations when the conditions in csharpUnitExcludeE2ESupported are met.
const csharpUnitExcludeE2EFilterArg = `--filter "` + csharpUnitExcludeE2EFilter + `"`

// csharpDefaultUnitTestCommandWithE2EExclusion is the shell command emitted in place of the runner's
// bare `dotnet test -c Release --no-build` when we need to guarantee E2E-named tests never enter the
// unit pass. Kept in one place so runner defaults, scoped-compile fallbacks, and tests agree on the
// exact form.
const csharpDefaultUnitTestCommandWithE2EExclusion = `dotnet test -c Release --no-build ` + csharpUnitExcludeE2EFilterArg

// applyCSharpE2EExclusionToUnitCommand augments the resolved unit-test command with a
// `FullyQualifiedName!~E2E` filter when the evaluation run is configured for a dual-pass (unit then
// E2E) C# workflow whose E2E pass uses the default FQN~E2E heuristic. When the user provides an
// explicit `E2ETestCommand`, or the E2E framework is something other than playwright-dotnet/selenium
// (e.g. Node @playwright/test driven against a C# backend), the partition between "unit" and "E2E"
// is no longer our concern and we return the command unchanged. Likewise, any user command that
// already declares its own `--filter` (unit or otherwise) is left alone to avoid clobbering intent.
func applyCSharpE2EExclusionToUnitCommand(unitCmd string, opts EvalOptions) string {
	if !csharpUnitExcludeE2ESupported(opts) {
		return unitCmd
	}
	cmd := strings.TrimSpace(unitCmd)
	if cmd == "" {
		return csharpDefaultUnitTestCommandWithE2EExclusion
	}
	if containsVSTestFilterArg(cmd) {
		return cmd
	}
	if !looksLikeDotnetTestCommand(cmd) {
		return cmd
	}
	return cmd + " " + csharpUnitExcludeE2EFilterArg
}

// csharpUnitExcludeE2ESupported returns true when automatic `!~E2E` filtering of the unit pass is
// both safe and valuable for the current evaluation. Safe = we (not the user) picked the E2E filter,
// so the inverse filter accurately partitions the same test set. Valuable = the E2E pass will
// actually run (RunE2ETestPass) for a C# project using a VSTest-based E2E framework.
func csharpUnitExcludeE2ESupported(opts EvalOptions) bool {
	if !opts.RunE2ETestPass {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(opts.Lang)) {
	case "csharp", "cs":
	default:
		return false
	}
	if strings.TrimSpace(opts.E2ETestCommand) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(opts.E2EFramework)) {
	case "playwright-dotnet", "selenium":
		return true
	default:
		return false
	}
}

// containsVSTestFilterArg is true when cmd already declares a VSTest-compatible test selector.
// Both `--filter` and the legacy `/Filter:` slash-form are honored by dotnet test / vstest.console.
func containsVSTestFilterArg(cmd string) bool {
	low := strings.ToLower(cmd)
	return strings.Contains(low, "--filter") || strings.Contains(low, "/filter:")
}

// looksLikeDotnetTestCommand is a conservative check so we only extend commands that actually run
// the VSTest runner. Custom wrapper scripts (e.g. `./run-tests.sh`) are left alone — appending
// `--filter` to them would most likely break their argv handling.
func looksLikeDotnetTestCommand(cmd string) bool {
	low := strings.ToLower(cmd)
	return strings.Contains(low, "dotnet test") || strings.Contains(low, "dotnet vstest")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
