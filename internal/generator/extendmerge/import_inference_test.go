package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownerControllerTests is the shape that failed in the run of 2026-08-29: a Spring MVC test class
// that legitimately imports org.springframework.data.domain.Page for its pagination tests.
const ownerControllerTests = `package petclinic;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.data.domain.Page;
import org.springframework.data.domain.PageImpl;
import org.springframework.data.domain.Pageable;

class OwnerControllerTests {

	@Test
	void findsPage() {
		Page<Object> p = new PageImpl<>(null);
	}

}
`

// classpathWith answers TypeExists from a fixed set: everything else is absent.
func classpathWith(present ...string) func([]string) map[string]bool {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(fqns []string) map[string]bool {
		out := make(map[string]bool, len(fqns))
		for _, f := range fqns {
			out[f] = set[f]
		}
		return out
	}
}

func writeTarget(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// 4a: a Playwright wildcard merged into a file that already binds Page from Spring Data compiles
// cleanly at the import line and then resolves every page.navigate(...) to the wrong type. The
// merge must be refused, not shipped.
func TestWrite_refusesMergeWhenWildcardIsShadowedByExistingImport(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/OwnerControllerTests.java"
	full := writeTarget(t, dir, rel, ownerControllerTests)

	payload := `import com.microsoft.playwright.*;
import org.springframework.boot.test.web.server.LocalServerPort;

	private Page page;

	@Test
	void e2e() {
		page.navigate("/owners");
	}
`
	wrote, written, skips, report := WriteWithImportReport(dir, []Item{{
		Path: rel, Content: payload, ExtendExisting: true,
		TypeExists: classpathWith("com.microsoft.playwright.Page"),
	}})

	if wrote != 0 || len(written) != 0 {
		t.Fatalf("wrote=%d written=%v; the merge must be refused", wrote, written)
	}
	if len(skips) != 1 || !strings.Contains(skips[0], "Page") {
		t.Fatalf("skips = %v; want one naming the shadowed Page", skips)
	}
	if !strings.Contains(skips[0], "org.springframework.data.domain.Page") {
		t.Errorf("the skip reason must name the import that shadows it: %q", skips[0])
	}
	if len(report) != 1 || report[0].ShadowedNames["Page"] != "org.springframework.data.domain.Page" {
		t.Fatalf("ShadowedNames = %v; want Page -> the Spring Data import", report)
	}
	// The target must be byte-identical: a refused merge writes nothing.
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ownerControllerTests {
		t.Error("a refused merge must leave the target untouched")
	}
}

// A wildcard for a package the target does not already bind is not shadowed and must still merge.
func TestWrite_allowsWildcardWithNoShadowedName(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/VetTests.java"
	body := "package petclinic;\n\nimport org.junit.jupiter.api.Test;\n\nclass VetTests {\n\n\t@Test\n\tvoid a() {}\n\n}\n"
	full := writeTarget(t, dir, rel, body)

	payload := `import java.util.*;

	@Test
	void usesList() {
		List<String> xs = new ArrayList<>();
	}
`
	wrote, _, skips, report := WriteWithImportReport(dir, []Item{{
		Path: rel, Content: payload, ExtendExisting: true,
		TypeExists: classpathWith("java.util.List", "java.util.ArrayList"),
	}})
	if wrote != 1 {
		t.Fatalf("wrote=%d skips=%v; an unshadowed wildcard must merge", wrote, skips)
	}
	if len(report) != 1 || len(report[0].ShadowedNames) != 0 {
		t.Fatalf("ShadowedNames = %v; want none", report)
	}
	got, _ := os.ReadFile(full)
	if !strings.Contains(string(got), "import java.util.*;") {
		t.Error("the wildcard should have been merged into the target")
	}
}

// 4b: annotations the payload uses but never imported are resolved against the classpath and added.
// @AfterEach / @DisplayName were two of the five such names in the run of 2026-08-29.
func TestWrite_infersImportsForAnnotationsThePayloadNeverDeclared(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/VetsTests.java"
	body := "package petclinic;\n\nimport org.junit.jupiter.api.Test;\n\nclass VetsTests {\n\n\t@Test\n\tvoid a() {}\n\n}\n"
	full := writeTarget(t, dir, rel, body)

	payload := `	@AfterEach
	void tearDown() {}

	@DisplayName("reads vets")
	@Test
	void reads() {}
`
	var asked []string
	resolver := func(names []string) map[string]string {
		asked = names
		return map[string]string{
			"AfterEach":   "org.junit.jupiter.api.AfterEach",
			"DisplayName": "org.junit.jupiter.api.DisplayName",
			// Ambiguous on this classpath; the resolver's contract is to omit it entirely.
			"Order": "",
		}
	}
	wrote, _, skips, report := WriteWithImportReport(dir, []Item{{
		Path: rel, Content: payload, ExtendExisting: true, ImportResolver: resolver,
	}})
	if wrote != 1 {
		t.Fatalf("wrote=%d skips=%v", wrote, skips)
	}
	// @Test is already bound by the target, so it must not be asked about.
	for _, n := range asked {
		if n == "Test" {
			t.Errorf("asked to resolve %q, which the target already imports", n)
		}
	}
	got, _ := os.ReadFile(full)
	for _, want := range []string{"import org.junit.jupiter.api.AfterEach;", "import org.junit.jupiter.api.DisplayName;"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("merged file is missing %q\n%s", want, got)
		}
	}
	if len(report) != 1 {
		t.Fatalf("report = %v", report)
	}
	if strings.Join(report[0].Inferred, ",") != "AfterEach,DisplayName" {
		t.Errorf("Inferred = %v; want the two resolved names, and Order omitted as unresolvable", report[0].Inferred)
	}
}

