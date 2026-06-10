// Package llmfix provides an evaluator.Fixer that uses an LLM (ChatCompleter) to fix compile or test failures.
// It works with any provider (OpenAI, Anthropic, etc.). Context includes errors, test and source files, and metadata (language, test framework, build commands) for best results.
package llmfix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/evaluator/errloc"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// Fixer uses the LLM to produce fixed file content when compile or test fails.
type Fixer struct {
	LLM    model.ChatCompleter
	Prompt string // optional system prompt; empty = use default
	// MultiTurnRepair when true (default when set from qualitybot via runner.disable_multi_turn_fixer=false), reuses prior user/assistant turns for the same repo path and sandbox step within one evaluation run. Follow-up user turns send new errors plus artifact files only, reducing repeated static context (conversational repair; see docs/DOCUMENTATION.md).
	MultiTurnRepair bool
	// DisableStructuredFixOutput when true, skips provider JSON-schema structured completions and relies on the prompt plus the existing multi-strategy parser (and optional repair turn). Default false: first completion uses structured output when supported (e.g. OpenAI); see runner.disable_structured_fix_output.
	DisableStructuredFixOutput bool
	// StructuredUserMessage when true (Phase 3 opt-in: `runner.fixer_structured_user_message`)
	// emits the user message with explicit XML-like section boundaries — `<error>`, `<file
	// path=… role=… writable=…>` — instead of the legacy `--- path ---` layout. Default false
	// keeps the legacy layout. Older / weaker models may handle tagged blocks worse; the flag is
	// meant for providers that follow structural cues more reliably (e.g. GPT-4-class models).
	StructuredUserMessage bool

	mu               sync.Mutex
	convRepoPath     string
	convGapSessionID string
	convStep         evaluator.SandboxStep
	convMsgs         []model.Message // user/assistant pairs only; system is always prepended fresh
}

// FixRequestAuditMetadata implements evaluator.FixRequestIntrospector so the workflow can surface
// fixer-side configuration (multi-turn repair, structured output request) in the
// evaluator.fix_request audit payload. Callers that assemble their own Fixer variant should
// implement this if they want the same keys; otherwise the evaluator omits them.
func (f *Fixer) FixRequestAuditMetadata() map[string]any {
	return map[string]any{
		"multi_turn":                  f.MultiTurnRepair,
		"structured_output_requested": !f.DisableStructuredFixOutput,
		"structured_user_message":     f.StructuredUserMessage,
	}
}

var _ evaluator.FixRequestIntrospector = (*Fixer)(nil)

// maxFixRequestRunes caps the total user message size to avoid oversized requests (unexpected EOF, timeouts, 413).
// When error log or files exceed their caps, only a gist is sent so the request stays small and completes.
const maxFixRequestRunes = 50000

// Gist limits: when content exceeds these, only the gist (start + optional end) is sent to the LLM.
const maxErrorLogRunes = 8000  // error log gist: first N runes (main errors); tail added if over limit
const errorLogTailRunes = 1500 // last N runes kept when truncating (stack trace tail)
const maxRunesPerFile = 12000  // per-file gist; large mock data or embedded content is truncated

// maxMultiTurnConvRunes caps stored user+assistant history so long fix sessions stay bounded.
const maxMultiTurnConvRunes = 64000

const defaultFixPrompt = `You are an expert programmer. The build (compile) or tests failed. You will receive:
1) Metadata: language, test framework, build/test commands, and fix attempt number (if attempt > 1, try a different strategy than before).
2) Retrieval/planning context for each artifact when available (dependency graph, fixtures/config snippets, existing-test branch-gap hints, output contract). Treat this as the authoritative intent for what the test should validate; preserve intent while fixing failures.
3) Dependency manifest(s) (e.g. package.json, pom.xml, and for C# often ***.csproj** plus **Directory.Packages.props** when included) when available — CRITICAL: only use packages and imports that appear in these manifests. Do not suggest or add imports for packages that are not in the project. If the test needs something not installed, use an alternative (e.g. mock, or an existing package).
4) The error log (possibly a gist). A "PRIMARY ERROR" line may be shown first — fix that first. Use line numbers, file names, and error messages.
5) Files labeled "ARTIFACT TO FIX" (the generated test file(s) you must fix) and "DEPENDENCY, reference only" (source files — use for API reference only; do not modify). When a file begins with [ERROR-LOCALIZED CONTEXT], only stack-relevant line ranges (and for artifacts, the top of the file for imports/package) are shown—not the entire file. Use the ERROR LOG and line markers to reason; your JSON output must still be the **full** corrected file content for each artifact you change.

On **follow-up** turns in the same conversation, the user message may be shorter (new error output and current artifact test file(s) only). Use earlier turns for manifests, dependency files, and prior errors—do not assume those are repeated.

Provide practical fixes: fix imports to match the manifest, fix assertions to match actual APIs in the source files, fix syntax to match the test framework. Modify ONLY the artifact test file(s). Do not modify dependency/source files. Do not add new dependencies. If this is a retry (attempt > 1), try a meaningfully different fix.

If tests only read a source file and assert toContain/match on text (no real invocation/mocks), rewrite them to **behavioral** tests: mock dependencies, call the function under test, assert return values and mock calls.

If tests use **tautological** assertions (e.g. expect(true).toBe(true), expect('./path').toBe('./path'), or identical literals/constants on both sides of expect), replace them with real checks: invoke the export, or for React use @testing-library/react (render + screen + user-visible assertions or mock verifications). Do not leave always-passing expectations.

Prefer fixing the real error (imports, selectors, assertions, timing). Do **not** use @Disabled, @Ignore, xUnit [Ignore], test.skip/describe.skip, or Assumptions.assume* as the **first** fix—especially vague "Docker/CI/Testcontainers" excuses when the sandbox is often Docker + Playwright. Remove gratuitous skips you added earlier. **Last resort only:** if the failure is genuinely unfixable in-repo (e.g. hard dependency on unavailable infra), you may skip **one** failing test method with a **short, honest** reason; prefer method-level over class-level; do not skip the entire artifact unless every test in the file is blocked for the same documented reason.

**Never** return **empty** file content for an artifact, **delete all** test methods, or replace the file with only using directives and namespace comments to "fix" compile errors—preserve working tests and unrelated methods; only change what is needed to address the error.

Output format: a single JSON object mapping repo-relative file path to full file content. Include only the test file(s) you changed. **Keys must be the exact artifact path(s)** from the prompt (the generated or failing **test** file such as *.test.ts / __tests__/*.ts). **Never** use the implementation/source file path as a JSON key — those files are read-only context; writing fixes to them corrupts the app (e.g. Strapi lifecycles.ts). Use \\n for newlines in string values. No markdown, no explanation, only the JSON object.

Example: {"src/test/java/FooTest.java": "package p;\\nimport org.junit.Test;\\npublic class FooTest { ... }"}`

