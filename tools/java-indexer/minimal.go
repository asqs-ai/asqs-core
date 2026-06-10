// Package javaindexer provides a minimal Java language indexer (line-based heuristics, no AST).
// It implements the indexer.LangIndexer contract so the indexer pipeline can plug it in without changes.
package javaindexer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

// Index extracts symbols from Java source using line-based heuristics (no AST).
// It finds package, top-level classes/interfaces/enums, and their methods so that the indexer can
// store symbols and chunks. Spring Web controllers additionally emit API_ROUTE symbols and
// ROUTE_TO_HANDLER / CONTAINS edges (see spring_web.go). Use when the full java-indexer
// (JavaParser) is not available; for production, prefer an AST-based indexer.
// For non-Java files it delegates to indexer.StubLangIndexer.
func Index(ctx context.Context, path string, lang string, source []byte) (*indexer.ParsedFile, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	if lang != "java" {
		return indexer.StubLangIndexer(ctx, path, lang, source)
	}
	content := string(source)
	lines := strings.Split(content, "\n")
	pkg := extractJavaPackage(lines)
	module := javaPathToModule(path)
	symbols := extractJavaSymbols(lines, path, pkg)
	extraSyms, springEdges := enrichSpringWebRoutes(lines, symbols)
	if len(extraSyms) > 0 {
		symbols = append(symbols, extraSyms...)
	}
	var edges []indexer.ParsedEdge
	if len(springEdges) > 0 {
		edges = springEdges
	}
	e2eSyms, e2eEdges := enrichJavaPageObjectAndUserFlow(lines, path, pkg, symbols)
	if len(e2eSyms) > 0 {
		symbols = append(symbols, e2eSyms...)
	}
	if len(e2eEdges) > 0 {
		edges = append(edges, e2eEdges...)
	}
	specSyms, specEdges := enrichJavaE2ESpec(lines, path, pkg)
	if len(specSyms) > 0 {
		symbols = append(symbols, specSyms...)
	}
	if len(specEdges) > 0 {
		edges = append(edges, specEdges...)
	}
	return &indexer.ParsedFile{
		Path:    path,
		Lang:    "java",
		Module:  module,
		IsTest:  indexer.IsLikelyTestSourcePath(path),
		Source:  content,
		Symbols: symbols,
		Edges:   edges,
	}, nil
}

var (
	javaPackageRe  = regexp.MustCompile(`^\s*package\s+([\w.]+)\s*;\s*$`)
	javaClassRe    = regexp.MustCompile(`^\s*(?:public\s+)?(?:abstract\s+)?(?:strictfp\s+)?(class|interface|enum)\s+(\w+)`)
	javaMethodRe   = regexp.MustCompile(`^\s*(public|protected|private)\s+\S+\s+(\w+)\s*\(`)
)

func extractJavaPackage(lines []string) string {
	for _, line := range lines {
		if m := javaPackageRe.FindStringSubmatch(line); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func extractJavaSymbols(lines []string, path, pkg string) []indexer.ParsedSymbol {
	var symbols []indexer.ParsedSymbol
	var currentClass string
	currentClassIdx := -1
	braceDepth := 0
	inClass := false

	for i, line := range lines {
		lineNum := i + 1
		for _, r := range line {
			if r == '{' {
				braceDepth++
			} else if r == '}' {
				braceDepth--
			}
		}

		if !inClass {
			if m := javaClassRe.FindStringSubmatch(line); len(m) >= 3 {
				kind := m[1]
				name := m[2]
				if pkg != "" {
					currentClass = pkg + "." + name
				} else {
					currentClass = name
				}
				inClass = true
				braceDepth = 0
				for _, r := range line {
					if r == '{' {
						braceDepth++
					} else if r == '}' {
						braceDepth--
					}
				}
				currentClassIdx = len(symbols)
				symbols = append(symbols, indexer.ParsedSymbol{
					Kind:      kind,
					FQName:    currentClass,
					StartLine: lineNum,
					EndLine:   lineNum,
				})
			}
			continue
		}

		if braceDepth == 0 {
			if currentClassIdx >= 0 && currentClassIdx < len(symbols) {
				symbols[currentClassIdx].EndLine = lineNum
			}
			inClass = false
			currentClass = ""
			currentClassIdx = -1
			continue
		}

		if m := javaMethodRe.FindStringSubmatch(line); len(m) >= 3 && currentClass != "" {
			visibility := strings.TrimSpace(strings.ToLower(m[1]))
			methodName := m[2]
			fqMethod := currentClass + "#" + methodName
			sigJSON, _ := json.Marshal(map[string]interface{}{
				"visibility": visibility,
				"exported":   visibility == "public",
				"static":     false,
			})
			symbols = append(symbols, indexer.ParsedSymbol{
				Kind:          "method",
				FQName:        fqMethod,
				StartLine:     lineNum,
				EndLine:       lineNum,
				SignatureJSON: sigJSON,
			})
		}
	}

	if inClass && currentClassIdx >= 0 && currentClassIdx < len(symbols) {
		symbols[currentClassIdx].EndLine = len(lines)
	}

	// Set method end lines to the line before the next symbol or end of file
	for i := range symbols {
		if symbols[i].Kind != "method" {
			continue
		}
		nextStart := len(lines) + 1
		for j := i + 1; j < len(symbols); j++ {
			if symbols[j].StartLine > symbols[i].StartLine {
				nextStart = symbols[j].StartLine
				break
			}
		}
		symbols[i].EndLine = nextStart - 1
		if symbols[i].EndLine < symbols[i].StartLine {
			symbols[i].EndLine = symbols[i].StartLine
		}
	}

	return symbols
}

func javaPathToModule(path string) string {
	dir := filepath.Dir(path)
	dir = filepath.ToSlash(dir)
	idx := strings.Index(dir, "src/main/java/")
	if idx >= 0 {
		return strings.TrimPrefix(dir[idx+len("src/main/java/"):], "/")
	}
	idx = strings.Index(dir, "src/main/")
	if idx >= 0 {
		return strings.TrimPrefix(dir[idx+len("src/main/"):], "/")
	}
	if dir != "" && dir != "." {
		return dir
	}
	return ""
}
