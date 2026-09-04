// Package uitesthooks adds deterministic `data-testid` attributes to the application's own UI
// source before test generation, so the E2E generator has real selectors to assert on.
//
// The gap it closes: run api-3cf0a2f72bb2f470d6edf4e3cd0f2c41 (2026-09-04) generated Playwright
// specs for a React fixture that carries no test hook of any kind. The selector inventory was
// correctly empty, the generator guessed a page title and a link name that matched twice, and the
// fix loop spent three rounds rewriting assertions against markup it could not address. A test id
// on the page's root, its headings, its interactive elements and its lists is what makes the spec
// deterministic — and adding one is the smallest source change ASQS makes: an attribute with no
// runtime semantics, on intrinsic elements only, never on a component whose props type could reject
// it.
//
// Safety rules, each of which exists because the alternative bit or would bite:
//   - intrinsic (lowercase) JSX elements and plain HTML tags only; a `data-testid` on `<Foo>` is a
//     type error unless Foo's props extend HTMLAttributes;
//   - never an element that already carries data-testid, data-cy or data-test, and never one with
//     a spread attribute (`{...props}`) that could carry its own;
//   - never a test, spec, story, declaration or generated file, and never a component with a
//     snapshot test beside it (the attribute would fail that snapshot in the repository's own CI);
//   - names are derived, not invented: `<file-stem>-<role>[-<text>]`, unique within a file, so a
//     second run changes nothing (idempotent) and a reviewer can predict them;
//   - bounded: at most MaxFiles files and MaxPerFile attributes per file, in document order;
//   - every write is journalled by the caller and verified by a compile step; a tree that stops
//     compiling is restored before generation sees it.
package uitesthooks

// DefaultMaxFiles bounds how many source files one run may touch. Forty covers a mid-sized SPA's
// page and layout components; a repository above that is better served by an operator raising the
// cap deliberately than by a diff nobody asked for.
const DefaultMaxFiles = 40

// DefaultMaxPerFile bounds attributes per file. Twenty-five is more than any page needs for E2E
// navigation and assertions; the limit keeps a long form or table from receiving one id per cell.
const DefaultMaxPerFile = 25

// Options gates and bounds the pass. Off by default: the pass changes files a reviewer will see in
// the pull request, so it is opted into per repository (generation.policy.ui_test_hooks.enabled).
type Options struct {
	// Enabled turns the pass on.
	Enabled bool
	// MaxFiles caps the number of source files touched; 0 means DefaultMaxFiles.
	MaxFiles int
	// MaxPerFile caps attributes added to one file; 0 means DefaultMaxPerFile.
	MaxPerFile int
	// Templates also processes Angular component templates (`*.component.html`). Off by default
	// because a template edit is not type-checked the way a TSX edit is, so the compile
	// verification catches less; the HTML inserter is correspondingly more conservative.
	Templates bool
}

// Normalized returns o with zero caps replaced by the defaults.
func (o Options) Normalized() Options {
	if o.MaxFiles <= 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	if o.MaxPerFile <= 0 {
		o.MaxPerFile = DefaultMaxPerFile
	}
	return o
}