// ResetConversation clears multi-turn state (e.g. before tests or a new repo evaluation with a shared Fixer).
func (f *Fixer) ResetConversation() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convMsgs = nil
	f.convRepoPath = ""
	f.convGapSessionID = ""
	f.convStep = ""
}

// Fix implements evaluator.Fixer by sending the error, ordered file context, and metadata to the LLM and parsing the response.
func (f *Fixer) Fix(ctx context.Context, req evaluator.FixRequest) (evaluator.FixResponse, error) {
	if f.LLM == nil {
		return evaluator.FixResponse{}, fmt.Errorf("llmfix: ChatCompleter required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.MultiTurnRepair {
		f.convMsgs = nil
		f.convRepoPath = ""
		f.convGapSessionID = ""
		f.convStep = ""
	} else {
		if req.RepoPath != f.convRepoPath {
			f.convMsgs = nil
			f.convRepoPath = req.RepoPath
			f.convStep = ""
			f.convGapSessionID = ""
		}
		gapID := strings.TrimSpace(req.GapSessionID)
		if f.convGapSessionID != "" && gapID != "" && gapID != f.convGapSessionID {
			f.convMsgs = nil
		}
		if gapID != "" {
			f.convGapSessionID = gapID
		}
		if f.convStep != "" && req.Step != f.convStep {
			f.convMsgs = nil
		}
		f.convStep = req.Step
	}
	savedConv := append([]model.Message(nil), f.convMsgs...)

	system := f.Prompt
	if system == "" {
		system = defaultFixPrompt
	}
	system = augmentFixSystemPrompt(system, req)

	structuredOn := !f.DisableStructuredFixOutput
	// useStructuredUser resolves the tagged-<error>/<file> user-message layout: it's on when the
	// Fixer was constructed with StructuredUserMessage=true **or** when the current attempt has
	// crossed FixAttemptAutoEscalationThreshold (workflow mirrors this in
	// evaluator.fix_auto_escalated so the audit payload and the prompt agree). On escalated
	// attempts we also bypass the multi-turn follow-up branch: those follow-ups are shorter by
	// design and rely on prior turns carrying manifests/deps — exactly the context the model
	// isn't using when it re-emits the same broken fix on attempts 1-2. Sending a full structured
	// prompt on attempt 3+ is the intended loop-breaker.
	useStructuredUser := f.StructuredUserMessage || (req.FixAttempt >= evaluator.FixAttemptAutoEscalationThreshold)
	escalatedThisTurn := useStructuredUser && !f.StructuredUserMessage
	buildMainUser := func(retryLevel int) ([]model.Message, string) {
		lim := fixPromptLimitsForTransientRetryLevel(retryLevel)
		var u string
		switch {
		case f.MultiTurnRepair && len(f.convMsgs) > 0 && !escalatedThisTurn:
			u = buildFixFollowUpUserMessage(req, lim)
		case useStructuredUser:
			u = buildStructuredFixUserMessage(req, lim)
		default:
			u = buildFixUserMessage(req, lim)
		}
		msgs := []model.Message{{Role: "system", Content: system}}
		// Drop prior conversation turns when escalation forced structured layout: the whole point
		// of the break-out is to stop replaying the same follow-up shape that didn't converge on
		// attempts 1-2. A fresh system+user pair on attempt 3+ gives the model the full re-framed
		// context, which the tagged layout then partitions cleanly.
		if !escalatedThisTurn {
			msgs = append(msgs, f.convMsgs...)
		}
		msgs = append(msgs, model.Message{Role: "user", Content: u})
		return msgs, u
	}
	completeMain := func() (content string, sentUser string, err error) {
		opts := f.fixCompleteOpts(structuredOn, req)
		result, sentUser, err := f.completeWithRetryBuilder(ctx, buildMainUser, opts)
		if err != nil && structuredOn && isStructuredOutputAPIError(err) {
			structuredOn = false
			result, sentUser, err = f.completeWithRetryBuilder(ctx, buildMainUser, f.fixCompleteOpts(false, req))
		}
		// Large json_schema bodies + long JS/TS logs occasionally yield mid-stream EOF from the API or proxies.
		if err != nil && structuredOn && isTransientNetworkError(err) {
			structuredOn = false
			result, sentUser, err = f.completeWithRetryBuilder(ctx, buildMainUser, f.fixCompleteOpts(false, req))
		}
		if err != nil {
			return "", "", err
		}
		return result.Content, sentUser, nil
	}

	assistant1, user, err := completeMain()
	if err != nil {
		f.convMsgs = savedConv
		return evaluator.FixResponse{}, err
	}
	parsed, parseErr := parseFixResponse(assistant1)
	if parseErr != nil && structuredOn {
		structuredOn = false
		assistant1, user, err = completeMain()
		if err != nil {
			f.convMsgs = savedConv
			return evaluator.FixResponse{}, err
		}
		parsed, parseErr = parseFixResponse(assistant1)
	}
	if parseErr == nil {
		if f.MultiTurnRepair {
			f.convMsgs = append(savedConv,
				model.Message{Role: "user", Content: user},
				model.Message{Role: "assistant", Content: assistant1},
			)
			trimConvMsgs(&f.convMsgs)
		}
		return evaluator.FixResponse{Files: parsed}, nil
	}

	repairBase, _ := buildMainUser(0)
	repairMsgs := make([]model.Message, 0, len(repairBase)+2)
	repairMsgs = append(repairMsgs, repairBase...)
	repairMsgs = append(repairMsgs,
		model.Message{Role: "assistant", Content: assistant1},
		model.Message{Role: "user", Content: repairFixJSONUserMessage},
	)
	repairOpts := f.fixCompleteOpts(structuredOn, req)
	repairResult, err2 := f.completeWithRetry(ctx, repairMsgs, repairOpts)
	if err2 != nil && structuredOn && (isStructuredOutputAPIError(err2) || isTransientNetworkError(err2)) {
		repairResult, err2 = f.completeWithRetry(ctx, repairMsgs, f.fixCompleteOpts(false, req))
	}
	if err2 != nil {
		f.convMsgs = savedConv
		return evaluator.FixResponse{}, fmt.Errorf("llmfix: repair Complete failed: %w; first_parse=%v; first_preview=%q", err2, parseErr, previewForFixError(assistant1))
	}
	assistant2 := repairResult.Content
	parsed2, parseErr2 := parseFixResponse(assistant2)
	if parseErr2 != nil || len(parsed2) == 0 {
		f.convMsgs = savedConv
		return evaluator.FixResponse{}, fmt.Errorf("llmfix: invalid JSON after repair: %v (first: %v); first_preview=%q repair_preview=%q",
			parseErr2, parseErr, previewForFixError(assistant1), previewForFixError(assistant2))
	}
	if f.MultiTurnRepair {
		f.convMsgs = append(savedConv,
			model.Message{Role: "user", Content: user},
			model.Message{Role: "assistant", Content: assistant1},
			model.Message{Role: "user", Content: repairFixJSONUserMessage},
			model.Message{Role: "assistant", Content: assistant2},
		)
		trimConvMsgs(&f.convMsgs)
	}
	return evaluator.FixResponse{Files: parsed2}, nil
}

func (f *Fixer) fixCompleteOpts(structuredOn bool, req evaluator.FixRequest) model.CompleteOptions {
	opts := model.CompleteOptions{MaxTokens: 8192}
	if structuredOn {
		opts.Structured = newFixFilesStructuredSchemaForRequest(req)
	}
	return opts
}

// sleepFixerOuterRetry waits with exponential backoff and jitter before another outer fixer attempt.
func sleepFixerOuterRetry(ctx context.Context, attempt int) error {
	d := time.Duration(1<<uint(min(attempt, 3))) * time.Second
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	base := d / 2
	var jitter time.Duration
	if base > 0 {
		jitter = time.Duration(rand.Int64N(int64(base)))
	}
	sleep := base + jitter
	if sleep <= 0 {
		sleep = d
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(sleep):
		return nil
	}
}

func (f *Fixer) completeWithRetry(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	// Each call to LLM.Complete already retries internally (e.g. OpenAI: 5 attempts). Extra attempts
	// here help when EOF/reset persists across those bursts (common with large fix prompts).
	const maxRetries = 5
	var result *model.CompleteResult
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err = f.LLM.Complete(ctx, messages, opts)
		if err == nil {
			return result, nil
		}
		if attempt == maxRetries-1 {
			return nil, err
		}
		if !isTransientNetworkError(err) {
			return nil, err
		}
		if err := sleepFixerOuterRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, err
}

// completeWithRetryBuilder calls Complete; on transient errors it bumps retryLevel, rebuilds messages
// (tighter prompt budgets via build), sleeps with jitter, and retries. Returns the user string from the last build attempt.
func (f *Fixer) completeWithRetryBuilder(ctx context.Context, build func(retryLevel int) ([]model.Message, string), opts model.CompleteOptions) (*model.CompleteResult, string, error) {
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	const maxRetries = 5
	var result *model.CompleteResult
	var err error
	retryLevel := 0
	var sentUser string
	for attempt := 0; attempt < maxRetries; attempt++ {
		msgs, u := build(retryLevel)
		sentUser = u
		result, err = f.LLM.Complete(ctx, msgs, opts)
		if err == nil {
			return result, sentUser, nil
		}
		if attempt == maxRetries-1 {
			return nil, sentUser, err
		}
		if !isTransientNetworkError(err) {
			return nil, sentUser, err
		}
		retryLevel++
		if err := sleepFixerOuterRetry(ctx, attempt); err != nil {
			return nil, sentUser, err
		}
	}
	return nil, sentUser, err
}

func trimConvMsgs(msgs *[]model.Message) {
	for len(*msgs) >= 2 && convMsgsRunes(*msgs) > maxMultiTurnConvRunes {
		*msgs = (*msgs)[2:]
	}
}

func convMsgsRunes(msgs []model.Message) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
	}
	return n
}

