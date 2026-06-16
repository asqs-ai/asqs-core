package llmfix

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/errloc"
	"github.com/asqs/asqs-core/internal/evaluator/errout"
)

// buildStructuredFixUserMessage emits the fixer user message with explicit XML-like section
// boundaries. Enabled by Fixer.StructuredUserMessage (Phase 3 opt-in). The intent is to give
// models that follow structural cues more reliably (e.g. GPT-4-class) a clearer signal about
// which block is writable, which are reference-only, and which is the error log, at the cost of
// slightly more tokens than the legacy `--- path ---` layout.
//
// The section order and content budgets exactly mirror buildFixUserMessage so this variant is a
// drop-in replacement at the same per-request rune budget.
func buildStructuredFixUserMessage(req evaluator.FixRequest, lim fixPromptLimits) string {
	var b strings.Builder

	b.WriteString("<metadata>\n")
	b.WriteString(fmt.Sprintf("  <step>%s</step>\n", escXML(string(req.Step))))
	b.WriteString(fmt.Sprintf("  <language>%s</language>\n", escXML(req.Lang)))
	if req.FixAttempt > 0 && req.MaxFixAttempt > 0 {
		b.WriteString(fmt.Sprintf("  <fix_attempt>%d of %d</fix_attempt>\n", req.FixAttempt, req.MaxFixAttempt))
	}
	if req.TestFramework != "" {
		b.WriteString(fmt.Sprintf("  <test_framework>%s</test_framework>\n", escXML(req.TestFramework)))
	}
	if req.BuildTool != "" {
		b.WriteString(fmt.Sprintf("  <build_tool>%s</build_tool>\n", escXML(req.BuildTool)))
	}
	if req.CompileCommand != "" {
		b.WriteString(fmt.Sprintf("  <compile_command>%s</compile_command>\n", escXML(req.CompileCommand)))
	}
	if req.TestCommand != "" {
		b.WriteString(fmt.Sprintf("  <test_command>%s</test_command>\n", escXML(req.TestCommand)))
	}
	if isJavaScriptOrTypeScriptFixLang(req.Lang) {
		if names := packageNamesFromPackageJSON(req.Manifests); len(names) > 0 {
			if line := formatAvailableNpmPackagesLineForPrompt(names); line != "" {
				b.WriteString(fmt.Sprintf("  <available_npm_packages>%s</available_npm_packages>\n", escXML(line)))
			}
		}
	}
	b.WriteString("</metadata>\n\n")

	b.WriteString(fixUserTurnFocusBlock(req))
	b.WriteString("\n")

	// Prior-attempt framing: when we're on retry N > 1, tell the model explicitly that the
	// `<file role="failing_artifact">` body below is the *current on-disk state* — which is the
	// result of whatever fix it (or a prior turn) produced — and that the diagnostic above was
	// obtained by running that body. This breaks the "attempt 1-indistinguishable-from-attempt-10"
	// failure mode the petclinic "protected access" audit exhibited, where the model happily
	// re-emits a variant of the same broken code because nothing in the prompt anchors its prior
	// attempt to the current error. Kept short — the body and error are already in context, we
	// just need to label their relationship.
	if req.FixAttempt >= 2 {
		b.WriteString(fmt.Sprintf("<prior_attempt fix_attempt=\"%d\" of=\"%d\">\n", req.FixAttempt, req.MaxFixAttempt))
		b.WriteString("The <file role=\"failing_artifact\"> body below is the current state on disk after the previous fix attempt. The <error> block above is what the build/test step reported when run against exactly that body. Do not re-emit a trivial variant of the current content — previous approach did not clear the diagnostic. Change strategy (different API, different imports, different test shape) or, if the symptom points at an external framework type that cannot be reached directly, switch to a verify-side-effect pattern (spy + verify, reflection helpers) instead of calling the inaccessible member.\n")
		b.WriteString("</prior_attempt>\n\n")
	}

	// Manifests.
	if len(req.Manifests) > 0 {
		for path, content := range req.Manifests {
			b.WriteString(fmt.Sprintf("<manifest path=%q>\n", path))
			runes := []rune(content)
			maxM := lim.MaxManifestRunes
			if maxM <= 0 {
				maxM = maxManifestRunes
			}
			if len(runes) > maxM {
				b.WriteString(string(runes[:maxM]))
				b.WriteString("\n[MANIFEST TRUNCATED]\n")
			} else {
				b.WriteString(content)
				b.WriteString("\n")
			}
			b.WriteString("</manifest>\n\n")
		}
	}

	// Artifact retrieval / planning context.
	if len(req.ArtifactContexts) > 0 {
		keys := make([]string, 0, len(req.ArtifactContexts))
		for p, ctx := range req.ArtifactContexts {
			if strings.TrimSpace(ctx) == "" {
				continue
			}
			keys = append(keys, evaluator.NormalizeRepoRelPath(p))
		}
		sort.Strings(keys)
		seen := make(map[string]bool, len(keys))
		for _, path := range keys {
			if seen[path] {
				continue
			}
			seen[path] = true
			ctx := strings.TrimSpace(req.ArtifactContexts[path])
			if ctx == "" {
				continue
			}
			b.WriteString(fmt.Sprintf("<artifact_context path=%q>\n", path))
			runes := []rune(ctx)
			if len(runes) > maxArtifactContextRunes {
				b.WriteString(string(runes[:maxArtifactContextRunes]))
				b.WriteString("\n[RETRIEVAL CONTEXT TRUNCATED]\n")
			} else {
				b.WriteString(ctx)
				b.WriteString("\n")
			}
			b.WriteString("</artifact_context>\n\n")
		}
	}

	// Error block: tag with language / step and whether the model should treat it as a compile-
	// shaped failure even when it surfaces during the test step.
	compileShaped := errout.IsCompileShaped(req.ErrorOutput)
	b.WriteString(fmt.Sprintf("<error language=%q step=%q compile_phase=%q>\n", req.Lang, string(req.Step), boolAttr(compileShaped)))
	if primary := primaryErrorLine(req.ErrorOutput); primary != "" {
		b.WriteString("PRIMARY ERROR (fix this first): ")
		b.WriteString(primary)
		b.WriteString("\n")
	}
	b.WriteString(errorLogGistWithLimits(req.ErrorOutput, lim.MaxErrorLogRunes, lim.ErrorLogTailRunes))
	b.WriteString("\n</error>\n\n")

	// Files: artifacts first (writable=true, role=failing_artifact), then dependencies
	// (writable=false, role=dependency).
	canonicalPaths := make([]string, 0, len(req.Files))
	for p := range req.Files {
		canonicalPaths = append(canonicalPaths, evaluator.NormalizeRepoRelPath(p))
	}
	lineByPath := errloc.LinesByCanonicalPaths(req.ErrorOutput, canonicalPaths)
	winOpts := errloc.DefaultPromptOpts()
	winOpts.MaxRunes = lim.MaxRunesPerFile
	if winOpts.MaxRunes <= 0 {
		winOpts.MaxRunes = maxRunesPerFile
	}

	maxTotal := lim.MaxTotalRunes
	if maxTotal <= 0 {
		maxTotal = maxFixRequestRunes
	}
	totalRunes := len([]rune(b.String()))
	// slicedLikely returns true when the file body looks like a signature-only slice produced by
	// internal/evaluator/fixslice. We sniff for the elided-body marker so the LLM gets a matching
	// `sliced="signatures_only"` attribute without plumbing a separate map through.
	slicedLikely := func(body string) bool {
		return strings.Contains(body, "{ /* body elided for fixer context */ }")
	}
	emitFile := func(path, content string, isArtifact bool) {
		if totalRunes >= maxTotal {
			b.WriteString(fmt.Sprintf("<file path=%q role=\"omitted\" writable=%q>\n[OMITTED - context limit]\n</file>\n\n", path, boolAttr(isArtifact)))
			return
		}
		role := "dependency"
		writable := "false"
		if isArtifact {
			role = "failing_artifact"
			writable = "true"
		}
		slicedAttr := ""
		if !isArtifact && slicedLikely(content) {
			slicedAttr = " sliced=\"signatures_only\""
		}
		b.WriteString(fmt.Sprintf("<file path=%q role=%q writable=%q%s>\n", path, role, writable, slicedAttr))
		lines := lineByPath[path]
		o := winOpts
		o.IsArtifact = isArtifact
		display := errloc.FormatFileForPrompt(content, lines, o)
		b.WriteString(display)
		if !strings.HasSuffix(display, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("</file>\n\n")
		totalRunes = len([]rune(b.String()))
	}

	emitted := make(map[string]bool)
	for _, ap := range req.ArtifactPaths {
		apKey := evaluator.NormalizeRepoRelPath(ap)
		for path, content := range req.Files {
			if evaluator.NormalizeRepoRelPath(path) != apKey {
				continue
			}
			canonical := evaluator.NormalizeRepoRelPath(path)
			if emitted[canonical] {
				break
			}
			emitFile(canonical, content, true)
			emitted[canonical] = true
			break
		}
	}
	// Error-referenced dependencies first, then alphabetical (deterministic) — so the per-request
	// budget is spent on the files the diagnostic points at, not dropped for unrelated deps.
	for _, canonical := range rankDependencyPaths(req.Files, emitted, lineByPath) {
		emitFile(canonical, req.Files[canonicalKey(canonical, req.Files)], false)
	}

	b.WriteString("<output_contract>\n")
	b.WriteString("Respond with a single JSON object: { \"path/to/file\": \"full content\" } covering every `<file writable=\"true\">` you changed. No markdown or explanation.\n")
	b.WriteString("</output_contract>\n")
	return b.String()
}

// canonicalKey returns the original (possibly non-normalised) key inside req.Files that matches
// the given canonical path. buildFixUserMessage iterates the original map directly so we do the
// same; this helper keeps emitFile working with the original content even when the map key isn't
// already canonical.
func canonicalKey(canonical string, files map[string]string) string {
	for k := range files {
		if evaluator.NormalizeRepoRelPath(k) == canonical {
			return k
		}
	}
	return canonical
}

// escXML escapes the handful of characters that would confuse a naive XML reader inside our tag
// attribute / text values. We never ship binary content so this is sufficient.
func escXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func boolAttr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
