package runner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

var reDotnetShellInsertTFM = regexp.MustCompile(`(?i)^(\s*dotnet\s+(?:build|test|restore|publish|pack|run|msbuild|format|add|remove|list|clean|watch)\s+)(.*)$`)

var (
	reShellQuotedCsproj = regexp.MustCompile(`(?i)"([^"]+\.csproj)"`)
	reDotnetCwdDot      = regexp.MustCompile(`(?i)\bdotnet\s+[a-z]+\s+\.(\s|;|&|$)`)
)

// dotnetFirstArgIsCLI is true when argv[0] is the dotnet driver (basename "dotnet", any path).
func dotnetFirstArgIsCLI(argv []string) bool {
	if len(argv) < 1 {
		return false
	}
	return strings.EqualFold(filepath.Base(strings.TrimSpace(argv[0])), "dotnet")
}

// ensureDotnetDockerInvocation appends a project/sln for C# Docker when argv is exec-form dotnet *or* sh -c "dotnet …".
// compile_command / test_command overrides use the latter; bare dotnet argv was not patched before (bug).
func ensureDotnetDockerInvocation(p profile.ToolchainProfile, argv []string, absCwd string) ([]string, error) {
	if p.ID != profile.CSharpDotnet {
		return argv, nil
	}
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		patched, err := ensureDotnetShellScriptHasProject(argv[2], absCwd)
		if err != nil {
			return nil, err
		}
		return []string{"sh", "-c", patched}, nil
	}
	return ensureDotnetProjectArg(p, argv, absCwd)
}

// applyDotnetDockerTargetFrameworkFallback appends /p:TargetFramework for exec-form or sh -c dotnet commands.
func applyDotnetDockerTargetFrameworkFallback(argv []string, cwdAbs, fallback string) ([]string, error) {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		patched, err := applyDotnetShellScriptTargetFrameworkFallback(argv[2], cwdAbs, fallback)
		if err != nil {
			return nil, err
		}
		return []string{"sh", "-c", patched}, nil
	}
	return applyDotnetTargetFrameworkFallbackArgv(argv, cwdAbs, fallback)
}

func shellScriptReferencesDotnetProject(script string) bool {
	low := strings.ToLower(script)
	if strings.Contains(low, ".csproj") || strings.Contains(low, ".slnx") {
		return true
	}
	if shellScriptMentionsSlnNotSlnx(low) {
		return true
	}
	return reDotnetCwdDot.MatchString(script)
}

// shellScriptMentionsSlnNotSlnx is true if low contains ".sln" that is not the ".sln" prefix of ".slnx".
func shellScriptMentionsSlnNotSlnx(low string) bool {
	for i := 0; i+4 <= len(low); i++ {
		if low[i:i+4] != ".sln" {
			continue
		}
		if i+4 < len(low) && low[i+4] == 'x' {
			continue
		}
		return true
	}
	return false
}

// parseSimpleDotnetShellArgv splits a single-line dotnet command without shell operators or quotes into argv.
// Used so `dotnet format [flags]` gets the same workspace placement as exec-form (especially before --include).
func parseSimpleDotnetShellArgv(script string) ([]string, bool) {
	s := strings.TrimSpace(script)
	if formatCommandNeedsShell(s) {
		return nil, false
	}
	if strings.ContainsAny(s, `'"`) {
		return nil, false
	}
	parts := strings.Fields(s)
	if len(parts) < 2 || !strings.EqualFold(parts[0], "dotnet") {
		return nil, false
	}
	return parts, true
}

func argvToShellSingleCommandLine(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(a))
	}
	return b.String()
}

func ensureDotnetShellScriptHasProject(script string, absCwd string) (string, error) {
	script = strings.TrimSpace(script)
	if parts, ok := parseSimpleDotnetShellArgv(script); ok {
		argv, err := ensureDotnetProjectArg(profile.ToolchainProfile{ID: profile.CSharpDotnet}, parts, absCwd)
		if err != nil {
			return "", err
		}
		if argvHasDotnetProjectFile(argv) {
			return argvToShellSingleCommandLine(argv), nil
		}
		return script, nil
	}
	if shellScriptReferencesDotnetProject(script) {
		return script, nil
	}
	trim := strings.TrimSpace(script)
	if !strings.HasPrefix(strings.ToLower(trim), "dotnet") {
		return script, nil
	}
	rel, err := resolveDotnetEntryRel(absCwd)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return "", fmt.Errorf("no .sln/.slnx or SDK-style .csproj found under %q (dotnet shell command needs a project)", absCwd)
	}
	return script + " " + strconv.Quote(rel), nil
}

