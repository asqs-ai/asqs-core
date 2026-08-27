package apisurface

import "testing"

func playwrightSurfaces() []TypeSurface {
	return []TypeSurface{
		NewTypeSurface("com.microsoft.playwright.assertions.APIResponseAssertions", []string{
			"public abstract void isOK();",
			"public abstract com.microsoft.playwright.assertions.APIResponseAssertions not();",
		}, "", "playwright.jar"),
		NewTypeSurface("com.microsoft.playwright.assertions.PageAssertions", []string{
			"public abstract void hasTitle(java.lang.String);",
			"public abstract void hasURL(java.lang.String);",
		}, "", "playwright.jar"),
		NewTypeSurface("com.microsoft.playwright.assertions.LocatorAssertions", []string{
			"public abstract void isVisible();",
			"public abstract void hasText(java.lang.String);",
		}, "", "playwright.jar"),
		// The static factory and an annotation, as the real prompt carries them.
		NewTypeSurface("com.microsoft.playwright.assertions.PlaywrightAssertions", []string{
			"public static com.microsoft.playwright.assertions.PageAssertions assertThat(com.microsoft.playwright.Page);",
		}, "", "playwright.jar"),
		{FQCN: "org.springframework.boot.test.context.SpringBootTest"},
	}
}

// Run api-5e5535208f4ba61613f60c345ba9b567 wrote both of these against a complete two-member list
// that was sitting in its own prompt, and each cost a containerised compile plus a share of eleven
// repair rounds.
func TestInventedAssertionMemberReason_catchesMembersThatDoNotExist(t *testing.T) {
	for _, src := range []string{
		"class T { void a() { assertThat(response).hasStatus(200); } }",
		"class T { void a() { assertThat(response).hasHeader(\"Content-Type\", \"text/html\"); } }",
		"class T { void a() { PlaywrightAssertions.assertThat(response).hasStatus(200); } }",
		"class T { void a() { assertThat(response)\n\t\t.not()\n\t\t.hasStatus(200); } }",
	} {
		if got := InventedAssertionMemberReason(src, playwrightSurfaces()); got == "" {
			t.Errorf("no violation reported for %q", src)
		}
	}
}

func TestInventedAssertionMemberReason_leavesValidChainsAlone(t *testing.T) {
	for _, src := range []string{
		"class T { void a() { assertThat(page).hasTitle(\"Welcome\"); } }",
		"class T { void a() { assertThat(response).not().isOK(); } }",
		"class T { void a() { assertThat(locator)\n\t\t.isVisible(); } }",
		// A local helper of the same name is the file's own, not a classpath member.
		"class T {\n\tvoid hasStatus(int code) {\n\t}\n\tvoid a() {\n\t\tassertThat(response).hasStatus(200);\n\t}\n}",
		// Not an assertion chain at all.
		"class T { void a() { owner.getPetsInternal().add(pet); } }",
		// Text, not code.
		"class T { /* assertThat(response).hasStatus(200); */ void a() {} }",
		"class T { void a() { String s = \"assertThat(response).hasStatus(200)\"; } }",
	} {
		if got := InventedAssertionMemberReason(src, playwrightSurfaces()); got != "" {
			t.Errorf("false rejection %q for %q", got, src)
		}
	}
}

// Absence from a cut list is not proof of absence, and a file that can reach AssertJ's fluent
// asserts has members this check never resolved. Both must degrade to a no-op rather than to a
// false rejection — the direction that would destroy a correct artifact.
func TestInventedAssertionMemberReason_skipsWhatItCannotProve(t *testing.T) {
	src := "class T { void a() { assertThat(response).hasStatus(200); } }"
	withAssertJ := "import static org.assertj.core.api.Assertions.assertThat;\n" + src
	if got := InventedAssertionMemberReason(withAssertJ, playwrightSurfaces()); got != "" {
		t.Errorf("AssertJ import produced %q, want no claim", got)
	}
	// One assertion type missing means a member it declares would read as invented.
	partial := playwrightSurfaces()[:2]
	if got := InventedAssertionMemberReason(src, partial); got != "" {
		t.Errorf("partial resolution produced %q, want no claim", got)
	}
	if got := InventedAssertionMemberReason(src, nil); got != "" {
		t.Errorf("no surfaces produced %q", got)
	}
}

// Truncation is a PROMPT budget, not a resolution failure, and must not silence the check.
//
// Gating on TypeSurface.Truncated made this unreachable in practice: run
// api-f34f51a6e1fb10a79f2f57314aae3d23 rendered LocatorAssertions as `40 member(s),
// truncated=true` and PageAssertions as `7 member(s), truncated=true` on every Java Playwright
// gap, so hasStatus and hasHeader shipped to the compiler with the answer sitting in the prompt.
func TestInventedAssertionMemberReason_answersFromTheCompleteListNotTheRenderedOne(t *testing.T) {
	// Enough overloads of one name that RankMembers cuts the rendered view.
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, "public abstract void isVisible(int arg"+string(rune('a'+i%26))+");")
	}
	many = append(many, "public abstract void hasText(java.lang.String);")

	surfaces := playwrightSurfaces()
	for i := range surfaces {
		if surfaces[i].FQCN == "com.microsoft.playwright.assertions.LocatorAssertions" {
			surfaces[i] = NewTypeSurface(surfaces[i].FQCN, many, "", "playwright.jar")
		}
	}
	var locator TypeSurface
	for _, s := range surfaces {
		if s.FQCN == "com.microsoft.playwright.assertions.LocatorAssertions" {
			locator = s
		}
	}
	if !locator.Truncated {
		t.Fatal("fixture must produce a truncated rendered view; otherwise this test proves nothing")
	}
	if !locator.DeclaresMember("hasText") {
		t.Fatal("the complete list must still carry a member the rendered view dropped")
	}
	if got := InventedAssertionMemberReason("class T { void a() { assertThat(response).hasStatus(200); } }", surfaces); got == "" {
		t.Error("a truncated rendered view must not silence the check")
	}
	if got := InventedAssertionMemberReason("class T { void a() { assertThat(locator).hasText(\"x\"); } }", surfaces); got != "" {
		t.Errorf("false rejection %q: hasText is in the complete list, just not the rendered one", got)
	}
}
