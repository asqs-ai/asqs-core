// Package extendmerge writes generated test artifacts, extending an existing test file in place
// when one already owns the path instead of refusing or overwriting it.
//
// Merging is the whole point. A repository whose suite is FooTests.java gets a second FooTest.java
// from a tool that only ever creates, and from then on the two shadow each other. Extending keeps
// one artifact per subject — but a naive splice of a methods-only payload into a class body ships
// a file that cannot compile, because every symbol the new methods introduce arrives unimported.
// Hence the import union, the payload classifier, and the positional check.
package extendmerge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
)

var (
	reJestItFn    = regexp.MustCompile(`(?i)\bit\s*\(`)
	reJestTestFn  = regexp.MustCompile(`(?i)\btest\s*\(`)
	reJestSuiteFn = regexp.MustCompile(`(?i)\bsuite\s*\(`)
)

// Item is one generated artifact to write.
//
// ExtendExisting is decided by the caller, which is the only place that knows whether the path was
// chosen because a test file already owns it (see generator.ExistingOrSuggestedTestPath).
type Item struct {
	// Path is the repo-relative destination.
	Path string
	// Content is the generated body: a whole file when creating, a methods-only payload when
	// extending (though a model that ignores that contract is recovered, see classifyExtendPayload).
	Content string
	// ExtendExisting merges into the file already at Path rather than refusing as "already exists".
	ExtendExisting bool
	// SourceSymbolFile is the symbol-under-test's file, used to refuse writing unit tests into
	// production source.
	SourceSymbolFile string
	// Err, when set, skips the item; it carries a generation failure the caller already recorded.
	Err error
}