func shellScriptHasTargetFrameworkMSBuildProp(script string) bool {
	low := strings.ToLower(script)
	return strings.Contains(low, "/p:targetframework=") || strings.Contains(low, "-p:targetframework=")
}

// msbuildPropKey returns the property name for a /p:Name=value or -p:Name=value token, lowercased, or "".
func msbuildPropKey(arg string) string {
	a := strings.TrimSpace(arg)
	la := strings.ToLower(a)
	if strings.HasPrefix(la, "/p:") {
		a = a[len("/p:"):]
	} else if strings.HasPrefix(la, "-p:") {
		a = a[len("-p:"):]
	} else {
		return ""
	}
	a = strings.TrimSpace(a)
	if i := strings.IndexByte(a, '='); i >= 0 {
		return strings.ToLower(strings.TrimSpace(a[:i]))
	}
	return ""
}

func argvHasMSBuildPropKey(argv []string, key string) bool {
	key = strings.ToLower(key)
	for _, a := range argv {
		if msbuildPropKey(a) == key {
			return true
		}
	}
	return false
}

func shellScriptHasMSBuildPropKey(script, key string) bool {
	low := strings.ToLower(script)
	key = strings.ToLower(key)
	return strings.Contains(low, "/p:"+key+"=") || strings.Contains(low, "-p:"+key+"=")
}

// dotnetTestFrameworkBootstrapMSBuildProps relaxes test_framework_bootstrap / e2e C# verify builds:
// NU1900 on private feeds; net6 + net9 transitive packages (SuppressTfmSupportBuildWarnings); warnings promoted by TreatWarningsAsErrors.
var dotnetTestFrameworkBootstrapMSBuildProps = []string{
	"/p:NuGetAudit=false",
	"/p:SuppressTfmSupportBuildWarnings=true",
	"/p:TreatWarningsAsErrors=false",
}

// dotnetTestDockerHangMitigationProps disables Roslyn shared compilation and the Razor build server during
// `dotnet test` in ephemeral Docker. On Linux/macOS the VBCSCompiler / Razor server processes have been
// observed to keep the CLI alive long after xUnit/VSTest prints results (symptom: wall-clock timeout while
// logs already show failures). MSBUILDDISABLENODEREUSE=1 alone does not shut those down. See e.g.
// https://github.com/dotnet/sdk/issues/9452 and related MSBuild server discussions.
var dotnetTestDockerHangMitigationProps = []string{
	"/p:UseSharedCompilation=false",
	"/p:UseRazorBuildServer=false",
}

// vstestSessionTimeoutMS returns a TestSessionTimeout (ms) for VSTest slightly under the Docker job wall clock so a
// wedged test host fails with a clear error instead of running until the container is SIGKILL'd with no result line.
func vstestSessionTimeoutMS(jobTimeout time.Duration) int {
	if jobTimeout <= 0 {
		jobTimeout = 15 * time.Minute
	}
	ms := int(jobTimeout.Milliseconds() * 9 / 10)
	if ms < 120_000 {
		ms = 120_000
	}
	return ms
}

// ApplyDotnetTestDockerVSTestCLIArgs appends console logger + RunConfiguration.TestSessionTimeout for `dotnet test`
// in Docker (exec argv or sh -c). Improves stuck-after-"Starting test execution" diagnostics and caps hung sessions.
func ApplyDotnetTestDockerVSTestCLIArgs(argv []string, jobTimeout time.Duration) []string {
	ms := vstestSessionTimeoutMS(jobTimeout)
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return []string{"sh", "-c", applyDotnetTestDockerVSTestShellScript(argv[2], ms)}
	}
	if len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	if strings.ToLower(strings.TrimSpace(argv[1])) != "test" {
		return argv
	}
	out := append([]string(nil), argv...)
	if !argvHasArgPrefix(out, "--logger") {
		out = append(out, "--logger", "console;verbosity=normal")
	}
	if argvHasRunConfigurationTestSessionTimeout(out) {
		return out
	}
	token := fmt.Sprintf("RunConfiguration.TestSessionTimeout=%d", ms)
	if i := argvIndexExact(out, "--"); i >= 0 {
		out = append(append(append([]string(nil), out[:i+1]...), token), out[i+1:]...)
		return out
	}
	out = append(out, "--", token)
	return out
}

func argvIndexExact(argv []string, want string) int {
	for i, a := range argv {
		if a == want {
			return i
		}
	}
	return -1
}

