package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/websearch"
)

// get_symbol miss-ladder tests. asqs-core has no symbol_miss_fallback_test.go of its own; these are the
// cases that changed in the F7 port plus the third-party fallback they must not break.

func missRegistry(t *testing.T) *Registry {
	t.Helper()
	r, _, _ := testRegistry(t)
	return r
}
func TestGetSymbol_missFallsBackToWebSearch(t *testing.T) {
	r := missRegistry(t)
	web, reason := websearch.NewForTest(websearch.TestConfig{
		Results: []websearch.Result{{
			Title: "Route.FulfillOptions (Playwright Java)", URL: "https://docs.spring.io/route-fulfill", Snippet: "setBody, setContentType, setStatus",
		}},
		AllowedHosts: []string{"docs.spring.io"},
	})
	if web == nil {
		t.Fatal(reason)
	}
	r.Web = web
	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"com.microsoft.playwright.Route.FulfillOptions"}`)
	if !strings.Contains(out, "https://docs.spring.io/route-fulfill") {
		t.Fatalf("web fallback result missing:\n%s", out)
	}
	// Web-derived text keeps the injection fence — it is external content whichever tool name it
	// came back under.
	if !strings.Contains(out, "<<external:") {
		t.Fatalf("web-derived answer must stay fenced:\n%s", out)
	}
	if !strings.Contains(out, "not in the repository index") {
		t.Fatalf("the answer must say WHY web results are shown:\n%s", out)
	}
}

// F7. A name whose first segment is a directory of the repository is a mis-qualified LOCAL symbol,
// not a third-party type: run api-72dad6bb281cacee338f43c48432a780 asked for
// src.pages.OrdersPage.fetchOrdersPreview (the function lives in src/services/apiClient.ts), and
// the miss ladder searched the web for "src pages OrdersPage fetchOrdersPreview api reference".
// The web rung must be skipped and the model pointed at search_code.
func TestGetSymbol_repoLocalNameSkipsWebSearch(t *testing.T) {
	r := missRegistry(t)
	r.RepoRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(r.RepoRoot, "src", "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	web, reason := websearch.NewForTest(websearch.TestConfig{
		Results:      []websearch.Result{{Title: "should never be shown", URL: "https://docs.spring.io/never", Snippet: "x"}},
		AllowedHosts: []string{"docs.spring.io"},
	})
	if web == nil {
		t.Fatal(reason)
	}
	r.Web = web

	_, err := r.Invoke(context.Background(), ToolGetSymbol, json.RawMessage(`{"fq_name":"src.pages.OrdersPage.fetchOrdersPreview"}`))
	if err == nil {
		t.Fatal("a repo-local miss must stay a miss, not become web results")
	}
	if strings.Contains(err.Error(), "docs.spring.io") {
		t.Fatalf("web results leaked into a repo-local miss: %v", err)
	}
	if !strings.Contains(err.Error(), "search_code") || !strings.Contains(err.Error(), "looks like a path inside it") {
		t.Fatalf("the miss must redirect the model to search_code: %v", err)
	}

	// A genuinely external name in the same registry still reaches the web rung.
	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"com.microsoft.playwright.Route.FulfillOptions"}`)
	if !strings.Contains(out, "https://docs.spring.io/never") {
		t.Fatalf("third-party miss must still fall back to the web:\n%s", out)
	}
}

func TestFqLooksRepoLocal(t *testing.T) {
	r := &Registry{RepoRoot: t.TempDir()}
	for _, d := range []string{"src", "internal"} {
		if err := os.MkdirAll(filepath.Join(r.RepoRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for fq, want := range map[string]bool{
		"src.pages.OrdersPage.fetchOrdersPreview":   true,
		"src/services/apiClient#fetchOrdersPreview": true,
		"internal.evaluator.RunEvaluation":          true,
		"com.microsoft.playwright.Route":            false,
		"@nestjs/common#Injectable":                 false,
		"OrdersPage":                                false, // bare name: no directory to check
		"":                                          false,
	} {
		if got := r.fqLooksRepoLocal(fq); got != want {
			t.Errorf("fqLooksRepoLocal(%q) = %v, want %v", fq, got, want)
		}
	}
	if (&Registry{}).fqLooksRepoLocal("src.x.Y") {
		t.Error("without a RepoRoot nothing can be repo-local")
	}
}