const repairFixJSONUserMessage = `Your previous reply could not be parsed as required. Do NOT return {} or an empty object. Reply with ONLY a single JSON object (no markdown fences, no explanation): keys = repo-relative file paths (exactly as in the prompt for ARTIFACT TO FIX), values = full file content as ONE string per file using \n for newlines inside the string. Include at least one artifact path key with non-empty content.

Example: {"src/test/java/com/example/FooTest.java": "package com.example;\n\npublic class FooTest {\n}\n"}`

func previewForFixError(s string) string {
	r := []rune(strings.TrimSpace(s))
	const max = 500
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

// maxManifestRunes caps each manifest file in the prompt so package.json/pom.xml don't blow the limit.
const maxManifestRunes = 6000
const maxArtifactContextRunes = 12000

// fixPromptLimits bounds fixer user-message pieces; tightened on transient network retries (slow models / EOF).
type fixPromptLimits struct {
	MaxTotalRunes     int
	MaxErrorLogRunes  int
	ErrorLogTailRunes int
	MaxRunesPerFile   int
	MaxManifestRunes  int
}

func defaultFixPromptLimits() fixPromptLimits {
	return fixPromptLimits{
		MaxTotalRunes:     maxFixRequestRunes,
		MaxErrorLogRunes:  maxErrorLogRunes,
		ErrorLogTailRunes: errorLogTailRunes,
		MaxRunesPerFile:   maxRunesPerFile,
		MaxManifestRunes:  maxManifestRunes,
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fixPromptLimitsForTransientRetryLevel shrinks context for retryLevel 1+ after EOF/timeout-style failures (0 = defaults).
func fixPromptLimitsForTransientRetryLevel(retryLevel int) fixPromptLimits {
	d := defaultFixPromptLimits()
	if retryLevel <= 0 {
		return d
	}
	rows := []fixPromptLimits{
		{42000, 6500, 1200, 10000, 5000},
		{34000, 5000, 1000, 8500, 4200},
		{26000, 3800, 800, 7000, 3500},
		{20000, 2800, 600, 5500, 2800},
	}
	idx := retryLevel - 1
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	L := rows[idx]
	d.MaxTotalRunes = clampInt(L.MaxTotalRunes, 15000, maxFixRequestRunes)
	d.MaxErrorLogRunes = clampInt(L.MaxErrorLogRunes, 2200, maxErrorLogRunes)
	d.ErrorLogTailRunes = clampInt(L.ErrorLogTailRunes, 500, errorLogTailRunes)
	d.MaxRunesPerFile = clampInt(L.MaxRunesPerFile, 4000, maxRunesPerFile)
	d.MaxManifestRunes = clampInt(L.MaxManifestRunes, 2200, maxManifestRunes)
	return d
}

// npmPackagePromptLimits apply only when isJavaScriptOrTypeScriptFixLang(req.Lang) (see buildFixUserMessage).
// Large package.json projects can list hundreds of deps; an uncapped comma-separated line bloats prompts and contributes to API EOF/timeouts.
// Java/C# fix prompts never use this path (different manifests; Lang gate).
const (
	maxNpmPackageNamesInFixPrompt    = 300
	maxAvailableNpmPackagesLineRunes = 7500
)

// isJavaScriptOrTypeScriptFixLang matches manifestPathsForLang in internal/evaluator/workflow.go for JS/TS.
func isJavaScriptOrTypeScriptFixLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "javascript", "typescript", "js", "ts":
		return true
	default:
		return false
	}
}

// formatAvailableNpmPackagesLineForPrompt returns a bounded comma-separated list for the fixer user message.
func formatAvailableNpmPackagesLineForPrompt(names []string) string {
	if len(names) == 0 {
		return ""
	}
	omittedByCount := 0
	list := names
	if len(list) > maxNpmPackageNamesInFixPrompt {
		omittedByCount = len(list) - maxNpmPackageNamesInFixPrompt
		list = list[:maxNpmPackageNamesInFixPrompt]
	}
	s := strings.Join(list, ", ")
	if omittedByCount > 0 {
		s += fmt.Sprintf(" ... (%d more dependency names omitted; full list in package.json below)", omittedByCount)
	}
	runes := []rune(s)
	if len(runes) > maxAvailableNpmPackagesLineRunes {
		s = string(runes[:maxAvailableNpmPackagesLineRunes]) + " ... [available_npm_packages line rune-capped]"
	}
	return s
}

// buildFixUserMessage builds the user message: metadata, then dependency manifests (so LLM only uses listed packages), then error log, then files.
func buildFixUserMessage(req evaluator.FixRequest, lim fixPromptLimits) string {
	var b strings.Builder
	// --- Metadata ---
	b.WriteString("=== METADATA ===\n")
	b.WriteString(fmt.Sprintf("step: %s\n", req.Step))
	b.WriteString(fmt.Sprintf("language: %s\n", req.Lang))
	if req.FixAttempt > 0 && req.MaxFixAttempt > 0 {
		b.WriteString(fmt.Sprintf("fix_attempt: %d of %d (if previous fix failed or was insufficient, try a different approach)\n", req.FixAttempt, req.MaxFixAttempt))
	}
	if req.TestFramework != "" {
		b.WriteString(fmt.Sprintf("test_framework: %s\n", req.TestFramework))
	}
	if req.BuildTool != "" {
		b.WriteString(fmt.Sprintf("build_tool: %s\n", req.BuildTool))
	}
	if req.CompileCommand != "" {
		b.WriteString(fmt.Sprintf("compile_command: %s\n", req.CompileCommand))
	}
	if req.TestCommand != "" {
		b.WriteString(fmt.Sprintf("test_command: %s\n", req.TestCommand))
	}
	// Available packages summary from package.json (JS/TS only — same Lang family as workflow manifestPathsForLang).
	if isJavaScriptOrTypeScriptFixLang(req.Lang) {
		if names := packageNamesFromPackageJSON(req.Manifests); len(names) > 0 {
			if line := formatAvailableNpmPackagesLineForPrompt(names); line != "" {
				b.WriteString(fmt.Sprintf("available_npm_packages_use_only_these: %s\n", line))
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(fixUserTurnFocusBlock(req))
	b.WriteString("\n")
	// --- Dependency manifest(s): only use packages listed here ---
	if len(req.Manifests) > 0 {
		b.WriteString("=== DEPENDENCY MANIFEST(S) - ONLY use packages/imports listed here; do not add new packages ===\n\n")
		for path, content := range req.Manifests {
			b.WriteString(fmt.Sprintf("--- %s ---\n", path))
			runes := []rune(content)
			maxM := lim.MaxManifestRunes
			if maxM <= 0 {
				maxM = maxManifestRunes
			}
			if len(runes) > maxM {
				b.WriteString(string(runes[:maxM]))
				b.WriteString("\n\n[MANIFEST TRUNCATED]\n\n")
			} else {
				b.WriteString(content)
				b.WriteString("\n\n")
			}
		}
	}
	if len(req.ArtifactContexts) > 0 {
		b.WriteString("=== RETRIEVAL / PLANNING CONTEXT (dependency graph, fixtures/config, branch gaps, output contract) ===\n\n")
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
			b.WriteString(fmt.Sprintf("--- CONTEXT FOR ARTIFACT: %s ---\n", path))
			runes := []rune(ctx)
			if len(runes) > maxArtifactContextRunes {
				b.WriteString(string(runes[:maxArtifactContextRunes]))
				b.WriteString("\n\n[RETRIEVAL CONTEXT TRUNCATED]\n\n")
			} else {
				b.WriteString(ctx)
				b.WriteString("\n\n")
			}
		}
	}
	// --- Error log (gist when truncated); surface primary error first when possible ---
	b.WriteString("=== ERROR LOG (gist when truncated) ===\n")
	if primary := primaryErrorLine(req.ErrorOutput); primary != "" {
		b.WriteString("PRIMARY ERROR (fix this first): ")
		b.WriteString(primary)
		b.WriteString("\n\n")
	}
	errorOut := errorLogGistWithLimits(req.ErrorOutput, lim.MaxErrorLogRunes, lim.ErrorLogTailRunes)
	b.WriteString(errorOut)
	b.WriteString("\n\n")
	// --- Files: artifacts to fix first, then dependencies; error-localized windows when over per-file cap ---
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
	emitFile := func(path, content string, isArtifact bool) {
		if totalRunes >= maxTotal {
			b.WriteString(fmt.Sprintf("--- FILE: %s ---\n[OMITTED - context limit; use error log line numbers]\n\n", path))
			return
		}
		if isArtifact {
			b.WriteString(fmt.Sprintf("--- FILE (ARTIFACT TO FIX): %s ---\n", path))
		} else {
			b.WriteString(fmt.Sprintf("--- FILE (DEPENDENCY, reference only - do not modify): %s ---\n", path))
		}
		lines := lineByPath[path]
		o := winOpts
		o.IsArtifact = isArtifact
		display := errloc.FormatFileForPrompt(content, lines, o)
		b.WriteString(display)
		b.WriteString("\n\n")
		totalRunes = len([]rune(b.String()))
	}
	b.WriteString("=== FILES (artifacts to fix + dependencies for reference) ===\n\n")
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
	for path, content := range req.Files {
		if emitted[evaluator.NormalizeRepoRelPath(path)] {
			continue
		}
		emitFile(evaluator.NormalizeRepoRelPath(path), content, false)
	}
	b.WriteString("Respond with a single JSON object: { \"path/to/file\": \"full content\" }. Only the test file(s) you changed. No markdown or explanation.")
	return b.String()
}

// buildFixFollowUpUserMessage is used when MultiTurnRepair is on and convMsgs is non-empty: new error + artifact test files only (no repeated manifests / dependency sources).
func buildFixFollowUpUserMessage(req evaluator.FixRequest, lim fixPromptLimits) string {
	var b strings.Builder
	b.WriteString("=== FOLLOW-UP (multi-turn repair, same step) ===\n")
	b.WriteString("Earlier messages in this thread contain dependency manifests, reference sources, and prior errors. Your last JSON output was applied where paths were valid. The workspace was re-run; below is the **new** tool output.\n\n")
	b.WriteString("=== METADATA ===\n")
	b.WriteString(fmt.Sprintf("step: %s\n", req.Step))
	b.WriteString(fmt.Sprintf("language: %s\n", req.Lang))
	if req.FixAttempt > 0 && req.MaxFixAttempt > 0 {
		b.WriteString(fmt.Sprintf("fix_attempt: %d of %d (try a different approach if the same failure pattern repeats)\n", req.FixAttempt, req.MaxFixAttempt))
	}
	if req.TestFramework != "" {
		b.WriteString(fmt.Sprintf("test_framework: %s\n", req.TestFramework))
	}
	if req.BuildTool != "" {
		b.WriteString(fmt.Sprintf("build_tool: %s\n", req.BuildTool))
	}
	if req.CompileCommand != "" {
		b.WriteString(fmt.Sprintf("compile_command: %s\n", req.CompileCommand))
	}
	if req.TestCommand != "" {
		b.WriteString(fmt.Sprintf("test_command: %s\n", req.TestCommand))
	}
	b.WriteString("\n")
	b.WriteString(fixUserTurnFocusBlock(req))
	b.WriteString("\n")
	b.WriteString("=== NEW ERROR LOG (gist when truncated) ===\n")
	if primary := primaryErrorLine(req.ErrorOutput); primary != "" {
		b.WriteString("PRIMARY ERROR (fix this first): ")
		b.WriteString(primary)
		b.WriteString("\n\n")
	}
	b.WriteString(errorLogGistWithLimits(req.ErrorOutput, lim.MaxErrorLogRunes, lim.ErrorLogTailRunes))
	b.WriteString("\n\n")

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
	emitArtifact := func(path, content string) {
		if totalRunes >= maxTotal {
			b.WriteString(fmt.Sprintf("--- FILE: %s ---\n[OMITTED - context limit]\n\n", path))
			return
		}
		b.WriteString(fmt.Sprintf("--- FILE (ARTIFACT TO FIX): %s ---\n", path))
		lines := lineByPath[path]
		o := winOpts
		o.IsArtifact = true
		display := errloc.FormatFileForPrompt(content, lines, o)
		b.WriteString(display)
		b.WriteString("\n\n")
		totalRunes = len([]rune(b.String()))
	}
	b.WriteString("=== ARTIFACT TEST FILES (current content from disk) ===\n\n")
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
			emitArtifact(canonical, content)
			emitted[canonical] = true
			break
		}
	}
	b.WriteString("Reply with ONLY the JSON object for artifact test file(s) you change (full file content per key). Same rules as before: keys = exact artifact paths.\n")
	return b.String()
}

// primaryErrorLine returns the first line of the error output that looks like an error or failure (e.g. "[ERROR]", "error:", "failed", "exception", "cannot find"). Empty if none found. Used to surface the main issue at the top of the fix context. Avoids matching lines that merely mention the word "error" in prose.
func primaryErrorLine(s string) string {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Match common error patterns; require a strong signal so we don't surface irrelevant lines.
		if strings.Contains(lower, "[error]") || strings.Contains(lower, "error:") ||
			strings.Contains(lower, "failed") || strings.Contains(lower, "failure") ||
			strings.Contains(lower, "exception") || strings.Contains(lower, "cannot find") ||
			strings.Contains(lower, "symbol not found") || strings.Contains(lower, "undefined") ||
			strings.Contains(lower, "assertionerror") || strings.Contains(lower, "expected") && strings.Contains(lower, "but got") {
			// Keep line short for the summary; cap at 200 runes
			runes := []rune(line)
			if len(runes) > 200 {
				return string(runes[:200]) + "..."
			}
			return line
		}
	}
	return ""
}

