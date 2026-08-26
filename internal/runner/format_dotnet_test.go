package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIsDotNetFormatCommand(t *testing.T) {
	if !IsDotNetFormatCommand("dotnet format") {
		t.Fatal()
	}
	if !IsDotNetFormatCommand("dotnet format ") {
		t.Fatal()
	}
	if !IsDotNetFormatCommand("DOTNET FORMAT --verbosity quiet") {
		t.Fatal()
	}
	if IsDotNetFormatCommand("") || IsDotNetFormatCommand("prettier --write .") {
		t.Fatal()
	}
}

func TestRunFormatCommand_skipsWhenDotnetNotInPATH(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	err := RunFormatCommand(ctx, repo, "dotnet format", time.Second)
	if !errors.Is(err, ErrFormatSkippedNoDotnet) {
		t.Fatalf("got %v want %v", err, ErrFormatSkippedNoDotnet)
	}
}

func TestDotnetFormatIncludeBatches_singleProject(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "services", "Tests")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	csproj := filepath.Join(sub, "Tests.csproj")
	if err := os.WriteFile(csproj, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	batches, prefs, legacy := dotnetFormatIncludeBatches(dir, []string{
		"services/Tests/A.cs",
		"services/Tests/Sub/B.cs",
	})
	if legacy {
		t.Fatal("unexpected legacy")
	}
	if len(batches) != 1 || len(prefs) != 1 {
		t.Fatalf("batches=%v prefs=%v", batches, prefs)
	}
	if prefs[0] != "services/Tests/Tests.csproj" {
		t.Fatalf("prefs[0]=%q", prefs[0])
	}
	want := [][]string{{"services/Tests/A.cs", "services/Tests/Sub/B.cs"}}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches=%v want %v", batches, want)
	}
}

func TestDotnetRestoreArgvFromFormatArgv(t *testing.T) {
	argv := []string{"dotnet", "format", "svc/App.csproj", "--verbosity", "quiet", "--include", "a.cs", "/p:TargetFramework=net8.0"}
	got := dotnetRestoreArgvFromFormatArgv(argv)
	want := []string{"dotnet", "restore", "svc/App.csproj", "/p:TargetFramework=net8.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if dotnetRestoreArgvFromFormatArgv([]string{"dotnet", "build", "x"}) != nil {
		t.Fatal("expected nil for non-format")
	}
}

func TestDotnetRestoreArgvForPreFormat_includesNuGetAuditFalse(t *testing.T) {
	format := []string{"dotnet", "format", "App.csproj", "--verbosity", "quiet", "--include", "a.cs"}
	got := dotnetRestoreArgvForPreFormat(format)
	// dotnet restore /p:NuGetAudit=false App.csproj …
	if len(got) < 4 || got[0] != "dotnet" || got[1] != "restore" || got[2] != "/p:NuGetAudit=false" || got[3] != "App.csproj" {
		t.Fatalf("got %v", got)
	}
}

func TestDotnetFormatArgvInsertNoRestore(t *testing.T) {
	in := []string{"dotnet", "format", "App.csproj", "--verbosity", "quiet", "--include", "a.cs"}
	got := dotnetFormatArgvInsertNoRestore(in)
	want := []string{"dotnet", "format", "App.csproj", "--no-restore", "--verbosity", "quiet", "--include", "a.cs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got2 := dotnetFormatArgvInsertNoRestore(got); !reflect.DeepEqual(got2, got) {
		t.Fatalf("idempotent: got %v", got2)
	}
}

func TestDotnetFormatIncludeBatches_twoProjects(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []struct {
		dir, proj string
	}{
		{"lib/A", "A.csproj"},
		{"lib/B", "B.csproj"},
	} {
		d := filepath.Join(dir, filepath.FromSlash(rel.dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, rel.proj)
		if err := os.WriteFile(p, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	batches, prefs, legacy := dotnetFormatIncludeBatches(dir, []string{"lib/A/X.cs", "lib/B/Y.cs"})
	if legacy {
		t.Fatal("unexpected legacy")
	}
	if len(batches) != 2 || len(prefs) != 2 {
		t.Fatalf("batches=%d prefs=%d", len(batches), len(prefs))
	}
	// Sorted by project path
	if prefs[0] != "lib/A/A.csproj" || prefs[1] != "lib/B/B.csproj" {
		t.Fatalf("prefs=%v", prefs)
	}
}