func argvHasArgPrefix(argv []string, prefix string) bool {
	prefix = strings.ToLower(prefix)
	for _, a := range argv {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a)), prefix) {
			return true
		}
	}
	return false
}

func argvHasRunConfigurationTestSessionTimeout(argv []string) bool {
	for _, a := range argv {
		if strings.Contains(strings.ToLower(a), "runconfiguration.testsessiontimeout=") {
			return true
		}
	}
	return false
}

func applyDotnetTestDockerVSTestShellScript(script string, timeoutMs int) string {
	s := strings.TrimSpace(script)
	low := strings.ToLower(s)
	if !strings.Contains(low, "dotnet test") {
		return script
	}
	if strings.Contains(low, "--logger") && strings.Contains(low, "runconfiguration.testsessiontimeout=") {
		return script
	}
	// Append after the full shell command; quoted so semicolons in logger verbosity survive one sh -c string.
	return s + fmt.Sprintf(` --logger "console;verbosity=normal" -- RunConfiguration.TestSessionTimeout=%d`, timeoutMs)
}

// WrapDotnetDockerTestWithBuildServerShutdown runs `dotnet build-server shutdown` after `dotnet test` (preserving the
// test exit code). MSBuild worker nodes can otherwise keep the main process alive after VSTest has printed results,
// which makes `docker run` appear to hang until the job timeout despite tests having finished.
func WrapDotnetDockerTestWithBuildServerShutdown(argv []string) []string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		s := strings.TrimSpace(argv[2])
		low := strings.ToLower(s)
		if !strings.Contains(low, "dotnet test") {
			return argv
		}
		if strings.Contains(low, "build-server shutdown") {
			return argv
		}
		// Run the original script in a subshell so `set -e` / `&&` chains inside it do not skip ec= capture.
		return []string{"sh", "-c", "(" + s + "); ec=$?; dotnet build-server shutdown 2>/dev/null || true; exit $ec"}
	}
	if len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	if strings.ToLower(strings.TrimSpace(argv[1])) != "test" {
		return argv
	}
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(a))
	}
	return []string{"sh", "-c", "(" + b.String() + "); ec=$?; dotnet build-server shutdown 2>/dev/null || true; exit $ec"}
}

// applyDotnetExecInsertMSBuildPropsAfterVerb inserts MSBuild /p: properties immediately after the dotnet CLI verb.
func applyDotnetExecInsertMSBuildPropsAfterVerb(argv []string, props []string) []string {
	if len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	var add []string
	for _, p := range props {
		k := msbuildPropKey(p)
		if k == "" || argvHasMSBuildPropKey(argv, k) {
			continue
		}
		// Also skip if already scheduled in add (duplicate keys).
		dup := false
		for _, q := range add {
			if msbuildPropKey(q) == k {
				dup = true
				break
			}
		}
		if !dup {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return argv
	}
	verb := strings.ToLower(strings.TrimSpace(argv[1]))
	switch verb {
	case "format":
		// Same trailing /p: rule as insertDotnetTargetFrameworkMSBuildProp: only after --include + paths.
		if !argvDotNetFormatHasIncludeOption(argv) {
			return argv
		}
		out := append([]string(nil), argv...)
		return append(out, add...)
	case "build", "test", "restore", "publish", "pack", "run", "msbuild", "add", "remove", "list", "clean", "watch":
		out := make([]string, 0, len(argv)+len(add))
		out = append(out, argv[0], argv[1])
		out = append(out, add...)
		out = append(out, argv[2:]...)
		return out
	default:
		out := append([]string(nil), argv...)
		out = append(out, add...)
		return out
	}
}

func applyDotnetShellScriptInsertMSBuildProps(script string, props []string) string {
	s := strings.TrimSpace(script)
	for _, p := range props {
		k := msbuildPropKey(p)
		if k == "" || shellScriptHasMSBuildPropKey(s, k) {
			continue
		}
		if m := reDotnetShellInsertTFM.FindStringSubmatch(s); len(m) == 3 {
			rest := strings.TrimLeft(m[2], " \t")
			if rest != "" {
				s = m[1] + p + " " + rest
			} else {
				s = m[1] + p
			}
			continue
		}
		// No dotnet verb match: leave script unchanged (do not append to npm/sh scripts).
	}
	return s
}

// applyDotnetDisableNuGetAuditArgv inserts /p:NuGetAudit=false after the dotnet verb when argv is exec-form `dotnet …`.
// This avoids NU1900 in CI/Docker when NuGet audit cannot load vulnerability metadata from private feeds (e.g. Azure Artifacts).
func applyDotnetDisableNuGetAuditArgv(argv []string) []string {
	return applyDotnetExecInsertMSBuildPropsAfterVerb(argv, []string{"/p:NuGetAudit=false"})
}

func applyDotnetShellScriptDisableNuGetAudit(script string) string {
	return applyDotnetShellScriptInsertMSBuildProps(script, []string{"/p:NuGetAudit=false"})
}

// ApplyDotnetDockerDisableNuGetAudit disables NuGet vulnerability audit for dotnet (exec argv or sh -c).
// Used for Docker eval/format and for local post-generate `dotnet restore` / `dotnet format --include` so NU1900
// does not fail restores when audit metadata cannot be fetched from private feeds.
func ApplyDotnetDockerDisableNuGetAudit(argv []string) []string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return []string{"sh", "-c", applyDotnetShellScriptDisableNuGetAudit(argv[2])}
	}
	return applyDotnetDisableNuGetAuditArgv(argv)
}

