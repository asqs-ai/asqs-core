package apisurface

import (
	"strings"
	"testing"
)

// THE MISS THIS CLOSES. Run api-428e4bb1f792d71bacd54d8ba3953801 intercepted correctly — right
// API-only URL pattern, registered before navigation — and then reached for a member to hold the
// response open:
//
//	await page.route('**/api/catalog*', async (route) => { await route.delay(1500); … })
//	  → TypeError: route.delay is not a function
//
// `delay` is a real name in Playwright's surface (an option field on keyboard/mouse options) on the
// wrong receiver, which is precisely what a complete member list separates from a member that
// exists.
func TestPregenerateTargets_routingTypesReachEveryPlaywrightBinding(t *testing.T) {
	cases := map[string]struct {
		lang, framework     string
		wantRoute, wantPage string
	}{
		"typescript": {"typescript", "playwright", "Route", "Page"},
		"javascript": {"js", "playwright", "Route", "Page"},
		"java":       {"java", "playwright-java", "com.microsoft.playwright.Route", "com.microsoft.playwright.Page"},
		"csharp":     {"csharp", "playwright-dotnet", "Microsoft.Playwright.IRoute", "Microsoft.Playwright.IPage"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			names := map[string]bool{}
			for _, tgt := range PregenerateTargets(tc.lang, tc.framework, true) {
				names[tgt.Name] = true
			}
			if !names[tc.wantRoute] {
				t.Errorf("%s is not requested, so an invented Route member reaches the runtime again", tc.wantRoute)
			}
			if !names[tc.wantPage] {
				t.Errorf("%s is not requested, so route()'s own signature is unstated", tc.wantPage)
			}
		})
	}
}

// Routing is an E2E concern and a Playwright concern. A unit gap, or a Cypress suite, must not pay
// for it.
func TestPregenerateTargets_routingTypesAreScoped(t *testing.T) {
	carriesRouting := func(lang, framework string, isE2E bool) bool {
		for _, tgt := range PregenerateTargets(lang, framework, isE2E) {
			n := tgt.Name
			if n == "Route" || n == "Page" || strings.HasSuffix(n, ".Route") ||
				strings.HasSuffix(n, ".Page") || strings.HasSuffix(n, ".IRoute") || strings.HasSuffix(n, ".IPage") {
				return true
			}
		}
		return false
	}

	if carriesRouting("typescript", "playwright", false) {
		t.Error("a unit gap carries the routing types")
	}
	if carriesRouting("typescript", "cypress", true) {
		t.Error("a Cypress suite carries Playwright's routing types")
	}
	if carriesRouting("python", "playwright", true) {
		t.Error("an unsupported language carries routing types")
	}
	if !carriesRouting("typescript", "playwright", true) {
		t.Error("a Playwright E2E gap must carry the routing types")
	}
}
