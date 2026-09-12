package testbootstrap

import (
	"testing"
)

// The surface has to reach the contract: it is the artifact generation and the E2E bootstrap read,
// and re-detecting it in each consumer would let them disagree about the same repository.
func TestCSharpContract_carriesTheSurface(t *testing.T) {
	prof := csharpTestProfile{
		Framework:       CSharpFrameworkAspNetCore,
		TestFramework:   CSharpTestXunit,
		TargetFramework: "net8.0",
		Stack:           "xunit-aspnetcore",
		UISurface:       CSharpSurfaceMixed,
		UIFramework:     CSharpUIRazorPages,
	}
	c := csharpContract(prof)
	if c.E2ESurface != string(CSharpSurfaceMixed) {
		t.Errorf("E2ESurface = %q, want mixed", c.E2ESurface)
	}
	if c.UIFramework != string(CSharpUIRazorPages) {
		t.Errorf("UIFramework = %q, want razor-pages", c.UIFramework)
	}
}

// A surface of "none" is a fact, not an absence: a library really has no E2E surface, and a reader
// must be able to tell that from a contract written before the field existed.
func TestCSharpContract_recordsNoneExplicitly(t *testing.T) {
	c := csharpContract(csharpTestProfile{UISurface: CSharpSurfaceNone, UIFramework: CSharpUINone})
	if c.E2ESurface != string(CSharpSurfaceNone) {
		t.Errorf("E2ESurface = %q, want an explicit none", c.E2ESurface)
	}
	if c.UIFramework != "" {
		t.Errorf("UIFramework = %q, want empty when there is no UI", c.UIFramework)
	}
}

// DetectE2E is what the workflow calls, and it must report the surface so BuildPlanOptions and the
// E2E summary do not each re-read the contract.
func TestDetectE2E_reportsTheCSharpSurface(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "src/Web/Web.csproj", webCsproj)
	writeSurfaceFile(t, repo, "src/Web/Program.cs", `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
var app = builder.Build();
app.MapRazorPages();
app.Run();`)
	writeSurfaceFile(t, repo, "src/Web/Pages/Index.cshtml", "@page\n<h1>Hello</h1>")

	rep, err := DetectE2E(repo, "csharp")
	if err != nil {
		t.Fatalf("DetectE2E: %v", err)
	}
	if rep.Surface != string(CSharpSurfaceUI) {
		t.Errorf("Surface = %q, want ui", rep.Surface)
	}
	if rep.UIFramework != string(CSharpUIRazorPages) {
		t.Errorf("UIFramework = %q, want razor-pages", rep.UIFramework)
	}
}

// Other languages do not detect a surface, and must not claim one.
func TestDetectE2E_noSurfaceForJava(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "pom.xml", "<project></project>")

	rep, err := DetectE2E(repo, "java")
	if err != nil {
		t.Fatalf("DetectE2E: %v", err)
	}
	if rep.Surface != "" {
		t.Errorf("Surface = %q, want empty for java", rep.Surface)
	}
}