// A nil resolver must behave exactly as before: no inference, no panic.
func TestWrite_nilResolverInfersNothing(t *testing.T) {
	dir := t.TempDir()
	rel := "src/test/java/petclinic/VetsTests.java"
	body := "package petclinic;\n\nimport org.junit.jupiter.api.Test;\n\nclass VetsTests {\n\n\t@Test\n\tvoid a() {}\n\n}\n"
	full := writeTarget(t, dir, rel, body)

	wrote, _, _, _ := WriteWithImportReport(dir, []Item{{
		Path: rel, Content: "\t@AfterEach\n\tvoid tearDown() {}\n\n\t@Test\n\tvoid b() {}\n", ExtendExisting: true,
	}})
	if wrote != 1 {
		t.Fatalf("wrote=%d; a nil resolver must not block the merge", wrote)
	}
	got, _ := os.ReadFile(full)
	if strings.Contains(string(got), "AfterEach;") {
		t.Error("nothing may be inferred without a resolver")
	}
}

func TestPayloadAnnotationNames_excludesJavadocAndJavaLang(t *testing.T) {
	payload := `	/**
	 * @param x the thing
	 * @return nothing
	 */
	@Override
	@SuppressWarnings("unchecked")
	@AfterEach
	@Order(2)
	void t() {}
`
	got := strings.Join(payloadAnnotationNames(payload), ",")
	if got != "AfterEach,Order" {
		t.Errorf("payloadAnnotationNames = %q; want only the two real, non-java.lang annotations", got)
	}
}

// A wildcard in scope must NOT suppress inference. Every file broken by a missing annotation import
// in the run of 2026-08-29 also carried a Playwright wildcard, so bailing out on one skipped
// inference on exactly the cases it exists to repair. Safety comes from the resolver's
// exactly-one-candidate rule, not from avoiding wildcards.
func TestUnresolvedPayloadAnnotations_wildcardDoesNotSuppressInference(t *testing.T) {
	existing := parseImports("package p;\nimport org.junit.jupiter.api.Test;\n", ".java")
	incoming := parseImports("package p;\nimport com.microsoft.playwright.*;\n", ".java")
	got := unresolvedPayloadAnnotations("\t@AfterEach\n\t@Test\n\tvoid t() {}\n", existing, incoming)
	if strings.Join(got, ",") != "AfterEach" {
		t.Errorf("got %v; want AfterEach (unbound) and not Test (already single-type imported)", got)
	}
}

func TestUsesSimpleName_wordBoundaries(t *testing.T) {
	payload := "Pageable p; PageImpl q;"
	if usesSimpleName(payload, "Page") {
		t.Error("Page must not match Pageable or PageImpl")
	}
	if !usesSimpleName("Page page;", "Page") {
		t.Error("Page must match a real use")
	}
}

// The narrowing that the first draft of the shadow check got wrong: a payload using @Test in a file
// that imports org.junit.jupiter.api.Test, while adding an unrelated wildcard, is the NORMAL shape.
// Flagging it refused every merge that touched a wildcard.
func TestShadowDetection_ignoresNamesTheWildcardPackageDoesNotProvide(t *testing.T) {
	existing := parseImports("package p;\nimport org.junit.jupiter.api.Test;\n", ".java")
	add := parseImports("package p;\nimport java.util.*;\n", ".java")
	payload := "\t@Test\n\tvoid t() { List<String> xs = new ArrayList<>(); }\n"

	// java.util.Test does not exist, so nothing is shadowed.
	if got := shadowedByExistingSingleImport(payload, existing, add, classpathWith("java.util.List")); got != nil {
		t.Errorf("got %v; @Test beside java.util.* is not a hazard — java.util.Test does not exist", got)
	}
	// The same shape IS a hazard when the wildcard package really provides the name.
	pw := parseImports("package p;\nimport com.microsoft.playwright.*;\n", ".java")
	spring := parseImports("package p;\nimport org.springframework.data.domain.Page;\n", ".java")
	got := shadowedByExistingSingleImport("\tPage page;\n", spring, pw, classpathWith("com.microsoft.playwright.Page"))
	if got["Page"] != "org.springframework.data.domain.Page" {
		t.Errorf("got %v; want Page reported as shadowed by the Spring Data import", got)
	}
}

// Without a way to ask the classpath we cannot tell the two cases above apart, so nothing is
// refused: an unprovable hazard must not block a merge.
func TestShadowDetection_nilTypeExistsRefusesNothing(t *testing.T) {
	spring := parseImports("package p;\nimport org.springframework.data.domain.Page;\n", ".java")
	pw := parseImports("package p;\nimport com.microsoft.playwright.*;\n", ".java")
	if got := shadowedByExistingSingleImport("\tPage page;\n", spring, pw, nil); got != nil {
		t.Errorf("got %v; an unanswerable question must not refuse the merge", got)
	}
}

// A wildcard over the same package as the existing single-type import names the same type, so the
// single import winning is harmless and must not be reported.
func TestShadowDetection_samePackageWildcardIsHarmless(t *testing.T) {
	existing := parseImports("package p;\nimport org.junit.jupiter.api.Test;\n", ".java")
	add := parseImports("package p;\nimport org.junit.jupiter.api.*;\n", ".java")
	if got := shadowedByExistingSingleImport("\t@Test\n\tvoid t() {}\n", existing, add, classpathWith("org.junit.jupiter.api.Test")); got != nil {
		t.Errorf("got %v; a wildcard over the same package shadows nothing", got)
	}
}
