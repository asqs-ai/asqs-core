package indexer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file indexes the markup half of a C# UI: Razor Pages, MVC views and Blazor components.
//
// The Roslyn indexer reads `.cs` and nothing else, so everything a UI test needs to know lived
// outside the index entirely — the route a page answers at, the controls a test can address, and
// the text it can assert on. A generated Razor Pages E2E test therefore had no selector to use and
// no URL to navigate to, and invented both.
//
// The Java side has done this for Thymeleaf since the html-hooks pass; this is its Razor twin, and
// it is deliberately unconditional: markup with no UI signals simply yields nothing, so there is no
// surface gate to get wrong.

var (
	// reMarkupTestID matches the attributes a test addresses a control by, most specific first.
	// data-testid is the convention ASQS's own seam writes and the one Playwright's GetByTestId
	// reads by default.
	reMarkupTestID = regexp.MustCompile(`(?i)\b(data-testid|data-test-id|data-cy|data-qa)\s*=\s*["']([^"']+)["']`)
	// reMarkupIDName matches the weaker handles: an id or a name. Both are real selectors and both
	// are what a page written before anyone thought about testing actually offers.
	reMarkupIDName = regexp.MustCompile(`(?i)\s(id|name)\s*=\s*["']([^"'@{][^"']*)["']`)
	// reRazorAspFor matches ASP.NET tag helpers. asp-for generates the id and name attributes from
	// a model expression, so `asp-for="Order.Sku"` renders id="Order_Sku" — which is the selector a
	// test needs and which appears nowhere in the source.
	reRazorAspFor = regexp.MustCompile(`(?i)\basp-for\s*=\s*["']([^"']+)["']`)
	// reRazorAspRoute matches the tag helpers that generate a link target.
	reRazorAspRoute = regexp.MustCompile(`(?i)\basp-(page|action|controller)\s*=\s*["']([^"']+)["']`)
	// reBlazorBind matches Blazor's two-way binding, which names the property a control edits.
	reBlazorBind = regexp.MustCompile(`(?i)@bind(?:-Value)?\s*=\s*["']([^"']+)["']`)
	// rePageDirective matches the @page directive that declares a route. The quoted form overrides
	// the path convention; the bare form defers to it.
	rePageDirective = regexp.MustCompile(`(?m)^\s*@page(?:\s+"([^"]*)")?\s*$`)
)

// CSharpMarkupHook is one addressable thing found in markup.
type CSharpMarkupHook struct {
	Attribute string // data-testid, id, name, asp-for, @bind
	Value     string
	Line      int
}

// MergeCSharpMarkupIntoMap indexes every Razor, MVC-view and Blazor file under repoPath into the
// parsed map, and returns how many files it added symbols to.
//
// Files already in the map are enriched rather than replaced: the Roslyn indexer owns `.cs`, and a
// Razor page's code-behind is a different file from its markup.
func MergeCSharpMarkupIntoMap(repoPath string, byPath map[string]*ParsedFile) int {
	if byPath == nil {
		return 0
	}
	enriched := 0
	for path, pf := range byPath {
		if pf == nil || !isCSharpMarkupPath(path) {
			continue
		}
		if enrichCSharpMarkupFile(path, pf) {
			enriched++
		}
	}
	return enriched
}

func isCSharpMarkupPath(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".cshtml", ".razor":
		return true
	}
	return false
}

// enrichCSharpMarkupFile adds the symbols a UI test needs from one markup file.
func enrichCSharpMarkupFile(path string, pf *ParsedFile) bool {
	if pf == nil || strings.TrimSpace(pf.Source) == "" {
		return false
	}
	hooks := CSharpMarkupHooks(pf.Source)
	routes := csharpMarkupRoutes(path, pf.Source)
	if len(hooks) == 0 && len(routes) == 0 {
		return false
	}
	pf.Lang = "csharp"
	module := csharpMarkupModuleFQ(path)
	maxLine := 1
	for _, h := range hooks {
		if h.Line > maxLine {
			maxLine = h.Line
		}
	}
	if !csharpMarkupHasModule(pf) {
		pf.Symbols = append(pf.Symbols, ParsedSymbol{Kind: "MODULE", FQName: module, StartLine: 1, EndLine: maxLine})
	}
	if pf.Module == "" {
		pf.Module = module
	}

	existing := map[string]bool{}
	for _, s := range pf.Symbols {
		existing[s.Kind+"\x00"+s.FQName] = true
	}
	add := func(kind, fq string, start, end int) {
		key := kind + "\x00" + fq
		if existing[key] {
			return
		}
		existing[key] = true
		pf.Symbols = append(pf.Symbols, ParsedSymbol{Kind: kind, FQName: fq, StartLine: start, EndLine: end})
		pf.Edges = append(pf.Edges, ParsedEdge{CallerFQName: module, CalleeFQName: fq, EdgeType: "CONTAINS"})
	}

	if len(hooks) > 0 {
		add("STATIC_TEMPLATE", "STATIC_TEMPLATE:"+path, 1, maxLine)
	}
	for _, h := range hooks {
		add("UI_TEST_HOOK", fmt.Sprintf("UI_TEST_HOOK:%s#%s=%s", path, h.Attribute, h.Value), h.Line, h.Line)
	}
	for _, r := range routes {
		add("PAGE_ROUTE", "PAGE_ROUTE:"+r.path+"@"+path, r.line, r.line)
	}
	return true
}

