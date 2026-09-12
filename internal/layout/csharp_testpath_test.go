package layout

import "testing"

// Three predicates answered "is this a C# test path" and disagreed. fix_write_path.go had the full
// rule; workflow/postgenerate_write.go and orchestrator/workflow.go both used
// strings.Contains(base, "tests"), which rejects FooTest.cs — the MSTest and NUnit convention — so
// a correctly named generated test could be written by one gate and refused by the next.
func TestIsCSharpTestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// The three runner conventions for a file name.
		{"tests/Shop.Tests/BasketTests.cs", true},
		{"tests/Shop.Tests/BasketTest.cs", true},
		{"src/Shop.Core/Services/PricingServiceTests.cs", true},
		// A file called exactly Test.cs names nothing; it is far likelier to be a production type.
		{"src/Shop/Test.cs", false},
		{"src/Shop/Tests.cs", false},
		// A directory segment is enough, whatever the file is called.
		{"tests/Shop.Tests/Fixtures/Builders.cs", true},
		{"test/Support/Helpers.cs", true},
		{"Shop.UnitTests/Basket.cs", true},
		{"Shop.IntegrationTests/Api.cs", true},
		{"src/Shop.Tests/Basket.cs", true},
		// E2E trees often name neither.
		{"e2e/Shop.E2E/Checkout.cs", true},
		{"tests/E2E/SmokeE2E.cs", true},
		// Production code, including names that merely CONTAIN the word.
		{"src/Shop.Core/Services/PricingService.cs", false},
		{"src/Shop/Controllers/HomeController.cs", false},
		{"src/Shop/Contest.cs", false},
		{"src/Shop/LatestOrder.cs", false},
		{"src/Protest/Handler.cs", false},
		// Not C# at all.
		{"tests/Shop.Tests/BasketTests.fs", false},
		{"src/app/basket.spec.ts", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsCSharpTestPath(tc.path); got != tc.want {
			t.Errorf("IsCSharpTestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// Windows separators reach this from a Windows runner and from MSBuild output alike.
func TestIsCSharpTestPath_windowsSeparators(t *testing.T) {
	for _, p := range []string{`tests\Shop.Tests\BasketTests.cs`, `Shop.Tests\Basket.cs`, `e2e\Smoke.cs`} {
		if !IsCSharpTestPath(p) {
			t.Errorf("IsCSharpTestPath(%q) = false, want true", p)
		}
	}
	if IsCSharpTestPath(`src\Shop\Handler.cs`) {
		t.Errorf(`IsCSharpTestPath("src\Shop\Handler.cs") = true, want false`)
	}
}
