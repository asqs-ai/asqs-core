package evaluator

import "testing"

// A test PROJECT holds more than tests. CustomWebApplicationFactory.cs, a fixture, a builder, a
// GlobalUsings.cs — none of them carry [Fact], and all of them are ordinary files the fixer may have
// to repair. A validation run lost its whole fix loop to the gate firing on one of them: three of
// its four failing tests came from a broken SQLite fallback in that factory, the model returned an
// edit for it on three consecutive rounds, and every round was refused as an "empty C# test file"
// until the run stopped with fixer_response_unusable.
func TestFixEmptyTestGateApplies(t *testing.T) {
	const support = "tests/App.FunctionalTests/CustomWebApplicationFactory.cs"
	const artifact = "tests/App.UnitTests/NewThingTests.cs"
	const realTest = "tests/App.UnitTests/OrderTests.cs"

	supportBody := "public class CustomWebApplicationFactory : WebApplicationFactory<Program> { }\n"
	testBody := "public class OrderTests { [Fact] public void Totals() { } }\n"

	cases := []struct {
		name   string
		rel    string
		opts   EvalOptions
		before map[string]string
		want   bool
	}{
		{
			name:   "test support that never declared a test is not judged",
			rel:    support,
			before: map[string]string{support: supportBody},
			want:   false,
		},
		{
			name:   "a file that declared tests is judged, so they cannot be removed",
			rel:    realTest,
			before: map[string]string{realTest: testBody},
			want:   true,
		},
		{
			name:   "a path this run generated is always judged",
			rel:    artifact,
			opts:   EvalOptions{ArtifactPaths: []string{artifact}},
			before: map[string]string{artifact: "public class NewThingTests { }\n"},
			want:   true,
		},
		{
			name:   "a path with no prior content is always judged",
			rel:    "tests/App.UnitTests/Invented.cs",
			before: map[string]string{},
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fixEmptyTestGateApplies(tc.rel, tc.opts, tc.before); got != tc.want {
				t.Fatalf("fixEmptyTestGateApplies(%q) = %v; want %v", tc.rel, got, tc.want)
			}
		})
	}
}
