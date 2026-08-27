package apisurface

import "testing"

// Run api-5e5535208f4ba61613f60c345ba9b567 spent its last four fixer rounds on this exact
// diagnostic. Every round resolved PlaywrightAssertions — the class javac had just finished ruling
// out — and rendered its complete member list under "these are the ONLY members that exist". The
// type that answers the error was never looked up.
const noSuitableMethodOutput = `[ERROR] COMPILATION ERROR : 
[ERROR] /workspace/src/test/java/p/WelcomeControllerE2EIT.java:[63,17] no suitable method found for assertThat(int)
    method com.microsoft.playwright.assertions.PlaywrightAssertions.assertThat(com.microsoft.playwright.Page) is not applicable
      (argument mismatch; int cannot be converted to com.microsoft.playwright.Page)
    method com.microsoft.playwright.assertions.PlaywrightAssertions.assertThat(com.microsoft.playwright.Locator) is not applicable
      (argument mismatch; int cannot be converted to com.microsoft.playwright.Locator)
`

func TestParseTargets_noSuitableMethodOffersAProviderThatCanWork(t *testing.T) {
	got := ParseTargets(noSuitableMethodOutput)
	if len(got) == 0 {
		t.Fatal("no targets parsed")
	}
	idx := func(name string) int {
		for i, tgt := range got {
			if tgt.Name == name {
				return i
			}
		}
		return -1
	}
	assertJ := idx("org.assertj.core.api.Assertions")
	if assertJ < 0 {
		t.Fatalf("AssertJ was not offered; the only member list the fixer gets is the rejected one: %+v", got)
	}
	if got[assertJ].Member != "assertThat" {
		t.Errorf("Member = %q, want assertThat so RankMembers has something to rank against", got[assertJ].Member)
	}
	// The rejected candidate stays — a wrong overload on the RIGHT class is the other half of this
	// diagnostic shape — but it must not outrank the type that can take the argument, because
	// CapTargets truncates from the end.
	rejected := idx("com.microsoft.playwright.assertions.PlaywrightAssertions")
	if rejected >= 0 && rejected < assertJ {
		t.Errorf("rejected candidate ranked ahead of the provider: %+v", got)
	}
}

// A wrong overload on the right class must be unaffected: no provider is invented for a call the
// table does not name, and the type javac blamed is still looked up.
func TestParseTargets_notApplicableWithoutNoSuitableMethodHeaderIsUnchanged(t *testing.T) {
	out := `[ERROR] /workspace/src/test/java/p/T.java:[10,5] method a.b.C.doThing(java.lang.String) is not applicable`
	got := ParseTargets(out)
	if len(got) != 1 || got[0].Name != "a.b.C" || got[0].Member != "doThing" {
		t.Fatalf("got %+v, want the blamed type only", got)
	}
}

func TestStaticCallProviders_onlyKnownEntryPoints(t *testing.T) {
	if got := staticCallProviders("processFindForm"); got != nil {
		t.Errorf("staticCallProviders(app method) = %v, want nil", got)
	}
	if got := staticCallProviders("assertEquals"); len(got) != 1 || got[0] != "org.junit.jupiter.api.Assertions" {
		t.Errorf("staticCallProviders(assertEquals) = %v", got)
	}
}
