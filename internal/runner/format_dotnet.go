package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// ErrFormatSkippedNoDotnet is returned when a dotnet-based format command was not run because
// the dotnet CLI is not on PATH (common when only Docker provides the SDK).
var ErrFormatSkippedNoDotnet = errors.New("format skipped: dotnet not in PATH")

// dotnetOnPATH is true when the dotnet CLI is discoverable via PATH (same as a bare `dotnet` exec).
func dotnetOnPATH() bool {
	_, err := exec.LookPath("dotnet")
	return err == nil
}

// EffectivePostGenerateFormatCommand returns runner.format_command when set; for C# when empty, defaults to "dotnet format".
func EffectivePostGenerateFormatCommand(lang, configured string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		return "dotnet format"
	default:
		return ""
	}
}

// IsDotNetFormatCommand is true when the format step should use dotnet format --include (format_only_added path).
func IsDotNetFormatCommand(cmd string) bool {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return false
	}
	low := strings.ToLower(c)
	return low == "dotnet format" || strings.HasPrefix(low, "dotnet format ")
}

// dotnetFormatIncludeBatches groups repo-relative .cs paths by dotnetproj.NearestCsprojRel.
// legacy is true when any path has no discoverable owning project; the caller should run one format pass
// with workspace discovery only (resolveDotnetEntryRel), matching pre-batching behavior.
func dotnetFormatIncludeBatches(repoAbs string, files []string) (batches [][]string, preferred []string, legacy bool) {
	if len(files) == 0 {
		return nil, nil, false
	}
	byProj := make(map[string][]string)
	for _, f := range files {
		proj, ok := dotnetproj.NearestCsprojRel(repoAbs, f)
		if !ok {
			return nil, nil, true
		}
		byProj[proj] = append(byProj[proj], f)
	}
	keys := make([]string, 0, len(byProj))
	for k := range byProj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	batches = make([][]string, len(keys))
	preferred = make([]string, len(keys))
	for i, k := range keys {
		preferred[i] = k
		batches[i] = byProj[k]
	}
	return batches, preferred, false
}

// dotnetFormatWorkspaceMSBuildProps returns the workspace token (solution/project/dir) after `dotnet format` and any
// trailing /p: or -p: MSBuild properties from the full argv (used to build a matching `dotnet restore`).
func dotnetFormatWorkspaceMSBuildProps(argv []string) (workspace string, props []string, ok bool) {
	if len(argv) < 3 || !strings.EqualFold(strings.TrimSpace(argv[1]), "format") {
		return "", nil, false
	}
	ws := strings.TrimSpace(argv[2])
	if ws == "" || strings.HasPrefix(ws, "-") {
		return "", nil, false
	}
	for _, a := range argv[3:] {
		t := strings.TrimSpace(a)
		lt := strings.ToLower(t)
		if strings.HasPrefix(lt, "/p:") || strings.HasPrefix(lt, "-p:") {
			props = append(props, a)
		}
	}
	return ws, props, true
}

// dotnetRestoreArgvFromFormatArgv builds `dotnet restore <workspace> …props` from a `dotnet format <workspace> …` argv.
func dotnetRestoreArgvFromFormatArgv(formatArgv []string) []string {
	ws, props, ok := dotnetFormatWorkspaceMSBuildProps(formatArgv)
	if !ok {
		return nil
	}
	out := []string{"dotnet", "restore", ws}
	out = append(out, props...)
	return out
}

// dotnetRestoreArgvForPreFormat is the argv for the explicit `dotnet restore` run before `dotnet format --no-restore`.
// It adds /p:NuGetAudit=false (same as Docker eval) so restore does not fail with NU1900 when vulnerability audit
// cannot reach private feeds (e.g. Azure Artifacts without credentials on the format host).
func dotnetRestoreArgvForPreFormat(formatArgv []string) []string {
	r := dotnetRestoreArgvFromFormatArgv(formatArgv)
	if r == nil {
		return nil
	}
	return ApplyDotnetDockerDisableNuGetAudit(append([]string(nil), r...))
}

