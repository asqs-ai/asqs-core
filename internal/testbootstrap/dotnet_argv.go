package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/runner"
)

// dotnetTFMFallbackFromRunner returns runner.dotnet_fallback_target_framework, or when unset
// RUNNER_DOTNET_FALLBACK_TARGET_FRAMEWORK (bootstrap may run with a partial RunnerConfig in some deployments).
func dotnetTFMFallbackFromRunner(r *config.RunnerConfig) string {
	if r != nil {
		if v := strings.TrimSpace(r.DotNetFallbackTargetFramework); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv("RUNNER_DOTNET_FALLBACK_TARGET_FRAMEWORK"))
}

// appendDotnetCLIArgsTFMFallback inserts /p:TargetFramework=<fallback> immediately after the dotnet subcommand
// (build|test|restore|…) when the .csproj does not declare a concrete in-file TFM. Trailing placement breaks some
// dotnet CLI / MSBuild flows (e.g. args after --filter). If analysis of the project file fails, the fallback is
// still applied when the user configured it.
func appendDotnetCLIArgsTFMFallback(argv []string, csprojAbs, fallback string) []string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" || len(argv) < 2 || !strings.EqualFold(filepath.Base(strings.TrimSpace(argv[0])), "dotnet") {
		return argv
	}
	for _, a := range argv {
		la := strings.ToLower(strings.TrimSpace(a))
		if strings.HasPrefix(la, "/p:targetframework=") || strings.HasPrefix(la, "-p:targetframework=") {
			return argv
		}
	}
	ok, err := runner.CsprojDeclaresConcreteTargetFramework(csprojAbs)
	if err == nil && ok {
		return argv
	}
	prop := "/p:TargetFramework=" + fallback
	verb := strings.ToLower(strings.TrimSpace(argv[1]))
	switch verb {
	case "build", "test", "restore", "publish", "pack", "run", "msbuild", "format", "add", "remove", "list", "clean", "watch":
		out := make([]string, 0, len(argv)+1)
		out = append(out, argv[0], argv[1], prop)
		out = append(out, argv[2:]...)
		return out
	default:
		out := append([]string(nil), argv...)
		out = append(out, prop)
		return out
	}
}
