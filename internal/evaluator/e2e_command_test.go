package evaluator

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEffectiveE2ETestCommandFromOpts_javaMaven(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mvnw path style differs on Windows CI")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>"), 0644); err != nil {
		t.Fatal(err)
	}
	got := EffectiveE2ETestCommandFromOpts(EvalOptions{
		RepoPath:     dir,
		Lang:         "java",
		BuildTool:    "mvn",
		E2EFramework: "playwright-java",
	})
	want := "mvn -q -B failsafe:integration-test"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestEffectiveE2ETestCommandFromOpts_csharpPlaywrightDotnet(t *testing.T) {
	got := EffectiveE2ETestCommandFromOpts(EvalOptions{
		Lang:         "csharp",
		E2EFramework: "playwright-dotnet",
	})
	want := `dotnet test -c Release --filter "FullyQualifiedName~E2E"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestEffectiveE2ETestCommandFromOpts_csharpNodePlaywright(t *testing.T) {
	got := EffectiveE2ETestCommandFromOpts(EvalOptions{
		Lang:         "csharp",
		E2EFramework: "playwright",
	})
	if want := "npx playwright test"; got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// The unit pass must mirror the C# E2E `~E2E` filter with `!~E2E` when a dual-pass run is configured
// and we picked the E2E filter ourselves. Without this, the C# Playwright bootstrap smoke test
// (`AsqsPlaywrightSmokeE2E`) runs in the browser-less unit image and the fixer adds `[Fact(Skip=…)]`
// to otherwise-healthy code. See `applyCSharpE2EExclusionToUnitCommand`.
func TestResolveUnitTestCommand_csharpAppendsE2EExclusion_defaultCommand(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		RunE2ETestPass: true,
		E2EFramework:   "playwright-dotnet",
	})
	want := `dotnet test -c Release --no-build --filter "FullyQualifiedName!~E2E"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestResolveUnitTestCommand_csharpAppendsE2EExclusion_toUserTestCommand(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    "dotnet test -c Release --no-build",
		RunE2ETestPass: true,
		E2EFramework:   "playwright-dotnet",
	})
	want := `dotnet test -c Release --no-build --filter "FullyQualifiedName!~E2E"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestResolveUnitTestCommand_csharpAppendsE2EExclusion_toUnitTestCommand(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:            "csharp",
		UnitTestCommand: "dotnet test -c Release --no-build",
		TestCommand:     "dotnet test ignored",
		RunE2ETestPass:  true,
		E2EFramework:    "selenium",
	})
	want := `dotnet test -c Release --no-build --filter "FullyQualifiedName!~E2E"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// Respect explicit user filter: never clobber or double-filter when the command already declares
// `--filter` (or the slash-form `/Filter:`). The user owns the partition in that case.
func TestResolveUnitTestCommand_csharpPreservesUserFilter(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    `dotnet test -c Release --no-build --filter "Category=Unit"`,
		RunE2ETestPass: true,
		E2EFramework:   "playwright-dotnet",
	})
	want := `dotnet test -c Release --no-build --filter "Category=Unit"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestResolveUnitTestCommand_csharpSkipsWhenE2EPassDisabled(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    "dotnet test -c Release --no-build",
		RunE2ETestPass: false,
		E2EFramework:   "playwright-dotnet",
	})
	want := "dotnet test -c Release --no-build"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// When the user provides their own E2ETestCommand we can no longer assume the partition is
// FQN-based (they may use Category= traits etc.); the inverse filter would be unsafe.
func TestResolveUnitTestCommand_csharpSkipsWhenUserE2ECommand(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    "dotnet test -c Release --no-build",
		RunE2ETestPass: true,
		E2EFramework:   "playwright-dotnet",
		E2ETestCommand: "dotnet test --filter Category=E2E",
	})
	want := "dotnet test -c Release --no-build"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// Node Playwright (`playwright` framework on csharp) uses `npx playwright test`, which runs in a
// completely different test runner; appending a VSTest filter to the dotnet unit pass makes no
// sense in that case.
func TestResolveUnitTestCommand_csharpSkipsForNodePlaywright(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    "dotnet test -c Release --no-build",
		RunE2ETestPass: true,
		E2EFramework:   "playwright",
	})
	want := "dotnet test -c Release --no-build"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestResolveUnitTestCommand_skipsForNonCsharpLang(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "java",
		TestCommand:    "mvn -q test",
		RunE2ETestPass: true,
		E2EFramework:   "playwright-java",
	})
	want := "mvn -q test"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// Non-dotnet custom commands (e.g. repo-supplied wrapper scripts) are left untouched because
// naively appending `--filter` to an arbitrary script is likely to break its argv handling.
func TestResolveUnitTestCommand_csharpSkipsUnknownCustomCommand(t *testing.T) {
	got := resolveUnitTestCommand(EvalOptions{
		Lang:           "csharp",
		TestCommand:    "./scripts/run-tests.sh",
		RunE2ETestPass: true,
		E2EFramework:   "playwright-dotnet",
	})
	want := "./scripts/run-tests.sh"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// Scoped-compile fallback sets opts.UnitTestCommand to a csproj-scoped `dotnet test` command; the
// exclusion filter should still compose on top of that so the fallback path also skips E2E-named
// tests in the unit pass.
func TestResolveUnitTestCommand_csharpAppendsE2EExclusion_toScopedDotnetTestCommand(t *testing.T) {
	scoped := `dotnet test 'src/App.Tests/App.Tests.csproj' -c Release /p:RestoreIgnoreFailedSources=true`
	got := resolveUnitTestCommand(EvalOptions{
		Lang:            "csharp",
		UnitTestCommand: scoped,
		RunE2ETestPass:  true,
		E2EFramework:    "playwright-dotnet",
	})
	want := scoped + ` --filter "FullyQualifiedName!~E2E"`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}
