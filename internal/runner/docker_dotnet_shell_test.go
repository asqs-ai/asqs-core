package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

func TestShellScriptReferencesDotnetProject_slnVsSlnx(t *testing.T) {
	if !shellScriptReferencesDotnetProject(`dotnet build "src/App.sln"`) {
		t.Fatal("want .sln")
	}
	if !shellScriptReferencesDotnetProject(`dotnet build src/App.slnx`) {
		t.Fatal("want .slnx")
	}
	// ".sln" is a substring of ".slnx"; only .slnx must not be treated as .sln alone.
	if shellScriptReferencesDotnetProject(`dotnet build`) {
		t.Fatal("bare dotnet build")
	}
}

func TestDotnetFirstArgIsCLI(t *testing.T) {
	if !dotnetFirstArgIsCLI([]string{"dotnet", "build"}) {
		t.Fatal("bare dotnet")
	}
	if !dotnetFirstArgIsCLI([]string{"/usr/share/dotnet/dotnet", "restore"}) {
		t.Fatal("path dotnet")
	}
	if dotnetFirstArgIsCLI([]string{"sh", "-c", "dotnet build"}) {
		t.Fatal("sh is not dotnet")
	}
}

func TestEnsureDotnetDockerInvocation_shCFormatInsertsWorkspaceBeforeFlags(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Root.sln"), []byte("Microsoft Visual Studio Solution File\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"sh", "-c", "dotnet format --verbosity quiet"}
	got, err := ensureDotnetInvocation(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("argv: %v", got)
	}
	s := got[2]
	if !strings.Contains(s, "dotnet") || !strings.Contains(s, "format") || !strings.Contains(s, "Root.sln") {
		t.Fatalf("script %q", s)
	}
	// Workspace must appear before long flags (dotnet format quirk).
	low := strings.ToLower(s)
	if !strings.Contains(low, "format") {
		t.Fatal(s)
	}
	slnI := strings.Index(s, "Root.sln")
	verbI := strings.Index(low, "--verbosity")
	if slnI < 0 || verbI < 0 || slnI > verbI {
		t.Fatalf("want sln before --verbosity, got %q", s)
	}
}

func TestEnsureDotnetDockerInvocation_shCAppendsProject(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Svc")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Svc.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"sh", "-c", "dotnet build -c Release --no-restore"}
	got, err := ensureDotnetInvocation(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("argv: %v", got)
	}
	s := got[2]
	if !strings.Contains(s, "Svc/Svc.csproj") || strings.Contains(s, "--project") {
		t.Fatalf("script %q", s)
	}
}

func TestApplyDotnetDockerTargetFrameworkFallback_shC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "App.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `dotnet build -c Release --no-restore "` + filepath.ToSlash(filepath.Join("App.csproj")) + `"`
	argv := []string{"sh", "-c", script}
	got, err := applyDotnetEvalTargetFrameworkFallback(argv, dir, "net8.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatal(got)
	}
	if !strings.Contains(got[2], "/p:TargetFramework=net8.0") {
		t.Fatal(got[2])
	}
}

func TestApplyDotnetDisableNuGetAudit_exec(t *testing.T) {
	argv := []string{"dotnet", "test", "App.csproj", "-nologo"}
	got := ApplyDotnetDisableNuGetAudit(argv)
	if len(got) != 5 || got[2] != "/p:NuGetAudit=false" {
		t.Fatalf("%v", got)
	}
	if len(ApplyDotnetDisableNuGetAudit(got)) != 5 {
		t.Fatal("idempotent")
	}
}

