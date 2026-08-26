package indexer

import "testing"

// Classification decides whether a file's symbols are eligible for gap analysis at all. A false
// positive here does not degrade quality — it silently removes code from consideration forever, and
// nothing reports it.
//
// The rule under test replaced `strings.Contains(relSlash, "Test")` over the whole path.
func TestIsLikelyTestSourcePath_classification(t *testing.T) {
	cases := []struct {
		path string
		want bool
		why  string
	}{
		// The finding, verbatim: a fixture-data directory is not test code.
		{"src/TestData/Model.cs", false, "TestData is fixture data; its symbols must stay gap-eligible"},
		{"src/TestData/Nested/Deep.cs", false, "same, nested"},
		// A file literally NAMED TestData matches the base-name rule, and that is fine: it is test
		// fixture data, not production code anyone wants tests generated for. The finding is about
		// the DIRECTORY case above, where the file is Model.cs and only the folder says "Test".
		{"src/Fixtures/TestData.json", true, "the file itself is named TestData"},

		// Substring false positives.
		{"src/main/java/com/acme/TestimonialService.java", false, "Testimonial merely starts with Test"},
		{"src/main/java/com/acme/ContestEntry.java", false, "Contest contains test"},
		{"src/main/java/com/acme/LatestOrder.java", false, "Latest contains test"},
		{"src/main/java/com/acme/Attestation.java", false, "Attestation contains test"},

		// Genuine test files, which must keep working.
		{"src/test/java/com/acme/FooTest.java", true, "conventional test dir and suffix"},
		{"src/main/java/com/acme/FooTest.java", true, "suffix alone is enough"},
		{"src/main/java/com/acme/FooTests.java", true, "plural suffix"},
		{"src/main/java/com/acme/TestUtils.java", true, "TestUtils is a test helper: word boundary at the U"},
		{"src/OrderTest.cs", true, "C# suffix"},
		{"tests/unit/thing.spec.ts", true, "tests directory"},
		{"cypress/e2e/login.cy.ts", true, "cypress e2e"},
		{"src/app/order.spec.ts", true, "spec suffix"},

		// Production code that must remain eligible.
		{"src/main/java/com/acme/OrderService.java", false, "ordinary production class"},
		{"src/app/order.service.ts", false, "ordinary production service"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := IsLikelyTestSourcePath(tc.path); got != tc.want {
				verb := "classified as a test file"
				if tc.want {
					verb = "NOT classified as a test file"
				}
				t.Errorf("%q was %s (want %v): %s", tc.path, verb, tc.want, tc.why)
			}
		})
	}
}

// The base-name rule on its own, so a regression is attributable without going through the whole
// classifier.
func TestFileBaseNamesATest_wordBoundaries(t *testing.T) {
	cases := map[string]bool{
		"FooTest.java":   true,
		"FooTests.java":  true,
		"TestUtils.java": true,
		"Test.java":      true,
		// Lowercase `_test` is Go's convention; Go is not an indexed language here and the rule is
		// deliberately case-sensitive, because lowercase "test" is what produced the Contest/Latest
		// false positives this replaced.
		"order_test.go":           false,
		"Test_Helper.cs":          true,
		"TestimonialService.java": false,
		"Contest.java":            false,
		"Latest.java":             false,
		"Model.cs":                false,
		"Attestation.cs":          false,
	}
	// Testimonial.java: "Test" followed by lowercase 'i' — not a boundary, so not a test.
	cases["Testimonial.java"] = false

	for name, want := range cases {
		if got := fileBaseNamesATest(name); got != want {
			t.Errorf("fileBaseNamesATest(%q) = %v, want %v", name, got, want)
		}
	}
}
