package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// countingRestoreRepo writes a repo whose "package manager" is a stub that records a line each time
// it is invoked WITH THE RESTORE GOAL, so a test can count restores specifically.
//
// Matching on the goal matters: the same stub also serves the compile/test/coverage steps, and a
// naive counter would report five extra "restores" that were really build invocations.
func countingRestoreRepo(t *testing.T, files map[string]string, binName, restoreGoal string) (repo, counter string) {
	t.Helper()
	repo = writeRepoTree(t, files, nil)
	counter = filepath.Join(t.TempDir(), "runs.log")
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *" + restoreGoal + "*) echo run >> " + counter + " ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, binName), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return repo, counter
}

func restoreRuns(t *testing.T, counter string) int {
	t.Helper()
	b, err := os.ReadFile(counter)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

// D1/U4: the local target restores at all. Before this it never did, so `npm run build` ran against
// whatever node_modules happened to be on disk — nothing on a fresh clone.
func TestLocalRestore_runsBeforeTheStep(t *testing.T) {
	repo, counter := countingRestoreRepo(t, map[string]string{"pom.xml": jacocoPom}, "mvn", "dependency:go-offline")
	sb := &Sandbox{Type: "local", Timeout: "30s"}

	_ = captureStderr(t, func() { sb.Compile(context.Background(), repo, "java") })

	if got := restoreRuns(t, counter); got == 0 {
		t.Fatal("local compile ran no dependency restore")
	}
}

// Once per ROUND, not once per step. Restore used to fire on compile, test and coverage
// independently — and again for the E2E pass and every scoped-compile fallback.
func TestLocalRestore_runsOncePerRoundAcrossStepsAndClones(t *testing.T) {
	repo, counter := countingRestoreRepo(t, map[string]string{"pom.xml": jacocoPom}, "mvn", "dependency:go-offline")
	sb := &Sandbox{Type: "local", Timeout: "30s"}
	ctx := context.Background()

	_ = captureStderr(t, func() {
		sb.Compile(ctx, repo, "java")
		sb.Test(ctx, repo, "java")
		sb.Coverage(ctx, repo, "java")
		// Clones: these share run state via clone(), so they must share the memo too.
		sb.TestWithCommand(ctx, repo, "java", "")
		sb.CoverageWithCommand(ctx, repo, "java", "")
	})

	if got := restoreRuns(t, counter); got != 1 {
		t.Fatalf("restore ran %d times across 5 step invocations, want exactly 1", got)
	}
}

// The memo must NOT survive a manifest edit: the fix loop adds a missing test dependency to
// pom.xml/package.json/.csproj mid-round, and a stale memo would test against old dependencies —
// a wrong answer that reads as a flaky failure.
func TestLocalRestore_manifestEditInvalidatesTheMemo(t *testing.T) {
	repo, counter := countingRestoreRepo(t, map[string]string{"pom.xml": jacocoPom}, "mvn", "dependency:go-offline")
	sb := &Sandbox{Type: "local", Timeout: "30s"}
	ctx := context.Background()

	_ = captureStderr(t, func() { sb.Compile(ctx, repo, "java") })
	if got := restoreRuns(t, counter); got != 1 {
		t.Fatalf("first restore ran %d times, want 1", got)
	}

	// The fixer adds a dependency.
	edited := strings.Replace(jacocoPom, "</plugins>", "</plugins><dependencies/>", 1)
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	_ = captureStderr(t, func() { sb.Test(ctx, repo, "java") })
	if got := restoreRuns(t, counter); got != 2 {
		t.Fatalf("restore ran %d times after a pom edit, want 2 — the memo did not invalidate", got)
	}
}

// A manifest that did not change must not re-trigger, even though the step differs.
func TestRestoreKey_isStableWhenNothingChanges(t *testing.T) {
	dir := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	a := restoreKeyFor(dir, profile.JavaMaven, dir)
	b := restoreKeyFor(dir, profile.JavaMaven, dir)
	if a != b {
		t.Fatal("restore key is not stable for an unchanged repo")
	}
}

func TestRestoreKey_changesWithContentToolchainAndRepo(t *testing.T) {
	dir := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	base := restoreKeyFor(dir, profile.JavaMaven, dir)

	if restoreKeyFor(dir, profile.JavaGradle, dir) == base {
		t.Error("key must depend on the toolchain")
	}
	if restoreKeyFor(dir, profile.JavaMaven, dir+"-other") == base {
		t.Error("key must depend on the repo, or two repos in one process collide")
	}
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(jacocoPom+"<!--x-->"), 0o644); err != nil {
		t.Fatal(err)
	}
	if restoreKeyFor(dir, profile.JavaMaven, dir) == base {
		t.Error("key must depend on manifest content")
	}
}

// Adding a lockfile is a dependency change even though no existing file was edited.
func TestRestoreKey_addingALockfileInvalidates(t *testing.T) {
	dir := writeRepoTree(t, map[string]string{"package.json": `{"scripts":{}}`}, nil)
	base := restoreKeyFor(dir, profile.TypeScriptNPM, dir)
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if restoreKeyFor(dir, profile.TypeScriptNPM, dir) == base {
		t.Fatal("adding a lockfile must invalidate the restore memo")
	}
}

