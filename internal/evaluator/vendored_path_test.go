package evaluator

import "testing"

func TestPathIsVendored(t *testing.T) {
	vendored := []string{
		"node_modules/react-dom/cjs/react-dom.development.js",
		"/workspace/node_modules/jsdom/lib/api.js",
		"///workspace/node_modules/@vitest/spy/dist/index.js",
		"vendor/github.com/pkg/errors/errors.go",
	}
	for _, p := range vendored {
		if !pathIsVendored(p) {
			t.Errorf("pathIsVendored(%q) = false, want true", p)
		}
	}
	// Build output is derived from this repository's own sources, so a diagnostic naming it is
	// still evidence about repo code and must keep reaching the unwinnable-run guard. `packages/`
	// is a workspace root in most JS monorepos, not a vendor directory.
	own := []string{
		"src/pages/PaymentPlaygroundPage.tsx",
		"target/generated-sources/p/Foo.java",
		"obj/Debug/net8.0/Program.g.cs",
		"dist/bundle.js",
		"packages/ui/src/Button.tsx",
	}
	for _, p := range own {
		if pathIsVendored(p) {
			t.Errorf("pathIsVendored(%q) = true, want false", p)
		}
	}
}
