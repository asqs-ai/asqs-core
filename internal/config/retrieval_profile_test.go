package config

import (
	"strings"
	"testing"
)

func TestNormalizeRetrievalProfileName_aliases(t *testing.T) {
	cases := map[string]string{
		"":               RetrievalProfileJavaUnit, // documented default
		"java":           RetrievalProfileJavaUnit,
		"java_unit":      RetrievalProfileJavaUnit,
		"  JAVA-UNIT  ":  RetrievalProfileJavaUnit,
		"unit":           RetrievalProfileJavaUnit,
		"http_api":       RetrievalProfileHTTPAPI,
		"http-api":       RetrievalProfileHTTPAPI,
		"spring":         RetrievalProfileHTTPAPI,
		"e2e":            RetrievalProfileE2EPlaywright,
		"e2e-playwright": RetrievalProfileE2EPlaywright,
		"react":          RetrievalProfileReactFeature,
		"nest":           RetrievalProfileNestModule,
		"full-stack":     RetrievalProfileFullStack,
		"react_http_api": RetrievalProfileFullStack,
	}
	for in, want := range cases {
		got, err := NormalizeRetrievalProfileName(in)
		if err != nil {
			t.Errorf("NormalizeRetrievalProfileName(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeRetrievalProfileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeRetrievalProfileName_failsClosed is the regression test for M-5. Falling back to
// java_unit — outgoing edges only, `test` chunks only — is the largest quality regression reachable
// through a config typo, and it used to be delivered silently.
func TestNormalizeRetrievalProfileName_failsClosed(t *testing.T) {
	bad := []string{"http api", "httpapi", "e2e_playwrite", "javaunit", "nest-modules", "fullstack ui", "unknown"}
	for _, in := range bad {
		got, err := NormalizeRetrievalProfileName(in)
		if err == nil {
			t.Errorf("NormalizeRetrievalProfileName(%q) = %q with no error; a typo must not silently select java_unit", in, got)
			continue
		}
		// The message must be actionable: it has to list what the operator can actually write.
		for _, want := range ValidRetrievalProfiles {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error for %q does not mention the valid profile %q: %v", in, want, err)
				break
			}
		}
	}
}

func TestValidate_rejectsUnknownRetrievalProfile(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.Database.MetadataURL = "postgres://localhost/asqs"
		return c
	}

	c := base()
	c.Retrieval.Profile = "http-api" // valid alias
	if err := Validate(c, "audit"); err != nil {
		t.Fatalf("a valid alias must pass validation: %v", err)
	}

	c = base()
	c.Retrieval.Profile = "http api" // typo
	err := Validate(c, "audit")
	if err == nil {
		t.Fatal("Validate accepted an unknown retrieval.profile; it must fail at startup instead of degrading every run")
	}
	if !strings.Contains(err.Error(), "retrieval.profile") {
		t.Errorf("error should name the offending key: %v", err)
	}

	c = base()
	c.Retrieval.ProfileE2E = "playwrite"
	if err := Validate(c, "audit"); err == nil {
		t.Fatal("Validate accepted an unknown retrieval.profile_e2e")
	}

	c = base()
	c.Retrieval.ProfileBudgets = map[string]RetrievalProfileBudget{"htp_api": {}}
	if err := Validate(c, "audit"); err == nil {
		t.Fatal("Validate accepted an unknown key in retrieval.profile_budgets")
	}
}

// Validation must run in every mode: a typo'd profile degrades every run regardless of which
// command is executing, so gating it behind full mode would hide it from audit and e2e paths.
func TestValidate_profileCheckRunsInAllModes(t *testing.T) {
	for _, mode := range []string{"", "full", "audit", "e2e"} {
		c := &Config{}
		c.Database.MetadataURL = "postgres://localhost/asqs"
		c.Retrieval.Profile = "not_a_profile"
		if err := Validate(c, mode); err == nil {
			t.Errorf("mode %q: Validate accepted an unknown retrieval.profile", mode)
		}
	}
}

// "auto" is schema v2's explicit spelling of the retrieval default. v1 expressed it as an empty
// string, which reads like an oversight rather than a decision; NormalizeRetrievalProfileName still
// rejects the literal word, so the translate layer resolves the sentinel before validation ever sees
// it. This pins both halves — the sentinel resolves, and an unrecognised name still fails closed.
// The v2-schema profile-alias tests return with the config-restructure bundle (CP38),
// which brings SchemaV2 and TranslateV2ToRuntime.
