package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// `dotnet format` on the docker target runs INSIDE the SDK image, which always has the CLI. The
// resolver asked the HOST instead, so an operator running ASQS on a machine with no .NET SDK — the
// normal case for a docker-target deployment, and the reason the docker target exists — got
// "formatter_not_available:dotnet" and C# artifacts were never formatted. Java has not had this
// problem since mvn and gradle joined the image-provided whitelist; dotnet is on that list already
// and these four call sites simply did not consult it.
func TestResolveFormatCommand_dotnetIsImageProvidedOnDocker(t *testing.T) {
	repo := t.TempDir()
	writeFmt(t, filepath.Join(repo, "App.csproj"), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	withoutDotnetOnPATH(t)

	for _, onlyAdded := range []bool{false, true} {
		got := ResolveFormatCommand(repo, "csharp", "", "", onlyAdded, TargetDocker)
		if got.SkipReason != "" {
			t.Errorf("onlyAdded=%v: SkipReason = %q, want none on the docker target", onlyAdded, got.SkipReason)
		}
		if got.Command == "" {
			t.Errorf("onlyAdded=%v: no format command resolved on the docker target", onlyAdded)
		}
	}
}

// The host probe still governs the local target: there the command really does run on this machine,
// and claiming a missing binary exists would turn a silent skip into a hard failure.
func TestResolveFormatCommand_dotnetStillProbedOnLocal(t *testing.T) {
	repo := t.TempDir()
	writeFmt(t, filepath.Join(repo, "App.csproj"), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	withoutDotnetOnPATH(t)

	for _, onlyAdded := range []bool{false, true} {
		got := ResolveFormatCommand(repo, "csharp", "", "", onlyAdded, TargetLocal)
		if got.SkipReason != "formatter_not_available:dotnet" {
			t.Errorf("onlyAdded=%v: SkipReason = %q, want formatter_not_available:dotnet on local", onlyAdded, got.SkipReason)
		}
	}
}

// withoutDotnetOnPATH empties PATH for the duration of the test so the host probe cannot find a
// dotnet the developer's machine happens to have.
func withoutDotnetOnPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func writeFmt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
