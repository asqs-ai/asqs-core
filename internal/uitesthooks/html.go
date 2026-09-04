package uitesthooks

import (
	"regexp"
	"strings"
)

// The HTML inserter is deliberately more conservative than the JSX one: a template is not
// type-checked, so nothing downstream would catch a bad edit. It handles Angular component
// templates and only the shapes below.

var (
	// htmlOpenTagRE matches an opening tag of a hookable element and captures name and attribute
	// text. Self-closing (`/>`) forms are included. Angular control-flow blocks and interpolations
	// never start with `<name`, so they are not matched.
	htmlOpenTagRE = regexp.MustCompile(`(?is)<(button|a|input|select|textarea|form|nav|main|section|header|footer|aside|h[1-6]|ul|ol|li|table|tr|img)(\s[^<>]*?)?(/?)>`)
	// htmlCommentRE marks ranges the inserter must not edit.
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	// htmlHookAttrRE detects an existing hook, including Angular's binding spellings.
	htmlHookAttrRE = regexp.MustCompile(`(?i)(?:^|\s)(?:\[attr\.)?data-(?:testid|cy|test)\]?\s*=`)
	// htmlAttrRE extracts a literal attribute value for the name text.
	htmlAttrRE = regexp.MustCompile(`(?is)\s(aria-label|placeholder|name|href|type)\s*=\s*"([^"{}]*)"`)
	// htmlLiteralTextRE captures direct text after the opening tag up to the next tag, used for
	// buttons, links and headings; text with an interpolation is not literal.
	htmlLiteralTextRE = regexp.MustCompile(`(?s)^([^<{]*)<`)
)

var htmlRoleByTag = map[string]string{
	"button": "button", "a": "link", "input": "input", "select": "select", "textarea": "textarea",
	"form": "form", "nav": "nav", "main": "main", "section": "section", "header": "header",
	"footer": "footer", "aside": "aside", "h1": "heading", "h2": "heading", "h3": "heading",
	"h4": "heading", "h5": "heading", "h6": "heading", "ul": "list", "ol": "list", "li": "item",
	"table": "table", "tr": "row", "img": "image",
}

// HTMLResult is the outcome of ApplyHTML for one template.
type HTMLResult struct {
	Source  string
	Changed bool
	Added   []Hook
	Skipped int
}

// ApplyHTML adds data-testid attributes to an Angular component template's hookable elements.
// The first hookable element in the file is the root. Idempotent: elements that already carry a
// hook are skipped, so a second run returns Changed=false.
func ApplyHTML(source, prefix string, maxPerFile int) HTMLResult {
	if maxPerFile <= 0 {
		maxPerFile = DefaultMaxPerFile
	}
	comments := htmlCommentRE.FindAllStringIndex(source, -1)
	inComment := func(pos int) bool {
		for _, c := range comments {
			if pos >= c[0] && pos < c[1] {
				return true
			}
		}
		return false
	}
	names := nameSet{}
	res := HTMLResult{Source: source}
	type insert struct {
		pos  int
		text string
	}
	var inserts []insert
	first := true
	for _, m := range htmlOpenTagRE.FindAllStringSubmatchIndex(source, -1) {
		if len(res.Added) >= maxPerFile {
			break
		}
		if inComment(m[0]) {
			continue
		}
		tag := strings.ToLower(source[m[2]:m[3]])
		attrs := ""
		if m[4] >= 0 {
			attrs = source[m[4]:m[5]]
		}
		role := htmlRoleByTag[tag]
		if first {
			role = "root"
			first = false
		}
		if role == "" {
			continue
		}
		if htmlHookAttrRE.MatchString(attrs) {
			res.Skipped++
			continue
		}
		text := ""
		if role != "root" {
			if am := htmlLiteralTextRE.FindStringSubmatch(source[m[1]:]); am != nil {
				text = strings.TrimSpace(am[1])
			}
			if text == "" {
				if am := htmlAttrRE.FindStringSubmatch(" " + attrs); am != nil {
					text = am[2]
				}
			}
		}
		name := names.take(HookName(prefix, role, Slug(text)))
		line := 1 + strings.Count(source[:m[0]], "\n")
		res.Added = append(res.Added, Hook{Name: name, Line: line, Element: tag})
		inserts = append(inserts, insert{pos: m[3], text: ` data-testid="` + name + `"`})
	}
	if len(inserts) == 0 {
		return res
	}
	var b strings.Builder
	last := 0
	for _, ins := range inserts {
		b.WriteString(source[last:ins.pos])
		b.WriteString(ins.text)
		last = ins.pos
	}
	b.WriteString(source[last:])
	res.Source = b.String()
	res.Changed = true
	return res
}
