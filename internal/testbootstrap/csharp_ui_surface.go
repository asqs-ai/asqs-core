package testbootstrap

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// CSharpUISurface is what an E2E test can drive against this application.
//
// Bootstrap already classified a project's SDK (plain, aspnetcore, blazor-wasm, workload) but never
// answered the question every E2E decision actually depends on: does the application serve pages a
// browser can drive, an HTTP API a client can call, both, or neither? Without it everything
// downstream assumed one answer — DefaultRetrievalProfileE2E returned http_api for every C# repo,
// the E2E bootstrap always installed Playwright and Chromium, and the hints always described an
// API-shaped test. A Razor Pages application got E2E gaps for routes it does not have, and a pure
// Web API got a browser install it can never use.
type CSharpUISurface string

const (
	// CSharpSurfaceNone: a library, a console app, a worker. No E2E surface at all.
	CSharpSurfaceNone CSharpUISurface = "none"
	// CSharpSurfaceAPI: HTTP endpoints only — controllers, minimal APIs, gRPC.
	CSharpSurfaceAPI CSharpUISurface = "api"
	// CSharpSurfaceUI: pages a browser drives — Razor Pages, MVC views, Blazor, a hosted SPA.
	CSharpSurfaceUI CSharpUISurface = "ui"
	// CSharpSurfaceMixed: both, which is the common shape for an app with a UI and a JSON API.
	CSharpSurfaceMixed CSharpUISurface = "mixed"
)

// CSharpUIFramework names how the UI is built, which decides what a browser test has to do to
// reach a page and which selectors exist to address it.
type CSharpUIFramework string

const (
	CSharpUINone         CSharpUIFramework = "none"
	CSharpUIRazorPages   CSharpUIFramework = "razor-pages"
	CSharpUIMvcViews     CSharpUIFramework = "mvc-views"
	CSharpUIBlazorServer CSharpUIFramework = "blazor-server"
	CSharpUIBlazorWasm   CSharpUIFramework = "blazor-wasm"
	CSharpUISPAStatic    CSharpUIFramework = "spa-static"
)

// csharpUISurfaceDetection is the detection result plus the evidence behind it. The evidence exists
// so an operator reading the audit can check the call rather than take it on faith — and so a wrong
// answer names the file that caused it.
type csharpUISurfaceDetection struct {
	Surface     CSharpUISurface
	UIFramework CSharpUIFramework
	Evidence    string
}

// maxSurfaceScanDepth bounds the walk. Signals live in a project's own tree, not twelve levels down.
const maxSurfaceScanDepth = 10

// maxSurfaceScanFileBytes bounds how much of one file is read. Every signal is a using, an
// attribute or a builder call — all of which appear near the top — and a generated .cs file can be
// megabytes.
const maxSurfaceScanFileBytes = 256 * 1024

var (
	// UI signals. Presence of markup alone is not enough: a .cshtml can be an email template, so the
	// framework registration in Program.cs / Startup.cs is what confirms it is served.
	razorPagesRegistrations = []string{"addrazorpages(", "maprazorpages(", "addrazorcomponents("}
	mvcViewRegistrations    = []string{"addcontrollerswithviews(", "mapcontrollerroute(", "mapdefaultcontrollerroute(", "addmvc("}
	blazorServerMarkers     = []string{"addserversideblazor(", "mapblazorhub(", "maprazorcomponents(", "addinteractiveserver"}
	spaStaticMarkers        = []string{"mapfallbacktofile(", "usespastaticfiles(", "usespa("}

	// API signals.
	apiControllerMarkers = []string{"[apicontroller]", ": controllerbase", ":controllerbase"}
	apiRegistrations     = []string{"addcontrollers(", "mapcontrollers(", "addendpointsapiexplorer(", "addswaggergen("}
	minimalAPIMarkers    = []string{"mapget(", "mappost(", "mapput(", "mapdelete(", "mappatch(", "mapmethods(", "mapgroup("}
	grpcMarkers          = []string{"addgrpc(", "mapgrpcservice"}
)

// DetectCSharpUISurface reports what an E2E test can drive against the repository. File and text
// based, no build: bootstrap runs before anything is compiled.
func DetectCSharpUISurface(repoAbs string) (CSharpUISurface, CSharpUIFramework, string, error) {
	d, err := detectCSharpUISurface(repoAbs)
	if err != nil {
		return CSharpSurfaceNone, CSharpUINone, "", err
	}
	return d.Surface, d.UIFramework, d.Evidence, nil
}

