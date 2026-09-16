package evaluator

import "testing"

// relevantDependencyPaths decides what the context clamp sheds first. Two of its three branches key
// on a NAME — the base name appearing in the error log, and the module name appearing in an
// artifact's text — and a name is only evidence when it points at one file.
//
// Run api-aace45f1f78d36a4474c06ad7d7ffae3 ran against a monorepo of three applications that each
// declare MiddlewareConfig, SeedData, InfrastructureServiceExtensions and EventDispatchInterceptor.
// Every tree's copy matched the artifact's module name, so all three were marked relevant and
// protected from the clamp: one round shed four MinimalClean and seven sample files only after the
// budget was already blown, and the fixer was reading two sibling applications' source while
// repairing the third.
func TestRelevantDependencyPaths_aSharedBaseNameProvesNothing(t *testing.T) {
	files := map[string]string{
		"tests/App.Tests/MiddlewareConfigTests.cs":                    "class MiddlewareConfigTests { MiddlewareConfig c; }",
		"src/App.Web/Configurations/MiddlewareConfig.cs":              "class MiddlewareConfig { }",
		"sample/src/Other.Web/Configurations/MiddlewareConfig.cs":     "class MiddlewareConfig { }",
		"MinimalClean/src/Min.Web/Configurations/MiddlewareConfig.cs": "class MiddlewareConfig { }",
	}
	protected := map[string]bool{
		normalizePathForFix("tests/App.Tests/MiddlewareConfigTests.cs"): true,
	}

	got := relevantDependencyPaths(files, protected, "")
	for p := range got {
		t.Errorf("%s was called relevant on a base name three files share", p)
	}
}

// The same match in a repository where the name points at ONE file is real evidence and must stand,
// or the clamp starts shedding the dependency the artifact is written against.
func TestRelevantDependencyPaths_aUniqueBaseNameStillCounts(t *testing.T) {
	files := map[string]string{
		"tests/App.Tests/OrderServiceTests.cs": "class OrderServiceTests { OrderService s; }",
		"src/App.Core/OrderService.cs":         "class OrderService { }",
		"src/App.Core/Unrelated.cs":            "class Unrelated { }",
	}
	protected := map[string]bool{
		normalizePathForFix("tests/App.Tests/OrderServiceTests.cs"): true,
	}

	got := relevantDependencyPaths(files, protected, "")
	if !got[normalizePathForFix("src/App.Core/OrderService.cs")] {
		t.Error("the one file the artifact names must stay relevant")
	}
	if got[normalizePathForFix("src/App.Core/Unrelated.cs")] {
		t.Error("a file the artifact never names must not be relevant")
	}
}

// An exact path in the error output is evidence whatever else shares its name — that is the
// compiler telling us which file it means.
func TestRelevantDependencyPaths_anExactPathSurvivesAmbiguity(t *testing.T) {
	files := map[string]string{
		"tests/App.Tests/MiddlewareConfigTests.cs":                "class MiddlewareConfigTests { }",
		"src/App.Web/Configurations/MiddlewareConfig.cs":          "class MiddlewareConfig { }",
		"sample/src/Other.Web/Configurations/MiddlewareConfig.cs": "class MiddlewareConfig { }",
	}
	protected := map[string]bool{
		normalizePathForFix("tests/App.Tests/MiddlewareConfigTests.cs"): true,
	}
	errOut := "/workspace/src/App.Web/Configurations/MiddlewareConfig.cs(12,5): error CS0246: nope"

	got := relevantDependencyPaths(files, protected, errOut)
	if !got[normalizePathForFix("src/App.Web/Configurations/MiddlewareConfig.cs")] {
		t.Error("the path the compiler named must be relevant")
	}
	if got[normalizePathForFix("sample/src/Other.Web/Configurations/MiddlewareConfig.cs")] {
		t.Error("a sibling application's same-named file was not named by the compiler")
	}
}

// Languages differ in file layout but not in this: a name shared by several files identifies none
// of them.
func TestRelevantDependencyPaths_ambiguityRuleIsLanguageNeutral(t *testing.T) {
	for _, c := range []struct {
		name     string
		files    map[string]string
		artifact string
	}{
		{
			name:     "java",
			artifact: "src/test/java/app/OwnerTest.java",
			files: map[string]string{
				"src/test/java/app/OwnerTest.java":        "class OwnerTest { Owner o; }",
				"services/a/src/main/java/app/Owner.java": "class Owner { }",
				"services/b/src/main/java/app/Owner.java": "class Owner { }",
			},
		},
		{
			name:     "typescript",
			artifact: "apps/web/src/cart.test.ts",
			files: map[string]string{
				"apps/web/src/cart.test.ts": "import { cart } from './cart';",
				"apps/web/src/cart.ts":      "export const cart = 1;",
				"apps/admin/src/cart.ts":    "export const cart = 2;",
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			protected := map[string]bool{normalizePathForFix(c.artifact): true}
			got := relevantDependencyPaths(c.files, protected, "")
			for p := range got {
				t.Errorf("%s: %s called relevant on an ambiguous name", c.name, p)
			}
		})
	}
}
