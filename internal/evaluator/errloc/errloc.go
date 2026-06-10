// Package errloc parses compiler/test logs for file:line references and builds focused code windows
// for LLM fixer prompts (fault localization–style context). See docs/DOCUMENTATION.md for citations.
package errloc

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Location is a file reference extracted from a log line (path as in the log, not necessarily repo-canonical).
type Location struct {
	File string
	Line int // 1-based; 0 invalid
}

var (
	// file:line or file:line:column (TS/Jest, rustc-style, many compilers)
	reFileLineColon = regexp.MustCompile(`([A-Za-z0-9_.][A-Za-z0-9_./\\~-]*\.(?:java|kt|kts|ts|tsx|js|jsx|mjs|cjs|cs|go|scala)):(\d+)(?::\d+)?`)
	// Java / JVM stack: at pkg.Class.method(File.java:12)
	reJavaParen = regexp.MustCompile(`\(([A-Za-z0-9_./\\$-]+\.(?:java|kt|kts|scala)):(\d+)\)`)
	// C# / MSBuild: Path\File.cs(33,12) or (Path/File.cs(33,12) in some logs
	reCSharpParen = regexp.MustCompile(`(?:^|[\s(:'"[])([A-Za-z0-9_./\\]+\.cs)\((\d+),\s*\d+\)`)
)

// ParseLocations extracts (file, line) pairs from build/test output. Order is scan order; callers dedupe.
func ParseLocations(log string) []Location {
	if log == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []Location
	add := func(file string, line int) {
		file = strings.TrimSpace(file)
		file = strings.Trim(file, `"'`)
		if file == "" || line < 1 {
			return
		}
		key := file + "\x00" + strconv.Itoa(line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Location{File: file, Line: line})
	}
	for _, sub := range reJavaParen.FindAllStringSubmatch(log, -1) {
		if len(sub) >= 3 {
			line, _ := strconv.Atoi(sub[2])
			add(sub[1], line)
		}
	}
	for _, sub := range reCSharpParen.FindAllStringSubmatch(log, -1) {
		if len(sub) >= 3 {
			line, _ := strconv.Atoi(sub[2])
			add(sub[1], line)
		}
	}
	for _, sub := range reFileLineColon.FindAllStringSubmatch(log, -1) {
		if len(sub) >= 3 {
			line, _ := strconv.Atoi(sub[2])
			add(sub[1], line)
		}
	}
	return out
}

// NormalizePath matches evaluator path style: forward slashes, no leading slash.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
	return p
}

