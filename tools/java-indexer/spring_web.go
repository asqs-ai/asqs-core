package javaindexer

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

var (
	springMappingPathRe = regexp.MustCompile(`@(?:Get|Post|Put|Patch|Delete)Mapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	// Multiline: path string may appear on the next line inside parentheses.
	springGetMappingMultiRe    = regexp.MustCompile(`(?s)@GetMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	springPostMappingMultiRe   = regexp.MustCompile(`(?s)@PostMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	springPutMappingMultiRe    = regexp.MustCompile(`(?s)@PutMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	springPatchMappingMultiRe  = regexp.MustCompile(`(?s)@PatchMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	springDeleteMappingMultiRe = regexp.MustCompile(`(?s)@DeleteMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	// Class-level @RequestMapping("/prefix") before the controller class.
	springClassMappingRe = regexp.MustCompile(`@RequestMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	// Method-level @RequestMapping (path only; method= is best-effort).
	springRequestMappingPathRe = regexp.MustCompile(`@RequestMapping\s*\(\s*(?:value\s*=\s*)?["']([^"']*)["']`)
	// @RequestMapping(method = RequestMethod.POST, path = "/x") — parse attributes on full concatenated block.
	springRequestPathOrValueRe = regexp.MustCompile(`(?i)(?:path|value)\s*=\s*["']([^"']*)["']`)
	springRequestMethodEnumRe   = regexp.MustCompile(`RequestMethod\.(GET|POST|PUT|PATCH|DELETE)`)
	// Same as minimal.go javaMethodRe: return type then method name.
	javaMethodForSpringRe = regexp.MustCompile(`^\s*(public|protected|private)\s+\S+\s+(\w+)\s*\(`)
)

func looksLikeSpringWebSource(source string) bool {
	s := source
	return strings.Contains(s, "@RestController") ||
		strings.Contains(s, "@Controller") && (strings.Contains(s, "Mapping") || strings.Contains(s, "springframework.web"))
}

// controllerDeclarationLine returns true if this line starts a class/interface after optional annotations (heuristic).
// enrichSpringWebRoutes adds API_ROUTE symbols and ROUTE_TO_HANDLER / CONTAINS edges for Spring MVC
// when @RestController / @Controller and mapping annotations are present.
// Method FQ names must match minimal indexer: pkg.Class#methodName.
func enrichSpringWebRoutes(lines []string, symbols []indexer.ParsedSymbol) (extra []indexer.ParsedSymbol, edges []indexer.ParsedEdge) {
	source := strings.Join(lines, "\n")
	if !looksLikeSpringWebSource(source) {
		return nil, nil
	}

	// Map class FQName -> symbol index for line range
	type classInfo struct {
		start, end int
		fq         string
	}
	var classes []classInfo
	for _, sym := range symbols {
		if sym.Kind != "class" && sym.Kind != "interface" {
			continue
		}
		classes = append(classes, classInfo{start: sym.StartLine, end: sym.EndLine, fq: sym.FQName})
	}

	for _, ci := range classes {
		if !isSpringControllerClass(lines, ci.start) {
			continue
		}
		classPrefix := extractClassLevelRequestMapping(lines, ci.start)
		startIdx := max(0, ci.start-1)
		endIdx := min(len(lines), ci.end)
		var pendingMethod, pendingPath string

		flush := func(methodName string, lineNum int) {
			if pendingMethod == "" {
				return
			}
			fullPath := normalizeWebPath(classPrefix, pendingPath)
			handlerFq := ci.fq + "#" + methodName
			apiFq := springAPIRouteFQName(pendingMethod, fullPath, handlerFq)
			sig, _ := json.Marshal(map[string]string{
				"framework":    "spring_web",
				"http_method":  pendingMethod,
				"path_pattern": fullPath,
				"handler_fq":   handlerFq,
				"class":        ci.fq,
			})
			extra = append(extra, indexer.ParsedSymbol{
				Kind:          "API_ROUTE",
				FQName:        apiFq,
				StartLine:     lineNum,
				EndLine:       lineNum,
				SignatureJSON: sig,
			})
			edges = append(edges,
				indexer.ParsedEdge{CallerFQName: ci.fq, CalleeFQName: apiFq, EdgeType: "CONTAINS"},
				indexer.ParsedEdge{CallerFQName: apiFq, CalleeFQName: handlerFq, EdgeType: "ROUTE_TO_HANDLER"},
			)
			pendingMethod, pendingPath = "", ""
		}

		for i := startIdx; i < endIdx; {
			line := lines[i]
			lineNum := i + 1
			if springLineOpensMapping(line) {
				block, next := springConcatMappingAnnotation(lines, i)
				if m, pth := springMappingFromBlock(block); m != "" {
					pendingMethod, pendingPath = m, pth
					i = next
					continue
				}
				i++
				continue
			}
			if m := javaMethodForSpringRe.FindStringSubmatch(line); len(m) == 3 && pendingMethod != "" {
				methodName := m[2]
				// Only link methods that exist as symbols for this class
				handlerFq := ci.fq + "#" + methodName
				if methodSymbolExists(symbols, handlerFq) {
					flush(methodName, lineNum)
				} else {
					pendingMethod, pendingPath = "", ""
				}
			}
			i++
		}
	}
	return extra, edges
}

func methodSymbolExists(symbols []indexer.ParsedSymbol, handlerFq string) bool {
	for _, s := range symbols {
		if strings.EqualFold(s.Kind, "method") && s.FQName == handlerFq {
			return true
		}
	}
	return false
}

func isSpringControllerClass(lines []string, classStartLine1 int) bool {
	lo := max(0, classStartLine1-1-25)
	hi := min(len(lines), classStartLine1)
	for i := lo; i < hi; i++ {
		l := lines[i]
		if strings.Contains(l, "@RestController") {
			return true
		}
		if strings.Contains(l, "@Controller") && !strings.Contains(l, "Advice") {
			return true
		}
	}
	return false
}

func extractClassLevelRequestMapping(lines []string, classStartLine1 int) string {
	lo := max(0, classStartLine1-1-25)
	hi := min(len(lines), classStartLine1)
	var last string
	for i := lo; i < hi; i++ {
		if m := springClassMappingRe.FindStringSubmatch(lines[i]); len(m) == 2 {
			last = m[1]
		}
	}
	return last
}

func springLineOpensMapping(line string) bool {
	s := strings.TrimSpace(line)
	return strings.Contains(s, "@GetMapping") ||
		strings.Contains(s, "@PostMapping") ||
		strings.Contains(s, "@PutMapping") ||
		strings.Contains(s, "@PatchMapping") ||
		strings.Contains(s, "@DeleteMapping") ||
		strings.Contains(s, "@RequestMapping")
}

// springConcatMappingAnnotation joins lines until mapping annotation parentheses balance (max 12 lines).
func springConcatMappingAnnotation(lines []string, start int) (block string, nextLine int) {
	nextLine = start + 1
	if start < 0 || start >= len(lines) {
		return "", nextLine
	}
	var b strings.Builder
	depth := 0
	started := false
	const maxLines = 12
	for j := start; j < len(lines) && j < start+maxLines; j++ {
		line := strings.TrimSpace(lines[j])
		b.WriteString(line)
		b.WriteByte('\n')
		for _, c := range line {
			if c == '(' {
				depth++
				started = true
			}
			if c == ')' {
				depth--
			}
		}
		if started && depth <= 0 {
			nextLine = j + 1
			break
		}
	}
	return b.String(), nextLine
}

// springMappingFromBlock parses a (possibly multi-line) mapping annotation.
func springMappingFromBlock(block string) (method, path string) {
	// Multiline-safe regexes also match single-line forms; run before springMappingFromLine so
	// a first line like `@GetMapping(` does not short-circuit as GET,"".
	if m := springGetMappingMultiRe.FindStringSubmatch(block); len(m) == 2 {
		return "GET", m[1]
	}
	if m := springPostMappingMultiRe.FindStringSubmatch(block); len(m) == 2 {
		return "POST", m[1]
	}
	if m := springPutMappingMultiRe.FindStringSubmatch(block); len(m) == 2 {
		return "PUT", m[1]
	}
	if m := springPatchMappingMultiRe.FindStringSubmatch(block); len(m) == 2 {
		return "PATCH", m[1]
	}
	if m := springDeleteMappingMultiRe.FindStringSubmatch(block); len(m) == 2 {
		return "DELETE", m[1]
	}
	if strings.Contains(block, "@RequestMapping") {
		meth := "GET"
		if mm := springRequestMethodEnumRe.FindStringSubmatch(block); len(mm) == 2 {
			meth = strings.ToUpper(mm[1])
		}
		if pm := springRequestPathOrValueRe.FindStringSubmatch(block); len(pm) == 2 {
			return meth, pm[1]
		}
		if m := springRequestMappingPathRe.FindStringSubmatch(block); len(m) == 2 {
			return meth, m[1]
		}
	}
	return springMappingFromLine(block)
}

// springMappingFromLine returns (HTTP_METHOD, path) for a mapping annotation line, or ("", "").
func springMappingFromLine(line string) (method, path string) {
	line = strings.TrimSpace(line)
	switch {
	case strings.Contains(line, "@GetMapping"):
		if m := springMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			return "GET", m[1]
		}
		return "GET", ""
	case strings.Contains(line, "@PostMapping"):
		if m := springMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			return "POST", m[1]
		}
		return "POST", ""
	case strings.Contains(line, "@PutMapping"):
		if m := springMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			return "PUT", m[1]
		}
		return "PUT", ""
	case strings.Contains(line, "@PatchMapping"):
		if m := springMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			return "PATCH", m[1]
		}
		return "PATCH", ""
	case strings.Contains(line, "@DeleteMapping"):
		if m := springMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			return "DELETE", m[1]
		}
		return "DELETE", ""
	case strings.Contains(line, "@RequestMapping"):
		if m := springRequestMappingPathRe.FindStringSubmatch(line); len(m) == 2 {
			meth := "GET"
			if strings.Contains(line, "RequestMethod.POST") {
				meth = "POST"
			}
			return meth, m[1]
		}
	}
	return "", ""
}

func normalizeWebPath(prefix, suffix string) string {
	p := strings.TrimSpace(prefix)
	s := strings.TrimSpace(suffix)
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if s != "" && !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	if p == "" {
		if s == "" {
			return "/"
		}
		out := s
		for strings.Contains(out, "//") {
			out = strings.ReplaceAll(out, "//", "/")
		}
		return out
	}
	if s == "" {
		out := p
		for strings.Contains(out, "//") {
			out = strings.ReplaceAll(out, "//", "/")
		}
		return out
	}
	p = strings.TrimSuffix(p, "/")
	s = strings.TrimPrefix(s, "/")
	out := p + "/" + s
	for strings.Contains(out, "//") {
		out = strings.ReplaceAll(out, "//", "/")
	}
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

func springAPIRouteFQName(httpMethod, fullPath, handlerFq string) string {
	m := strings.ToUpper(httpMethod)
	p := normalizeWebPath("", fullPath)
	return "API_ROUTE:" + m + ":" + p + "@" + handlerFq
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
