package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// FormatAfterFixForSandbox runs formatCommand after an LLM fix. When formatOnlyAdded is true and the command is
// dotnet format, only *.cs paths in updatedRepoRelPaths are passed to dotnet format --include (local and Docker).
// When formatOnlyAdded is true and the command is a per-file Java-style formatter (no shell), local runs use
// RunFormatCommandFiles for .java paths only; Docker falls back to a whole-repo format run.
// When formatOnlyAdded is false, formatCommand runs once for the whole repo as before.
func FormatAfterFixForSandbox(sb *Sandbox, ctx context.Context, repoPath, lang, formatCommand string, formatOnlyAdded bool, updatedRepoRelPaths []string, timeout time.Duration) error {
	formatCommand = strings.TrimSpace(formatCommand)
	if formatCommand == "" || sb == nil {
		return nil
	}
	t := timeout
	if t <= 0 {
		t = 2 * time.Minute
	}
	isDocker := strings.ToLower(strings.TrimSpace(sb.Type)) == "docker"
	absGit, err := filepath.Abs(strings.TrimSpace(repoPath))
	if err != nil || absGit == "" {
		return fmt.Errorf("format: invalid repo path: %w", err)
	}
	formatCwd := sb.evalHostCwd(absGit)

	if formatOnlyAdded && IsDotNetFormatCommand(formatCommand) {
		return formatAfterFixDotNetInclude(sb, ctx, absGit, formatCwd, lang, updatedRepoRelPaths, t, isDocker)
	}

	if formatOnlyAdded && !IsDotNetFormatCommand(formatCommand) && !formatCommandNeedsShell(formatCommand) {
		if !isDocker {
			return RunFormatCommandFiles(ctx, formatCwd, formatCommand, updatedRepoRelPaths, []string{".java"}, t)
		}
		// Docker: no per-file helper yet; whole-repo format is better than skipping.
	}

	if !isDocker {
		return RunFormatCommand(ctx, formatCwd, formatCommand, t)
	}
	return sb.runDockerFormatAfterFix(ctx, absGit, lang, formatCommand, t)
}

