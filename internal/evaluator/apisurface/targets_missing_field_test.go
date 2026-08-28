package apisurface

import "testing"

// The primary diagnostic of run api-e08817ff5df431f6bb8f1fb92e7659a2, verbatim, plus the two
// secondary errors from the same file. Before javacVariableOnVariable / javacVariableOnType /
// javacMissingVariable the FIRST error yielded no target on any of five fix rounds: every pattern
// in this file keyed on `symbol: method` or `symbol: class`, and javac writes `symbol: variable`
// when the source reads a method as a property (`playwright.request`, no parentheses).
//
// The run stopped with OrderControllerE2EIT.java:33 blamed byte-for-byte in every round while
// javap on the classpath already resolved says `public abstract APIRequest request();`.
const missingFieldDiagnostic = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/com/example/javatest/api/OrderControllerE2EIT.java:[33,25] cannot find symbol
  symbol:   variable request
  location: variable playwright of type com.microsoft.playwright.Playwright
[ERROR] /workspace/src/test/java/com/example/javatest/api/OrderControllerE2EIT.java:[48,17] cannot find symbol
  symbol:   variable RequestOptions
  location: class com.example.javatest.api.OrderControllerE2EIT
[ERROR] /workspace/src/test/java/com/example/javatest/api/OrderControllerE2EIT.java:[84,38] cannot find symbol
  symbol:   method jsonBody(java.lang.Class<com.example.javatest.api.OrderResponse>)
  location: variable response of type com.microsoft.playwright.APIResponse
`

// A field read on a receiver whose type declares no such field names that type, so the member list
// can show the method the model meant.
func TestParseTargets_missingFieldOnVariableReceiver(t *testing.T) {
	got := ParseTargets(missingFieldDiagnostic)
	if len(got) == 0 {
		t.Fatal("the blamed site produced no target at all; the fixer gets nothing about the receiver")
	}
	// Targets are ordered by diagnostic position, and the primary error is the one worth the
	// first slot — the whole point of the ordering guarantee in ParseTargets.
	if got[0].Kind != KindType || got[0].Name != "com.microsoft.playwright.Playwright" {
		t.Fatalf("primary diagnostic must produce the first target, got %v", got)
	}
	if got[0].Member != "request" {
		t.Errorf("member should be the rejected name so RankMembers surfaces request() first, got %q", got[0].Member)
	}
}

// An unimported type used as an expression reads to javac as a variable in the enclosing class.
// The bare-symbol lookup is what names its real package; the enclosing-class target is repo-owned
// and is dropped by FilterOwnedTypes, exactly as for the `symbol: class` shape.
func TestParseTargets_unimportedTypeReadAsVariable(t *testing.T) {
	got := ParseTargets(missingFieldDiagnostic)
	got = FilterOwnedTypes(got, map[string]bool{"com.example.javatest.api.OrderControllerE2EIT": true})
	got = DropSymbolsCoveredByType(got)

	var found bool
	for _, tt := range got {
		if tt.Kind == KindSymbol && tt.Name == "RequestOptions" {
			found = true
		}
		if tt.Kind == KindType && tt.Name == "com.example.javatest.api.OrderControllerE2EIT" {
			t.Errorf("the repo-owned enclosing class survived filtering: %v", tt)
		}
	}
	if !found {
		t.Errorf("want a classpath search for RequestOptions to recover its package, got %v", got)
	}
}

// A lowercase unresolved name is a genuine undefined local. Searching the classpath for it burns a
// bounded target slot and renders every candidate to the model as an import suggestion, so it must
// not produce a bare-symbol target. `request` here is covered by its receiver type instead.
func TestParseTargets_lowercaseUnresolvedNameIsNotSearched(t *testing.T) {
	for _, tt := range ParseTargets(missingFieldDiagnostic) {
		if tt.Kind == KindSymbol && tt.Name == "request" {
			t.Fatalf("a lowercase identifier must not become a classpath search: %v", tt)
		}
	}
	out := `  symbol:   variable orderId
  location: class com.example.OrderServiceTest
`
	for _, tt := range ParseTargets(out) {
		if tt.Kind == KindSymbol {
			t.Errorf("undefined local produced a classpath search: %v", tt)
		}
	}
}

// A missing static import shows up as an uppercase unresolved variable, and the classpath search is
// the repair. Same diagnostic shape, opposite verdict from the lowercase case above.
func TestParseTargets_unresolvedStaticImportHolder(t *testing.T) {
	out := `[ERROR] /workspace/src/test/java/com/example/javatest/service/OrderServiceTest.java:[136,31] cannot find symbol
  symbol:   variable Mockito
  location: class com.example.javatest.service.OrderServiceTest
`
	got := ParseTargets(out)
	got = FilterOwnedTypes(got, map[string]bool{"com.example.javatest.service.OrderServiceTest": true})
	got = DropSymbolsCoveredByType(got)

	var found bool
	for _, tt := range got {
		if tt.Kind == KindSymbol && tt.Name == "Mockito" {
			found = true
		}
	}
	if !found {
		t.Errorf("want a classpath search for Mockito, got %v", got)
	}
}

// A constant that does not exist on a third-party type: the enclosing type is not repo-owned, so
// its member list survives and shows what the model should have written.
func TestParseTargets_missingConstantOnThirdPartyType(t *testing.T) {
	out := `  symbol:   variable ACCEPT_JSON
  location: class com.microsoft.playwright.options.RequestOptions
`
	got := ParseTargets(out)
	var found bool
	for _, tt := range got {
		if tt.Kind == KindType && tt.Name == "com.microsoft.playwright.options.RequestOptions" && tt.Member == "ACCEPT_JSON" {
			found = true
		}
	}
	if !found {
		t.Errorf("want the enclosing third-party type's member list, got %v", got)
	}
}