func TestApplyDotnetDisableNuGetAudit_formatIncludeAppendsTrailingMSBuildProp(t *testing.T) {
	argv := []string{"dotnet", "format", "App.csproj", "--verbosity", "quiet", "--include", "Program.cs"}
	got := ApplyDotnetDisableNuGetAudit(argv)
	want := []string{"dotnet", "format", "App.csproj", "--verbosity", "quiet", "--include", "Program.cs", "/p:NuGetAudit=false"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestApplyDotnetDisableNuGetAudit_formatWithoutIncludeUnchanged(t *testing.T) {
	argv := []string{"dotnet", "format", "App.csproj", "--verbosity", "quiet"}
	got := ApplyDotnetDisableNuGetAudit(argv)
	if len(got) != len(argv) {
		t.Fatalf("got %v", got)
	}
	for i := range argv {
		if got[i] != argv[i] {
			t.Fatalf("got %v", got)
		}
	}
}

func TestApplyDotnetTestFrameworkBootstrapMSBuildProps_exec(t *testing.T) {
	argv := []string{"dotnet", "test", "App.csproj", "-nologo"}
	got := ApplyDotnetTestFrameworkBootstrapMSBuildProps(argv)
	if len(got) != 7 {
		t.Fatalf("len=%d %v", len(got), got)
	}
	if got[2] != "/p:NuGetAudit=false" || got[3] != "/p:SuppressTfmSupportBuildWarnings=true" || got[4] != "/p:TreatWarningsAsErrors=false" {
		t.Fatalf("%v", got)
	}
}

func TestApplyDotnetTestHangMitigationProps_exec(t *testing.T) {
	argv := []string{"dotnet", "test", "-c", "Release", "--no-build", "App.csproj"}
	got := ApplyDotnetTestHangMitigationProps(argv)
	if len(got) != 8 {
		t.Fatalf("len=%d %v", len(got), got)
	}
	if got[2] != "/p:UseSharedCompilation=false" || got[3] != "/p:UseRazorBuildServer=false" {
		t.Fatalf("%v", got)
	}
}

func TestApplyDotnetTestHangMitigationProps_skipsBuild(t *testing.T) {
	argv := []string{"dotnet", "build", "-c", "Release", "App.csproj"}
	got := ApplyDotnetTestHangMitigationProps(argv)
	if len(got) != len(argv) {
		t.Fatalf("build argv should be unchanged: %v", got)
	}
}

func TestApplyDotnetTestHangMitigationProps_shC(t *testing.T) {
	argv := []string{"sh", "-c", "dotnet test -c Release --no-build App.csproj"}
	got := ApplyDotnetTestHangMitigationProps(argv)
	if len(got) != 3 || !strings.Contains(got[2], "/p:UseSharedCompilation=false") || !strings.Contains(got[2], "/p:UseRazorBuildServer=false") {
		t.Fatalf("%v", got)
	}
}

func TestApplyDotnetTestVSTestCLIArgs_exec(t *testing.T) {
	argv := []string{"dotnet", "test", "-c", "Release", "--no-build", "App.csproj"}
	got := ApplyDotnetTestVSTestCLIArgs(argv, 10*time.Minute)
	if !argvHasArgPrefix(got, "--logger") {
		t.Fatalf("missing logger: %v", got)
	}
	if !argvHasRunConfigurationTestSessionTimeout(got) {
		t.Fatalf("missing session timeout: %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "RunConfiguration.TestSessionTimeout=540000") {
		t.Fatalf("want 90%% of 10m wall clock, got %v", got)
	}
}

func TestApplyDotnetTestVSTestCLIArgs_insertsAfterExistingDoubleDash(t *testing.T) {
	argv := []string{"dotnet", "test", "App.csproj", "--", "NUnit.NumberOfTestWorkers=1"}
	got := ApplyDotnetTestVSTestCLIArgs(argv, 10*time.Minute)
	dd := argvIndexExact(got, "--")
	if dd < 0 {
		t.Fatal("missing --")
	}
	if got[dd+1] != "RunConfiguration.TestSessionTimeout=540000" {
		t.Fatalf("want timeout first vstest arg after --, got %v", got)
	}
	if got[dd+2] != "NUnit.NumberOfTestWorkers=1" {
		t.Fatalf("want existing vstest token preserved after timeout, got %v", got)
	}
}

func TestApplyDotnetTestVSTestCLIArgs_shC(t *testing.T) {
	argv := []string{"sh", "-c", "dotnet test -c Release --no-build App.csproj"}
	got := ApplyDotnetTestVSTestCLIArgs(argv, 10*time.Minute)
	if len(got) != 3 || !strings.Contains(got[2], `--logger "console;verbosity=normal"`) {
		t.Fatalf("%v", got[2])
	}
	if !strings.Contains(got[2], "RunConfiguration.TestSessionTimeout=540000") {
		t.Fatalf("%v", got[2])
	}
}

func TestApplyDotnetDisableNuGetAudit_shC(t *testing.T) {
	argv := []string{"sh", "-c", "dotnet build -c Release --no-restore"}
	got := ApplyDotnetDisableNuGetAudit(argv)
	if len(got) != 3 || !strings.Contains(got[2], "/p:NuGetAudit=false") {
		t.Fatalf("%v", got[2])
	}
}

func TestApplyDotnetDisableNuGetAudit_nonDotnetUnchanged(t *testing.T) {
	argv := []string{"npm", "ci"}
	if got := ApplyDotnetDisableNuGetAudit(argv); len(got) != 2 || got[0] != "npm" {
		t.Fatalf("%v", got)
	}
}

func TestWrapDotnetTestWithBuildServerShutdown_execForm(t *testing.T) {
	argv := []string{"dotnet", "test", "-c", "Release", "App.csproj"}
	got := WrapDotnetTestWithBuildServerShutdown(argv)
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("want sh -c wrapper, got %v", got)
	}
	s := got[2]
	if !strings.HasPrefix(s, "(") || !strings.Contains(s, `"dotnet"`) || !strings.Contains(s, `"test"`) {
		t.Fatalf("want subshell with quoted dotnet argv, got %q", s)
	}
	if !strings.Contains(s, "ec=$?") || !strings.Contains(s, "dotnet build-server shutdown") || !strings.Contains(s, "exit $ec") {
		t.Fatalf("want shutdown trailer, got %q", s)
	}
}

func TestWrapDotnetTestWithBuildServerShutdown_shC(t *testing.T) {
	argv := []string{"sh", "-c", "dotnet test -c Release App.csproj"}
	got := WrapDotnetTestWithBuildServerShutdown(argv)
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("argv: %v", got)
	}
	s := got[2]
	wantPrefix := "(dotnet test -c Release App.csproj); ec=$?"
	if !strings.HasPrefix(s, wantPrefix) {
		t.Fatalf("want prefix %q, got %q", wantPrefix, s)
	}
	if !strings.Contains(s, "dotnet build-server shutdown") {
		t.Fatal(s)
	}
}

func TestWrapDotnetTestWithBuildServerShutdown_idempotentWhenShutdownPresent(t *testing.T) {
	orig := []string{"sh", "-c", "dotnet test App.csproj; dotnet build-server shutdown"}
	if got := WrapDotnetTestWithBuildServerShutdown(orig); len(got) != len(orig) || got[2] != orig[2] {
		t.Fatalf("want unchanged, got %v", got)
	}
}

func TestWrapDotnetTestWithBuildServerShutdown_skipsNonTest(t *testing.T) {
	argv := []string{"dotnet", "build", "App.csproj"}
	if got := WrapDotnetTestWithBuildServerShutdown(argv); len(got) != len(argv) {
		t.Fatalf("%v", got)
	}
	argv2 := []string{"sh", "-c", "dotnet build App.csproj"}
	if got := WrapDotnetTestWithBuildServerShutdown(argv2); len(got) != len(argv2) {
		t.Fatalf("%v", got)
	}
}

// Regression: ASQS re-serialises a patched dotnet command through argvToShellSingleCommandLine,
// which used to quote EVERY token. Every downstream protection matches the literal substring
// "dotnet test", so a script ASQS produced itself silently lost the hang-mitigation props, the
// VSTest session timeout and the build-server shutdown — on exactly the runs that configure a
// test_command, where a hanging `dotnet test` is least affordable.
func TestArgvToShellSingleCommandLine_leavesPlainTokensUnquoted(t *testing.T) {
	got := argvToShellSingleCommandLine([]string{"dotnet", "test", "-c", "Release", "--no-build", "App.csproj"})
	if got != "dotnet test -c Release --no-build App.csproj" {
		t.Fatalf("line = %q, want plain tokens unquoted", got)
	}
	if !shellScriptRunsDotnetTest(got) {
		t.Error("the produced line must still be recognisable as a dotnet test invocation")
	}
}

// Tokens the shell would reinterpret are still quoted.
func TestArgvToShellSingleCommandLine_quotesWhatNeedsIt(t *testing.T) {
	got := argvToShellSingleCommandLine([]string{"dotnet", "test", "--logger", "console;verbosity=normal", "My Proj.csproj"})
	for _, want := range []string{`"console;verbosity=normal"`, `"My Proj.csproj"`} {
		if !strings.Contains(got, want) {
			t.Errorf("line %q should quote %s", got, want)
		}
	}
	if !strings.HasPrefix(got, "dotnet test ") {
		t.Errorf("line %q should leave the verb unquoted", got)
	}
}

// Defence in depth: even a fully quoted script must be recognised, so no future producer can
// silently disable the C# test protections again.
func TestShellScriptRunsDotnetTest_toleratesQuoting(t *testing.T) {
	for _, script := range []string{
		`dotnet test -c Release`,
		`"dotnet" "test" "-c" "Release"`,
		`'dotnet' 'test'`,
		`DOTNET TEST`,
	} {
		if !shellScriptRunsDotnetTest(script) {
			t.Errorf("script %q should be recognised as a dotnet test invocation", script)
		}
	}
	for _, script := range []string{`dotnet build -c Release`, `npm test`, `dotnet restore`} {
		if shellScriptRunsDotnetTest(script) {
			t.Errorf("script %q must not be treated as dotnet test", script)
		}
	}
}

// The end-to-end consequence: a configured test_command still receives the protections.
func TestDotnetTestProtections_survivePatchedOverride(t *testing.T) {
	script := argvToShellSingleCommandLine([]string{"dotnet", "test", "-c", "Release", "--no-build", "App.csproj"})
	argv := []string{"sh", "-c", script}

	withProps := ApplyDotnetTestHangMitigationProps(argv)
	if !strings.Contains(strings.Join(withProps, " "), "UseSharedCompilation=false") {
		t.Errorf("hang-mitigation props lost on a patched override: %v", withProps)
	}
	withVSTest := ApplyDotnetTestVSTestCLIArgs(argv, 2*time.Minute)
	if !strings.Contains(strings.Join(withVSTest, " "), "TestSessionTimeout") {
		t.Errorf("VSTest session timeout lost on a patched override: %v", withVSTest)
	}
	wrapped := WrapDotnetTestWithBuildServerShutdown(argv)
	if !strings.Contains(strings.Join(wrapped, " "), "build-server shutdown") {
		t.Errorf("build-server shutdown lost on a patched override: %v", wrapped)
	}
}

// Regression: the container-provisioning snippets are PREPENDED to the shell script, and the
// MSBuild-property insertion is anchored at the start of the line. Prepending before the props were
// inserted therefore stripped the hang-mitigation props and the VSTest session timeout from
// `dotnet test` — silently, and only for configs that set a NuGet credential envelope.
//
// Same class as the over-quoting defect: a transform that makes an earlier pattern stop matching.
func TestDotnetProvisioningPrepend_doesNotStripTestProtections(t *testing.T) {
	sb := &Sandbox{
		Type: "docker", Timeout: "30s",
		DockerEvalExtraEnv: []string{`VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[]}`},
	}
	repo := writeRepoTree(t, map[string]string{
		"App.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
	}, nil)

	plan, err := sb.buildStepPlan(repo, "csharp", "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(plan.Test, " ")

	if !strings.Contains(got, "CredentialProvider") {
		t.Fatalf("the credential-provider install should still be prepended:\n%s", got)
	}
	for _, want := range []string{"UseSharedCompilation=false", "UseRazorBuildServer=false", "TestSessionTimeout"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s lost when the credential envelope is configured:\n%s", want, got)
		}
	}
}
