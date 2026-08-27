package websearch

import (
	"strings"

	"golang.org/x/net/html"
)

// ExtractReadableText renders an HTML page as plain text with the structure that matters for
// documentation preserved: headings become "## " lines, list items get bullets, and — the part
// that carries the actual value for framework docs — <pre>/<code> blocks are fenced verbatim.
//
// Script, style, nav, header, footer, aside, iframe, form and template subtrees are dropped
// entirely. That is both a signal decision (chrome is noise) and a safety one: script bodies and
// hidden containers are where injection payloads hide in pages that LOOK like documentation.
//
// Zero-width and bidi control characters are stripped from the OUTPUT — they render invisibly to
// a human reviewing an audit trail while remaining fully legible to a model, which makes them the
// canonical carrier for hidden instructions.
func ExtractReadableText(page string) string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		// Unparseable HTML degrades to tag-stripped text rather than failing the fetch.
		return stripInvisibleRunes(page)
	}
	var b strings.Builder
	renderNode(&b, doc, false)
	out := collapseBlankLines(b.String())
	return stripInvisibleRunes(strings.TrimSpace(out))
}

var droppedElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"nav": true, "header": true, "footer": true, "aside": true,
	"iframe": true, "object": true, "embed": true, "form": true, "svg": true,
}

var headingPrefix = map[string]string{
	"h1": "# ", "h2": "## ", "h3": "### ", "h4": "#### ", "h5": "##### ", "h6": "###### ",
}

func renderNode(b *strings.Builder, n *html.Node, inPre bool) {
	switch n.Type {
	case html.TextNode:
		if inPre {
			b.WriteString(n.Data)
		} else if t := strings.TrimSpace(n.Data); t != "" {
			b.WriteString(strings.Join(strings.Fields(n.Data), " "))
			b.WriteString(" ")
		}
		return
	case html.ElementNode:
		name := strings.ToLower(n.Data)
		if droppedElements[name] {
			return
		}
		if p, ok := headingPrefix[name]; ok {
			b.WriteString("\n\n" + p)
		}
		switch name {
		case "pre":
			b.WriteString("\n\n```\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(b, c, true)
			}
			b.WriteString("\n```\n\n")
			return
		case "code":
			if !inPre {
				b.WriteString("`")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					renderNode(b, c, inPre)
				}
				b.WriteString("` ")
				return
			}
		case "li":
			b.WriteString("\n- ")
		case "p", "div", "section", "article", "tr", "table", "ul", "ol", "blockquote", "br", "hr":
			b.WriteString("\n")
		case "td", "th":
			b.WriteString(" | ")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(b, c, inPre)
	}
	if n.Type == html.ElementNode {
		if _, ok := headingPrefix[strings.ToLower(n.Data)]; ok {
			b.WriteString("\n")
		}
	}
}

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, strings.TrimRight(ln, " "))
	}
	return strings.Join(out, "\n")
}

// stripInvisibleRunes removes zero-width and bidirectional control characters.
//
// U+200B..U+200F (zero-width space/joiner/non-joiner, LRM/RLM), U+202A..U+202E (bidi embeds and
// overrides), U+2060 (word joiner), U+2066..U+2069 (bidi isolates), U+FEFF (BOM). Text carrying
// them reads one way to a human and another to a tokenizer; documentation needs none of them.
func stripInvisibleRunes(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200B && r <= 0x200F:
			return -1
		case r >= 0x202A && r <= 0x202E:
			return -1
		case r == 0x2060 || r == 0xFEFF:
			return -1
		case r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}
