package uitesthooks

import (
	"regexp"
	"strings"
)

// The Razor inserter is the HTML one with three things it must not touch.
//
// Razor markup is HTML with C# woven through it, and each of the three is invisible to an HTML
// scanner. Editing inside any of them produces a file that does not compile — and unlike a JSX edit
// nothing type-checks a template, so the damage would surface as a build failure in a production
// project the run was not asked to change.

var (
	// razorCommentRE is Razor's own comment syntax, which `<!-- -->` does not cover.
	razorCommentRE = regexp.MustCompile(`(?s)@\*.*?\*@`)
	// razorCodeBlockOpenRE finds the start of a C# region: Blazor's @code, Razor's @functions, and
	// the inline `@{ ... }` block. The body is C# and may contain a string that looks like markup.
	razorCodeBlockOpenRE = regexp.MustCompile(`@(?:code|functions)\s*\{|@\{`)
	// razorHookAttrRE detects an existing hook, including a Razor-interpolated one.
	razorHookAttrRE = regexp.MustCompile(`(?i)(?:^|\s)data-(?:testid|cy|test)\s*=`)
)

// ApplyRazor adds data-testid attributes to the hookable elements of a Razor page, an MVC view or a
// Blazor component.
//
// Idempotent, like the HTML inserter: an element that already carries a hook is skipped, so a second
// run returns Changed=false. That matters more here than elsewhere — this edits a file in the
// application, not a test, and a pass that kept appending attributes would corrupt it a little more
// each run.
func ApplyRazor(source, prefix string, maxPerFile int) HTMLResult {
	protected := razorProtectedRanges(source)
	res := applyMarkupHooks(source, prefix, maxPerFile, protected)
	return res
}

// razorProtectedRanges returns the byte ranges no attribute may be inserted into.
//
// The C# blocks are matched by scanning for their closing brace with a depth counter rather than by
// a regular expression: a `@code { ... }` body routinely contains nested braces, and a
// non-greedy match to the first `}` would leave the rest of the C# exposed to the element scan.
func razorProtectedRanges(source string) [][]int {
	var out [][]int
	out = append(out, razorCommentRE.FindAllStringIndex(source, -1)...)
	for _, m := range razorCodeBlockOpenRE.FindAllStringIndex(source, -1) {
		open := strings.IndexByte(source[m[0]:m[1]], '{')
		if open < 0 {
			continue
		}
		start := m[0]
		end := matchingCloseBrace(source, m[0]+open)
		if end < 0 {
			// Unbalanced: protect to the end of the file rather than guess where it stops. A
			// template this malformed is one nothing should be editing anyway.
			end = len(source)
		}
		out = append(out, []int{start, end})
	}
	return out
}

// matchingCloseBrace returns the index just past the brace that closes the one at open, or -1.
func matchingCloseBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// RazorFileIsHookable reports whether a path is markup this inserter understands.
func RazorFileIsHookable(rel string) bool {
	low := strings.ToLower(strings.TrimSpace(rel))
	switch {
	case strings.HasSuffix(low, ".cshtml"):
		// A code-behind is C#, not markup, however much its name resembles one.
		return !strings.HasSuffix(low, ".cshtml.cs")
	case strings.HasSuffix(low, ".razor"):
		return true
	}
	return false
}
