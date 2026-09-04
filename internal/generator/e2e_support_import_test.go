package generator

import (
	"strings"
	"testing"
)

// Run 2026-09-03: the notice named `e2e/support/api.ts` but not how to import it, and the model
// wrote `'../../support/api'` from e2e/routes/ — one level too many — losing the first E2E repair
// round to `Cannot find module`. The notice now carries the exact specifier for the spec directory
// the generator writes to.
func TestSupportModuleImportSpecifier(t *testing.T) {
	cases := map[[2]string]string{
		{"e2e/routes", "e2e/support/api.ts"}:      "../support/api",
		{"e2e", "e2e/support/api.ts"}:             "./support/api",
		{"e2e/routes/deep", "e2e/support/api.ts"}: "../../support/api",
		{"e2e/routes", "e2e/routes/helpers.ts"}:   "./helpers",
	}
	for in, want := range cases {
		if got := supportModuleImportSpecifier(in[0], in[1]); got != want {
			t.Errorf("supportModuleImportSpecifier(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestRenderE2EBackendNotice_carriesExactImportAndSignatures(t *testing.T) {
	got := renderE2EBackendNotice([]string{"http://localhost:4201"}, "e2e/support/api.ts")
	for _, want := range []string{
		"import { stubJson, stubJsonAfter, stubError } from '../support/api';",
		"`" + playwrightRouteSpecDir + "/`",
		"'./support/api'",
		"stubJsonAfter(page, urlPattern, body, delayMs, status = 200)",
		"stubError(page, urlPattern, status = 500, body?)",
		"do not look it up with `get_symbol`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "'../../support/api'") {
		t.Error("the notice must not suggest the two-level specifier the model previously guessed")
	}
	// Without a support module the notice keeps its inline page.route example and no import line.
	plain := renderE2EBackendNotice([]string{"http://localhost:4201"}, "")
	if strings.Contains(plain, "support/api") || !strings.Contains(plain, "page.route(") {
		t.Errorf("notice without a support module should fall back to the inline example:\n%s", plain)
	}
}
