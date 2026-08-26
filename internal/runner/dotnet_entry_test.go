package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

func TestResolveDotnetEntryRel_nestedSdkCsproj(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src", "App")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := filepath.Join(sub, "App.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := resolveDotnetEntryRel(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "src/App/App.csproj"
	if rel != want {
		t.Fatalf("got %q want %q", rel, want)
	}
}

func TestResolveDotnetEntryRel_prefersRootSln(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Root.sln"), []byte("\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "src", "App")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := filepath.Join(sub, "App.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := resolveDotnetEntryRel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "Root.sln" {
		t.Fatalf("got %q want Root.sln", rel)
	}
}

func TestEnsureDotnetProjectArg_appendsForDotnetBuild(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Lib.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "build", "-c", "Release", "--no-restore"}
	got, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err = applyDotnetTargetFrameworkFallbackArgv(got, dir, "net9.0")
	if err != nil {
		t.Fatal(err)
	}
	// Lib.csproj already has net8.0 — no /p:TargetFramework insert; entry path is positional.
	if len(got) != 6 || got[5] != "lib/Lib.csproj" {
		t.Fatalf("argv: %v", got)
	}
}

func TestApplyDotnetTargetFrameworkFallbackArgv_insertsWhenCsprojOmitsTFM(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Caching")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := filepath.Join(sub, "Caching.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><AssemblyName>X</AssemblyName></PropertyGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "build", "-c", "Release", "--no-restore"}
	argv, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	argv, err = applyDotnetTargetFrameworkFallbackArgv(argv, dir, "net8.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 7 || argv[2] != "/p:TargetFramework=net8.0" || argv[6] != "Caching/Caching.csproj" {
		t.Fatalf("argv: %v", argv)
	}
}

func TestApplyDotnetTargetFrameworkFallbackArgv_formatIncludeAppendsTrailingTFM(t *testing.T) {
	dir := t.TempDir()
	csproj := filepath.Join(dir, "Bare.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><AssemblyName>X</AssemblyName></PropertyGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "format", "--verbosity", "quiet", "--include", "P.cs"}
	argv, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	argv, err = applyDotnetTargetFrameworkFallbackArgv(argv, dir, "net8.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 8 {
		t.Fatalf("argv: %v", argv)
	}
	if argv[len(argv)-1] != "/p:TargetFramework=net8.0" {
		t.Fatalf("want trailing TFM, argv=%v", argv)
	}
	if argv[2] == "/p:TargetFramework=net8.0" {
		t.Fatal("TFM must not sit between format and the workspace (.csproj)")
	}
}

func TestApplyDotnetTargetFrameworkFallbackArgv_formatWithoutIncludeSkipsTFM(t *testing.T) {
	dir := t.TempDir()
	csproj := filepath.Join(dir, "Bare.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><AssemblyName>X</AssemblyName></PropertyGroup>
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "format", "--verbosity", "quiet"}
	argv, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), argv...)
	argv, err = applyDotnetTargetFrameworkFallbackArgv(argv, dir, "net8.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != len(before) {
		t.Fatalf("format without --include must not get TFM insert, argv=%v", argv)
	}
	for i := range before {
		if argv[i] != before[i] {
			t.Fatalf("format without --include must not get TFM insert, argv=%v", argv)
		}
	}
}

