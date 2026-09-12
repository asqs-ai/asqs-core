package testbootstrap

import "testing"

// Markers are matched against code, not against comments and string literals. Without that, a
// Razor Pages app whose Program.cs mentions MapGet in a `// TODO` reads as mixed, and a class
// library holding a controller template in a verbatim string reads as api — which hands it
// ProfileHTTPAPI and hints telling the model to derive from WebApplicationFactory<Program> in a
// project that has no Program.
func TestDetectCSharpUISurface_ignoresCommentsAndStringLiterals(t *testing.T) {
	cases := []struct {
		name        string
		files       map[string]string
		wantSurface CSharpUISurface
		wantUI      CSharpUIFramework
	}{
		{
			name:        "a minimal-API call named only in a line comment",
			wantSurface: CSharpSurfaceUI,
			wantUI:      CSharpUIRazorPages,
			files: map[string]string{
				"App/App.csproj": webCsproj,
				"App/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
var app = builder.Build();
app.MapRazorPages();
// TODO: expose a health endpoint, something like app.MapGet("/healthz", () => "ok");
app.Run();`,
				"App/Pages/Index.cshtml": "@page\n<h1>Hi</h1>",
			},
		},
		{
			name:        "a controller inside a verbatim string constant",
			wantSurface: CSharpSurfaceNone,
			wantUI:      CSharpUINone,
			files: map[string]string{
				"Lib/Lib.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
				"Lib/Templates.cs": "namespace Lib;\npublic static class Templates {\n" +
					"    public const string Controller = @\"public class FooController : ControllerBase { }\";\n}",
			},
		},
		{
			name:        "a block comment describing the API that was removed",
			wantSurface: CSharpSurfaceNone,
			wantUI:      CSharpUINone,
			files: map[string]string{
				"Lib/Lib.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
				"Lib/Notes.cs": `namespace Lib;
/* This used to be a web project:
     builder.Services.AddControllers();
     app.MapControllers();
   It is a plain library now. */
public class Basket { }`,
			},
		},
		{
			name:        "a raw string literal holding markup",
			wantSurface: CSharpSurfaceNone,
			wantUI:      CSharpUINone,
			files: map[string]string{
				"Lib/Lib.csproj": `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
				"Lib/Snippets.cs": "namespace Lib;\npublic static class Snippets {\n" +
					"    public const string Startup = \"\"\"\n    builder.Services.AddRazorPages();\n    app.MapRazorPages();\n    \"\"\";\n}",
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
			if got.Surface != tc.wantSurface || got.UIFramework != tc.wantUI {
				t.Fatalf("got %s/%s, want %s/%s (evidence: %s)",
					got.Surface, got.UIFramework, tc.wantSurface, tc.wantUI, got.Evidence)
			}
		})
	}
}

// A hosted SPA is identified by the REGISTRATION that serves it, not by a committed index.html.
// The two halves were conflated, and the conflation failed in both directions: a Web API that ships
// a static landing page read as a UI, and a real SPA host whose index.html is built at publish time
// — the normal ClientApp/ layout — was missed entirely.
func TestDetectCSharpUISurface_spaHostNeedsTheRegistration(t *testing.T) {
	t.Run("a Web API that serves a static landing page is still an API", func(t *testing.T) {
		repo := t.TempDir()
		writeSurfaceFile(t, repo, "Api/Api.csproj", webCsproj)
		writeSurfaceFile(t, repo, "Api/Program.cs", `var app = WebApplication.Create(args);
app.UseStaticFiles();
app.MapGet("/healthz", () => Results.Ok());
app.Run();`)
		writeSurfaceFile(t, repo, "Api/wwwroot/index.html", "<html><body>API docs</body></html>")

		got, _ := detectCSharpUISurface(repo)
		if got.Surface != CSharpSurfaceAPI {
			t.Fatalf("Surface = %s, want api (evidence: %s)", got.Surface, got.Evidence)
		}
	})

	t.Run("a SPA host is a UI even when the built index.html is not committed", func(t *testing.T) {
		repo := t.TempDir()
		writeSurfaceFile(t, repo, "Host/Host.csproj", webCsproj)
		writeSurfaceFile(t, repo, "Host/Program.cs", `var app = WebApplication.Create(args);
app.UseSpaStaticFiles();
app.MapFallbackToFile("index.html");
app.Run();`)

		got, _ := detectCSharpUISurface(repo)
		if got.Surface != CSharpSurfaceUI || got.UIFramework != CSharpUISPAStatic {
			t.Fatalf("got %s/%s, want ui/spa-static (evidence: %s)", got.Surface, got.UIFramework, got.Evidence)
		}
	})
}

// When the repository's only project is also its test project, pruning "test directories" prunes
// the whole repository and every signal disappears.
func TestDetectCSharpUISurface_testProjectAtTheRepoRoot(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "App.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup>
</Project>`)
	writeSurfaceFile(t, repo, "Program.cs", `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
var app = builder.Build();
app.MapRazorPages();
app.Run();`)
	writeSurfaceFile(t, repo, "Pages/Index.cshtml", "@page\n<h1>Hi</h1>")

	got, err := detectCSharpUISurface(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface != CSharpSurfaceUI {
		t.Fatalf("Surface = %s, want ui: a root test project must not prune the repository (evidence: %s)",
			got.Surface, got.Evidence)
	}
}
