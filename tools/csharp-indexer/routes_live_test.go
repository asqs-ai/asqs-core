package csharpindexer

import (
	"context"
	"strings"
	"testing"
	"time"
)

func fixtureSymbolFQs(t *testing.T, path, kind string) []string {
	t.Helper()
	dll := liveDLL(t)
	m, err := Run(context.Background(), minimalFixtureRepo(t), dll, RunConfig{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	pf := m[path]
	if pf == nil {
		t.Fatalf("%s not indexed; got %v", path, keysOf(m))
	}
	var out []string
	for _, s := range pf.Symbols {
		if s.Kind == kind {
			out = append(out, s.FQName)
		}
	}
	return out
}

// API routes were never extracted in practice. The compilations this tool builds reference the BCL
// and nothing else — no ASP.NET — so `[HttpGet]` does not resolve, and an unresolved attribute's
// symbol carries the name AS WRITTEN: "HttpGet", never "HttpGetAttribute". Every rule keyed on the
// suffixed spelling, so in a real repository not one of them ever matched, and everything built on
// C# routes was dead: uncovered-route gaps, TARGETS_API_ROUTE coverage, the E2E plan's anchors.
//
// The fixture-3 validation run confirms it: a controller with [HttpGet] produced zero API_ROUTE
// symbols.
func TestIndexer_extractsApiRoutes(t *testing.T) {
	got := fixtureSymbolFQs(t, "src/Controllers/BasketApiController.cs", "API_ROUTE")
	want := []string{
		// [controller] expanded, {id} normalised to a wildcard, path lower-cased.
		"API_ROUTE:GET:/api/basketapi/*@Minimal.Api.BasketApiController#GetById(string)",
		"API_ROUTE:POST:/api/basketapi@Minimal.Api.BasketApiController#Create()",
		// A verb the old five-verb switch did not know.
		"API_ROUTE:HEAD:/api/basketapi/ping@Minimal.Api.BasketApiController#Ping()",
		// [Route] with no verb attribute beside it is a GET, by ASP.NET's own convention.
		"API_ROUTE:GET:/api/basketapi/legacy/*@Minimal.Api.BasketApiController#Legacy(string)",
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing route %s\ngot: %v", w, got)
		}
	}
	// The token must never survive into a path: "/api/[controller]" matches no client call ever.
	for _, g := range got {
		if strings.Contains(g, "[") {
			t.Errorf("route kept an unexpanded token: %s", g)
		}
	}
}

// A client call and the route it reaches have to normalise to the SAME string, because the Go side
// compares them exactly. Three things stood in the way: the route wrote `{id}` while the call wrote
// a value, the route's [controller] kept its source casing while the call was lower-case, and an
// interpolated path — the normal way to address a parameterised endpoint — is not a constant and
// was dropped entirely.
func TestIndexer_clientCallsMatchTheirRoutes(t *testing.T) {
	clients := fixtureSymbolFQs(t, "src/ApiConsumer.cs", "API_CLIENT_REQUEST")
	routes := fixtureSymbolFQs(t, "src/Controllers/BasketApiController.cs", "API_ROUTE")

	routeKeys := map[string]bool{}
	for _, r := range routes {
		if _, path, ok := splitRouteFQ(r, "API_ROUTE:"); ok {
			routeKeys[path] = true
		}
	}
	if len(clients) != 2 {
		t.Fatalf("client requests = %v, want the GetFromJsonAsync and PostAsJsonAsync calls", clients)
	}
	for _, c := range clients {
		_, path, ok := splitRouteFQ(c, "API_CLIENT_REQUEST:")
		if !ok {
			t.Errorf("unparseable client FQ: %s", c)
			continue
		}
		if !routeKeys[path] {
			t.Errorf("client %s normalises to %q, which matches no route (routes: %v)", c, path, routeKeys)
		}
	}
}

// splitRouteFQ returns the METHOD:path key both sides must agree on.
func splitRouteFQ(fq, prefix string) (method, key string, ok bool) {
	if !strings.HasPrefix(fq, prefix) {
		return "", "", false
	}
	rest := fq[len(prefix):]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return "", "", false
	}
	rest = rest[:at]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", "", false
	}
	return rest[:colon], rest, true
}

// An MVC action that renders a view is a browser surface; a Razor Pages model's route comes from
// its FILE PATH, and an Index page answers at its directory as well as at its own name. Neither was
// emitted at all, so a UI E2E gap had no route to navigate to.
func TestIndexer_extractsPageRoutes(t *testing.T) {
	mvc := fixtureSymbolFQs(t, "src/Controllers/HomeController.cs", "PAGE_ROUTE")
	for _, w := range []string{
		"PAGE_ROUTE:/home@Minimal.Web.HomeController#Index()", // the default route makes Index the root
		"PAGE_ROUTE:/home/about@Minimal.Web.HomeController#About()",
	} {
		found := false
		for _, g := range mvc {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing MVC page route %s\ngot: %v", w, mvc)
		}
	}

	pages := fixtureSymbolFQs(t, "src/Pages/Orders/Index.cshtml.cs", "PAGE_ROUTE")
	joined := strings.Join(pages, " ")
	for _, w := range []string{"/orders/index@", "/orders@"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing Razor page route %s\ngot: %v", w, pages)
		}
	}
	// The handler, not the model, is what a route points at.
	if !strings.Contains(joined, "#OnGet()") || !strings.Contains(joined, "#OnPost(string)") {
		t.Errorf("page routes do not name their handlers: %v", pages)
	}
}

// An API controller renders no page, so it must not produce a PAGE_ROUTE — the two kinds decide
// which sort of test can drive the endpoint.
func TestIndexer_apiControllerIsNotAPageSurface(t *testing.T) {
	if got := fixtureSymbolFQs(t, "src/Controllers/BasketApiController.cs", "PAGE_ROUTE"); len(got) != 0 {
		t.Errorf("an [ApiController] produced page routes: %v", got)
	}
}