// errorLogGist returns the error log as-is if within default limits; otherwise start + optional tail.
func errorLogGist(s string) string {
	return errorLogGistWithLimits(s, maxErrorLogRunes, errorLogTailRunes)
}

// errorLogGistWithLimits returns the error log as-is if within maxHead runes; otherwise gist (head + tail).
func errorLogGistWithLimits(s string, maxHead, maxTail int) string {
	if maxHead <= 0 {
		maxHead = maxErrorLogRunes
	}
	if maxTail <= 0 {
		maxTail = errorLogTailRunes
	}
	runes := []rune(s)
	if len(runes) <= maxHead {
		return s
	}
	head := string(runes[:maxHead])
	tailStart := len(runes) - maxTail
	if tailStart <= maxHead {
		return head + "\n\n[ERROR LOG TRUNCATED - gist only; use line numbers and messages above]\n"
	}
	tail := string(runes[tailStart:])
	return head + "\n\n[... truncated - gist only; use line numbers above ...]\n\n" + tail
}

// packageNamesFromPackageJSON returns sorted list of dependency and devDependency names from manifest map (key "package.json" or path ending in package.json). Empty if not found or parse error.
func packageNamesFromPackageJSON(manifests map[string]string) []string {
	var content string
	for path, c := range manifests {
		if strings.HasSuffix(strings.ReplaceAll(path, "\\", "/"), "package.json") {
			content = c
			break
		}
	}
	if content == "" {
		return nil
	}
	var m struct {
		Dependencies    map[string]interface{} `json:"dependencies"`
		DevDependencies map[string]interface{} `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	add := func(k string) { seen[k] = true }
	for k := range m.Dependencies {
		add(k)
	}
	for k := range m.DevDependencies {
		add(k)
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// isPathToContentMap is true when keys look like repo file paths (not {"path","content"} array-element shapes).
func isPathToContentMap(m map[string]string) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if looksLikeRepoPath(k) {
			return true
		}
	}
	return false
}

// parseFixResponse extracts path -> content from the LLM response. Tries multiple strategies so both OpenAI and Anthropic (and varied response formats) work.
func parseFixResponse(content string) (map[string]string, error) {
	content = strings.TrimSpace(content)
	parsed := make(map[string]string)

	tryParse := func(s string) bool {
		s = strings.TrimSpace(s)
		if s == "" {
			return false
		}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return false
		}
		return isPathToContentMap(parsed)
	}

	// 1) Raw JSON
	if tryParse(content) {
		return parsed, nil
	}

	// 2) First code block (between first ``` and last ```)
	stripped := extractFixResponseCodeBlock(content)
	if stripped != content && tryParse(stripped) {
		return parsed, nil
	}

	// 3) Last code block only (some models output explanation then a final ```json ... ``` with the real payload)
	if lastBlock := extractLastCodeBlock(content); lastBlock != "" && tryParse(lastBlock) {
		return parsed, nil
	}

	// 4) First top-level JSON object in text (preamble, XML, etc.)
	if obj := extractJSONObject(content); obj != "" && tryParse(obj) {
		return parsed, nil
	}

	// 5) Any top-level JSON object in the response (model may output several; use first that is a non-empty path->content map)
	for _, obj := range extractAllJSONObjects(content) {
		parsed = make(map[string]string)
		if tryParse(obj) {
			return parsed, nil
		}
	}

	// 5b) [{"path":"...","content":"..."}, ...] or wrapper {"changes":[...]}
	if m := parsePathContentArray(content); len(m) > 0 {
		return m, nil
	}
	if m := parseArrayInsideObject(content); len(m) > 0 {
		return m, nil
	}
	// 5c) {"src/test/Foo.java": "..."} with only path-like keys (rejects {"explanation":"..."})
	if m := parsePathLikeStringMapFromText(content); len(m) > 0 {
		return m, nil
	}

	// 6) Nested wrapper e.g. {"files": {"path": "content"}} or {"result": {...}}
	if m := parseNestedPathContentMap(content); len(m) > 0 {
		return m, nil
	}

	return nil, fmt.Errorf("llmfix: invalid JSON response: expected object mapping file path to content")
}

// extractFixResponseCodeBlock returns the content between the first ``` and the last ```.
// Using the last fence as closing avoids truncation when JSON string values contain ``` (e.g. in code or comments).
func extractFixResponseCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	const fence = "```"
	first := strings.Index(s, fence)
	if first < 0 {
		return s
	}
	last := strings.LastIndex(s, fence)
	if last <= first {
		return s
	}
	inner := s[first+len(fence) : last]
	if i := strings.Index(inner, "\n"); i >= 0 {
		inner = strings.TrimSpace(inner[i+1:])
	} else {
		inner = strings.TrimSpace(inner)
	}
	return strings.TrimSpace(inner)
}

// extractLastCodeBlock returns the content of the last ```...``` block only (so when the model outputs multiple blocks, we use the final one).
func extractLastCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	const fence = "```"
	last := strings.LastIndex(s, fence)
	if last < 0 {
		return ""
	}
	// Find the opening fence of this last block (last occurrence of ``` that is before last and has nothing between)
	before := s[:last]
	prev := strings.LastIndex(before, fence)
	if prev < 0 {
		return ""
	}
	inner := s[prev+len(fence) : last]
	if i := strings.Index(inner, "\n"); i >= 0 {
		inner = strings.TrimSpace(inner[i+1:])
	} else {
		inner = strings.TrimSpace(inner)
	}
	return strings.TrimSpace(inner)
}

