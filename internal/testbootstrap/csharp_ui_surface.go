package testbootstrap

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/langid"
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
	testDirs, err := csharpTestProjectDirs(root)
	if err != nil {
		return csharpUISurfaceDetection{}, err
	}

	var (
		evidence         []string
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
		hasWwwrootIndex  bool
	)
	note := func(s string) {
		for _, e := range evidence {
			if e == s {
				return
			}
		}
		evidence = append(evidence, s)
	}

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
			// A test project's own fixtures are not the application's surface: a .cshtml under
			// tests/ is a fixture, and treating it as a page would put a pure Web API on the
			// browser path.
			if underAnyDir(root, path, testDirs) {
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(mustRel(root, path))
		lowRel := strings.ToLower(rel)
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".csproj":
			body, ok := readHeadLower(path)
			if !ok {
				return nil
			}
			if containsAny(body, csharpBlazorSDKMarkers) {
				hasBlazorWasm = true
				note("Blazor WebAssembly SDK in " + rel)
			}
		case ".cshtml":
			if strings.Contains(lowRel, "/pages/") || strings.HasPrefix(lowRel, "pages/") {
				hasRazorMarkup = true
				note("Razor Pages markup " + rel)
			} else if strings.Contains(lowRel, "/views/") || strings.HasPrefix(lowRel, "views/") {
				hasViewsMarkup = true
				note("MVC view " + rel)
			} else {
				hasRazorMarkup = true
				note("Razor markup " + rel)
			}
		case ".razor":
			hasBlazorMarkup = true
			note("Blazor component " + rel)
		case ".html":
			if strings.Contains(lowRel, "wwwroot/") && strings.EqualFold(d.Name(), "index.html") {
				hasWwwrootIndex = true
				note("static SPA entry " + rel)
			}
		case ".cs":
			body, ok := readHeadLower(path)
			if !ok {
				return nil
			}
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
				note("static file fallback in " + rel)
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
		return nil
	})
	if walkErr != nil {
		return csharpUISurfaceDetection{}, walkErr
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
	case (hasSPAStatic || hasWwwrootIndex) && hasWwwrootIndex:
		uiFramework = CSharpUISPAStatic
	}
	// Razor Pages wins over Blazor Server when both are present: its routes are plain URLs a browser
	// test navigates directly, while a Blazor circuit needs the page that hosts it — and that page
	// is a Razor Page. The mixed fixture has both, and either answer is defensible; this one names
	// the thing a generated test would actually open.
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

// csharpTestProjectDirs returns the directories of every project that references a test framework,
// so the scan can skip their fixtures.
func csharpTestProjectDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (dotnetproj.WalkSkipDir(d.Name()) || dotnetproj.WalkDepth(root, path) > maxSurfaceScanDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		body, ok := readHeadLower(path)
		if !ok {
			return nil
		}
		if csprojReferencesTestFrameworkLower(body) {
			out = append(out, filepath.Dir(path))
		}
		return nil
	})
	return out, err
}

// csprojReferencesTestFrameworkLower recognises the runner packages that make a project a test
// project. Matches the unit-path detection in internal/layout so the two cannot disagree about
// which directories hold tests.
func csprojReferencesTestFrameworkLower(lower string) bool {
	for _, m := range []string{
		"microsoft.net.test.sdk", "xunit", "nunit", "mstest.testframework", "mstest",
		"microsoft.aspnetcore.mvc.testing",
	} {
		if strings.Contains(lower, `include="`+m) || strings.Contains(lower, `include='`+m) {
			return true
		}
	}
	return false
}

func underAnyDir(root, path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d {
			return true
		}
		if rel, err := filepath.Rel(d, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
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
func readHeadLower(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, maxSurfaceScanFileBytes)
	n, err := f.Read(buf)
	if n <= 0 && err != nil {
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

// E2ESurfaceForBootstrap resolves the surface the E2E bootstrap should act on: the operator's
// override when set, otherwise detection. Non-C# languages return "" — they have no surface
// detection, and claiming one would put them on a path built for C#.
//
// The E2E bootstrap runs before the indexer and therefore before the run's own DetectE2E call, so it
// resolves the surface itself rather than receiving one.
func E2ESurfaceForBootstrap(repoAbs, lang string, rc *config.RunnerConfig) string {
	if !langid.IsCSharp(lang) {
		return ""
	}
	detected, err := detectCSharpUISurface(repoAbs)
	if err != nil {
		return ""
	}
	return string(resolveCSharpUISurface(csharpForcedE2ESurface(rc), detected).Surface)
}
