package retrieval

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// specMarkerCase is one language's pair: a file that really is a spec, and a file in the same test
// project that is not.
type specMarkerCase struct {
	lang      string
	spec      string // repo-relative path of the real spec
	notSpec   string // repo-relative path of the support file
	files     map[string]string
	framework string
}

func specMarkerCases() []specMarkerCase {
	return []specMarkerCase{
		{
			lang:      "csharp",
			spec:      "tests/App.Tests/WidgetSpec.cs",
			notSpec:   "tests/App.Tests/CustomWebApplicationFactory.cs",
			framework: "webapplicationfactory",
			files: map[string]string{
				"tests/App.Tests/WidgetSpec.cs": "using Xunit;\npublic class WidgetSpec { [Fact] public void A() { } }\n",
				"tests/App.Tests/CustomWebApplicationFactory.cs": "using Microsoft.AspNetCore.Mvc.Testing;\n" +
					"public class CustomWebApplicationFactory<T> : WebApplicationFactory<T> where T : class { }\n",
			},
		},
		{
			lang:      "java",
			spec:      "src/test/java/app/CheckoutIT.java",
			notSpec:   "src/test/java/app/AbstractIntegrationTest.java",
			framework: "playwright",
			files: map[string]string{
				"src/test/java/app/CheckoutIT.java": "package app;\nimport org.junit.jupiter.api.Test;\n" +
					"class CheckoutIT { @Test void placesOrder() { } }\n",
				"src/test/java/app/AbstractIntegrationTest.java": "package app;\n" +
					"abstract class AbstractIntegrationTest { protected void login() { } }\n",
			},
		},
		{
			lang:      "typescript",
			spec:      "e2e/checkout.spec.ts",
			notSpec:   "e2e/fixtures.ts",
			framework: "playwright",
			files: map[string]string{
				"e2e/checkout.spec.ts": "import { test, expect } from '@playwright/test';\n" +
					"test('places an order', async ({ page }) => { await expect(page).toBeTruthy(); });\n",
				"e2e/fixtures.ts": "export const baseURL = 'http://localhost:3000';\nexport function login() {}\n",
			},
		},
		{
			lang:      "javascript",
			spec:      "e2e/cart.spec.js",
			notSpec:   "e2e/support/commands.js",
			framework: "playwright",
			files: map[string]string{
				"e2e/cart.spec.js":        "describe('cart', () => { it('adds an item', () => {}); });\n",
				"e2e/support/commands.js": "export function addItem(page) { return page; }\n",
			},
		},
	}
}

func markerPlanOptions(root, lang, framework string) PlanOptions {
	return PlanOptions{
		Lang: lang, RepoID: "repo", RepoPath: root,
		MaxGapsE2E: 5, E2EFramework: framework,
	}
}

func specMarkerMeta(c specMarkerCase) *mockGapMetaReader {
	return &mockGapMetaReader{
		testSymbols: []*metadata.Symbol{
			{ID: "spec", Lang: c.lang, Kind: "E2E_SPEC", FQName: "E2E_SPEC:" + c.spec, File: c.spec},
			{ID: "notspec", Lang: c.lang, Kind: "E2E_SPEC", FQName: "E2E_SPEC:" + c.notSpec, File: c.notSpec},
		},
		files: map[string]*metadata.File{
			c.spec:    {File: c.spec, IsTest: true},
			c.notSpec: {File: c.notSpec, IsTest: true},
		},
	}
}

// An E2E_SPEC gap says "extend this end-to-end test". A file with no test in it is not one, and
// anchoring a gap to it invites the generator to write a test into shared support code.
//
// The C# indexer produced exactly that (every file in a test project was an E2E_SPEC), and this is
// the second line of defence: an index built before that fix, or another language's indexer being
// equally generous, must not reach the plan.
func TestListGapsE2E_skipsFilesWithNoTestInThem(t *testing.T) {
	for _, c := range specMarkerCases() {
		t.Run(c.lang, func(t *testing.T) {
			root := writeReachRepo(t, c.files)
			gaps, err := ListGapsE2E(context.Background(), specMarkerMeta(c), markerPlanOptions(root, c.lang, c.framework))
			if err != nil {
				t.Fatal(err)
			}
			files := gapFiles(gaps)
			if len(files) != 1 || files[0] != c.spec {
				t.Fatalf("want only %s planned, got %v", c.spec, files)
			}
		})
	}
}