// dotnetFormatArgvInsertNoRestore inserts --no-restore immediately after the workspace argument so `dotnet format`
// does not run an implicit restore (often fails with obscure MSBuild errors; explicit `dotnet restore` is clearer).
func dotnetFormatArgvInsertNoRestore(argv []string) []string {
	for _, a := range argv {
		if strings.EqualFold(strings.TrimSpace(a), "--no-restore") {
			return argv
		}
	}
	if len(argv) < 3 || !strings.EqualFold(strings.TrimSpace(argv[1]), "format") {
		return argv
	}
	ws := strings.TrimSpace(argv[2])
	if ws == "" || strings.HasPrefix(ws, "-") {
		return argv
	}
	out := make([]string, 0, len(argv)+1)
	out = append(out, argv[0], argv[1], argv[2], "--no-restore")
	out = append(out, argv[3:]...)
	return out
}

func runDotNetFormatIncludeOnce(ctx context.Context, repoPath string, relFiles []string, preferredWorkspace string, timeout time.Duration, dotnetFallbackTFM string) error {
	if len(relFiles) == 0 {
		return nil
	}
	if !dotnetOnPATH() {
		fmt.Fprintf(os.Stderr, "  warning: %v\n", ErrFormatSkippedNoDotnet)
		return ErrFormatSkippedNoDotnet
	}
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	dir := filepath.Clean(repoPath)
	argv := []string{"dotnet", "format", "--verbosity", "quiet", "--include"}
	argv = append(argv, relFiles...)
	var err error
	argv, err = ensureDotnetProjectArgPreferred(profile.ToolchainProfile{ID: profile.CSharpDotnet}, argv, dir, preferredWorkspace)
	if err != nil {
		return fmt.Errorf("dotnet format --include: %w", err)
	}
	argv, err = applyDotnetTargetFrameworkFallbackArgv(argv, dir, dotnetFallbackTFM)
	if err != nil {
		return fmt.Errorf("dotnet format --include: %w", err)
	}
	didRestore := false
	if rargv := dotnetRestoreArgvForPreFormat(argv); rargv != nil {
		rcmd := exec.CommandContext(runCtx, rargv[0], rargv[1:]...)
		rcmd.Dir = dir
		rcmd.Env = formatEnv(dir)
		var rout strings.Builder
		rcmd.Stdout = &rout
		rcmd.Stderr = &rout
		if err := rcmd.Run(); err != nil {
			return fmt.Errorf("dotnet restore (before format): %w\n%s", err, rout.String())
		}
		didRestore = true
	}
	if didRestore {
		argv = dotnetFormatArgvInsertNoRestore(argv)
	}
	argv = ApplyDotnetDockerDisableNuGetAudit(append([]string(nil), argv...))
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = formatEnv(dir)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dotnet format --include: %w\n%s", err, out.String())
	}
	return nil
}

// RunDotNetFormatInclude runs `dotnet format` with --include for repo-relative paths (typically .cs files written in post-generate).
// Files are grouped by nearest .csproj so the workspace matches mono-repo layouts (e.g. separate test trees under mono_repo_test_workspace)
// even when a root .sln would otherwise be chosen and would not load those projects.
// When dotnetFallbackTFM is set, may append /p:TargetFramework for projects that omit a concrete TFM in the .csproj file.
func RunDotNetFormatInclude(ctx context.Context, repoPath string, relFiles []string, timeout time.Duration, dotnetFallbackTFM string) error {
	var files []string
	seen := make(map[string]bool)
	for _, f := range relFiles {
		f = strings.TrimSpace(filepath.ToSlash(f))
		if f == "" || !strings.HasSuffix(strings.ToLower(f), ".cs") {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil
	}
	dir := filepath.Clean(repoPath)
	batches, prefs, legacy := dotnetFormatIncludeBatches(dir, files)
	if legacy {
		return runDotNetFormatIncludeOnce(ctx, repoPath, files, "", timeout, dotnetFallbackTFM)
	}
	for i := range batches {
		pref := ""
		if i < len(prefs) {
			pref = prefs[i]
		}
		if err := runDotNetFormatIncludeOnce(ctx, repoPath, batches[i], pref, timeout, dotnetFallbackTFM); err != nil {
			return err
		}
	}
	return nil
}
