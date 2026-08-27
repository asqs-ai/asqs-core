package apisurface

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// NodeProvider resolves API surfaces for TypeScript/JavaScript projects by reading the type
// declarations npm packages already ship.
//
// This is structurally simpler than the Java path and deliberately so. Java member lists live in
// compiled .class files, which is why JavaProvider has to resolve a Maven classpath and shell out to
// javap. A TypeScript package publishes its API as text — node_modules/playwright/types/test.d.ts
// declares `interface LocatorAssertions { … }` in full — so there is no classpath to resolve, no
// subprocess to run, and no cache invalidation problem beyond the file's own content.
//
// Scope is the assertion API of the E2E frameworks, for the same reason as the Java side: those are
// the types whose SOURCE the model can never see, and the ones it invents members on. A repo's own
// TypeScript is already shipped to the prompt by retrieval.
type NodeProvider struct {
	mu    sync.Mutex
	cache map[string][]string // cache key: repoPath + "\x00" + interface name
}

func NewNodeProvider() *NodeProvider {
	return &NodeProvider{cache: map[string][]string{}}
}

// nodeTypeDeclCandidates are the files that may declare the Playwright assertion interfaces,
// in resolution order.
//
// @playwright/test re-exports rather than declaring: its index.d.ts is 18 lines of
// `export * from 'playwright/test'`. The declarations live in the `playwright` package it depends
// on, so a lookup that only checked @playwright/test would find nothing and report a miss on a
// correctly installed project.
var nodeTypeDeclCandidates = []string{
	"node_modules/playwright/types/test.d.ts",
	"node_modules/@playwright/test/types/test.d.ts",
	"node_modules/playwright-core/types/types.d.ts",
	"node_modules/playwright/types/types.d.ts",
}

// tsInterfaceDeclRE matches the opening line of a top-level interface declaration.
var tsInterfaceDeclRE = regexp.MustCompile(`(?m)^(?:export\s+)?(?:declare\s+)?interface\s+(\w+)\s*(?:extends\s+[^{]+)?\{`)

// tsMemberStartRE matches the first line of a member inside an interface body: a name followed by
// a call signature, a property type, or a generic parameter list.
var tsMemberStartRE = regexp.MustCompile(`^\s+(\w+)\??\s*[(:<]`)

// Lookup implements Provider.
//
// Errors are returned only for conditions the caller can act on (no declaration file found at all).
// A type that simply is not in the file is a miss, not an error, matching JavaProvider: the caller
// renders whatever resolved and audits the rest.
func (p *NodeProvider) Lookup(ctx context.Context, repoPath string, targets []Target) ([]TypeSurface, error) {
	if strings.TrimSpace(repoPath) == "" || len(targets) == 0 {
		return nil, nil
	}
	declPath := ""
	for _, rel := range nodeTypeDeclCandidates {
		full := filepath.Join(repoPath, filepath.FromSlash(rel))
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			declPath = full
			break
		}
	}
	if declPath == "" {
		return nil, fmt.Errorf("apisurface: no Playwright type declarations under %s (looked for %s)",
			repoPath, strings.Join(nodeTypeDeclCandidates, ", "))
	}

	var out []TypeSurface
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		// A TS interface has no package-qualified name; targets carry the bare interface name, and
		// a dotted name (a Java/C# target reaching the wrong provider) resolves to its last segment
		// rather than silently missing.
		name := t.Name
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		members, err := p.interfaceMembers(declPath, name)
		if err != nil || len(members) == 0 {
			continue
		}
		out = append(out, NewTypeSurface(name, members, t.Member, relOriginFor(repoPath, declPath)))
	}
	return out, nil
}

func relOriginFor(repoPath, declPath string) string {
	if rel, err := filepath.Rel(repoPath, declPath); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(declPath)
}

func (p *NodeProvider) interfaceMembers(declPath, name string) ([]string, error) {
	key := declPath + "\x00" + name
	p.mu.Lock()
	if v, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	b, err := os.ReadFile(declPath)
	if err != nil {
		return nil, err
	}
	members := parseTSInterfaceMembers(string(b), name)

	p.mu.Lock()
	p.cache[key] = members
	p.mu.Unlock()
	return members, nil
}