// writeGeneratedFilesWithSkips is writeGeneratedFiles plus the reason every dropped item was
// dropped, as "path: reason" strings.
//
// The reasons used to exist only on stderr. That made a whole class of failure undiagnosable from
// the audit: run api-c3e4a6ea003d0f9b1aeb487b4a8faec6 logged `per_gap_write wrote 0 file(s)` six
// times out of thirteen with no indication of why, while three planned artifacts silently never
// reached disk and the evaluator later reported them as unreadable. The count was visible; the
// cause was not, and the cause is the part an operator can act on.
//
// PerGapWriteResult already carries a SkipReason field that the tool surfaces as `skip_reason` in
// the audit payload — it was simply never populated for this path. This returns what that field
// needs.
func Write(repoRoot string, items []Item) (int, []string, []string) {
	var wrote int
	var writtenPaths []string
	var skips []string
	for _, g := range items {
		if g.Path == "" || g.Content == "" || g.Err != nil {
			continue
		}
		// noteSkip records a drop for the audit and mirrors it to stderr, which the local CLI
		// still reads. Declared per item so it closes over this item's path.
		noteSkip := func(reason string) {
			fmt.Fprintf(os.Stderr, "  skip (%s): %s\n", reason, g.Path)
			skips = append(skips, g.Path+": "+reason)
		}
		src := strings.TrimSpace(filepath.ToSlash(g.SourceSymbolFile))
		out := strings.TrimSpace(filepath.ToSlash(g.Path))
		// Block unit tests overwriting implementation files (path forced to same as symbol file by mistake).
		// E2E gaps often have symbol.file == the Playwright/Cypress/Jest spec itself — same path is correct; allow when out looks like a test path.
		if src != "" && out != "" && strings.EqualFold(src, out) && !looksLikeTestPath(out) {
			noteSkip("refusing to write unit tests into source file; use a sibling *.test.* / *.spec.* file")
			continue
		}
		if !looksLikeTestPath(g.Path) {
			noteSkip("not a test path; tests go to test files only")
			continue
		}
		// Reject empty test-file shells (e.g. `class OwnerControllerE2EIT {}` with no @Test method) —
		// they compile but exercise nothing. Also reject markdown fences and truncated bodies, which
		// would otherwise only surface as a compile failure two phases later.
		//
		// Extend-existing runs used to bypass BOTH gates entirely, which is how a full compilation
		// unit spliced inside a class body reached disk unvalidated. They now run too, against the
		// right inputs — see the extend branch below for which gate sees what.
		if !g.ExtendExisting {
			// A public type must be named after its file. The generator is told the path but picks
			// its own class name, and javac then rejects the artifact outright. Renaming here turns
			// a guaranteed compile failure plus two or three fixer rounds into a no-op.
			if fixed, oldName, changed := enforcePrimaryTypeName(g.Path, g.Content); changed {
				fmt.Fprintf(os.Stderr, "  renamed type %s -> %s to match %s\n",
					oldName, strings.TrimSuffix(filepath.Base(g.Path), filepath.Ext(g.Path)), g.Path)
				g.Content = fixed
			}
			// The declared package must match the directory, for the same reason and with the same
			// remedy as the type name: rewrite the declaration, never move the file.
			if fixed, oldPkg, changed := enforceJavaPackageMatchesPath(g.Path, g.Content); changed {
				fmt.Fprintf(os.Stderr, "  corrected package %s -> %s to match %s\n",
					oldPkg, javaPackageForPath(g.Path), g.Path)
				g.Content = fixed
			}
			if reason := evaluator.EmptyTestFileReason(g.Path, g.Content); reason != "" {
				noteSkip(reason)
				continue
			}
			if reason := evaluator.SyntacticShellReason(g.Path, g.Content); reason != "" {
				noteSkip(reason)
				continue
			}
		}
		full := filepath.Join(repoRoot, filepath.FromSlash(g.Path))
		if g.ExtendExisting {
			existing, err := os.ReadFile(full)
			if err != nil {
				noteSkip(fmt.Sprintf("could not read existing file: %v", err))
				continue
			}
			payload := strings.TrimSpace(g.Content)
			// Lift the payload's imports out BEFORE anything else looks at it. unwrapCompilationUnit
			// keeps only the primary type's body and discards the header, so without this step every
			// symbol the new methods introduce arrives unimported and the merge ships a file that
			// cannot compile. Hoisting first also stops a methods-only payload that legitimately
			// declares imports from being misread as a full compilation unit.
			payloadImports, payload := hoistTopLevelImports(g.Path, payload)
			payload = strings.TrimSpace(payload)
			switch kind := classifyExtendPayload(g.Path, payload); kind {
			case payloadUnusable:
				noteSkip("extend payload unusable: empty or markdown-fenced")
				continue
			case payloadCompilationUnit:
				// Either the model ignored the methods-only contract, or WriteCoordinator flipped a
				// freshly generated full file into extend mode because a sibling gap already created
				// the path. Recover the primary type's body instead of splicing package/import lines
				// into a class body.
				body, ok := unwrapCompilationUnit(g.Path, payload)
				if !ok {
					noteSkip("extend payload is a full compilation unit and could not be unwrapped")
					continue
				}
				fmt.Fprintf(os.Stderr, "  unwrapped extend payload (was a full compilation unit): %s\n", g.Path)
				payload = body
			}
			if surviving, droppedNames := dropDuplicateMembers(g.Path, string(existing), payload); len(droppedNames) > 0 {
				fmt.Fprintf(os.Stderr, "  dropped %d already-defined member(s) from extend payload (%s): %s\n",
					len(droppedNames), strings.Join(droppedNames, ", "), g.Path)
				payload = surviving
				// Everything the model produced was already declared. Falling through here writes
				// the file back byte-identical and counts it as extended: EmptyTestFileReason
				// returns "" for empty content, insertInsideClassBody returns the original when
				// the payload is blank, and mergedPayloadInsideTypeBody compares a tail to itself.
				// The run would report a successful write for a gap that gained no coverage.
				if strings.TrimSpace(payload) == "" {
					noteSkip(fmt.Sprintf("extend payload was entirely already-defined member(s) (%s); nothing new to add",
						strings.Join(droppedNames, ", ")))
					continue
				}
			}
			// EmptyTestFileReason runs on the PAYLOAD: its marker regexes are unanchored, so a
			// methods-only body carrying @Test / [Fact] passes. Running it on the merged file would
			// be a tautology — the existing file always has markers.
			if reason := evaluator.EmptyTestFileReason(g.Path, payload); reason != "" {
				noteSkip(reason)
				continue
			}
			// Union the payload's imports into the target's import block. Done BEFORE the splice so
			// every downstream gate (SyntacticShellReason, mergedPayloadInsideTypeBody) validates
			// the file that will actually be written.
			base := string(existing)
			ext := strings.ToLower(filepath.Ext(g.Path))
			if len(payloadImports) > 0 && (ext == ".java" || ext == ".cs") {
				addImports, skippedImports := unionImports(parseImports(base, ext), payloadImports, ext)
				if len(addImports) > 0 {
					merged, ok := mergeImportsIntoFile(base, addImports, ext)
					if !ok {
						// Fail closed. A merge we know is missing imports compiles no better than
						// no merge at all, and it silently costs the fixer a round to rediscover.
						noteSkip("cannot locate an import insertion point; refusing a merge known to be missing imports")
						continue
					}
					base = merged
					rendered := make([]string, 0, len(addImports))
					for _, d := range addImports {
						rendered = append(rendered, d.render(ext))
					}
					fmt.Fprintf(os.Stderr, "  merged %d import(s) into %s: %s\n",
						len(addImports), g.Path, strings.Join(rendered, " "))
				}
				if s := describeSkippedImports(skippedImports); s != "" {
					fmt.Fprintf(os.Stderr, "  skipped redundant/colliding import(s) for %s: %s\n", g.Path, s)
				}
			}
			combined := insertInsideClassBody([]byte(base), payload)
			// SyntacticShellReason runs on the COMBINED result: a payload can never satisfy its
			// "must declare a top-level type" clause. Note this is a backstop for fences and
			// truncation — a full file spliced into a class body is brace-balanced and does declare
			// a type, so classifyExtendPayload above is the real defense, not this gate.
			if reason := evaluator.SyntacticShellReason(g.Path, combined); reason != "" {
				noteSkip(reason + " (after merge)")
				continue
			}
			// Positional check: the payload must be inside the type body, not after its closing
			// brace. A merge that appends at EOF stays brace-balanced and keeps its type
			// declaration, so no syntactic gate can see it — only position can.
			if !mergedPayloadInsideTypeBody(g.Path, string(existing), combined) {
				noteSkip("merge placed the new tests outside the type body; refusing to write")
				continue
			}
			if err := os.WriteFile(full, []byte(combined), 0644); err != nil {
				noteSkip(fmt.Sprintf("could not extend: %v", err))
				continue
			}
			fmt.Fprintf(os.Stderr, "  extended: %s\n", g.Path)
			wrote++
			writtenPaths = append(writtenPaths, filepath.ToSlash(strings.TrimSpace(g.Path)))
			continue
		}
		if _, err := os.Stat(full); err == nil {
			noteSkip("already exists")
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			noteSkip(fmt.Sprintf("could not create dir: %v", err))
			continue
		}
		if err := os.WriteFile(full, []byte(g.Content), 0644); err != nil {
			noteSkip(fmt.Sprintf("could not write: %v", err))
			continue
		}
		wrote++
		writtenPaths = append(writtenPaths, filepath.ToSlash(strings.TrimSpace(g.Path)))
	}
	return wrote, writtenPaths, skips
}