type csharpMarkupRoute struct {
	path string
	line int
}

// csharpMarkupRoutes returns the routes a markup file declares.
//
// `@page "/orders/{id}"` in a Razor page or a Blazor component states the route outright and
// overrides the path convention. A BARE `@page` opts the file into routing and leaves the path to
// its location under Pages/, which is the same rule the Roslyn side applies to the code-behind —
// the two dedupe on the normalised path, so whichever runs first wins and the other is a no-op.
func csharpMarkupRoutes(path, src string) []csharpMarkupRoute {
	var out []csharpMarkupRoute
	seen := map[string]bool{}
	for _, m := range rePageDirective.FindAllStringSubmatchIndex(src, -1) {
		line := 1 + strings.Count(src[:m[0]], "\n")
		explicit := ""
		if m[2] >= 0 {
			explicit = strings.TrimSpace(src[m[2]:m[3]])
		}
		var candidates []string
		if explicit != "" {
			candidates = []string{explicit}
		} else {
			candidates = csharpMarkupRoutesFromPath(path)
		}
		for _, c := range candidates {
			p := normalizeCSharpRoutePath(c)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, csharpMarkupRoute{path: p, line: line})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// csharpMarkupRoutesFromPath maps Pages/Orders/Index.cshtml to the URLs it serves. An Index page
// answers at its directory as well as at its own name, and both are URLs a test may navigate to.
func csharpMarkupRoutesFromPath(rel string) []string {
	norm := filepath.ToSlash(rel)
	idx := strings.LastIndex(strings.ToLower(norm), "/pages/")
	var under string
	switch {
	case idx >= 0:
		under = norm[idx+len("/pages/"):]
	case strings.HasPrefix(strings.ToLower(norm), "pages/"):
		under = norm[len("pages/"):]
	default:
		return nil
	}
	under = strings.TrimSuffix(under, filepath.Ext(under))
	under = strings.Trim(under, "/")
	if under == "" {
		return nil
	}
	out := []string{"/" + under}
	if strings.EqualFold(filepath.Base(under), "Index") {
		dir := strings.Trim(strings.TrimSuffix(under, filepath.Base(under)), "/")
		if dir == "" {
			out = append(out, "/")
		} else {
			out = append(out, "/"+dir)
		}
	}
	return out
}

// normalizeCSharpRoutePath matches what the Roslyn indexer emits, so the two sides dedupe: parameter
// segments become a wildcard and the path is lower-cased, because ASP.NET routing is
// case-insensitive.
func normalizeCSharpRoutePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, "*")
			continue
		}
		out = append(out, seg)
	}
	return "/" + strings.ToLower(strings.Join(out, "/"))
}

// CSharpMarkupHooks returns every addressable control in a markup file.
//
// The attributes are ordered by how reliable a selector they make, and only the FIRST match on a
// line is kept: one control yields one hook, so a button with both a data-testid and an id does not
// produce two competing answers to "how do I address this".
func CSharpMarkupHooks(src string) []CSharpMarkupHook {
	var out []CSharpMarkupHook
	seen := map[string]bool{}
	for i, line := range strings.Split(src, "\n") {
		lineNo := i + 1
		if m := reMarkupTestID.FindStringSubmatch(line); m != nil {
			appendMarkupHook(&out, seen, strings.ToLower(m[1]), m[2], lineNo)
			continue
		}
		if m := reRazorAspFor.FindStringSubmatch(line); m != nil {
			// asp-for="Order.Sku" renders id="Order_Sku": the selector a test needs appears
			// nowhere in the source, so the rendered form is what is recorded.
			appendMarkupHook(&out, seen, "asp-for", strings.ReplaceAll(m[1], ".", "_"), lineNo)
			continue
		}
		if m := reBlazorBind.FindStringSubmatch(line); m != nil {
			appendMarkupHook(&out, seen, "bind", m[1], lineNo)
			continue
		}
		if m := reMarkupIDName.FindStringSubmatch(line); m != nil {
			appendMarkupHook(&out, seen, strings.ToLower(m[1]), m[2], lineNo)
			continue
		}
		if m := reRazorAspRoute.FindStringSubmatch(line); m != nil {
			appendMarkupHook(&out, seen, "asp-"+strings.ToLower(m[1]), m[2], lineNo)
		}
	}
	return out
}

func appendMarkupHook(out *[]CSharpMarkupHook, seen map[string]bool, attr, value string, line int) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "@{") {
		// A value computed at render time is not a selector: recording `@Model.Id` would give a
		// test a string the page never contains.
		return
	}
	key := attr + "\x00" + value
	if seen[key] {
		return
	}
	seen[key] = true
	*out = append(*out, CSharpMarkupHook{Attribute: attr, Value: value, Line: line})
}

// csharpMarkupHasModule reports whether a file already carries its container symbol.
func csharpMarkupHasModule(pf *ParsedFile) bool {
	for _, s := range pf.Symbols {
		if s.Kind == "MODULE" {
			return true
		}
	}
	return false
}

// csharpMarkupModuleFQ names the container for a markup file's symbols.
func csharpMarkupModuleFQ(path string) string {
	p := strings.TrimSuffix(filepath.ToSlash(path), filepath.Ext(path))
	p = strings.Trim(p, "/")
	if p == "" {
		return "template.content"
	}
	return "template.content." + strings.ReplaceAll(p, "/", ".")
}