// parseTSInterfaceMembers extracts the member signatures of a named interface.
//
// Signatures are normalised to one line each, with nested object literals collapsed to `{…}`.
// That collapse is not cosmetic: Playwright's toHaveScreenshot spans 92 lines of options, and four
// interfaces emitted verbatim would be tens of thousands of runes — past the whole fix-request
// budget before a line of the file under repair had been written. What the model gets wrong is the
// member NAME (toHaveTextContaining for toContainText), and the name plus its leading parameters
// survive the collapse intact.
func parseTSInterfaceMembers(src, name string) []string {
	start, bodyStart := findTSInterfaceBody(src, name)
	if start < 0 {
		return nil
	}
	depth := 1
	var members []string
	var cur strings.Builder
	collecting := false

	for _, line := range strings.Split(src[bodyStart:], "\n") {
		if depth == 1 && !collecting {
			trimmed := strings.TrimSpace(line)
			// Skip doc comments and blanks between members.
			if trimmed == "" || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "//") {
				if d := netBraceDelta(line); d != 0 {
					depth += d
					if depth <= 0 {
						break
					}
				}
				continue
			}
			if tsMemberStartRE.MatchString(line) {
				collecting = true
			}
		}
		if collecting {
			cur.WriteString(" ")
			cur.WriteString(strings.TrimSpace(line))
		}
		depth += netBraceDelta(line)
		if depth <= 0 {
			// Closing brace of the interface itself.
			if collecting {
				if m := normaliseTSMember(cur.String()); m != "" {
					members = append(members, m)
				}
			}
			break
		}
		if collecting && depth == 1 && strings.HasSuffix(strings.TrimSpace(line), ";") {
			if m := normaliseTSMember(cur.String()); m != "" {
				members = append(members, m)
			}
			cur.Reset()
			collecting = false
		}
	}
	return members
}

// findTSInterfaceBody returns the index of the interface declaration and of the first byte inside
// its body, or (-1, -1). Matching the exact name avoids PageAssertions resolving to
// PageAssertionsToHaveScreenshotOptions, which is declared in the same file.
func findTSInterfaceBody(src, name string) (int, int) {
	for _, loc := range tsInterfaceDeclRE.FindAllStringSubmatchIndex(src, -1) {
		if src[loc[2]:loc[3]] != name {
			continue
		}
		brace := strings.IndexByte(src[loc[0]:loc[1]], '{')
		if brace < 0 {
			continue
		}
		return loc[0], loc[0] + brace + 1
	}
	return -1, -1
}

// tsNestedObjectRE matches an innermost `{ … }` with no nested braces, so repeated application
// collapses arbitrarily deep option objects from the inside out.
var tsNestedObjectRE = regexp.MustCompile(`\{[^{}]*\}`)

var tsWhitespaceRE = regexp.MustCompile(`\s+`)

// tsCollapsedObject is the placeholder a collapsed object literal is replaced with DURING the
// collapse loop, and the rendering it becomes afterwards.
//
// The placeholder must not itself contain braces. Replacing an inner object directly with `{…}`
// reintroduces them, so the enclosing object no longer matches `\{[^{}]*\}` and the collapse stops
// one level in — which is how `toHaveScreenshot(..., options?: { animations?: …; clip?: {…}; … })`
// survived a loop that was supposed to reduce it to `options?: {…}`.
const (
	tsCollapsePlaceholder = "\x00obj\x00"
	tsCollapsedObject     = "{…}"
)

func normaliseTSMember(s string) string {
	s = tsWhitespaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	if s == "" {
		return ""
	}
	// Collapse from the inside out, bounded so a pathological declaration cannot spin.
	for i := 0; i < 32 && strings.ContainsRune(s, '{'); i++ {
		next := tsNestedObjectRE.ReplaceAllString(s, tsCollapsePlaceholder)
		if next == s {
			break
		}
		s = next
	}
	s = strings.ReplaceAll(s, tsCollapsePlaceholder, tsCollapsedObject)
	s = tsWhitespaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func netBraceDelta(line string) int {
	return strings.Count(line, "{") - strings.Count(line, "}")
}
