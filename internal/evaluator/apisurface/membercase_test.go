package apisurface

import "testing"

// The exact surface the generator was handed in run api-3fdd28e8f16a37247fa6494315ff6176.
func apiResponseAssertions() TypeSurface {
	return TypeSurface{
		FQCN:      "com.microsoft.playwright.assertions.APIResponseAssertions",
		Members:   []string{"public void isOK();", "public APIResponseAssertions not();"},
		Truncated: false,
	}
}

func TestRepairMemberCase_fixesTheIsOkSlip(t *testing.T) {
	src := "class T {\n\t@Test\n\tvoid vets() {\n\t\tassertThat(response).isOk();\n\t}\n}\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if want := "assertThat(response).isOK();"; !contains2(got, want) {
		t.Fatalf("isOk was not repaired:\n%s", got)
	}
	if len(repairs) != 1 || repairs[0].From != "isOk" || repairs[0].To != "isOK" || repairs[0].Count != 1 {
		t.Fatalf("unexpected repair record: %+v", repairs)
	}
	if repairs[0].FQCN != "com.microsoft.playwright.assertions.APIResponseAssertions" {
		t.Errorf("repair must name the type that settled it, got %q", repairs[0].FQCN)
	}
}

// A truncated list cannot prove a name is wrong: absence from it is not evidence.
func TestRepairMemberCase_ignoresTruncatedSurfaces(t *testing.T) {
	s := apiResponseAssertions()
	s.Truncated = true
	got, repairs := RepairMemberCase("assertThat(r).isOk();", []TypeSurface{s})
	if len(repairs) != 0 || got != "assertThat(r).isOk();" {
		t.Errorf("a truncated surface must never drive a rewrite, got %q %+v", got, repairs)
	}
}

// Two members differing only in case is ambiguity, and ambiguity is what this package avoids.
func TestRepairMemberCase_ignoresAmbiguousMatches(t *testing.T) {
	s := TypeSurface{FQCN: "a.B", Members: []string{"public void isOK();", "public void ISOK();"}}
	got, repairs := RepairMemberCase("x.isOk();", []TypeSurface{s})
	if len(repairs) != 0 || got != "x.isOk();" {
		t.Errorf("ambiguous case-match must be left alone, got %q %+v", got, repairs)
	}
}

// A helper the test file declares itself is not a classpath member.
func TestRepairMemberCase_leavesLocallyDeclaredMethods(t *testing.T) {
	src := "class T {\n\tprivate boolean isOk(Object o) { return true; }\n\tvoid t() { this.isOk(x); }\n}\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if len(repairs) != 0 || got != src {
		t.Errorf("a locally declared helper must not be rewritten, got %+v", repairs)
	}
}

// Strings and comments are text, not calls. Rewriting one would corrupt an expected value.
func TestRepairMemberCase_skipsStringsAndComments(t *testing.T) {
	src := "class T {\n\t// call .isOk() here\n\tvoid t() {\n\t\tString s = \"x.isOk()\";\n\t\tassertThat(r).isOk();\n\t}\n}\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if !contains2(got, "// call .isOk() here") {
		t.Error("comment text must be untouched")
	}
	if !contains2(got, "\"x.isOk()\"") {
		t.Error("string literal must be untouched")
	}
	if !contains2(got, "assertThat(r).isOK();") {
		t.Errorf("the real call site must still be repaired:\n%s", got)
	}
	if len(repairs) != 1 || repairs[0].Count != 1 {
		t.Errorf("only the real call site counts, got %+v", repairs)
	}
}

// A name that already matches a member exactly is right; a name matching nothing is a real
// invention and must be left for the compiler and the fixer.
func TestRepairMemberCase_leavesCorrectAndUnknownNames(t *testing.T) {
	src := "void t() {\n\tassertThat(r).isOK();\n\tassertThat(r).hasStatus(200);\n}\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if got != src || len(repairs) != 0 {
		t.Errorf("nothing should change here, got %q %+v", got, repairs)
	}
}

// Bare (statically imported) calls have no receiver to attribute to a surface.
func TestRepairMemberCase_ignoresBareCalls(t *testing.T) {
	src := "void t() { isOk(value); }\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if got != src || len(repairs) != 0 {
		t.Errorf("a bare call must not be attributed to a surface, got %q %+v", got, repairs)
	}
}

func TestRepairMemberCase_repeatedCallSitesCounted(t *testing.T) {
	src := "void t() {\n\ta.isOk();\n\tb.isOk();\n}\n"
	got, repairs := RepairMemberCase(src, []TypeSurface{apiResponseAssertions()})
	if contains2(got, "isOk()") {
		t.Errorf("both sites should be repaired:\n%s", got)
	}
	if len(repairs) != 1 || repairs[0].Count != 2 {
		t.Errorf("want one repair covering 2 sites, got %+v", repairs)
	}
}

func TestRepairMemberCase_emptyInputs(t *testing.T) {
	if got, r := RepairMemberCase("", []TypeSurface{apiResponseAssertions()}); got != "" || r != nil {
		t.Error("empty content must be a no-op")
	}
	if got, r := RepairMemberCase("a.isOk();", nil); got != "a.isOk();" || r != nil {
		t.Error("no surfaces must be a no-op")
	}
}

func contains2(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
