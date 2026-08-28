package indexer

import "testing"

// The registry must agree with the confidence bands the retrieval layer used before it existed.
// Those bands were a switch in internal/intelligence/retrieval/profiles.go; if the registry scores
// a type differently, dependency ranking changes silently and no test elsewhere would notice.
func TestEdgeTypeConfidence_matchesThePreviousBands(t *testing.T) {
	// Verbatim from the switch this replaced.
	direct := []string{
		"CALLS", "INJECTS", "INJECTS_NAMED",
		"ROUTE_TO_HANDLER", "HANDLER_USES_DTO", "USES_GUARD", "USES_PIPE", "USES_INTERCEPTOR",
		"TARGETS_API_ROUTE", "CALLS_API", "USES_SELECTOR",
		"RENDERS", "USES_HOOK", "ACCEPTS_PROPS_TYPE",
		"IMPLEMENTS_SERVICE", "REGISTERS_SERVICE",
	}
	structural := []string{
		"EXTENDS", "IMPLEMENTS", "CONTAINS", "DECLARES",
		"MODULE_IMPORTS", "MODULE_EXPORTS", "MODULE_PROVIDERS", "MODULE_REGISTERS",
	}
	ambient := []string{
		"IMPORTS", "DEPENDS_ON", "DEPENDS_ON_DEV",
		"PACKAGE_MAIN", "PACKAGE_MODULE", "PACKAGE_EXPORT", "PACKAGE_ENTRY", "PACKAGE_BIN",
	}

	for _, et := range direct {
		if got := EdgeTypeConfidence(et); got != EdgeConfidenceDirect {
			t.Errorf("%s = %d, want %d (direct)", et, got, EdgeConfidenceDirect)
		}
	}
	for _, et := range structural {
		if got := EdgeTypeConfidence(et); got != EdgeConfidenceStructural {
			t.Errorf("%s = %d, want %d (structural)", et, got, EdgeConfidenceStructural)
		}
	}
	for _, et := range ambient {
		if got := EdgeTypeConfidence(et); got != EdgeConfidenceAmbient {
			t.Errorf("%s = %d, want %d (ambient)", et, got, EdgeConfidenceAmbient)
		}
	}
	// An unregistered type keeps the switch's default rather than scoring 0 — a producer that grows
	// a new edge type must degrade, not disappear.
	if got := EdgeTypeConfidence("SOMETHING_NEW"); got != EdgeConfidenceDefault {
		t.Errorf("unknown type = %d, want %d", got, EdgeConfidenceDefault)
	}
	// Case and whitespace are normalized, matching the switch's ToUpper(TrimSpace(...)).
	if EdgeTypeConfidence("  calls ") != EdgeConfidenceDirect {
		t.Error("lookup is not normalizing case and whitespace")
	}
}

// Every edge type the canonicalizer names must be in the registry: those are the ones the system
// definitely produces, so an absent one would score by default without anyone deciding it should.
func TestEdgeTypeRegistry_coversTheCanonicalizedTypes(t *testing.T) {
	for _, raw := range []string{"calls", "imports", "contains", "extends", "implements"} {
		et := CanonicalEdgeType(raw)
		if !IsRegisteredEdgeType(et) {
			t.Errorf("CanonicalEdgeType(%q) = %q is not in the registry", raw, et)
		}
	}
	// TESTS_SOURCE is materialized rather than parsed, and was easy to forget for that reason.
	if !IsRegisteredEdgeType("TESTS_SOURCE") {
		t.Error("TESTS_SOURCE is not registered")
	}
}

// UnknownEdgeTypes is what makes a producer typo visible instead of silent.
func TestUnknownEdgeTypes(t *testing.T) {
	got := UnknownEdgeTypes([]string{"CALLS", "CALSL", "imports", "  ", "CALSL", "MADE_UP"})
	want := []string{"CALSL", "MADE_UP"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