// LinesByCanonicalPaths maps repo-relative paths (canonical) to sorted unique 1-based line numbers mentioned in the log for that file.
func LinesByCanonicalPaths(log string, canonicalRepoPaths []string) map[string][]int {
	locs := ParseLocations(log)
	if len(locs) == 0 || len(canonicalRepoPaths) == 0 {
		return nil
	}
	canonical := make([]string, 0, len(canonicalRepoPaths))
	seenCanon := make(map[string]bool)
	for _, p := range canonicalRepoPaths {
		c := NormalizePath(p)
		if c == "" || seenCanon[c] {
			continue
		}
		seenCanon[c] = true
		canonical = append(canonical, c)
	}
	baseCount := make(map[string]int)
	for _, c := range canonical {
		baseCount[filepath.Base(c)]++
	}
	out := make(map[string][]int)
	for _, loc := range locs {
		raw := NormalizePath(loc.File)
		if hit, ok := matchCanonical(raw, canonical, baseCount); ok {
			out[hit] = append(out[hit], loc.Line)
		}
	}
	for k, lines := range out {
		sort.Ints(lines)
		out[k] = uniqSortedInts(lines)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func uniqSortedInts(a []int) []int {
	if len(a) == 0 {
		return nil
	}
	cur := a[0]
	b := []int{cur}
	for i := 1; i < len(a); i++ {
		if a[i] != cur {
			cur = a[i]
			b = append(b, cur)
		}
	}
	return b
}

func matchCanonical(raw string, canonical []string, baseCount map[string]int) (string, bool) {
	if raw == "" {
		return "", false
	}
	// Exact
	for _, c := range canonical {
		if raw == c {
			return c, true
		}
	}
	// Suffix / prefix overlap (CI logs often embed subpaths)
	for _, c := range canonical {
		if strings.HasSuffix(c, "/"+raw) || strings.HasSuffix(raw, "/"+c) {
			return c, true
		}
		if strings.HasSuffix(raw, c) && (len(raw) == len(c) || raw[len(raw)-len(c)-1] == '/') {
			return c, true
		}
		if strings.HasSuffix(c, raw) && (len(c) == len(raw) || c[len(c)-len(raw)-1] == '/') {
			return c, true
		}
	}
	base := filepath.Base(raw)
	if base == "" || base == "." {
		return "", false
	}
	if baseCount[base] == 1 {
		for _, c := range canonical {
			if filepath.Base(c) == base {
				return c, true
			}
		}
	}
	return "", false
}

// PromptOpts configures error-localized windows for the fixer user message.
type PromptOpts struct {
	IsArtifact bool // if true, include top-of-file preamble (imports/package) when truncating
	// MaxRunes caps the returned string (UTF-8 runes).
	MaxRunes int
	// PreambleLines is how many initial lines to always include for artifacts when windows do not cover the top.
	PreambleLines int
	// WindowBefore / WindowAfter are half-widths in lines around each cited line.
	WindowBefore, WindowAfter int
}

// DefaultPromptOpts returns defaults aligned with “focus the model on failure lines” (see DOCUMENTATION.md references).
func DefaultPromptOpts() PromptOpts {
	return PromptOpts{
		PreambleLines: 120,
		WindowBefore:  40,
		WindowAfter:   40,
		MaxRunes:      12000,
	}
}

// FormatFileForPrompt builds a string for the LLM: either full content (if short), else merged line windows.
// If lineNums is empty and content exceeds MaxRunes, returns head-only truncation (same spirit as before).
func FormatFileForPrompt(content string, lineNums []int, o PromptOpts) string {
	if o.MaxRunes <= 0 {
		o.MaxRunes = DefaultPromptOpts().MaxRunes
	}
	if o.PreambleLines <= 0 {
		o.PreambleLines = DefaultPromptOpts().PreambleLines
	}
	if o.WindowBefore <= 0 {
		o.WindowBefore = DefaultPromptOpts().WindowBefore
	}
	if o.WindowAfter <= 0 {
		o.WindowAfter = DefaultPromptOpts().WindowAfter
	}
	runes := []rune(content)
	if len(runes) <= o.MaxRunes {
		return content
	}
	if len(lineNums) == 0 {
		return string(runes[:o.MaxRunes]) + "\n\n[GIST ONLY - truncated to stay within limit; use error log line numbers to fix]\n"
	}
	nl := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(nl, "\n")
	n := len(lines)
	if n == 0 {
		return content
	}
	var hits []int
	for _, ln := range lineNums {
		if ln >= 1 && ln <= n {
			hits = append(hits, ln)
		}
	}
	if len(hits) == 0 {
		return string(runes[:o.MaxRunes]) + "\n\n[GIST ONLY - truncated to stay within limit; use error log line numbers to fix]\n"
	}
	before, after := o.WindowBefore, o.WindowAfter
	for attempt := 0; attempt < 12; attempt++ {
		iv := mergedIntervals(hits, before, after, n)
		if o.IsArtifact {
			iv = mergeIntervalUnion(iv, [2]int{1, minInt(o.PreambleLines, n)})
		}
		iv = mergeOverlapping(iv)
		s := renderIntervals(lines, iv, n)
		s = "[ERROR-LOCALIZED CONTEXT — not full file; total_lines=" + strconv.Itoa(n) + "]\n" + s
		if len([]rune(s)) <= o.MaxRunes {
			return s
		}
		if before > 8 {
			before -= 4
		}
		if after > 8 {
			after -= 4
		}
		if before <= 8 && after <= 8 {
			break
		}
	}
	// Last resort: shrink to maxRunes from error-localized view with minimal windows
	iv := mergedIntervals(hits, 5, 5, n)
	if o.IsArtifact {
		iv = mergeIntervalUnion(iv, [2]int{1, minInt(minInt(40, o.PreambleLines), n)})
	}
	iv = mergeOverlapping(iv)
	s := renderIntervals(lines, iv, n)
	s = "[ERROR-LOCALIZED CONTEXT — not full file; total_lines=" + strconv.Itoa(n) + "]\n" + s
	out := []rune(s)
	if len(out) > o.MaxRunes {
		return string(out[:o.MaxRunes]) + "\n[TRUNCATED]\n"
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mergedIntervals(hits []int, before, after, n int) [][2]int {
	var iv [][2]int
	for _, ln := range hits {
		lo := ln - before
		hi := ln + after
		if lo < 1 {
			lo = 1
		}
		if hi > n {
			hi = n
		}
		iv = append(iv, [2]int{lo, hi})
	}
	return mergeOverlapping(iv)
}

func mergeIntervalUnion(iv [][2]int, extra [2]int) [][2]int {
	out := append(append([][2]int(nil), iv...), extra)
	return mergeOverlapping(out)
}

func mergeOverlapping(iv [][2]int) [][2]int {
	if len(iv) == 0 {
		return nil
	}
	sort.Slice(iv, func(i, j int) bool {
		if iv[i][0] != iv[j][0] {
			return iv[i][0] < iv[j][0]
		}
		return iv[i][1] < iv[j][1]
	})
	var out [][2]int
	cur := iv[0]
	for i := 1; i < len(iv); i++ {
		if iv[i][0] <= cur[1]+1 {
			if iv[i][1] > cur[1] {
				cur[1] = iv[i][1]
			}
		} else {
			out = append(out, cur)
			cur = iv[i]
		}
	}
	out = append(out, cur)
	return out
}

func renderIntervals(lines []string, iv [][2]int, n int) string {
	var b strings.Builder
	prevEnd := 0
	for _, seg := range iv {
		lo, hi := seg[0], seg[1]
		if lo < 1 {
			lo = 1
		}
		if hi > n {
			hi = n
		}
		if prevEnd > 0 && lo > prevEnd+1 {
			b.WriteString("\n... [lines ")
			b.WriteString(strconv.Itoa(prevEnd + 1))
			b.WriteString("-")
			b.WriteString(strconv.Itoa(lo - 1))
			b.WriteString(" omitted] ...\n\n")
		}
		b.WriteString("--- lines ")
		b.WriteString(strconv.Itoa(lo))
		b.WriteString("-")
		b.WriteString(strconv.Itoa(hi))
		b.WriteString(" (from error log) ---\n")
		for i := lo - 1; i < hi; i++ {
			b.WriteString(lines[i])
			b.WriteByte('\n')
		}
		prevEnd = hi
	}
	return b.String()
}