func formatAfterFixDotNetInclude(sb *Sandbox, ctx context.Context, absGit, formatCwd, lang string, updatedRepoRelPaths []string, timeout time.Duration, isDocker bool) error {
	var cs []string
	seen := make(map[string]bool)
	for _, f := range updatedRepoRelPaths {
		f = strings.TrimSpace(filepath.ToSlash(f))
		if f == "" || !strings.HasSuffix(strings.ToLower(f), ".cs") {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		cs = append(cs, f)
	}
	if len(cs) == 0 {
		return nil
	}
	if !isDocker {
		return RunDotNetFormatInclude(ctx, formatCwd, cs, timeout, sb.DotNetFallbackTargetFramework)
	}
	return sb.runDockerDotNetFormatAfterFixInclude(ctx, absGit, lang, cs, timeout)
}

func (s *Sandbox) runDockerDotNetFormatAfterFixInclude(ctx context.Context, gitRootAbs, lang string, relCSFiles []string, timeout time.Duration) error {
	lang = strings.ToLower(strings.TrimSpace(lang))
	abs, err := filepath.Abs(strings.TrimSpace(gitRootAbs))
	if err != nil || abs == "" {
		return fmt.Errorf("format (docker): invalid repo path: %w", err)
	}
	absCwd := s.evalHostCwd(abs)
	p, err := profile.ResolveToolchain(absCwd, lang, s.EvalProfile, s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		return fmt.Errorf("format (docker): %w", err)
	}
	if p.ID != profile.CSharpDotnet {
		return fmt.Errorf("format (docker): expected csharp-dotnet profile for dotnet format --include")
	}

	net := strings.TrimSpace(s.JobNetworkTest)
	if net == "" {
		net = "none"
	}
	if s.DockerDisableOfflineTest {
		net = strings.TrimSpace(s.JobNetworkRestore)
		if net == "" {
			net = "bridge"
		}
	}

	batches, prefs, legacy := dotnetFormatIncludeBatches(absCwd, relCSFiles)
	if legacy {
		return s.runDockerDotNetFormatIncludeBatch(ctx, abs, p, absCwd, relCSFiles, "", net, timeout)
	}
	for i := range batches {
		pref := ""
		if i < len(prefs) {
			pref = prefs[i]
		}
		if err := s.runDockerDotNetFormatIncludeBatch(ctx, abs, p, absCwd, batches[i], pref, net, timeout); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "  FormatAfterFix (docker --include): ok.")
	return nil
}

func (s *Sandbox) runDockerDotNetFormatIncludeBatch(ctx context.Context, absGit string, p profile.ToolchainProfile, absCwd string, relCSFiles []string, preferredWorkspace string, net string, timeout time.Duration) error {
	argv := []string{"dotnet", "format", "--verbosity", "quiet", "--include"}
	for _, f := range relCSFiles {
		argv = append(argv, f)
	}
	var derr error
	argv, derr = ensureDotnetProjectArgPreferred(p, argv, absCwd, preferredWorkspace)
	if derr != nil {
		return fmt.Errorf("format (docker): %w", derr)
	}
	// TFM props on exec-form argv before docker shell wrapping so we can derive a matching `dotnet restore`.
	argv, derr = applyDotnetDockerTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
	if derr != nil {
		return fmt.Errorf("format (docker): %w", derr)
	}
	netRestore := strings.TrimSpace(s.JobNetworkRestore)
	if netRestore == "" {
		netRestore = "bridge"
	}
	didRestore := false
	if rargv0 := dotnetRestoreArgvFromFormatArgv(argv); rargv0 != nil {
		rargv := ApplyDotnetDockerDisableNuGetAudit(append([]string(nil), rargv0...))
		rargv, derr = ensureDotnetDockerInvocation(p, rargv, absCwd)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=restore-deps argv=[%s] network=%s (include)\n", strings.Join(rargv, " "), netRestore)
		rr, rerr := s.runDockerJobWithTimeout(ctx, absGit, p, rargv, netRestore, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
		rout := rr.CombinedOutput
		if rerr != nil && rr.ExitCode == 0 {
			return fmt.Errorf("format (docker): restore before format: %w\n%s", rerr, rout)
		}
		if rr.ExitCode != 0 {
			return fmt.Errorf("format (docker): dotnet restore before format: exit %d\n%s", rr.ExitCode, rout)
		}
		didRestore = true
	}
	if didRestore {
		argv = dotnetFormatArgvInsertNoRestore(argv)
	}
	argv, derr = ensureDotnetDockerInvocation(p, argv, absCwd)
	if derr != nil {
		return fmt.Errorf("format (docker): %w", derr)
	}
	argv = ApplyDotnetDockerDisableNuGetAudit(argv)

	fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=main argv=[%s] network=%s (include)\n", strings.Join(argv, " "), net)
	res, runErr := s.runDockerJobWithTimeout(ctx, absGit, p, argv, net, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
	out := res.CombinedOutput
	if runErr != nil && res.ExitCode == 0 {
		return fmt.Errorf("format (docker): %w\n%s", runErr, out)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("format (docker): exit %d\n%s", res.ExitCode, out)
	}
	return nil
}

func (s *Sandbox) runDockerFormatAfterFix(ctx context.Context, gitRootAbs, lang, formatCommand string, timeout time.Duration) error {
	lang = strings.ToLower(strings.TrimSpace(lang))
	abs, err := filepath.Abs(strings.TrimSpace(gitRootAbs))
	if err != nil || abs == "" {
		return fmt.Errorf("format (docker): invalid repo path: %w", err)
	}
	absCwd := s.evalHostCwd(abs)
	// Same image resolution as compile/test; do not apply compile/test argv overrides to the format command.
	p, err := profile.ResolveToolchain(absCwd, lang, s.EvalProfile, s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if err != nil {
		return fmt.Errorf("format (docker): %w", err)
	}

	net := strings.TrimSpace(s.JobNetworkTest)
	if net == "" {
		net = "none"
	}
	if s.DockerDisableOfflineTest {
		net = strings.TrimSpace(s.JobNetworkRestore)
		if net == "" {
			net = "bridge"
		}
	}

	var argv []string
	if formatCommandNeedsShell(formatCommand) {
		argv = []string{"sh", "-c", formatCommand}
	} else {
		argv = strings.Fields(formatCommand)
		if len(argv) == 0 {
			return nil
		}
	}
	if p.ID == profile.CSharpDotnet {
		var derr error
		argv, derr = ensureDotnetDockerInvocation(p, argv, absCwd)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		argv, derr = applyDotnetDockerTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		argv = ApplyDotnetDockerDisableNuGetAudit(argv)
	}

	fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=main argv=[%s] network=%s\n", strings.Join(argv, " "), net)
	res, runErr := s.runDockerJobWithTimeout(ctx, abs, p, argv, net, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
	out := res.CombinedOutput
	if runErr != nil && res.ExitCode == 0 {
		return fmt.Errorf("format (docker): %w\n%s", runErr, out)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("format (docker): exit %d\n%s", res.ExitCode, out)
	}
	fmt.Fprintln(os.Stderr, "  FormatAfterFix (docker): ok.")
	return nil
}
