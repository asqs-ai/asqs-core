package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// javaTestItem is the minimal Java plan item these tests classify (upstream keeps this helper in a
// sibling test file this port does not carry).
func javaTestItem() *retrieval.TestPlanItem {
	return &retrieval.TestPlanItem{
		Gap: &retrieval.TestGap{
			Symbol: &metadata.Symbol{Kind: "METHOD", Lang: "java", File: "src/main/java/p/Foo.java"},
		},
	}
}

func TestIsE2ETestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Java
		{"src/test/java/p/OwnerControllerE2EIT.java", true},
		{"src/test/java/p/OwnerControllerIT.java", true},
		{"src/test/java/p/OwnerControllerTests.java", false},
		{"src/test/java/p/OwnerControllerTest.java", false},
		// C#
		{"e2e/Owners/OwnerControllerE2ETests.cs", true},
		{"tests/Owners/OwnerControllerTests.cs", false},
		// JS/TS
		{"e2e/api/owners.e2e-spec.ts", true},
		{"cypress/e2e/owners.cy.ts", true},
		{"src/owners.spec.ts", false},
		{"src/owners.test.ts", false},
		// Directory evidence alone
		{"e2e/whatever.ts", true},
		{"playwright/whatever.ts", true},
		// "integration" is deliberately NOT treated as proof of layer: plenty of repos put
		// ordinary slice tests under IntegrationTests/, and misclassifying those would send a
		// unit gap to a fresh default path instead of extending the suite that already exists.
		{"IntegrationTests/OwnerControllerTests.cs", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsE2ETestPath(tc.path); got != tc.want {
			t.Errorf("IsE2ETestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestExistingOrSuggestedTestPath_e2eNeverRedirectsIntoUnitTestFile is the regression test for the
// layer-blind redirect.
//
// RetrievalContext.ExistingTestPaths is keyed on the SOURCE file, and createTestPlanFromGaps fills
// it identically for the unit and e2e plan layers. So an API_ROUTE e2e gap on OwnerController.java
// saw the unit suite OwnerControllerTests.java and redirected into it, discarding the E2EIT path
// the e2e suggester produced. The result was @SpringBootTest-shaped end-to-end code spliced into a
// @WebMvcTest slice — a context that cannot load however correct the individual tests are.
func TestExistingOrSuggestedTestPath_e2eNeverRedirectsIntoUnitTestFile(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "src", "test", "java", "p", "OwnerControllerTests.java")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("// existing unit suite"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &retrieval.TestPlanItem{
		Layer: "e2e",
		Gap: &retrieval.TestGap{
			Symbol: &metadata.Symbol{
				Kind: "API_ROUTE",
				Lang: "java",
				File: "src/main/java/p/OwnerController.java",
			},
		},
		Context: &retrieval.RetrievalContext{
			ExistingTestPaths: []string{"src/test/java/p/OwnerControllerTests.java"},
		},
	}
	got, hit, def := ExistingOrSuggestedTestPath(item, "", "", tmp, false)
	if hit {
		t.Fatalf("e2e gap must not redirect into a unit test file; got %q", got)
	}
	if strings.Contains(filepath.ToSlash(got), "OwnerControllerTests.java") {
		t.Fatalf("e2e artifact landed in the unit suite: %q", got)
	}
	if !IsE2ETestPath(got) {
		t.Fatalf("e2e gap should fall back to its own e2e default; got %q (default %q)", got, def)
	}
}

// TestExistingOrSuggestedTestPath_e2eRedirectsIntoExistingE2EFile is the other half: when the repo
// DOES have an e2e suite for the source, extending it is still the right call. Without this the
// layer gate would be indistinguishable from disabling redirects for e2e entirely.
func TestExistingOrSuggestedTestPath_e2eRedirectsIntoExistingE2EFile(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "src", "test", "java", "p", "OwnerControllerE2EIT.java")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("// existing e2e suite"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &retrieval.TestPlanItem{
		Layer: "e2e",
		Gap: &retrieval.TestGap{
			Symbol: &metadata.Symbol{
				Kind: "API_ROUTE",
				Lang: "java",
				File: "src/main/java/p/OwnerController.java",
			},
		},
		Context: &retrieval.RetrievalContext{
			ExistingTestPaths: []string{
				"src/test/java/p/OwnerControllerTests.java", // unit — must be ignored
				"src/test/java/p/OwnerControllerE2EIT.java", // e2e — must win
			},
		},
	}
	got, hit, _ := ExistingOrSuggestedTestPath(item, "", "", tmp, false)
	if !hit {
		t.Fatalf("e2e gap should extend an existing e2e suite; got %q", got)
	}
	if filepath.ToSlash(got) != "src/test/java/p/OwnerControllerE2EIT.java" {
		t.Fatalf("got %q, want the existing E2EIT file", got)
	}
}

// TestExistingOrSuggestedTestPath_unitNeverRedirectsIntoE2EFile guards the symmetric direction.
func TestExistingOrSuggestedTestPath_unitNeverRedirectsIntoE2EFile(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "src", "test", "java", "p", "FooE2EIT.java")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("// e2e"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := javaTestItem()
	item.Context = &retrieval.RetrievalContext{
		ExistingTestPaths: []string{"src/test/java/p/FooE2EIT.java"},
	}
	got, hit, _ := ExistingOrSuggestedTestPath(item, "", "", tmp, false)
	if hit {
		t.Fatalf("unit gap must not redirect into an e2e file; got %q", got)
	}
	if IsE2ETestPath(got) {
		t.Fatalf("unit gap resolved to an e2e path: %q", got)
	}
}

func TestFilterExistingTestPathsByLayer(t *testing.T) {
	paths := []string{
		"src/test/java/p/FooTests.java",
		"src/test/java/p/FooE2EIT.java",
		"e2e/api/foo.e2e-spec.ts",
		"src/foo.spec.ts",
	}
	kept, dropped := filterExistingTestPathsByLayer(paths, true)
	if len(kept) != 2 || len(dropped) != 2 {
		t.Fatalf("e2e split: kept=%v dropped=%v", kept, dropped)
	}
	kept, dropped = filterExistingTestPathsByLayer(paths, false)
	if len(kept) != 2 || len(dropped) != 2 {
		t.Fatalf("unit split: kept=%v dropped=%v", kept, dropped)
	}
	for _, k := range kept {
		if IsE2ETestPath(k) {
			t.Errorf("unit filter kept an e2e path: %q", k)
		}
	}
}
