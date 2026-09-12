package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/runner/profile"
)

// The coverage step for a C# repo with no coverlet is a byte-identical re-run of the test step that
// produces nothing. Java skips its equivalent; C# ran it on every project.
func TestPlanProfileStep_coverageSkippedWithoutCoverlet(t *testing.T) {
	repo := t.TempDir()
	mustWriteProj(t, filepath.Join(repo, "tests", "App.Tests", "App.Tests.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup></Project>`)

	s := &Sandbox{}
	p, err := profile.ResolveToolchain(repo, "csharp", string(profile.CSharpDotnet), "", "", "", "")
	if err != nil {
		t.Fatalf("resolve toolchain: %v", err)
	}
	plan := &StepPlan{Lang: "csharp"}
	dec := s.planProfileStep(plan, p, evaluator.StepCoverage, repo, repo, TargetDocker)
	if dec.Action == ActionRun {
		t.Fatalf("coverage step planned to run; want skip. decision=%+v", dec)
	}
	if !containsFold(dec.Reason, "coverlet.collector") {
		t.Fatalf("skip reason %q does not name the package the operator must add", dec.Reason)
	}
}

// With coverlet present the step runs, and its argv pins a results directory so every test
// project's report lands under one known root.
func TestPlanProfileStep_coverageRunsWithCoverlet(t *testing.T) {
	repo := t.TempDir()
	mustWriteProj(t, filepath.Join(repo, "tests", "App.Tests", "App.Tests.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
  <PackageReference Include="coverlet.collector" Version="6.0.2" />
</ItemGroup></Project>`)

	s := &Sandbox{}
	p, err := profile.ResolveToolchain(repo, "csharp", string(profile.CSharpDotnet), "", "", "", "")
	if err != nil {
		t.Fatalf("resolve toolchain: %v", err)
	}
	plan := &StepPlan{Lang: "csharp"}
	dec := s.planProfileStep(plan, p, evaluator.StepCoverage, repo, repo, TargetDocker)
	if dec.Action != ActionRun {
		t.Fatalf("coverage step skipped with coverlet present: %+v", dec)
	}
	argv := plan.Coverage
	if !argvHasFlag(argv, "--results-directory") {
		t.Fatalf("coverage argv %v does not pin --results-directory", argv)
	}
}

// A Java repo's gate is untouched by any of this.
func TestPlanProfileStep_javaCoverageGateUnchanged(t *testing.T) {
	repo := t.TempDir()
	mustWriteProj(t, filepath.Join(repo, "pom.xml"), `<project><build><plugins></plugins></build></project>`)

	s := &Sandbox{}
	p, err := profile.ResolveToolchain(repo, "java", string(profile.JavaMaven), "", "", "", "")
	if err != nil {
		t.Fatalf("resolve toolchain: %v", err)
	}
	plan := &StepPlan{Lang: "java"}
	dec := s.planProfileStep(plan, p, evaluator.StepCoverage, repo, repo, TargetDocker)
	if dec.Action == ActionRun {
		t.Fatalf("java coverage planned to run without JaCoCo: %+v", dec)
	}
	if !containsFold(dec.Reason, "jacoco") {
		t.Fatalf("java skip reason = %q, want it to still name JaCoCo", dec.Reason)
	}
}

func argvHasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	// sh -c form: look inside the script.
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return containsFold(argv[2], flag)
	}
	return false
}

func mustWriteProj(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