// extractAllJSONObjects returns all top-level JSON objects {...} in s in order (brace-matched, ignoring braces inside strings).
func extractAllJSONObjects(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		obj := extractJSONObjectFrom(s, i)
		if obj != "" {
			out = append(out, obj)
			i += len(obj) - 1 // advance past this object
		}
	}
	return out
}

// extractJSONObject finds the first top-level JSON object {...} in s (brace-matched, ignoring braces inside strings) and returns it. Returns "" if none found.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	return extractJSONObjectFrom(s, start)
}

// extractJSONObjectFrom returns the brace-matched JSON object in s starting at start. Returns "" if invalid.
func extractJSONObjectFrom(s string, start int) string {
	inString := false
	var escape bool
	var quote byte
	depth := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == quote {
				inString = false
			}
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		case '"', '\'':
			inString = true
			quote = c
		}
	}
	return ""
}

func jsonStringField(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func looksLikeRepoPath(k string) bool {
	k = strings.TrimSpace(k)
	if k == "" {
		return false
	}
	lower := strings.ToLower(k)
	switch lower {
	case "files", "changes", "edits", "patches", "updates", "result", "output", "data", "content", "fix", "fixes":
		return false
	}
	return strings.Contains(k, "/") || strings.Contains(k, "\\") ||
		strings.HasSuffix(k, ".java") || strings.HasSuffix(k, ".kt") ||
		strings.HasSuffix(k, ".ts") || strings.HasSuffix(k, ".tsx") ||
		strings.HasSuffix(k, ".js") || strings.HasSuffix(k, ".jsx") || strings.HasSuffix(k, ".cs")
}

func parsePathContentArray(content string) map[string]string {
	try := func(s string) map[string]string {
		s = strings.TrimSpace(s)
		if s == "" || s[0] != '[' {
			return nil
		}
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return nil
		}
		out := make(map[string]string)
		for _, item := range arr {
			if item == nil {
				continue
			}
			p := jsonStringField(item, "path", "file", "filepath", "file_path", "filename")
			c := jsonStringField(item, "content", "body", "source", "code", "fixed", "fixed_content", "text")
			if p != "" && c != "" {
				out[p] = c
			}
		}
		if len(out) > 0 {
			return out
		}
		return nil
	}
	if m := try(content); m != nil {
		return m
	}
	if b := extractLastCodeBlock(content); b != "" {
		if m := try(b); m != nil {
			return m
		}
	}
	inner := extractFixResponseCodeBlock(content)
	if inner != content && inner != "" {
		if m := try(inner); m != nil {
			return m
		}
	}
	return nil
}

