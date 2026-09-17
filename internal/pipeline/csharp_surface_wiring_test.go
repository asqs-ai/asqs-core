package pipeline

import (
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

// The detection, the hints and the anchor code were all ported and none of them could be reached: a
// run built its PlanOptions without a surface and resolved the E2E profile with the two-argument,
// non-surface-aware function, so every C# repository still got http_api and browser hints.
func TestBuildPlanOptions_carriesTheE2ESurface(t *testing.T) {
	cfg := &config.Config{}

	if got := buildPlanOptions(cfg, "csharp", "repo-1", "ui"); got.E2ESurface != "ui" {
		t.Fatalf("PlanOptions.E2ESurface = %q, want ui", got.E2ESurface)
	}
	if got := buildPlanOptions(cfg, "csharp", "repo-1", "ui").RetrievalProfileE2E; got != "full_stack" {
		t.Fatalf("RetrievalProfileE2E = %q, want full_stack for a ui surface", got)
	}
	if got := buildPlanOptions(cfg, "csharp", "repo-1", "api").RetrievalProfileE2E; got != "http_api" {
		t.Fatalf("RetrievalProfileE2E = %q, want http_api for an api surface", got)
	}
	if got := buildPlanOptions(cfg, "csharp", "repo-1", "").RetrievalProfileE2E; got != "http_api" {
		t.Fatalf("RetrievalProfileE2E = %q, want http_api when no surface was detected", got)
	}
}

func TestDefaultRetrievalProfileE2E_bySurface(t *testing.T) {
	cfg := &config.Config{}
	cases := []struct{ lang, surface, want string }{
		{"csharp", "ui", "full_stack"},
		{"csharp", "mixed", "full_stack"},
		{"csharp", "api", "http_api"},
		{"cs", "ui", "full_stack"},
		{"java", "ui", "http_api"},
		{"typescript", "api", "e2e_playwright"},
	}
	for _, tc := range cases {
		if got := defaultRetrievalProfileE2E(cfg, tc.lang, tc.surface); got != tc.want {
			t.Errorf("defaultRetrievalProfileE2E(%q, %q) = %q, want %q", tc.lang, tc.surface, got, tc.want)
		}
	}
	cfg.Retrieval.ProfileE2E = "e2e_playwright"
	if got := defaultRetrievalProfileE2E(cfg, "csharp", "ui"); got != "e2e_playwright" {
		t.Errorf("an explicit profile_e2e lost to the surface: %q", got)
	}
}
