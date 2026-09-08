package evaluator

import "testing"

func TestPathLooksLikeTestArtifact(t *testing.T) {
	tests := []struct {
		rel  string
		lang string
		want bool
	}{
		{"apps/cms/src/api/page/content-types/page/lifecycles.ts", "typescript", false},
		{"apps/cms/src/api/page/content-types/page/__tests__/lifecycles.test.ts", "typescript", true},
		{"src/components/Button.test.tsx", "typescript", true},
		{"e2e/smoke.ts", "typescript", true},
		{"src/test/java/com/example/FooTest.java", "java", true},
		{"src/main/java/com/example/Foo.java", "java", false},
		{"tests/Unit/HandlersTests.cs", "csharp", true},
		{"tests/Unit/HandlerTest.cs", "csharp", true},
		{"src/MyApp.Tests/Support/FixtureHelper.cs", "csharp", true},
		{"src/MyApp/Program.cs", "csharp", false},
		{"tests/Clean.Architecture.FunctionalTests/E2E/AsqsPlaywrightSmokeE2E.cs", "csharp", true},
		{"Clean.Architecture.FunctionalTests/E2E/AsqsPlaywrightSmokeE2E.cs", "csharp", true},
		{"src/Services/Worker.cs", "csharp", false},
		// Compiler output and dependencies are never artifacts, whatever their names say.
		{"dist/__tests__/asqs-bootstrap-smoke.test.d.ts", "typescript", false},
		{"dist/__tests__/asqs-bootstrap-smoke.test.js", "typescript", false},
		{"dist/src/asqs-typecheck-probe.test.js", "typescript", false},
		{"packages/api/dist/x.test.js", "typescript", false},
		{"build/test-results/FooTest.java", "java", false},
		{"target/test-classes/com/example/FooTest.java", "java", false},
		{"node_modules/pkg/lib/x.test.js", "typescript", false},
		{"tests/Unit/bin/Debug/net8.0/HandlerTests.cs", "csharp", false},
		{"src/types/foo.d.ts", "typescript", false},
		// A source directory that happens to be called build/out is still source.
		{"packages/api/src/build/x.test.ts", "typescript", true},
		{"src/out/foo.spec.ts", "typescript", true},
	}
	for _, tc := range tests {
		if got := pathLooksLikeTestArtifact(tc.rel, tc.lang); got != tc.want {
			t.Errorf("pathLooksLikeTestArtifact(%q, %q) = %v; want %v", tc.rel, tc.lang, got, tc.want)
		}
	}
}