// looksLikeTestPath reports whether path looks like a test file (.test., .spec., .cy., _test.go, *Test.java, *Tests.cs).
// Java: *Test.java, paths under src/test/ or src/it/ (covers *E2EIT.java and Maven *IT.java — basename may not contain "test").
// JS/TS: also Cypress *.cy.* and scripts under e2e/ (Playwright/Cypress layout) so E2E writes are not skipped.
// Used so tests are only written to test files and doc inserts are only applied to source (non-test) files.
func looksLikeTestPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, ".cy.") {
		return true
	}
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".java") {
		if strings.Contains(base, "test") {
			return true
		}
		pl := strings.ToLower(filepath.ToSlash(path))
		if strings.Contains(pl, "src/test/") || strings.Contains(pl, "src/it/") {
			return true
		}
		return false
	}
	if strings.HasSuffix(base, ".cs") && strings.Contains(base, "tests") {
		return true
	}
	// JS/TS: __tests__/foo.ts (Jest/Vitest) and E2E trees (e2e/foo.ts, cypress/)
	pl := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(pl, "/__tests__/") {
		return true
	}
	if strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".jsx") || strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs") {
		if strings.Contains(pl, "/e2e/") || strings.HasPrefix(pl, "e2e/") || strings.Contains(pl, "/cypress/") {
			return true
		}
	}
	return false
}

