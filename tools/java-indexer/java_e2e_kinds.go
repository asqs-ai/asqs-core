package javaindexer

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

// enrichJavaPageObjectAndUserFlow adds PAGE_OBJECT (@FindBy) and USER_FLOW (RestAssured-style) symbols for chunk parity.
func enrichJavaPageObjectAndUserFlow(
	lines []string,
	path string,
	pkg string,
	symbols []indexer.ParsedSymbol,
) (extra []indexer.ParsedSymbol, edges []indexer.ParsedEdge) {
	src := strings.Join(lines, "\n")
	if path == "" {
		return nil, nil
	}

	seenPO := make(map[string]struct{})
	if strings.Contains(src, "@FindBy") {
		for i, line := range lines {
			if !strings.Contains(line, "@FindBy") {
				continue
			}
			lineNum := i + 1
			classFq := classFQNameForLine(symbols, lineNum)
			if classFq == "" {
				continue
			}
			if _, ok := seenPO[classFq]; ok {
				continue
			}
			seenPO[classFq] = struct{}{}
			poFq := "PAGE_OBJECT:" + classFq + "@" + path
			sig, _ := json.Marshal(map[string]string{
				"framework": "selenium_page",
				"class_fq":  classFq,
			})
			extra = append(extra, indexer.ParsedSymbol{
				Kind:          "PAGE_OBJECT",
				FQName:        poFq,
				StartLine:     lineNum,
				EndLine:       lineNum,
				SignatureJSON: sig,
			})
			edges = append(edges, indexer.ParsedEdge{
				CallerFQName: classFq,
				CalleeFQName: poFq,
				EdgeType:     "CONTAINS",
			})
		}
	}

	if strings.Contains(src, "io.restassured") || strings.Contains(src, "RestAssured") {
		if strings.Contains(src, "given(") || strings.Contains(src, ".given()") {
			for _, sym := range symbols {
				if sym.Kind != "class" {
					continue
				}
				if sym.StartLine > sym.EndLine {
					continue
				}
				ufFq := "USER_FLOW:" + sym.FQName + "@" + path
				sig, _ := json.Marshal(map[string]string{
					"framework": "rest_assured",
					"class_fq":  sym.FQName,
				})
				extra = append(extra, indexer.ParsedSymbol{
					Kind:          "USER_FLOW",
					FQName:        ufFq,
					StartLine:     sym.StartLine,
					EndLine:       sym.EndLine,
					SignatureJSON: sig,
				})
				edges = append(edges, indexer.ParsedEdge{
					CallerFQName: sym.FQName,
					CalleeFQName: ufFq,
					EdgeType:     "CONTAINS",
				})
				break
			}
		}
	}

	return extra, edges
}

// isLikelyJavaE2EPath mirrors JavaIndexer.isLikelyJavaE2EPath (advanced JAR) so minimal indexing
// still emits E2E_SPEC for bootstrap paths like src/test/java/.../e2e/...Playwright....java.
func isLikelyJavaE2EPath(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	p = strings.ToLower(p)
	if strings.Contains(p, "/e2e/") {
		return true
	}
	if strings.Contains(p, "playwright") {
		return true
	}
	if javaE2eFileSuffix.MatchString(p) {
		return true
	}
	if strings.Contains(p, "/it/") && strings.Contains(p, "/test/") {
		return true
	}
	return false
}

func detectJavaE2EFrameworkImports(src string) string {
	if strings.Contains(src, "com.microsoft.playwright") {
		return "playwright_java"
	}
	if strings.Contains(src, "org.openqa.selenium") {
		return "selenium_java"
	}
	if strings.Contains(src, "org.junit.jupiter") || strings.Contains(src, "org.junit.") {
		return "junit_e2e"
	}
	return ""
}

// javaJUnitMethodStart matches a typical JUnit 5 test method after @Test (package-private void ok).
var javaJUnitMethodStart = regexp.MustCompile(`^\s*(?:public|protected|private)?\s*(?:static\s+)?(?:void|[\w.<>,\[\]]+)\s+(\w+)\s*\(`)

var javaE2eFileSuffix = regexp.MustCompile(`(?i)\.e2e\.java$`)

