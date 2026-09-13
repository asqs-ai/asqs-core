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
	// razorExpressionOpenRE finds the start of a C# EXPRESSION: the explicit `@( … )` form and the
	// implicit `@Html.Raw( … )` call form, with `@await` in front of it when the call is awaited.
	//
	// Its arguments are C#, and a string among them may hold markup — `@Html.Raw("<li>x</li>")` is
	// ordinary MVC and Razor Pages. Inserting an attribute there writes a double quote into the
	// middle of a C# string literal and the file stops compiling (CS1003, then CS0103 on whatever
	// the attribute name parsed as). The `@` must not follow a word character or another `@`, which
	// is Razor's own rule and what keeps an email address and an escaped `@@` out.
	razorExpressionOpenRE = regexp.MustCompile(`(^|[^\w@])@(?:await\s+)?(?:\(|[A-Za-z_][\w.]*\s*\()`)
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
// The C# regions are matched by scanning for their closing delimiter with a depth counter rather
// than by a regular expression: a `@code { ... }` body routinely contains nested braces, and a
// non-greedy match to the first `}` would leave the rest of the C# exposed to the element scan.
func razorProtectedRanges(source string) [][]int {
	var out [][]int
	out = append(out, razorCommentRE.FindAllStringIndex(source, -1)...)
	for _, m := range razorCodeBlockOpenRE.FindAllStringIndex(source, -1) {
		open := strings.IndexByte(source[m[0]:m[1]], '{')
		if open < 0 {
			continue
		}
		out = append(out, []int{m[0], closeOrEndOfFile(source, m[0]+open, '{', '}')})
	}
	for _, m := range razorExpressionOpenRE.FindAllStringIndex(source, -1) {
		open := strings.LastIndexByte(source[m[0]:m[1]], '(')
		if open < 0 {
			continue
		}
		out = append(out, []int{m[0], closeOrEndOfFile(source, m[0]+open, '(', ')')})
	}
	return out
}

// closeOrEndOfFile is matchingCloseDelim with the fallback the callers all want: an unbalanced
// region is protected to the end of the file rather than guessed at. A template that malformed is
// one nothing should be editing anyway.
func closeOrEndOfFile(s string, open int, openCh, closeCh byte) int {
	if end := matchingCloseDelim(s, open, openCh, closeCh); end >= 0 {
		return end
	}
	return len(s)
}

// matchingCloseDelim returns the index just past the delimiter that closes the one at open, or -1.
//
// It skips C# string and character literals and C# comments, which a plain depth counter does not —
// and the difference is not academic. `private string Close = "}";` inside a `@code` block returned
// the depth to zero on the brace INSIDE the literal, so the block's protection ended two lines
// early and the markup in the next literal was rewritten into something that does not compile.
func matchingCloseDelim(s string, open int, openCh, closeCh byte) int {
	depth := 0
	for i := open; i < len(s); {
		c := s[i]
		switch {
		case c == openCh:
			depth++
			i++
		case c == closeCh:
			depth--
			if depth == 0 {
				return i + 1
			}
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return -1
			}
			i += j + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return -1
			}
			i += 2 + j + 2
		case c == '"' || c == '\'':
			next := endOfCSharpLiteral(s, i)
			if next < 0 {
				return -1
			}
			i = next
		case (c == '@' || c == '$') && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '@' || s[i+1] == '$'):
			// `@"…"`, `$"…"`, `$@"…"` and `@$"…"`: the prefix characters are consumed here so the
			// quote that follows is scanned as the literal it opens.
			i++
		default:
			i++
		}
	}
	return -1
}

// endOfCSharpLiteral returns the index just past the literal opening at i, or -1 if it never closes.
//
// Verbatim and raw strings are handled because a C# codebase that holds SQL or markup holds it in
// exactly those forms — and both spell their escapes differently from the regular form.
func endOfCSharpLiteral(s string, i int) int {
	quote := s[i]
	if quote == '"' && strings.HasPrefix(s[i:], `"""`) {
		open := 0
		for i+open < len(s) && s[i+open] == '"' {
			open++
		}
		closer := strings.Repeat(`"`, open)
		j := strings.Index(s[i+open:], closer)
		if j < 0 {
			return -1
		}
		return i + open + j + open
	}
	// Verbatim: `@"a ""quoted"" b"` — a doubled quote is one quote, and a backslash is a backslash.
	verbatim := i > 0 && (s[i-1] == '@' || (i > 1 && s[i-1] == '$' && s[i-2] == '@') || (i > 1 && s[i-1] == '@' && s[i-2] == '$'))
	for j := i + 1; j < len(s); j++ {
		switch {
		case !verbatim && s[j] == '\\':
			j++ // the escaped character, whatever it is
		case s[j] == quote:
			if verbatim && j+1 < len(s) && s[j+1] == quote {
				j++ // "" is an escaped quote, not the end
				continue
			}
			return j + 1
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
