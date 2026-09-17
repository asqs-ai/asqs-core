package runner

import (
	"context"
	"strings"
	"testing"
)

// ASQS injects three MSBuild properties into every C# eval step — NuGetAudit=false,
// SuppressTfmSupportBuildWarnings=true and TreatWarningsAsErrors=false — and that is a deliberate
// decision (D6), not an accident: it keeps a feed's audit warnings and a repo's warnings-as-errors
// policy from failing a step for reasons unrelated to the generated test.
//
// It was also invisible. An operator reading a passing C# eval had no way to know their
// TreatWarningsAsErrors was being overridden, and the only way to find out was to read the source.
// The plan resolution now states it; behaviour is unchanged.
func TestAuditEvalPlan_namesInjectedMSBuildProperties(t *testing.T) {
	repo := t.TempDir()
	writePlanFixtureFile(t, repo, "App.csproj",
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)

	rec := &planRecordingAuditor{}
	s := &Sandbox{Type: "docker", Audit: rec}
	plan, err := s.buildStepPlan(repo, "csharp", "")
	if err != nil {
		t.Fatal(err)
	}
	s.auditEvalPlan(context.Background(), plan, repo)

	payload := rec.payloadFor("runner.eval_plan_resolved")
	if payload == nil {
		t.Fatal("no runner.eval_plan_resolved event")
	}
	props, ok := payload["msbuild_properties"].([]string)
	if !ok || len(props) == 0 {
		t.Fatalf("msbuild_properties = %#v, want the injected property list", payload["msbuild_properties"])
	}
	for _, want := range []string{"/p:NuGetAudit=false", "/p:TreatWarningsAsErrors=false", "/p:SuppressTfmSupportBuildWarnings=true"} {
		found := false
		for _, p := range props {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("msbuild_properties %v is missing %q", props, want)
		}
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "TreatWarningsAsErrors=false") {
		t.Errorf("message does not state the override:\n%s", msg)
	}
}

// A Java plan says nothing about MSBuild: the key is C#-only, and an empty one would read as
// "nothing was injected" for a language where the question does not arise.
func TestAuditEvalPlan_noMSBuildKeyForJava(t *testing.T) {
	repo := t.TempDir()
	writePlanFixtureFile(t, repo, "pom.xml", `<project></project>`)

	rec := &planRecordingAuditor{}
	s := &Sandbox{Type: "docker", Audit: rec}
	plan, err := s.buildStepPlan(repo, "java", "")
	if err != nil {
		t.Fatal(err)
	}
	s.auditEvalPlan(context.Background(), plan, repo)

	payload := rec.payloadFor("runner.eval_plan_resolved")
	if payload == nil {
		t.Fatal("no runner.eval_plan_resolved event")
	}
	if _, present := payload["msbuild_properties"]; present {
		t.Errorf("java plan carries msbuild_properties: %#v", payload["msbuild_properties"])
	}
}