// A .csproj edit is the .NET form of a manifest change, and the paths are not knowable statically.
func TestRestoreKey_dotnetTracksProjectFiles(t *testing.T) {
	dir := writeRepoTree(t, map[string]string{
		"src/App/App.csproj":       `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"tests/App.Tests/T.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
	}, nil)
	base := restoreKeyFor(dir, profile.CSharpDotnet, dir)

	if err := os.WriteFile(filepath.Join(dir, "tests", "App.Tests", "T.csproj"),
		[]byte(`<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit"/></ItemGroup></Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if restoreKeyFor(dir, profile.CSharpDotnet, dir) == base {
		t.Fatal("a PackageReference added to a test project must invalidate the memo")
	}
}

// Build output is not a manifest: an obj/ directory full of generated .csproj-adjacent files must
// not churn the key (and the walk must not descend into it).
func TestDotnetProjectManifests_skipsBuildOutput(t *testing.T) {
	dir := writeRepoTree(t, map[string]string{
		"App.csproj":                     `<Project/>`,
		"obj/Debug/Generated.csproj":     `<Project/>`,
		"node_modules/pkg/Vendor.csproj": `<Project/>`,
	}, nil)
	got := dotnetProjectManifests(dir)
	if len(got) != 1 || got[0] != "App.csproj" {
		t.Fatalf("manifests = %v, want only App.csproj", got)
	}
}

// A restore whose binary is missing must not fail the step: on a host that is a provisioning fact,
// and the step's own error is the more useful diagnostic.
func TestLocalRestore_missingBinaryIsNotFatal(t *testing.T) {
	repo := writeRepoTree(t, map[string]string{"pom.xml": jacocoPom}, nil)
	t.Setenv("PATH", t.TempDir()) // no mvn
	sb := &Sandbox{Type: "local", Timeout: "30s"}

	out := captureStderr(t, func() { sb.Compile(context.Background(), repo, "java") })

	if !strings.Contains(out, "local restore: skipped") {
		t.Errorf("a missing restore binary should be reported as skipped:\n%s", out)
	}
}

// Both targets read the restore argv from one toolchain profile, so they cannot drift.
func TestRestoreArgv_comesFromTheToolchainProfile(t *testing.T) {
	for _, id := range []profile.ToolchainID{
		profile.JavaMaven, profile.JavaGradle, profile.TypeScriptNPM,
		profile.TypeScriptPNPM, profile.TypeScriptYarn, profile.CSharpDotnet,
	} {
		want := profile.BuiltinToolchain(id, "", "", "", "").Restore
		got := restoreArgvFor(id)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: restoreArgvFor = %v, profile = %v", id, got, want)
		}
	}
	if got := restoreArgvFor(profile.UnsupportedDocker); got != nil {
		t.Errorf("unsupported toolchain should have no restore argv, got %v", got)
	}
}

// The Docker path had the same once-per-step problem, and it is the more expensive one: compile,
// test, coverage, the E2E pass and every scoped-compile fallback each spawned a restore container.
func TestDockerRestore_runsOncePerRoundAcrossStepsAndClones(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs.log")
	// The stub sees the whole `docker run … mvn -q -B dependency:go-offline` line.
	sb, repo := fakeDockerSandbox(t, "30s",
		`case "$*" in *dependency:go-offline*) echo run >> `+counter+` ;; esac
exit 0`)
	ctx := context.Background()

	_ = captureStderr(t, func() {
		sb.Compile(ctx, repo, "java")
		sb.Test(ctx, repo, "java")
		sb.Coverage(ctx, repo, "java")
		sb.TestWithCommand(ctx, repo, "java", "")
		sb.CoverageWithCommand(ctx, repo, "java", "")
	})

	if got := restoreRuns(t, counter); got != 1 {
		t.Fatalf("docker restore ran %d times across 5 step invocations, want exactly 1", got)
	}
}

// Two repositories driven by one Sandbox must not share a memo entry.
func TestRestoreMemo_isPerRepository(t *testing.T) {
	st := &sandboxRunState{}
	var runs int
	st.restoreOnce("key-a", func() { runs++ })
	st.restoreOnce("key-a", func() { runs++ })
	st.restoreOnce("key-b", func() { runs++ })
	if runs != 2 {
		t.Fatalf("runs = %d, want 2 (one per distinct key)", runs)
	}
}

// An empty key means "cannot fingerprint", which must degrade to always restoring rather than
// silently never restoring.
func TestRestoreMemo_emptyKeyAlwaysRuns(t *testing.T) {
	st := &sandboxRunState{}
	var runs int
	st.restoreOnce("", func() { runs++ })
	st.restoreOnce("", func() { runs++ })
	if runs != 2 {
		t.Fatalf("runs = %d, want 2: an unfingerprintable restore must not be skipped", runs)
	}
}

// fakeDockerSandbox returns a docker-type Sandbox whose "docker binary" is the given shell script,
// plus a repo that resolves to the java-maven toolchain profile. (Upstream keeps this helper in
// its docker-eval failure tests, which arrive with CP34.)
func fakeDockerSandbox(t *testing.T, timeout, script string) (*Sandbox, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Sandbox{Type: "docker", Timeout: timeout, DockerBinary: bin}, repo
}
