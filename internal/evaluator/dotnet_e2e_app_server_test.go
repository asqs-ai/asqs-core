package evaluator

import (
	"context"
	"os"
	"testing"
)

// A running application is needed only where a BROWSER drives it. The in-process API stack hosts
// the application inside the test process, so starting a second copy would only contend for a port.
func TestCSharpBrowserE2EFramework(t *testing.T) {
	for _, fw := range []string{"playwright-dotnet", "selenium", "selenium-dotnet", "PLAYWRIGHT-DOTNET"} {
		if !csharpBrowserE2EFramework(fw) {
			t.Errorf("csharpBrowserE2EFramework(%q) = false, want true", fw)
		}
	}
	for _, fw := range []string{"webapplicationfactory", "playwright", "cypress", "", "junit"} {
		if csharpBrowserE2EFramework(fw) {
			t.Errorf("csharpBrowserE2EFramework(%q) = true, want false", fw)
		}
	}
}

// An unset surface is treated as possibly having a UI: the surface is a refinement, and refusing to
// start the application because nobody detected one would break exactly the case this exists for.
func TestCSharpSurfaceHasUI(t *testing.T) {
	for _, s := range []string{"ui", "mixed", "", "UI", "Mixed"} {
		if !csharpSurfaceHasUI(s) {
			t.Errorf("csharpSurfaceHasUI(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"api", "none", "API", "None"} {
		if csharpSurfaceHasUI(s) {
			t.Errorf("csharpSurfaceHasUI(%q) = true, want false", s)
		}
	}
}

// Every condition that does not apply is a run that proceeds exactly as before: another language,
// an in-process API stack, a surface with no UI, or no repository to start anything from.
func TestStartCSharpE2EAppServer_skipsWhereItDoesNotApply(t *testing.T) {
	cases := []struct {
		name string
		opts EvalOptions
	}{
		{"another language", EvalOptions{Lang: "java", E2EFramework: "playwright-java", E2ESurface: "ui", RepoPath: t.TempDir()}},
		{"the in-process API stack", EvalOptions{Lang: "csharp", E2EFramework: "webapplicationfactory", E2ESurface: "ui", RepoPath: t.TempDir()}},
		{"a surface with no UI", EvalOptions{Lang: "csharp", E2EFramework: "playwright-dotnet", E2ESurface: "api", RepoPath: t.TempDir()}},
		{"no repository", EvalOptions{Lang: "csharp", E2EFramework: "playwright-dotnet", E2ESurface: "ui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stop := startCSharpE2EAppServer(context.Background(), tc.opts, nil)
			defer stop()
			if len(env) != 0 {
				t.Errorf("started a server that does not apply: %v", env)
			}
		})
	}
}

// An operator who exported ASQS_BASE_URL to point at a deployed environment meant it, and a run
// must not silently delete their setting.
func TestApplyCSharpE2EAppServerEnv_restoresTheOperatorsValue(t *testing.T) {
	t.Setenv("ASQS_BASE_URL", "https://staging.example.test")
	// A configuration that starts nothing, so only the restore path is exercised.
	restore := applyCSharpE2EAppServerEnv(context.Background(), EvalOptions{Lang: "java"}, nil)
	restore()
	if got := os.Getenv("ASQS_BASE_URL"); got != "https://staging.example.test" {
		t.Errorf("ASQS_BASE_URL = %q after the pass, want the operator's value", got)
	}
}
