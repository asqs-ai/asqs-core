package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSurfaceFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const webCsproj = `<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`

// Bootstrap knew a project's SDK but never whether the application has pages a browser can drive.
// Everything downstream then assumed one answer: DefaultRetrievalProfileE2E picked http_api for
// every C# repo, the E2E bootstrap always installed Playwright, and a Razor Pages app got API-shaped
// E2E gaps while a pure Web API got a browser install it could never use.
func TestDetectCSharpUISurface(t *testing.T) {
	cases := []struct {
		name          string
		files         map[string]string
		wantSurface   CSharpUISurface
		wantUIFramewk CSharpUIFramework
		wantEvidence  string
	}{
		{
			name: "API controllers only",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllers();
builder.Services.AddEndpointsApiExplorer();
var app = builder.Build();
app.MapControllers();
app.Run();`,
				"src/Api/Controllers/OrdersController.cs": `[ApiController]
[Route("api/[controller]")]
public class OrdersController : ControllerBase { }`,
			},
			wantSurface:   CSharpSurfaceAPI,
			wantUIFramewk: CSharpUINone,
			wantEvidence:  "ApiController",
		},
		{
			name: "minimal APIs with no controllers at all",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var app = WebApplication.Create(args);
app.MapGet("/healthz", () => Results.Ok());
app.MapPost("/api/events", (Payload p) => Results.Accepted());
app.Run();`,
			},
			wantSurface:   CSharpSurfaceAPI,
			wantUIFramewk: CSharpUINone,
		},
		{
			name: "Razor Pages only",
			files: map[string]string{
				"src/Web/Web.csproj": webCsproj,
				"src/Web/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddRazorPages();
var app = builder.Build();
app.MapRazorPages();
app.Run();`,
				"src/Web/Pages/Index.cshtml": "@page\n<h1>Hello</h1>",
			},
			wantSurface:   CSharpSurfaceUI,
			wantUIFramewk: CSharpUIRazorPages,
			wantEvidence:  "Razor Pages",
		},
		{
			name: "MVC views plus API controllers is mixed",
			files: map[string]string{
				"src/Web/Web.csproj": webCsproj,
				"src/Web/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllersWithViews();
var app = builder.Build();
app.MapDefaultControllerRoute();
app.MapControllers();
app.Run();`,
				"src/Web/Views/Home/Index.cshtml": "<h1>Home</h1>",
				"src/Web/Controllers/ApiController.cs": `[ApiController]
[Route("api/things")]
public class ThingsController : ControllerBase { }`,
			},
			wantSurface:   CSharpSurfaceMixed,
			wantUIFramewk: CSharpUIMvcViews,
		},
		{
			name: "Blazor Server",
			files: map[string]string{
				"src/Web/Web.csproj": webCsproj,
				"src/Web/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddServerSideBlazor();
var app = builder.Build();
app.MapBlazorHub();
app.Run();`,
				"src/Web/Components/Counter.razor": "@page \"/counter\"\n<h1>Counter</h1>",
			},
			wantSurface:   CSharpSurfaceUI,
			wantUIFramewk: CSharpUIBlazorServer,
		},
		{
			name: "Blazor WebAssembly",
			files: map[string]string{
				"src/Client/Client.csproj":      `<Project Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
				"src/Client/Pages/Index.razor":  "@page \"/\"\n<h1>Hi</h1>",
				"src/Client/wwwroot/index.html": "<html><body><div id=app></div></body></html>",
			},
			wantSurface:   CSharpSurfaceUI,
			wantUIFramewk: CSharpUIBlazorWasm,
		},
		{
			name: "a class library has no surface at all",
			files: map[string]string{
				"src/Core/Core.csproj": `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
				"src/Core/Basket.cs":   "namespace Core; public class Basket {}",
			},
			wantSurface:   CSharpSurfaceNone,
			wantUIFramewk: CSharpUINone,
		},
		{
			name: "a worker service has no surface",
			files: map[string]string{
				"src/Worker/Worker.csproj": `<Project Sdk="Microsoft.NET.Sdk.Worker"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`,
				"src/Worker/Program.cs":    `var host = Host.CreateDefaultBuilder(args).Build(); host.Run();`,
			},
			wantSurface:   CSharpSurfaceNone,
			wantUIFramewk: CSharpUINone,
		},
		{
			name: "a static SPA host is a UI surface",
			files: map[string]string{
				"src/Host/Host.csproj": webCsproj,
				"src/Host/Program.cs": `var app = WebApplication.Create(args);
app.UseStaticFiles();
app.MapFallbackToFile("index.html");
app.Run();`,
				"src/Host/wwwroot/index.html": "<html><body><div id=root></div></body></html>",
			},
			wantSurface:   CSharpSurfaceUI,
			wantUIFramewk: CSharpUISPAStatic,
		},
		{
			name: "gRPC is an API surface",
			files: map[string]string{
				"src/Grpc/Grpc.csproj": webCsproj,
				"src/Grpc/Program.cs": `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddGrpc();
var app = builder.Build();
app.MapGrpcService<GreeterService>();
app.Run();`,
			},
			wantSurface:   CSharpSurfaceAPI,
			wantUIFramewk: CSharpUINone,
		},
		{
			name: "markup under a test project does not make the app a UI",
			files: map[string]string{
				"src/Api/Api.csproj": webCsproj,
				"src/Api/Program.cs": `var app = WebApplication.Create(args);
app.MapGet("/healthz", () => Results.Ok());
app.Run();`,
				"tests/Api.Tests/Api.Tests.csproj":     `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup></Project>`,
				"tests/Api.Tests/Pages/Fixture.cshtml": "@page\n<h1>fixture</h1>",
			},
			wantSurface:   CSharpSurfaceAPI,
			wantUIFramewk: CSharpUINone,
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
				t.Fatalf("detect: %v", err)
			}
			if got.Surface != tc.wantSurface {
				t.Errorf("Surface = %q, want %q (evidence: %s)", got.Surface, tc.wantSurface, got.Evidence)
			}
			if got.UIFramework != tc.wantUIFramewk {
				t.Errorf("UIFramework = %q, want %q (evidence: %s)", got.UIFramework, tc.wantUIFramewk, got.Evidence)
			}
			if tc.wantEvidence != "" && !strings.Contains(got.Evidence, tc.wantEvidence) {
				t.Errorf("Evidence = %q, want it to mention %q", got.Evidence, tc.wantEvidence)
			}
			if got.Surface != CSharpSurfaceNone && got.Evidence == "" {
				t.Error("a detected surface carries no evidence; an operator cannot check the call")
			}
		})
	}
}

// An operator's explicit choice always wins: detection is a default, not a policy.
func TestResolveCSharpUISurface_override(t *testing.T) {
	detected := csharpUISurfaceDetection{Surface: CSharpSurfaceAPI, UIFramework: CSharpUINone, Evidence: "[ApiController]"}

	for _, forced := range []string{"ui", "UI", " ui "} {
		got := resolveCSharpUISurface(forced, detected)
		if got.Surface != CSharpSurfaceUI {
			t.Errorf("forced %q: Surface = %q, want ui", forced, got.Surface)
		}
		if !strings.Contains(got.Evidence, "forced") {
			t.Errorf("forced %q: Evidence = %q, want it to say the surface was forced", forced, got.Evidence)
		}
	}
	for _, auto := range []string{"", "auto", "AUTO"} {
		if got := resolveCSharpUISurface(auto, detected); got.Surface != CSharpSurfaceAPI {
			t.Errorf("%q: Surface = %q, want the detected api", auto, got.Surface)
		}
	}
	// An unrecognised value is not silently treated as a surface.
	if got := resolveCSharpUISurface("nonsense", detected); got.Surface != CSharpSurfaceAPI {
		t.Errorf("unknown override: Surface = %q, want the detected api", got.Surface)
	}
}

// The real second fixture: Razor Pages + Blazor Server + MVC views beside API controllers.
func TestDetectCSharpUISurface_mixedFixtureShape(t *testing.T) {
	repo := t.TempDir()
	writeSurfaceFile(t, repo, "src/Shop/Shop.csproj", webCsproj)
	writeSurfaceFile(t, repo, "src/Shop/Program.cs", `var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllersWithViews();
builder.Services.AddRazorPages();
builder.Services.AddServerSideBlazor();
builder.Services.AddEndpointsApiExplorer();
var app = builder.Build();
app.MapControllers();
app.MapDefaultControllerRoute();
app.MapRazorPages();
app.MapBlazorHub();
app.MapGet("/healthz", () => Results.Ok());
app.Run();`)
	writeSurfaceFile(t, repo, "src/Shop/Pages/Orders/Index.cshtml", "@page\n<h1>Orders</h1>")
	writeSurfaceFile(t, repo, "src/Shop/Views/Home/Index.cshtml", "<h1>Home</h1>")
	writeSurfaceFile(t, repo, "src/Shop/Components/Counter.razor", "@page \"/counter\"\n<h1>Counter</h1>")
	writeSurfaceFile(t, repo, "src/Shop/Controllers/BasketApiController.cs", `[ApiController]
[Route("api/[controller]")]
public class BasketApiController : ControllerBase { }`)

	got, err := detectCSharpUISurface(repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got.Surface != CSharpSurfaceMixed {
		t.Fatalf("Surface = %q, want mixed (evidence: %s)", got.Surface, got.Evidence)
	}
	// Razor Pages is the most specific driveable UI here and the one a Playwright test would target.
	if got.UIFramework != CSharpUIRazorPages && got.UIFramework != CSharpUIBlazorServer {
		t.Fatalf("UIFramework = %q, want a driveable UI framework (evidence: %s)", got.UIFramework, got.Evidence)
	}
}