func detectCSharpUISurface(repoAbs string) (csharpUISurfaceDetection, error) {
	root := filepath.Clean(strings.TrimSpace(repoAbs))
	if root == "" {
		return csharpUISurfaceDetection{Surface: CSharpSurfaceNone, UIFramework: CSharpUINone}, nil
	}

	var (
		evidence         []string
		testDirs         []string
		hasRazorPages    bool
		hasMvcViews      bool
		hasBlazorServer  bool
		hasBlazorWasm    bool
		hasSPAStatic     bool
		hasAPIController bool
		hasAPIRegistered bool
		hasMinimalAPI    bool
		hasGrpc          bool
		hasRazorMarkup   bool
		hasBlazorMarkup  bool
		hasViewsMarkup   bool
	)
	note := func(s string) {
		for _, e := range evidence {
			if e == s {
				return
			}
		}
		evidence = append(evidence, s)
	}

	// One walk, collecting the project layout and the signals together. The scan used to make two
	// passes over the whole tree — one to find the test projects, one to read the signals — and is
	// called several times per run.
	type pending struct{ path, rel string }
	var sources []pending
	buf := make([]byte, maxSurfaceScanFileBytes)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxSurfaceScanDepth {
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(mustRel(root, path))
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".csproj":
			body, ok := readHeadLower(path, buf)
			if !ok {
				return nil
			}
			// A test project AT the repository root is not a "test directory": pruning it would
			// prune the whole repository, which is what a single-project repo that also holds its
			// own tests looks like — and the scan then found no surface at all.
			if csprojReferencesTestFrameworkLower(body) {
				if dir := filepath.Dir(path); filepath.Clean(dir) != root {
					testDirs = append(testDirs, dir)
				}
			}
			if containsAny(body, csharpBlazorSDKMarkers) {
				hasBlazorWasm = true
				note("Blazor WebAssembly SDK in " + rel)
			}
		case ".cshtml", ".razor", ".cs":
			// Deferred, all three: whether a file counts depends on the test-project directories,
			// and a .csproj deeper in the walk can still add one. Markup was classified inline here
			// and so escaped the filter entirely — a Web API with golden .cshtml fixtures under
			// tests/ read as an MVC application, and a bUnit repo as a Blazor one.
			sources = append(sources, pending{path: path, rel: rel})
		}
		return nil
	})
	if walkErr != nil {
		return csharpUISurfaceDetection{}, walkErr
	}

	for _, src := range sources {
		// A test project's own fixtures are not the application's surface: a WebApplicationFactory
		// smoke test mentions MapControllers, and reading it would put every repo on the API path.
		if underAnyDir(src.path, testDirs) {
			continue
		}
		lowRel := strings.ToLower(src.rel)
		switch strings.ToLower(filepath.Ext(src.path)) {
		case ".cshtml":
			// A file whose name begins with `_` is not a routable page by ASP.NET convention — it is
			// a layout, a view-start or a Blazor host shell — so it is not evidence of one.
			if strings.HasPrefix(strings.ToLower(filepath.Base(src.path)), "_") {
				continue
			}
			if strings.Contains(lowRel, "/pages/") || strings.HasPrefix(lowRel, "pages/") {
				hasRazorMarkup = true
				note("Razor Pages markup " + src.rel)
			} else if strings.Contains(lowRel, "/views/") || strings.HasPrefix(lowRel, "views/") {
				hasViewsMarkup = true
				note("MVC view " + src.rel)
			} else {
				hasRazorMarkup = true
				note("Razor markup " + src.rel)
			}
			continue
		case ".razor":
			hasBlazorMarkup = true
			note("Blazor component " + src.rel)
			continue
		}
		body, ok := readHeadLower(src.path, buf)
		if !ok {
			continue
		}
		// Markers describe CODE, so comments and string literals are removed first: a `// TODO:
		// app.MapGet(...)` comment used to make a Razor Pages app read as mixed, and a class library
		// holding a controller in a verbatim string read as a Web API.
		body = dotnetproj.StripCSharpCommentsAndStrings(body)
		rel := src.rel
		if containsAny(body, razorPagesRegistrations) {
			hasRazorPages = true
			note("Razor Pages registered in " + rel)
		}
		if containsAny(body, mvcViewRegistrations) {
			hasMvcViews = true
			note("MVC views registered in " + rel)
		}
		if containsAny(body, blazorServerMarkers) {
			hasBlazorServer = true
			note("Blazor Server registered in " + rel)
		}
		if containsAny(body, spaStaticMarkers) {
			hasSPAStatic = true
			note("SPA static-file fallback in " + rel)
		}
		if containsAny(body, apiControllerMarkers) {
			hasAPIController = true
			note("[ApiController] / ControllerBase in " + rel)
		}
		if containsAny(body, apiRegistrations) {
			hasAPIRegistered = true
			note("API controllers registered in " + rel)
		}
		if containsAny(body, minimalAPIMarkers) {
			hasMinimalAPI = true
			note("minimal API endpoints in " + rel)
		}
		if containsAny(body, grpcMarkers) {
			hasGrpc = true
			note("gRPC service in " + rel)
		}
	}

	// A UI needs both the markup and the registration that serves it, except for Blazor WASM (whose
	// SDK is the registration) and a static SPA host (whose wwwroot/index.html is the page).
	uiFramework := CSharpUINone
	switch {
	case hasBlazorServer && hasBlazorMarkup:
		uiFramework = CSharpUIBlazorServer
	case hasBlazorWasm:
		uiFramework = CSharpUIBlazorWasm
	case hasRazorPages && hasRazorMarkup:
		uiFramework = CSharpUIRazorPages
	case hasMvcViews && hasViewsMarkup:
		uiFramework = CSharpUIMvcViews
	case hasSPAStatic:
		// The REGISTRATION is the signal, not a committed wwwroot/index.html. Requiring the file
		// failed in both directions: a Web API that ships a static landing page read as a UI, and a
		// real SPA host whose index.html is built at publish time — the normal ClientApp/ layout,
		// where nothing is in git — was missed entirely.
		uiFramework = CSharpUISPAStatic
	}
	// Razor Pages wins over Blazor Server only when the application has ROUTABLE Razor Pages: those
	// are plain URLs a browser test navigates directly. The stock Blazor Server template registers
	// Razor Pages purely to serve its host shell (Pages/_Host.cshtml), and calling that a Razor
	// Pages application sends a generated test looking for page routes that do not exist —
	// hasRazorMarkup excludes `_`-prefixed files for exactly this reason.
	if hasRazorPages && hasRazorMarkup && uiFramework == CSharpUIBlazorServer {
		uiFramework = CSharpUIRazorPages
	}

	hasAPI := hasAPIController || hasAPIRegistered || hasMinimalAPI || hasGrpc
	hasUI := uiFramework != CSharpUINone

	surface := CSharpSurfaceNone
	switch {
	case hasUI && hasAPI:
		surface = CSharpSurfaceMixed
	case hasUI:
		surface = CSharpSurfaceUI
	case hasAPI:
		surface = CSharpSurfaceAPI
	}

	sort.Strings(evidence)
	return csharpUISurfaceDetection{
		Surface:     surface,
		UIFramework: uiFramework,
		Evidence:    strings.Join(evidence, "; "),
	}, nil
}

