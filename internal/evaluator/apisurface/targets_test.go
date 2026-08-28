package apisurface

import (
	"strings"
	"testing"
)

// The head of the real error output from run api-d7e0cbece3e9260f73836f5d50d21c96, which the fixer
// failed to repair for five consecutive rounds.
const realJavacOutput = `[ERROR] COMPILATION ERROR :
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTest.java:[17,17] cannot find symbol
  symbol:   class RuntimeHints
  location: class org.springframework.samples.petclinic.PetClinicRuntimeHintsTests
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTest.java:[32,29] package RuntimeHints does not exist
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/OwnerControllerTest.java:[122,33] cannot find symbol
  symbol:   method hasURLContaining(java.lang.String)
  location: interface com.microsoft.playwright.assertions.PageAssertions
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/owner/OwnerTest.java:[119,17] reference to assertThat is ambiguous
  both method <T>assertThat(T) in org.assertj.core.api.Assertions and method <T>assertThat(T) in org.assertj.core.api.Assertions match
[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/vet/VetControllerTest.java:[78,37] cannot find symbol
  symbol:   method hasStatus(int)
  location: interface com.microsoft.playwright.assertions.APIResponseAssertions
`

// Every type the fixer needed and never got must be derivable from the diagnostic alone.
func TestParseTargets_recoversEveryTypeTheRunNeeded(t *testing.T) {
	got := ParseTargets(realJavacOutput)

	want := map[string]string{
		"com.microsoft.playwright.assertions.PageAssertions":        "hasURLContaining",
		"com.microsoft.playwright.assertions.APIResponseAssertions": "hasStatus",
		"org.assertj.core.api.Assertions":                           "assertThat",
	}
	seen := map[string]string{}
	var symbols []string
	for _, tgt := range got {
		switch tgt.Kind {
		case KindType:
			seen[tgt.Name] = tgt.Member
		case KindSymbol:
			symbols = append(symbols, tgt.Name)
		}
	}
	for fqcn, member := range want {
		if m, ok := seen[fqcn]; !ok {
			t.Errorf("missing type target %s (targets: %+v)", fqcn, got)
		} else if m != member {
			t.Errorf("%s: member = %q, want %q", fqcn, m, member)
		}
	}
	if !containsStr(symbols, "RuntimeHints") {
		t.Errorf("missing unresolved-symbol target RuntimeHints; got %v", symbols)
	}
}

// javac's `location:` for an unresolved symbol names the ENCLOSING class — the file under repair.
// Looking that up on the classpath can only miss, and it wastes a bounded target slot.
func TestFilterOwnedTypes_dropsTheFileUnderRepair(t *testing.T) {
	targets := ParseTargets(realJavacOutput)
	owned := map[string]bool{
		"org.springframework.samples.petclinic.PetClinicRuntimeHintsTests": true,
	}
	for _, tgt := range FilterOwnedTypes(targets, owned) {
		if tgt.Kind == KindType && tgt.Name == "org.springframework.samples.petclinic.PetClinicRuntimeHintsTests" {
			t.Fatal("repo-declared type survived the filter")
		}
	}
}

func TestParseTargets_boundsTargetCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("  symbol:   method m")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("(java.lang.String)\n  location: interface com.example.Type")
		b.WriteString(string(rune('A' + i%26)))
		b.WriteString("\n")
	}
	if got := len(ParseTargets(b.String())); got > maxParsedTargets {
		t.Errorf("returned %d parsed targets, want <= %d", got, maxParsedTargets)
	}
	// The lookup budget is enforced after the caller's filters, not during the parse.
	if got := len(CapTargets(ParseTargets(b.String()))); got > MaxLookupTargets {
		t.Errorf("CapTargets returned %d targets, want <= %d", got, MaxLookupTargets)
	}
}

// AssertJ declares ~150 assertThat overloads. Ranking purely by closeness to the rejected name puts
// every one of them ahead of assertThatThrownBy — the actual fix for assertThat(() -> …) — and the
// type cap then cuts before the model ever sees it. Breadth of NAMES has to survive truncation.
func TestRankMembers_keepsNameBreadthUnderTruncation(t *testing.T) {
	var members []string
	for i := 0; i < 150; i++ {
		members = append(members, "public static Assert assertThat(Type"+string(rune('A'+i%26))+" a);")
	}
	members = append(members,
		"public static AbstractThrowableAssert assertThatThrownBy(ThrowingCallable c);",
		"public static AbstractThrowableAssert assertThatCode(ThrowingCallable c);",
	)

	ranked, truncated := RankMembers(members, "assertThat")
	if !truncated {
		t.Error("expected truncation to be reported")
	}
	joined := strings.Join(ranked, "\n")
	for _, want := range []string{"assertThatThrownBy", "assertThatCode"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s was crowded out by assertThat overloads; the fix is invisible to the model:\n%s", want, joined)
		}
	}
	if got := strings.Count(joined, " assertThat("); got > maxOverloadsPerName {
		t.Errorf("assertThat overloads = %d, want <= %d", got, maxOverloadsPerName)
	}
}