func parseArrayInsideObject(content string) map[string]string {
	var raw map[string]interface{}
	trim := strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(trim), &raw); err != nil {
		if obj := extractJSONObject(content); obj != "" {
			if err := json.Unmarshal([]byte(obj), &raw); err != nil {
				return nil
			}
		} else {
			return nil
		}
	}
	out := make(map[string]string)
	for _, v := range raw {
		arr, ok := v.([]interface{})
		if !ok {
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			p := jsonStringField(m, "path", "file", "filepath", "file_path", "filename")
			c := jsonStringField(m, "content", "body", "source", "code", "fixed", "fixed_content", "text")
			if p != "" && c != "" {
				out[p] = c
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	return nil
}

func parsePathLikeStringMapFromText(content string) map[string]string {
	candidates := []string{extractJSONObject(content), strings.TrimSpace(content)}
	if b := extractLastCodeBlock(content); b != "" {
		candidates = append(candidates, b)
	}
	for _, s := range candidates {
		if s == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			continue
		}
		out := make(map[string]string)
		for k, v := range raw {
			str, ok := v.(string)
			if !ok || str == "" || !looksLikeRepoPath(k) {
				continue
			}
			out[k] = str
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// parseNestedPathContentMap parses JSON that may wrap the path->content map in one key, e.g. {"files": {"path": "content"}}.
// Also merges path-like keys from any nested object when the top level has multiple keys.
func parseNestedPathContentMap(content string) map[string]string {
	var raw map[string]interface{}
	trim := strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(trim), &raw); err != nil {
		if obj := extractJSONObject(content); obj != "" {
			if err := json.Unmarshal([]byte(obj), &raw); err != nil {
				return nil
			}
		} else {
			return nil
		}
	}
	if len(raw) == 0 {
		return nil
	}
	if len(raw) == 1 {
		for _, v := range raw {
			if m, ok := v.(map[string]interface{}); ok {
				out := make(map[string]string)
				for k, val := range m {
					str, ok := val.(string)
					if !ok {
						return nil
					}
					out[k] = str
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	merged := make(map[string]string)
	for _, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		for k, val := range m {
			s, ok := val.(string)
			if !ok || s == "" || !looksLikeRepoPath(k) {
				continue
			}
			merged[k] = s
		}
	}
	if len(merged) > 0 {
		return merged
	}
	return nil
}

// isStructuredOutputAPIError detects provider rejections of response_format / json_schema (wrong model, proxy, or older endpoint). Used to fall back to unconstrained completion + the existing parser.
func isStructuredOutputAPIError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "response_format") ||
		strings.Contains(s, "json_schema") ||
		strings.Contains(s, "structured output") ||
		strings.Contains(s, "invalid_request_error") ||
		strings.Contains(s, "unsupported_value") ||
		strings.Contains(s, "parse the json body") ||
		strings.Contains(s, "not valid json") ||
		strings.Contains(s, "was not valid json") ||
		strings.Contains(s, "invalid payload") ||
		(strings.Contains(s, "status code: 400") && (strings.Contains(s, "response_format") || strings.Contains(s, "json_schema") || strings.Contains(s, "invalid") || strings.Contains(s, "json")))
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	s := strings.ToLower(err.Error())
	// Avoid matching arbitrary "eof" substrings in unrelated words; pair with common transport phrasing.
	eofish := strings.Contains(s, "unexpected eof") ||
		strings.Contains(s, "error: eof") ||
		strings.Contains(s, " err: eof") ||
		strings.Contains(s, "stream error: eof") ||
		strings.Contains(s, "read: eof") ||
		strings.Contains(s, "write: eof") ||
		strings.Contains(s, "tls: ") && strings.Contains(s, "eof")
	return eofish ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "tls handshake") ||
		strings.Contains(s, "connection closed") ||
		strings.Contains(s, "server closed") ||
		strings.Contains(s, "read tcp")
}

// ParsePathContentMap parses assistant text into repo-relative path → full file content using the same strategies as the evaluator fixer (raw JSON, fenced blocks, arrays, nested wrappers).
func ParsePathContentMap(content string) (map[string]string, error) {
	return parseFixResponse(content)
}

// IsStructuredOutputAPIError reports whether err indicates the provider rejected JSON-schema / structured output.
func IsStructuredOutputAPIError(err error) bool {
	return isStructuredOutputAPIError(err)
}

// IsTransientNetworkError reports whether err may succeed on retry (timeouts, connection drops).
func IsTransientNetworkError(err error) bool {
	return isTransientNetworkError(err)
}