// looksLikeTestCode reports whether content looks like test code rather than a doc comment (e.g. Javadoc/JSDoc).
// Used to avoid inserting test code into source files via the doc path.
func looksLikeTestCode(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	// JSDoc/TSDoc (and C# XML summary) often include @example or prose with expect(, describe(, assert.* — those
	// must not trigger test detection. Check doc-comment shape before scanning the full string.
	if strings.HasPrefix(trimmed, "/**") || strings.HasPrefix(trimmed, "///") {
		return false
	}
	lower := strings.ToLower(trimmed)
	// Scan for test markers before treating a leading "//" as harmless doc. Models often output
	// "// path/to/foo.test.ts" then a full Jest/Vitest suite; that must not be inserted into index.ts.
	if strings.Contains(lower, "describe(") ||
		strings.Contains(lower, "beforeeach(") ||
		strings.Contains(lower, "aftereach(") ||
		strings.Contains(lower, "beforeall(") ||
		strings.Contains(lower, "afterall(") ||
		strings.Contains(lower, "testsuite(") ||
		strings.Contains(trimmed, "@Test") ||
		strings.Contains(lower, "[fact]") ||
		strings.Contains(lower, "expect(") ||
		strings.Contains(lower, "assert.") ||
		reJestItFn.MatchString(trimmed) ||
		reJestTestFn.MatchString(trimmed) ||
		reJestSuiteFn.MatchString(trimmed) {
		return true
	}
	// Pure comment / doc without test markers
	if strings.HasPrefix(trimmed, "/**") || strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "//") {
		return false
	}
	return false
}

// mergeArtifactInto folds a redundant test file's members into the canonical file, reusing the same
// machinery the extend-existing write path uses: import union (F04), duplicate-member suppression,
// and the positional gate that proves the payload landed inside the type body.
//
// Depends on F04's import union by design: a merge without it recreates the exact `cannot find
// symbol` class this plan exists to eliminate, and would hand the fixer a fresh compile failure as
// the reward for tidying up.
func MergeArtifact(canonical, redundant, canonicalPath, redundantPath string) (string, bool, string) {
	imports, body := hoistTopLevelImports(redundantPath, redundant)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false, "redundant file has no content to merge"
	}
	switch classifyExtendPayload(redundantPath, body) {
	case payloadUnusable:
		return "", false, "redundant file is unusable as a merge payload"
	case payloadCompilationUnit:
		unwrapped, ok := unwrapCompilationUnit(redundantPath, body)
		if !ok {
			return "", false, "cannot locate the redundant file's primary type body"
		}
		body = unwrapped
	}
	if surviving, dropped := dropDuplicateMembers(canonicalPath, canonical, body); len(dropped) > 0 {
		body = surviving
	}
	if strings.TrimSpace(body) == "" {
		// Everything it declared already exists in the canonical file: nothing to merge, but the
		// redundant copy is still redundant, so this counts as a successful reconciliation.
		return canonical, true, ""
	}
	base := canonical
	ext := strings.ToLower(filepath.Ext(canonicalPath))
	if len(imports) > 0 && (ext == ".java" || ext == ".cs") {
		add, _ := unionImports(parseImports(base, ext), imports, ext)
		if len(add) > 0 {
			next, ok := mergeImportsIntoFile(base, add, ext)
			if !ok {
				return "", false, "cannot locate an import insertion point in the canonical file"
			}
			base = next
		}
	}
	combined := insertInsideClassBody([]byte(base), body)
	if reason := evaluator.SyntacticShellReason(canonicalPath, combined); reason != "" {
		return "", false, "merged result failed the syntactic gate: " + reason
	}
	if !mergedPayloadInsideTypeBody(canonicalPath, base, combined) {
		return "", false, "merge placed members outside the canonical type body"
	}
	return combined, true, ""
}
