package testbootstrap

import (
	"strings"
	"testing"
)

// Markup under a test project is a FIXTURE, not the application's surface — the invariant the .cs
// deferral already enforced and the markup classification bypassed, because .cshtml and .razor were
// classified inline during the walk, before the set of test directories was even known.
//
// It flips the answer on shapes that are not exotic: a Web API using AddControllersWithViews for a
// single error page, with golden-output .cshtml under tests, read as mixed/mvc-views — browser E2E
// hints and page-route anchors for an application with no pages.
func TestDetectCSharpUISurface_markupUnderATestProjectIsAFixture(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "razor views registered for an error page, .cshtml golden files under tests",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllersWithViews();
var app = builder.Build();
app.MapControllers();
app.MapDefaultControllerRoute();
app.Run();`,
				"tests/Api.Tests/Api.Tests.csproj":         `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup></Project>`,
				"tests/Api.Tests/Views/Golden/Home.cshtml": "<h1>golden output</h1>",
			},
		},
		{
			name: "bUnit: Blazor registered for a widget, .razor components only under tests",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllers();
builder.Services.AddServerSideBlazor();
var app = builder.Build();
app.MapControllers();
app.MapBlazorHub();
app.Run();`,
				"tests/Api.Tests/Api.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup>
  <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
  <PackageReference Include="bunit" Version="1.31.3" />
</ItemGroup></Project>`,
				"tests/Api.Tests/Components/Stub.razor": "@page \"/stub\"\n<h1>stub</h1>",
			},
		},
		{
			name: "a .cshtml mail template under the test project",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var app = WebApplication.Create(args);
app.MapGet("/healthz", () => Results.Ok());
app.Run();`,
				"tests/Api.Tests/Api.Tests.csproj":     `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup></Project>`,
				"tests/Api.Tests/Fixtures/Mail.cshtml": "@page\n<h1>mail</h1>",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			for rel, body := range tc.files {
				writeSurfaceFile(t, repo, rel, body)
			}
			got, err := detectCSharpUISurface(repo)
			if err != nil {
				t.Fatal(err)
			}
			if got.Surface != CSharpSurfaceAPI {
				t.Fatalf("Surface = %s/%s, want api (evidence: %s)", got.Surface, got.UIFramework, got.Evidence)
			}
			if strings.Contains(got.Evidence, "tests/") {
				t.Errorf("a test-project file was cited as application evidence: %s", got.Evidence)
			}
		})
	}
}

// The stock .NET 6/7 Blazor Server template registers Razor Pages for its host shell
// (Pages/_Host.cshtml) and serves everything else from .razor components. Calling that razor-pages
// sends a browser test to look for Razor Page routes that do not exist. Files whose name begins
// with `_` are not routable pages by ASP.NET convention, so they are not evidence of one.
func TestDetectCSharpUISurface_blazorServerHostShellIsNotARazorPagesApp(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "App/App.csproj", webCsproj)
	writeSurfaceFile(t, repo, "App/Program.cs", `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
builder.Services.AddServerSideBlazor();
var app = builder.Build();
app.MapBlazorHub();
app.MapFallbackToPage("/_Host");
app.Run();`)
	writeSurfaceFile(t, repo, "App/Pages/_Host.cshtml", "@page \"/\"\n@namespace App.Pages")
	writeSurfaceFile(t, repo, "App/Pages/_Layout.cshtml", "<html><body>@RenderBody()</body></html>")
	writeSurfaceFile(t, repo, "App/Pages/Index.razor", "@page \"/\"\n<h1>Hello</h1>")
	writeSurfaceFile(t, repo, "App/Pages/Counter.razor", "@page \"/counter\"\n<h1>Counter</h1>")

	got, err := detectCSharpUISurface(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.UIFramework != CSharpUIBlazorServer {
		t.Fatalf("UIFramework = %s, want blazor-server (evidence: %s)", got.UIFramework, got.Evidence)
	}
	if got.Surface != CSharpSurfaceUI {
		t.Fatalf("Surface = %s, want ui", got.Surface)
	}
}

// An application with real Razor Pages BESIDE Blazor is a Razor Pages app for E2E purposes: those
// routes are plain URLs a browser test navigates. This is the validation fixture's shape.
func TestDetectCSharpUISurface_routableRazorPagesBeatBlazor(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "App/App.csproj", webCsproj)
	writeSurfaceFile(t, repo, "App/Program.cs", `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
builder.Services.AddServerSideBlazor();
var app = builder.Build();
app.MapRazorPages();
app.MapBlazorHub();
app.Run();`)
	writeSurfaceFile(t, repo, "App/Pages/_Host.cshtml", "@page \"/_Host\"")
	writeSurfaceFile(t, repo, "App/Pages/Orders/Index.cshtml", "@page\n<h1>Orders</h1>")
	writeSurfaceFile(t, repo, "App/Components/Counter.razor", "@page \"/counter\"\n<h1>Counter</h1>")

	got, err := detectCSharpUISurface(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.UIFramework != CSharpUIRazorPages {
		t.Fatalf("UIFramework = %s, want razor-pages (evidence: %s)", got.UIFramework, got.Evidence)
	}
}