// The gate is a negative claim about a file, so every path that cannot make it must keep the
// candidate. Filtering on ignorance silently removes work the run should have done, and nothing
// reports it — the same rule csharpReachableFilter follows.
func TestListGapsE2E_specMarkerKeepsWhatItCannotJudge(t *testing.T) {
	c := specMarkerCases()[0] // csharp

	t.Run("no repo path", func(t *testing.T) {
		opts := markerPlanOptions("", c.lang, c.framework)
		gaps, err := ListGapsE2E(context.Background(), specMarkerMeta(c), opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(gapFiles(gaps)); got != 2 {
			t.Fatalf("with no working tree to read, both candidates must stand; got %d", got)
		}
	})

	t.Run("file missing from the working tree", func(t *testing.T) {
		// Only the support file exists on disk; the spec was written this run and is not there yet.
		root := writeReachRepo(t, map[string]string{c.notSpec: c.files[c.notSpec]})
		gaps, err := ListGapsE2E(context.Background(), specMarkerMeta(c), markerPlanOptions(root, c.lang, c.framework))
		if err != nil {
			t.Fatal(err)
		}
		files := gapFiles(gaps)
		if len(files) != 1 || files[0] != c.spec {
			t.Fatalf("an unreadable file must be kept and a readable non-spec dropped; got %v", files)
		}
	})

}

// The marker sets are a whitelist, and a language absent from it must not be judged at all. The
// filter is a negative claim; with no marker set there is nothing to make it from.
func TestSpecMarkerFilter_unknownLanguageIsNeverFiltered(t *testing.T) {
	root := writeReachRepo(t, map[string]string{
		"e2e/helper.rb": "def login; end\n",
		"e2e/helper.go": "package e2e\n\nfunc Login() {}\n",
	})
	f := specMarkerFilter{repoRoot: root}
	for _, lang := range []string{"ruby", "go", "python", "", "kotlin"} {
		if !f.allows("e2e/helper.rb", lang) {
			t.Errorf("%q: a language with no marker set must be kept", lang)
		}
		if got := testMarkersForLang(lang); got != nil {
			t.Errorf("%q: want no marker set, got %v", lang, got)
		}
	}
}

// Every marker set must recognise the frameworks its ecosystem actually uses, or the filter drops
// real specs — the expensive direction to be wrong in.
func TestSpecMarkerFilter_recognisesEachFrameworksDeclaration(t *testing.T) {
	cases := []struct{ lang, src string }{
		{"csharp", "public class T { [Fact] public void A() { } }"},
		{"csharp", "public class T { [Theory] [InlineData(1)] public void A(int i) { } }"},
		{"csharp", "public class T { [Test] public void A() { } }"},             // NUnit
		{"csharp", "public class T { [TestCase(1)] public void A(int i) { } }"}, // NUnit
		{"csharp", "public class T { [TestMethod] public void A() { } }"},       // MSTest
		{"java", "class T { @Test void a() { } }"},
		{"java", "class T { @ParameterizedTest @ValueSource(ints = 1) void a(int i) { } }"},
		{"java", "class T { @RepeatedTest(3) void a() { } }"},
		{"typescript", "test('a', async ({ page }) => {});"}, // Playwright
		{"typescript", "it('a', () => {});"},                 // Jest/Vitest/Mocha
		{"typescript", "test.each([1])('a', (n) => {});"},    // Jest table
		{"typescript", "test.describe('suite', () => {});"},  // Playwright group
		{"javascript", "describe('s', () => { it('a', () => {}); });"},
	}
	for _, c := range cases {
		root := writeReachRepo(t, map[string]string{"spec": c.src})
		if !(specMarkerFilter{repoRoot: root}).allows("spec", c.lang) {
			t.Errorf("%s: failed to recognise a declared test in %q", c.lang, c.src)
		}
	}
}