func TestCsprojDeclaresConcreteTargetFramework(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "e.csproj")
	if err := os.WriteFile(empty, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework></TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := CsprojDeclaresConcreteTargetFramework(empty)
	if err != nil || ok {
		t.Fatalf("empty tag: ok=%v err=%v", ok, err)
	}
	prop := filepath.Join(dir, "p.csproj")
	if err := os.WriteFile(prop, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>$(Foo)</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = CsprojDeclaresConcreteTargetFramework(prop)
	if err != nil || ok {
		t.Fatalf("property ref: ok=%v", ok)
	}
	good := filepath.Join(dir, "g.csproj")
	if err := os.WriteFile(good, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = CsprojDeclaresConcreteTargetFramework(good)
	if err != nil || !ok {
		t.Fatalf("concrete: ok=%v err=%v", ok, err)
	}
}

func TestCsprojDeclaresConcreteTargetFramework_ignoresCommentedTags(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.csproj")
	if err := os.WriteFile(p, []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <!-- <TargetFramework>net8.0</TargetFramework> -->
</Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := CsprojDeclaresConcreteTargetFramework(p)
	if err != nil || ok {
		t.Fatalf("commented TFM must not count as concrete: ok=%v err=%v", ok, err)
	}
}

func TestDotnetShellLineWithProject_fallbackTFM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "App.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	line, err := dotnetShellLineWithProject(dir, "dotnet build -c Release", "net8.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "/p:TargetFramework=net8.0") || !strings.Contains(line, "App.csproj") {
		t.Fatal(line)
	}
}

func TestEnsureDotnetProjectArg_skipsWhenPathPresent(t *testing.T) {
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "build", "MyApp.csproj"}
	got, err := ensureDotnetProjectArg(p, argv, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("argv: %v", got)
	}
}

func TestEnsureDotnetProjectArg_formatInsertsWorkspaceAfterVerb(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Z.sln"), []byte("Microsoft Visual Studio Solution File\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A.slnx"), []byte("<Solution/>"), 0644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "format", "--verbosity", "quiet", "--include", "src/Foo.cs"}
	got, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	// rootSlnRel sorts names: A.slnx before Z.sln
	want := []string{"dotnet", "format", "A.slnx", "--verbosity", "quiet", "--include", "src/Foo.cs"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv=%v want %v", got, want)
		}
	}
}

func TestEnsureDotnetProjectArg_formatReplacesDotWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "App.sln"), []byte("Microsoft Visual Studio Solution File\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "format", ".", "--verbosity", "quiet"}
	got, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dotnet", "format", "App.sln", "--verbosity", "quiet"}
	if len(got) != len(want) {
		t.Fatalf("argv=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv=%v want %v", got, want)
		}
	}
}

func TestEnsureDotnetProjectArg_formatDotErrorsWhenNoEntry(t *testing.T) {
	dir := t.TempDir()
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	_, err := ensureDotnetProjectArg(p, []string{"dotnet", "format", "."}, dir)
	if err == nil {
		t.Fatal("expected error when no sln/csproj")
	}
}

func TestEnsureDotnetProjectArgPreferred_nestedCsprojOverRootSln(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Root.sln"), []byte("\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(dir, "tests", "My.Unit.Tests")
	if err := os.MkdirAll(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := filepath.Join(testsDir, "My.Unit.Tests.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.ToolchainProfile{ID: profile.CSharpDotnet}
	argv := []string{"dotnet", "format", "--verbosity", "quiet", "--include", "tests/My.Unit.Tests/Foo.cs"}
	got, err := ensureDotnetProjectArgPreferred(p, argv, dir, "tests/My.Unit.Tests/My.Unit.Tests.csproj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 || got[2] != "tests/My.Unit.Tests/My.Unit.Tests.csproj" {
		t.Fatalf("want nested csproj as workspace after format, got %v", got)
	}
	got2, err := ensureDotnetProjectArg(p, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) < 3 || got2[2] != "Root.sln" {
		t.Fatalf("baseline without preferred: want Root.sln, got %v", got2)
	}
}

func TestFormatAfterFixForSandbox_nilOrEmpty(t *testing.T) {
	ctx := context.Background()
	if err := FormatAfterFixForSandbox(nil, ctx, t.TempDir(), "csharp", FormatResolveResult{Command: "dotnet format"}, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := FormatAfterFixForSandbox(&Sandbox{Type: "local"}, ctx, t.TempDir(), "csharp", FormatResolveResult{}, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
}
