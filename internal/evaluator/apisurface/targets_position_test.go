package apisurface

import "testing"

// Run api-e2d8d10aba45c24e3dd53d2d722a4441: the primary diagnostic —
// `package org.springframework.boot.test.autoconfigure.web.servlet does not exist` on the first
// line of the log — is the one lookup that can name the relocated annotation, but assertion-call
// providers derived from LATER cascading errors used to be added first and consumed all six
// budget slots. All four fix rounds resolved the same four assertion surfaces and never looked up
// WebMvcTest.
func TestParseTargets_budgetFollowsDiagnosticOrder(t *testing.T) {
	errOut := `[ERROR] COMPILATION ERROR : 
[ERROR] /workspace/src/test/java/p/VetControllerE2EIT.java:[14,63] package org.springframework.boot.test.autoconfigure.web.servlet does not exist
[ERROR] /workspace/src/test/java/p/VetControllerE2EIT.java:[25,2] cannot find symbol
  symbol: class AutoConfigureMockMvc
[ERROR] /workspace/src/test/java/p/OwnerTests.java:[67,23] cannot find symbol
  symbol:   method setNew(boolean)
  location: variable newPet of type org.springframework.samples.petclinic.owner.Pet
[ERROR] /workspace/src/test/java/p/WelcomeControllerE2EIT.java:[66,17] cannot find symbol
  symbol:   method assertThat(int)
[ERROR] /workspace/src/test/java/p/OwnerControllerE2EIT.java:[70,9] cannot find symbol
  symbol:   method assertNotNull(java.lang.Object)
[ERROR] /workspace/src/test/java/p/PetTests.java:[80,9] cannot find symbol
  symbol:   method assertEquals(int,int)
`
	sources := map[string]string{
		"src/test/java/p/VetControllerE2EIT.java": "import org.springframework.boot.test.autoconfigure.web.servlet.WebMvcTest;\n",
	}
	got := ParseTargetsWithSources(errOut, sources)
	if len(got) == 0 {
		t.Fatal("no targets")
	}
	// The primary diagnostic's symbol must hold slot 0 — it is the only lookup that can answer
	// `package X does not exist`.
	if got[0].Kind != KindSymbol || got[0].Name != "WebMvcTest" {
		t.Fatalf("slot 0 = %+v, want the primary error's symbol WebMvcTest", got[0])
	}
	if got[1].Kind != KindSymbol || got[1].Name != "AutoConfigureMockMvc" {
		t.Fatalf("slot 1 = %+v, want AutoConfigureMockMvc (second diagnostic)", got[1])
	}
	// Assertion providers from cascading errors may fill the remaining slots but must not evict
	// the earlier diagnostics' targets.
	for i, tg := range got {
		if tg.Name == "org.assertj.core.api.Assertions" && i < 2 {
			t.Errorf("assertion provider at slot %d outranks the primary diagnostics", i)
		}
	}
}

// Several targets from one diagnostic (the two providers of a receiverless assertThat) keep their
// table order, and a later repeat of a name cannot demote its first occurrence.
func TestParseTargets_tieAndDuplicateOrder(t *testing.T) {
	errOut := `[ERROR] /workspace/src/test/java/p/T.java:[10,9] cannot find symbol
  symbol:   method assertThat(int)
[ERROR] /workspace/src/test/java/p/T.java:[40,9] cannot find symbol
  symbol: class WebMvcTest
[ERROR] /workspace/src/test/java/p/T.java:[50,9] cannot find symbol
  symbol:   method assertThat(long)
`
	got := ParseTargetsWithSources(errOut, nil)
	if len(got) < 3 {
		t.Fatalf("targets = %+v", got)
	}
	if got[0].Name != "org.assertj.core.api.Assertions" || got[1].Name != "org.hamcrest.MatcherAssert" {
		t.Errorf("tie order not preserved: %+v", got[:2])
	}
	if got[2].Kind != KindSymbol || got[2].Name != "WebMvcTest" {
		t.Errorf("slot 2 = %+v, want WebMvcTest ahead of the repeated assertThat", got[2])
	}
}