// A near-miss must rank first: hasURLContaining -> hasURL is the whole repair.
func TestRankMembers_prefersNearMissOnName(t *testing.T) {
	members := []string{
		"public abstract PageAssertions not();",
		"public default void hasTitle(java.lang.String s);",
		"public default void hasURL(java.lang.String s);",
	}
	ranked, _ := RankMembers(members, "hasURLContaining")
	if len(ranked) == 0 || !strings.Contains(ranked[0], "hasURL(") {
		t.Errorf("hasURL should rank first for hasURLContaining; got %v", ranked)
	}
}

func TestParseTargets_emptyInput(t *testing.T) {
	if got := ParseTargets("   "); got != nil {
		t.Errorf("expected nil for blank input, got %v", got)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// Two live runs pull this filter in opposite directions, and both are represented here.
//
//   - `java.lang.String` resolved to 40 truncated members while the real failures were a duplicate
//     field and a missing JUnit import: noise that crowds the prompt.
//   - `new Pattern("…")` stalled three rounds — Pattern's constructor is private and the API is
//     Pattern.compile — and `java.util.regex.Pattern` is precisely the dump that answers it. The
//     first cut of this filter covered all of java.util and hid it.
//
// So the rule is "language core", not "ships with the platform".
func TestFilterUninterestingTypes(t *testing.T) {
	in := []Target{
		{Kind: KindType, Name: "java.lang.String", Member: "any"},
		{Kind: KindType, Name: "java.util.regex.Pattern", Member: "Pattern"},
		{Kind: KindType, Name: "com.microsoft.playwright.assertions.PageAssertions", Member: "hasURLContaining"},
		// Unresolved simple names must survive: resolving one to its import line is the whole point.
		{Kind: KindSymbol, Name: "AfterEach"},
	}
	var names []string
	for _, tgt := range FilterUninterestingTypes(LangJava, in) {
		names = append(names, tgt.Name)
	}
	want := []string{
		"java.util.regex.Pattern",
		"com.microsoft.playwright.assertions.PageAssertions",
		"AfterEach",
	}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
	if !IsUninterestingType("java.lang.Object") {
		t.Error("java.lang types remain filtered")
	}
	if IsUninterestingType("java.util.regex.Pattern") {
		t.Error("java.util.regex.Pattern must not be filtered; it is the answer to a private-constructor error")
	}
	if IsUninterestingType("java.time.Duration") {
		t.Error("the standard library beyond java.lang earns its place like any third-party type")
	}
}

// The single remaining compile blocker of run api-f2aeff07319e5a246c51fb6c266758a8, which stalled
// three rounds byte-identically. `RuntimeHints.Resources` does not exist — Spring's API is
// `RuntimeHints.resources()` returning ResourceHints — but reading only the symbol name searched
// the classpath for anything called `Resources` and offered four unrelated candidates, each
// rendered as an import suggestion.
const nestedTypeDiagnostic = `[ERROR] /workspace/src/test/java/org/springframework/samples/petclinic/PetClinicRuntimeHintsTests.java:[31,69] cannot find symbol
  symbol:   class Resources
  location: class org.springframework.aot.hint.RuntimeHints
`

func TestParseTargets_nestedTypeDumpsEnclosingType(t *testing.T) {
	got := ParseTargets(nestedTypeDiagnostic)

	var typeTarget *Target
	for i := range got {
		if got[i].Kind == KindType && got[i].Name == "org.springframework.aot.hint.RuntimeHints" {
			typeTarget = &got[i]
		}
	}
	if typeTarget == nil {
		t.Fatalf("no member dump requested for the enclosing type; targets = %+v", got)
	}
	if typeTarget.Member != "Resources" {
		t.Errorf("Member = %q, want Resources so the near-miss ranking can surface resources()", typeTarget.Member)
	}
}

// Once the enclosing type explains the symbol, the classpath-wide search for the bare name is not
// merely redundant — every candidate is rendered as "import this", so it hands the model several
// answers that cannot be right.
func TestDropSymbolsCoveredByType(t *testing.T) {
	targets := ParseTargets(nestedTypeDiagnostic)
	// Nothing repo-owned here, so the type target survives filtering.
	targets = FilterOwnedTypes(targets, map[string]bool{
		"org.springframework.samples.petclinic.PetClinicRuntimeHintsTests": true,
	})
	targets = DropSymbolsCoveredByType(targets)

	for _, tgt := range targets {
		if tgt.Kind == KindSymbol && tgt.Name == "Resources" {
			t.Errorf("bare-symbol search for Resources survived; it produced jakarta.annotation.Resources, "+
				"org.apache.commons.codec.Resources and two more, all as import suggestions. targets=%+v", targets)
		}
	}
	if len(targets) == 0 {
		t.Fatal("suppression removed everything; the enclosing-type dump must survive")
	}
}

// A missing IMPORT has the same two-line shape, but its location is the enclosing test class. That
// case must keep working: the type target is repo-owned and filtered out, leaving the bare-symbol
// lookup that correctly resolves RuntimeHints -> org.springframework.aot.hint.RuntimeHints.
func TestParseTargets_missingImportStillResolvesBySymbol(t *testing.T) {
	diag := `[ERROR] PetClinicRuntimeHintsTests.java:[15,17] cannot find symbol
  symbol:   class RuntimeHints
  location: class org.springframework.samples.petclinic.PetClinicRuntimeHintsTests
`
	targets := ParseTargets(diag)
	targets = FilterOwnedTypes(targets, map[string]bool{
		"org.springframework.samples.petclinic.PetClinicRuntimeHintsTests": true,
	})
	targets = DropSymbolsCoveredByType(targets)

	found := false
	for _, tgt := range targets {
		if tgt.Kind == KindSymbol && tgt.Name == "RuntimeHints" {
			found = true
		}
		if tgt.Kind == KindType && strings.Contains(tgt.Name, "petclinic") {
			t.Errorf("repo-owned enclosing class survived as a lookup target: %+v", tgt)
		}
	}
	if !found {
		t.Fatalf("missing-import lookup was lost; targets = %+v", targets)
	}
}

// Rounds 4-6 of run api-c11271e28fd0c1e10a6e5af6263108ee stalled on `new Pattern("…")`. Pattern's
// constructor is private and the API is the static Pattern.compile, but neither diagnostic shape
// carries a `symbol:`/`location:` pair, so ParseTargets matched nothing and the type was never
// looked up. Verified against the real JDK: the dump shows 0 public constructors and 2 compile()
// overloads — it answers the error twice over.
func TestParseTargets_constructorAndPrivateAccess(t *testing.T) {
	cases := map[string]string{
		"arity": "[ERROR] E2EIT.java:[105,41] constructor Pattern in class java.util.regex.Pattern cannot be applied to given types;\n" +
			"  required: java.lang.String,int\n  found:    java.lang.String\n",
		"private access": "[ERROR] E2EIT.java:[105,41] Pattern(java.lang.String,int) has private access in java.util.regex.Pattern\n",
		"private member": "[ERROR] E2EIT.java:[12,9] parent has private access in org.springframework.samples.petclinic.owner.Owner\n",
	}
	want := map[string]string{
		"arity":          "java.util.regex.Pattern",
		"private access": "java.util.regex.Pattern",
		"private member": "org.springframework.samples.petclinic.owner.Owner",
	}
	for name, diag := range cases {
		t.Run(name, func(t *testing.T) {
			got := ParseTargets(diag)
			if len(got) == 0 {
				t.Fatalf("no target parsed; the type is never looked up and the fixer must guess")
			}
			var found bool
			for _, tgt := range got {
				if tgt.Kind == KindType && tgt.Name == want[name] {
					found = true
				}
			}
			if !found {
				t.Errorf("targets = %+v, want a type dump of %s", got, want[name])
			}
		})
	}
}

// The two fixes have to compose: parsing the type is useless if the filter then drops it, which is
// exactly what the first (over-broad) version of uninterestingTypePrefixes did to java.util.
func TestParseTargets_constructorTargetSurvivesFiltering(t *testing.T) {
	diag := "[ERROR] E2EIT.java:[105,41] constructor Pattern in class java.util.regex.Pattern cannot be applied to given types;\n"
	got := FilterUninterestingTypes(LangJava, ParseTargets(diag))
	if len(got) != 1 || got[0].Name != "java.util.regex.Pattern" || got[0].Member != "Pattern" {
		t.Fatalf("got %+v, want the Pattern type dump to survive filtering", got)
	}
}
