package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// FormatAfterFixForSandbox runs the resolved formatter after an LLM fix.
//
// It takes the FormatResolveResult rather than a raw command plus a boolean because per-file-ness
// was previously decided three separate times — by ResolveFormatCommand for the run-scope step, by
// the caller's format_only_added policy, and again here from the command's syntax. The three could
// disagree, and did: a configured `mvn spring-javaformat:apply` was classified per-file and handed
// a file path Maven cannot accept. The resolver decides once; this executes that decision.
func FormatAfterFixForSandbox(sb *Sandbox, ctx context.Context, repoPath, lang string, resolved FormatResolveResult, updatedRepoRelPaths []string, timeout time.Duration) error {
	formatCommand := strings.TrimSpace(resolved.Command)
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

	if resolved.PerFile && IsDotNetFormatCommand(formatCommand) {
		return formatAfterFixDotNetInclude(sb, ctx, absGit, formatCwd, lang, updatedRepoRelPaths, t, isDocker)
	}

	if resolved.PerFile {
		if !isDocker {
			return RunFormatCommandFiles(ctx, formatCwd, formatCommand, updatedRepoRelPaths, []string{".java"}, t)
		}
		// U10: Docker formats the same files rather than falling back to the whole repository.
		return sb.runDockerPerFileFormat(ctx, absGit, lang, formatCommand, updatedRepoRelPaths, []string{".java"}, t)
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
	argv, derr = applyDotnetEvalTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
	if derr != nil {
		return fmt.Errorf("format (docker): %w", derr)
	}
	netRestore := strings.TrimSpace(s.JobNetworkRestore)
	if netRestore == "" {
		netRestore = "bridge"
	}
	didRestore := false
	if rargv0 := dotnetRestoreArgvFromFormatArgv(argv); rargv0 != nil {
		rargv := ApplyDotnetDisableNuGetAudit(append([]string(nil), rargv0...))
		rargv, derr = ensureDotnetInvocation(p, rargv, absCwd)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=restore-deps argv=[%s] network=%s (include)\n", strings.Join(rargv, " "), netRestore)
		rr, rerr := s.runDockerJobWithTimeout(ctx, absGit, p, rargv, netRestore, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
		rout := rr.CombinedOutput
		if rerr != nil {
			return dockerJobRunError("format (docker): restore before format", rerr, rout, timeout)
		}
		if rr.ExitCode != 0 {
			return fmt.Errorf("format (docker): dotnet restore before format: exit %d\n%s", rr.ExitCode, rout)
		}
		didRestore = true
	}
	if didRestore {
		argv = dotnetFormatArgvInsertNoRestore(argv)
	}
	argv, derr = ensureDotnetInvocation(p, argv, absCwd)
	if derr != nil {
		return fmt.Errorf("format (docker): %w", derr)
	}
	argv = ApplyDotnetDisableNuGetAudit(argv)

	fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=main argv=[%s] network=%s (include)\n", strings.Join(argv, " "), net)
	res, runErr := s.runDockerJobWithTimeout(ctx, absGit, p, argv, net, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
	out := res.CombinedOutput
	if runErr != nil {
		return dockerJobRunError("format (docker)", runErr, out, timeout)
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
		argv, derr = ensureDotnetInvocation(p, argv, absCwd)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		argv, derr = applyDotnetEvalTargetFrameworkFallback(argv, absCwd, s.DotNetFallbackTargetFramework)
		if derr != nil {
			return fmt.Errorf("format (docker): %w", derr)
		}
		argv = ApplyDotnetDisableNuGetAudit(argv)
	}

	fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=main argv=[%s] network=%s\n", strings.Join(argv, " "), net)
	res, runErr := s.runDockerJobWithTimeout(ctx, abs, p, argv, net, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
	out := res.CombinedOutput
	if runErr != nil {
		return dockerJobRunError("format (docker)", runErr, out, timeout)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("format (docker): exit %d\n%s", res.ExitCode, out)
	}
	fmt.Fprintln(os.Stderr, "  FormatAfterFix (docker): ok.")
	return nil
}

// runDockerPerFileFormat runs a per-file formatter over exactly the given files, inside the
// toolchain container.
//
// One container run, not one per file: a fresh container per file would multiply an already
// non-trivial startup cost by the number of files the fixer touched. The loop stops at the first
// failure so the outcome matches RunFormatCommandFiles on the local target, which returns on the
// first error rather than formatting on regardless.
func (s *Sandbox) runDockerPerFileFormat(ctx context.Context, gitRootAbs, lang, formatCommand string, files, extensions []string, timeout time.Duration) error {
	filtered := filterFormatFilesByExtension(files, extensions)
	script := dockerPerFileFormatScript(formatCommand, filtered)
	if script == "" {
		return nil
	}
	abs, err := filepath.Abs(strings.TrimSpace(gitRootAbs))
	if err != nil || abs == "" {
		return fmt.Errorf("format (docker --per-file): invalid repo path: %w", err)
	}
	absCwd := s.evalHostCwd(abs)
	p, perr := profile.ResolveToolchain(absCwd, strings.ToLower(strings.TrimSpace(lang)), s.EvalProfile,
		s.ImageJavaMaven, s.ImageJavaGradle, s.ImageNode, s.ImageDotNet)
	if perr != nil {
		return fmt.Errorf("format (docker --per-file): %w", perr)
	}

	net := strings.TrimSpace(s.JobNetworkTest)
	if net == "" {
		net = "none"
	}
	if s.DockerDisableOfflineTest {
		if net = strings.TrimSpace(s.JobNetworkRestore); net == "" {
			net = "bridge"
		}
	}

	argv := []string{"sh", "-c", script}
	fmt.Fprintf(os.Stderr, "[asqs-eval] step=FormatAfterFix phase=main argv=[%s] network=%s (per-file, %d file(s))\n",
		strings.Join(argv, " "), net, len(filtered))
	res, runErr := s.runDockerJobWithTimeout(ctx, abs, p, argv, net, dockerImageNeedsPlaywrightIPC(p.Image), timeout)
	if runErr != nil {
		return dockerJobRunError("format (docker --per-file)", runErr, res.CombinedOutput, timeout)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("format (docker --per-file): exit %d\n%s", res.ExitCode, res.CombinedOutput)
	}
	fmt.Fprintln(os.Stderr, "  FormatAfterFix (docker --per-file): ok.")
	return nil
}

// dockerPerFileFormatScript builds `for f in <files>; do <cmd> "$f" || exit $?; done`.
//
// The path is passed as the last argument, matching RunFormatCommandFiles, and `|| exit $?`
// preserves the first failure's exit code rather than reporting the loop's last iteration.
func dockerPerFileFormatScript(formatCommand string, files []string) string {
	parts := strings.Fields(strings.TrimSpace(formatCommand))
	if len(parts) == 0 || len(files) == 0 {
		return ""
	}
	var quoted []string
	for _, f := range files {
		f = strings.TrimSpace(filepath.ToSlash(f))
		if f == "" {
			continue
		}
		quoted = append(quoted, strconv.Quote(f))
	}
	if len(quoted) == 0 {
		return ""
	}
	var cmd strings.Builder
	for i, p := range parts {
		if i > 0 {
			cmd.WriteByte(' ')
		}
		cmd.WriteString(shellQuoteTokenIfNeeded(p))
	}
	return fmt.Sprintf(`for f in %s; do %s "$f" || exit $?; done`, strings.Join(quoted, " "), cmd.String())
}