// enrichJavaE2ESpec adds E2E_SPEC for likely E2E test files (parity with advanced indexer heuristics).
// Without this, ListGapsE2E stays empty for minimal-indexer Java runs even when e2e_framework_bootstrap adds Playwright smoke tests.
func enrichJavaE2ESpec(lines []string, path string, pkg string) (extra []indexer.ParsedSymbol, edges []indexer.ParsedEdge) {
	if path == "" || !indexer.IsLikelyTestSourcePath(path) || !isLikelyJavaE2EPath(path) {
		return nil, nil
	}
	src := strings.Join(lines, "\n")
	fw := detectJavaE2EFrameworkImports(src)
	if fw == "" {
		return nil, nil
	}
	at := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "@Test") || strings.HasPrefix(t, "@org.junit.jupiter.api.Test") {
			at = i + 1
			break
		}
	}
	if at < 1 {
		return nil, nil
	}
	methodLine := -1
	for j := at; j < len(lines) && j < at+40; j++ {
		line := lines[j]
		if javaJUnitMethodStart.MatchString(line) {
			methodLine = j + 1
			break
		}
	}
	startLine, endLine := at, at
	if methodLine > 0 {
		startLine = methodLine
		endLine = endLineForJavaMethod(lines, methodLine-1)
		if endLine < startLine {
			endLine = startLine
		}
	}
	e2eFq := "E2E_SPEC:" + path
	sig, _ := json.Marshal(map[string]string{
		"framework": fw,
		"spec_path": path,
	})
	extra = append(extra, indexer.ParsedSymbol{
		Kind:          "E2E_SPEC",
		FQName:        e2eFq,
		StartLine:     startLine,
		EndLine:       endLine,
		SignatureJSON: sig,
	})
	if pkg != "" {
		edges = append(edges, indexer.ParsedEdge{
			CallerFQName: pkg,
			CalleeFQName: e2eFq,
			EdgeType:     "CONTAINS",
		})
	}
	return extra, edges
}

// AugmentParsedFileWithE2ESpecHeuristic appends E2E_SPEC (+ CONTAINS edge) when the advanced JAR map hit
// produced classes but no E2E_SPEC, or when used with canonicalPath after a map miss (minimal fallback).
// canonicalPath should be the repo-relative path from ScanRepoForFiles (matches DB file keys).
func AugmentParsedFileWithE2ESpecHeuristic(pf *indexer.ParsedFile, canonicalPath string) {
	if pf == nil || pf.Lang != "java" {
		return
	}
	for _, s := range pf.Symbols {
		if s.Kind == "E2E_SPEC" {
			return
		}
	}
	path := strings.TrimSpace(canonicalPath)
	if path == "" {
		path = pf.Path
	}
	path = filepath.ToSlash(path)
	lines := strings.Split(pf.Source, "\n")
	pkg := ""
	for _, s := range pf.Symbols {
		if s.Kind == "MODULE" {
			pkg = s.FQName
			break
		}
	}
	if pkg == "" {
		pkg = extractJavaPackage(lines)
	}
	syms, edges := enrichJavaE2ESpec(lines, path, pkg)
	if len(syms) == 0 {
		return
	}
	pf.Symbols = append(pf.Symbols, syms...)
	pf.Edges = append(pf.Edges, edges...)
}

func endLineForJavaMethod(lines []string, methodIdx0 int) int {
	if methodIdx0 < 0 || methodIdx0 >= len(lines) {
		return methodIdx0 + 1
	}
	depth := 0
	started := false
	for i := methodIdx0; i < len(lines); i++ {
		line := lines[i]
		for _, r := range line {
			if r == '{' {
				depth++
				started = true
			} else if r == '}' {
				depth--
				if started && depth == 0 {
					return i + 1
				}
			}
		}
	}
	return len(lines)
}

func classFQNameForLine(symbols []indexer.ParsedSymbol, line int) string {
	var best string
	var bestStart int
	for _, s := range symbols {
		if s.Kind != "class" && s.Kind != "interface" {
			continue
		}
		if line < s.StartLine || line > s.EndLine {
			continue
		}
		if best == "" || s.StartLine >= bestStart {
			best = s.FQName
			bestStart = s.StartLine
		}
	}
	return best
}