// ApplyDotnetTestFrameworkBootstrapMSBuildProps applies relaxed MSBuild properties for C# test_framework_bootstrap /
// e2e_framework_bootstrap verify (dotnet test/build). Use after TFM fallback injection.
func ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv []string) []string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return []string{"sh", "-c", applyDotnetShellScriptInsertMSBuildProps(argv[2], dotnetTestFrameworkBootstrapMSBuildProps)}
	}
	return applyDotnetExecInsertMSBuildPropsAfterVerb(argv, dotnetTestFrameworkBootstrapMSBuildProps)
}

// ApplyDotnetTestDockerHangMitigationProps applies UseSharedCompilation/UseRazorBuildServer disables for
// Docker eval `dotnet test` (and coverage, which re-invokes test). No-op for non-test argv shapes.
func ApplyDotnetTestDockerHangMitigationProps(argv []string) []string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		s := strings.TrimSpace(argv[2])
		if !strings.Contains(strings.ToLower(s), "dotnet test") {
			return argv
		}
		return []string{"sh", "-c", applyDotnetShellScriptInsertMSBuildProps(argv[2], dotnetTestDockerHangMitigationProps)}
	}
	if len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	if strings.ToLower(strings.TrimSpace(argv[1])) != "test" {
		return argv
	}
	return applyDotnetExecInsertMSBuildPropsAfterVerb(argv, dotnetTestDockerHangMitigationProps)
}

// shellScriptResolvePrimaryCsprojAbs finds a .csproj path in the script (quoted) or falls back to resolveDotnetEntryRel.
func shellScriptResolvePrimaryCsprojAbs(script, cwdAbs string) (abs string, ok bool, err error) {
	cwdAbs = filepath.Clean(cwdAbs)
	if m := reShellQuotedCsproj.FindStringSubmatch(script); len(m) >= 2 {
		rel := filepath.ToSlash(strings.TrimSpace(m[1]))
		if rel == "" {
			return "", false, nil
		}
		p := rel
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwdAbs, filepath.FromSlash(rel))
		}
		p = filepath.Clean(p)
		if strings.EqualFold(filepath.Ext(p), ".csproj") {
			return p, true, nil
		}
	}
	rel, err := resolveDotnetEntryRel(cwdAbs)
	if err != nil || rel == "" || !strings.HasSuffix(strings.ToLower(rel), ".csproj") {
		return "", false, err
	}
	p := filepath.Join(cwdAbs, filepath.FromSlash(rel))
	return filepath.Clean(p), true, nil
}

func applyDotnetShellScriptTargetFrameworkFallback(script, cwdAbs, fallback string) (string, error) {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return script, nil
	}
	if shellScriptHasTargetFrameworkMSBuildProp(script) {
		return script, nil
	}
	low := strings.ToLower(script)
	if !strings.Contains(low, "dotnet") {
		return script, nil
	}
	projAbs, ok, err := shellScriptResolvePrimaryCsprojAbs(script, cwdAbs)
	if err != nil {
		return "", err
	}
	if !ok {
		return script, nil
	}
	conc, err := CsprojDeclaresConcreteTargetFramework(projAbs)
	if err == nil && conc {
		return script, nil
	}
	prop := "/p:TargetFramework=" + fallback
	s := strings.TrimSpace(script)
	if m := reDotnetShellInsertTFM.FindStringSubmatch(s); len(m) == 3 {
		rest := strings.TrimLeft(m[2], " \t")
		if rest != "" {
			return m[1] + prop + " " + rest, nil
		}
		return m[1] + prop, nil
	}
	return s + " " + prop, nil
}
