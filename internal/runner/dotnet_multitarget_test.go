package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPrependShellSnippetToDockerCommand(t *testing.T) {
	prep := "set -eu; echo x"
	argv := []string{"dotnet", "build", "App.csproj"}
	got := PrependShellSnippetToDockerCommand(argv, prep)
	if len(got) != 3 || got[0] != "sh" || got[1] != "-c" {
		t.Fatalf("%v", got)
	}
	if !strings.HasPrefix(got[2], prep+" && ") {
		t.Fatalf("%q", got[2])
	}
	sh := []string{"sh", "-c", "dotnet test"}
	got2 := PrependShellSnippetToDockerCommand(sh, prep)
	if got2[2] != prep+" && dotnet test" {
		t.Fatalf("%q", got2[2])
	}
	if len(PrependShellSnippetToDockerCommand(argv, "")) != len(argv) {
		t.Fatal("empty prep should noop")
	}
}

func TestApplyDotnetMultiTargetFramework_shC(t *testing.T) {
	dir := t.TempDir()
	multi := filepath.Join(dir, "Multi.csproj")
	if err := os.WriteFile(multi, []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFrameworks>net48;net6.0</TargetFrameworks></PropertyGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := []string{"sh", "-c", `dotnet test ` + strconv.Quote(filepath.Base(multi))}
	got := ApplyDotnetMultiTargetFramework(argv, dir, multi, "")
	if len(got) != 3 || !strings.Contains(got[2], "/p:TargetFramework=net6.0") {
		t.Fatalf("%v", got)
	}
}