// resolveCSharpUISurface applies an operator override. Detection is a default, not a policy: an
// operator who knows their app better than a text scan can force the answer. An unrecognised value
// is ignored rather than treated as a surface — a typo must not silently disable browser testing.
func resolveCSharpUISurface(configured string, detected csharpUISurfaceDetection) csharpUISurfaceDetection {
	switch CSharpUISurface(strings.ToLower(strings.TrimSpace(configured))) {
	case CSharpSurfaceNone, CSharpSurfaceAPI, CSharpSurfaceUI, CSharpSurfaceMixed:
	default:
		return detected
	}
	forced := CSharpUISurface(strings.ToLower(strings.TrimSpace(configured)))
	out := detected
	out.Surface = forced
	if forced == CSharpSurfaceNone {
		out.UIFramework = CSharpUINone
	}
	detail := detected.Evidence
	if detail == "" {
		detail = "nothing detected"
	}
	out.Evidence = "surface forced to " + string(forced) + " by configuration (detected " +
		string(detected.Surface) + ": " + detail + ")"
	return out
}

// csprojReferencesTestFrameworkLower recognises the runner packages that make a project a test
// project. Matches the unit-path detection in internal/layout so the two cannot disagree about
// which directories hold tests.
func csprojReferencesTestFrameworkLower(lower string) bool {
	for _, m := range []string{
		"microsoft.net.test.sdk", "xunit", "nunit", "mstest.testframework", "mstest",
		"microsoft.aspnetcore.mvc.testing",
		// bUnit is a Blazor component test library and appears in no production project. Without
		// it, a bUnit project's .razor fixtures counted as the application's own components.
		"bunit",
	} {
		if strings.Contains(lower, `include="`+m) || strings.Contains(lower, `include='`+m) {
			return true
		}
	}
	return false
}

// underAnyDir reports whether path sits inside one of dirs. Strictly inside: when a repository's
// only project is also its test project, its directory is the repository root, and treating that as
// "a test directory" pruned every file in the repo and detected no surface at all.
func underAnyDir(path string, dirs []string) bool {
	fileDir := filepath.Dir(path)
	for _, d := range dirs {
		rel, err := filepath.Rel(d, fileDir)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // outside d
		}
		return true
	}
	return false
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

// readHeadLower reads up to maxSurfaceScanFileBytes and lower-cases it for substring matching.
//
// io.ReadFull rather than a bare Read: a single read(2) may legally return fewer bytes than asked
// for, and does on FUSE-backed mounts — which includes Docker Desktop's bind mounts. A short read
// would silently truncate the file and hand the same repository a different surface depending on
// the filesystem it was checked out on. buf is reused across files by the caller.
func readHeadLower(path string, buf []byte) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	n, err := io.ReadFull(f, buf)
	if n <= 0 {
		return "", err == nil
	}
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", false
	}
	return strings.ToLower(string(buf[:n])), true
}

// csharpForcedE2ESurface reads bootstrap.policy.e2e_framework.surface. "auto" and the empty string
// both mean "detect it", which is the default.
func csharpForcedE2ESurface(rc *config.RunnerConfig) string {
	if rc == nil {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(rc.E2EFrameworkBootstrap.Surface))
	if v == "auto" {
		return ""
	}
	return v
}

// ptrTo is the one-line helper that lets a caller say "I consulted the configuration and this is
// what it said", where nil says "I could not".
func ptrTo(s string) *string { return &s }
