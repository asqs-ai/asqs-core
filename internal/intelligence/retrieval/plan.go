package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/layout"
	"github.com/asqs/asqs-core/internal/storage/metadata"
	"github.com/asqs/asqs-core/internal/workspace"
	"golang.org/x/sync/errgroup"
)

// GapKind is the reason a symbol was selected as a test gap.
type GapKind string

const (
	GapNoTests            GapKind = "no_tests"             // public method with no (detected) tests
	GapBusinessCritical   GapKind = "business_critical"    // in payment, auth, etc.
	GapLowCoverageCentral GapKind = "low_coverage_central" // central dependency, under-tested
)

// TestGap is one candidate for test generation (e.g. a method with no tests).
type TestGap struct {
	Symbol   *metadata.Symbol
	Module   string
	Kind     GapKind
	Reason   string
	Priority int
}

// GapMetaReader is the subset of metadata needed to list gaps (symbols, files, edges for centrality).
type GapMetaReader interface {
	ListSymbolsInNonTestFiles(ctx context.Context, lang, kind string) ([]*metadata.Symbol, error)
	ListSymbolsInTestFiles(ctx context.Context, lang, kind string) ([]*metadata.Symbol, error)
	ListSymbolsByFQName(ctx context.Context, fqName string) ([]*metadata.Symbol, error)
	GetFile(ctx context.Context, file string) (*metadata.File, error)
	GetSymbolByID(ctx context.Context, id string) (*metadata.Symbol, error)
	GetEdgesFrom(ctx context.Context, callerSymbolID string) ([]*metadata.Edge, error)
	GetEdgesTo(ctx context.Context, calleeSymbolID string) ([]*metadata.Edge, error)
}

// TestPlanItem is one test-gap candidate with its symbol-aware retrieval context.
type TestPlanItem struct {
	Gap     *TestGap
	Context *RetrievalContext
	// Layer is "unit" (default) or "e2e" for end-to-end plan items (E2E_SPEC, PAGE_OBJECT, USER_FLOW, …).
	Layer string
}

// TestPlan is the result of "create test/docs plan": a small set of gaps with full context for each.
type TestPlan struct {
	Items []*TestPlanItem
}

// planLayerGapAuditLabel returns a short label for audit messages ("E2E" vs "unit").
func planLayerGapAuditLabel(layer string) string {
	if strings.EqualFold(strings.TrimSpace(layer), "e2e") {
		return "E2E"
	}
	return "unit"
}

// PlanOptions configures gap selection and retrieval limits.
type PlanOptions struct {
	Lang                   string
	RepoID                 string
	CriticalModulePrefixes []string
	SkipPathPrefixes       []string // repo-relative path prefixes to exclude from gaps (e.g. "app/lib"); matches path and FQName-style (dots).
	MaxGaps                int
	MaxGapsPerFile         int // max gaps to select per source file (0 = no cap). Use 1–2 to spread selection across files so the same files are not picked every run.
	MaxDependencyChunks    int
	MaxSimilarTests        int
	MaxFixtures            int
	MaxConfigChunks        int
	MaxContextChunks       int
	DependencyMaxDepth     int
	// ProfileBudgets optional per-profile caps (canonical keys). Nil = use globals + built-in defaults only.
	ProfileBudgets map[string]config.RetrievalProfileBudget
	// SimilarMMRLambda: see ContextRequest.SimilarMMRLambda (0 = default 0.5 in Retrieve).
	SimilarMMRLambda float64
	// RetrievalProfile selects graph expansion for each gap (java_unit, http_api, e2e_playwright, react_feature, nest_module). Empty = java_unit.
	RetrievalProfile string
	// MaxGapsE2E caps E2E plan items. Java: uncovered API_ROUTE entrypoints first; else E2E_SPEC / PAGE_OBJECT / USER_FLOW in test files. JS/TS: E2E_SPEC in test files. 0 = skip E2E plan branch.
	MaxGapsE2E int
	// MaxGapsPerFileE2E caps E2E gaps per file (0 = default 2).
	MaxGapsPerFileE2E int
	// RetrievalProfileE2E selects retrieval profile for E2E items. Empty in PlanOptions is unusual (orchestrator sets DefaultRetrievalProfileE2E: http_api for Java/C#, e2e_playwright for JS/TS when config omits both profile fields).
	RetrievalProfileE2E string
	// E2EFramework is the detected stack for audit/context hints (playwright, cypress, playwright-java, playwright-dotnet, selenium, …).
	E2EFramework string
	Audit        Auditor
	// SourceFilesWithExistingTest: source file paths (e.g. "src/foo.ts") that already have a test file. Their symbols are deprioritized (not excluded) so we pick "no test file" first, then extend those files with tests for uncovered symbols.
	SourceFilesWithExistingTest map[string]struct{}
	// ExistingTestPathsBySource maps a source repo-relative path to the sorted list of existing test
	// files that back-link to it via TestPathToSourcePath. Lets the generator redirect a new artifact
	// to an already-present test file even when the repo uses a non-default suffix (e.g. XTests.java
	// when the default is XTest.java, or x.spec.ts in a jest repo). Empty / nil keeps legacy
	// behaviour (always emit SuggestedTestPath).
	ExistingTestPathsBySource map[string][]string
	// FailureHint is optional stderr/test output passed to every Retrieve (e.g. prior run or WorkflowInput). See applyFailureLocalizedRetrieval.
	FailureHint string
	// FailureHintFile is an optional repo-relative path the orchestrator reads when FailureHint is empty (see config retrieval.failure_hint_file).
	FailureHintFile string
	// DisableHybridModuleFilter when true, disables structured module filter on similar-chunk retrieval (see ContextRequest).
	DisableHybridModuleFilter bool
	// MinSimilarTestsForGeneration when > 0: skip adding a gap to the plan (abstain) if len(SimilarTests) is below this after Retrieve. 0 = disabled. See AssessSimilarReferenceSufficiency.
	MinSimilarTestsForGeneration int
	// MinSimilarityCosine when > 0: abstain when the target chunk has an embedding, at least one similar-reference chunk exists, and max cosine to any similar chunk is below this (clamped to [0,1]). 0 = disabled. When there are no similar chunks (greenfield) or the target has no embedding, this criterion is not applied.
	MinSimilarityCosine float64
	// MonoRepoGapPrefix when non-empty: unit and E2E gap listing only considers symbols whose file path is under this repo-relative prefix (the primary indexer.mono_repo_workspace). Extra indexed paths (mono_repo_extra_paths) are excluded from gap selection.
	MonoRepoGapPrefix string
}

// Auditor is the interface for run-scoped audit logging (e.g. implemented by audit.Logger).
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}

func filterSymbolsByMonoGapPrefix(syms []*metadata.Symbol, monoPrefix string) []*metadata.Symbol {
	p := strings.TrimSpace(monoPrefix)
	if p == "" {
		return syms
	}
	var out []*metadata.Symbol
	for _, s := range syms {
		if s != nil && workspace.FileUnderPrefix(s.File, p) {
			out = append(out, s)
		}
	}
	return out
}

func gapSymbolUnderMonoScope(file, monoPrefix string) bool {
	if strings.TrimSpace(monoPrefix) == "" {
		return true
	}
	return workspace.FileUnderPrefix(file, monoPrefix)
}

// maxConcurrencyListGaps limits concurrent metadata calls in ListGaps to avoid overwhelming the DB.
const maxConcurrencyListGaps = 16

// gapSymbolKindsForLang returns the symbol kinds that represent testable units for the given language.
// Java: "method". JavaScript/TS: "FUNCTION" (declarations + const arrow/async), "METHOD" (class methods), "VARIABLE" (legacy: const arrow before indexer emitted FUNCTION).
func gapSymbolKindsForLang(lang string) []string {
	switch lang {
	case "javascript", "typescript", "js", "ts":
		return []string{"FUNCTION", "METHOD", "VARIABLE"}
	default:
		return []string{"method"}
	}
}

// isPrivateJavaMethod returns true if the symbol is a Java method with visibility "private" (from signature_json).
// We do not generate tests for private members; only public (and protected) API.
func isPrivateJavaMethod(sym *metadata.Symbol) bool {
	if sym == nil || sym.Lang != "java" || sym.Kind != "method" || len(sym.SignatureJSON) == 0 {
		return false
	}
	var parsed struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(sym.SignatureJSON, &parsed); err != nil {
		return false
	}
	return strings.TrimSpace(strings.ToLower(parsed.Visibility)) == "private"
}

// ListGaps returns a small set of test-gap candidates: public methods (or functions for JS/TS) with no tests, optionally
// prioritized by business-critical modules (payment, auth, …) and central dependencies (many callers).
// Private Java methods are excluded (we do not test private members). Metadata calls (GetFile, GetEdgesTo) run concurrently with bounded concurrency.
func ListGaps(ctx context.Context, meta GapMetaReader, opts PlanOptions) ([]*TestGap, error) {
	if opts.Lang == "" {
		opts.Lang = "java"
	}
	if opts.MaxGaps <= 0 {
		opts.MaxGaps = 10
	}
	maxPerFile := opts.MaxGapsPerFile
	if maxPerFile < 0 {
		maxPerFile = 0
	}
	if maxPerFile == 0 {
		maxPerFile = 2 // default: cap per file so we don't always pick the same files
	}

	kinds := gapSymbolKindsForLang(opts.Lang)
	var allSymbols []*metadata.Symbol
	// For JS/TS, query both "javascript" and "typescript" so we get all symbols (indexer may store .ts as "typescript", .js as "javascript"; legacy data may be "javascript" only).
	langsToQuery := []string{opts.Lang}
	if opts.Lang == "typescript" || opts.Lang == "javascript" || opts.Lang == "ts" || opts.Lang == "js" {
		langsToQuery = []string{"javascript", "typescript"}
	}
	seenID := make(map[string]bool)
	for _, kind := range kinds {
		for _, lang := range langsToQuery {
			symbols, err := meta.ListSymbolsInNonTestFiles(ctx, lang, kind)
			if err != nil {
				return nil, err
			}
			for _, s := range symbols {
				if s == nil || seenID[s.ID] {
					continue
				}
				if indexer.IsTypeScriptDeclarationPath(s.File) {
					continue
				}
				seenID[s.ID] = true
				allSymbols = append(allSymbols, s)
			}
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrencyListGaps)
	var mu sync.Mutex
	var list []*TestGap
	for _, sym := range allSymbols {
		sym := sym
		g.Go(func() error {
			if isPrivateJavaMethod(sym) {
				return nil
			}
			if !gapSymbolUnderMonoScope(sym.File, opts.MonoRepoGapPrefix) {
				return nil
			}
			if len(opts.SkipPathPrefixes) > 0 && indexer.PathMatchesSkipPrefix(sym.File, opts.SkipPathPrefixes) {
				return nil
			}
			f, err := meta.GetFile(gctx, sym.File)
			if err != nil || f == nil {
				return nil
			}
			gap := &TestGap{Symbol: sym, Module: f.Module, Kind: GapNoTests, Reason: "no tests detected", Priority: 0}
			hasExistingTest := false
			if len(opts.SourceFilesWithExistingTest) > 0 {
				symPathNorm := strings.TrimPrefix(filepath.ToSlash(sym.File), "/")
				_, hasExistingTest = opts.SourceFilesWithExistingTest[symPathNorm]
			}
			if isCritical(sym.File, f.Module, opts.CriticalModulePrefixes) {
				gap.Kind = GapBusinessCritical
				gap.Reason = "business-critical module"
				gap.Priority += 30 // highest band: critical beats "no tests" and "has tests"
			}
			edgesToRaw, _ := meta.GetEdgesTo(gctx, sym.ID)
			edgesToCentrality := edgesExcludingTypes(edgesToRaw, metadata.EdgeTypeTestsSource)
			if len(edgesToCentrality) >= 3 {
				if gap.Priority < 30 {
					gap.Kind = GapLowCoverageCentral
					gap.Reason = "central dependency, under-tested"
				}
				gap.Priority += len(edgesToCentrality)
			}
			// Priority order: 1) critical, 2) modules without tests, 3) modules with tests. Deprioritize "has existing test" only so they sort last.
			if hasExistingTest {
				gap.Priority -= 20
				if gap.Reason == "no tests detected" {
					gap.Reason = "extend existing test file (add test for this symbol)"
				}
			}
			if hasInboundTestsSourceTrace(gctx, meta, sym) {
				gap.Priority -= 38
				if gap.Reason == "no tests detected" {
					gap.Reason = "traceability: tests link to this symbol (TESTS_SOURCE)"
				}
			}
			mu.Lock()
			list = append(list, gap)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	list = sortByPriority(list)
	list = selectGapsWithDiversity(list, opts.MaxGaps, maxPerFile)
	return list, nil
}

// SupportsE2EGapListing is true when ListGapsE2E can return gaps for this workflow language.
// JS/TS: E2E_SPEC. Java: API_ROUTE coverage gaps and/or E2E_SPEC, PAGE_OBJECT, USER_FLOW. C#: API_ROUTE + E2E_SPEC (indexed symbols).
func SupportsE2EGapListing(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "javascript", "typescript", "js", "ts", "java", "csharp", "cs":
		return true
	default:
		return false
	}
}

type e2eSymbolQuery struct {
	lang, kind string
}

// e2eSymbolQueriesForWorkflowLang returns DB (lang, kind) pairs for ListSymbolsInTestFiles.
func e2eSymbolQueriesForWorkflowLang(workflowLang string) []e2eSymbolQuery {
	wl := strings.ToLower(strings.TrimSpace(workflowLang))
	var out []e2eSymbolQuery
	switch wl {
	case "javascript", "js", "typescript", "ts":
		for _, l := range []string{"javascript", "typescript"} {
			out = append(out, e2eSymbolQuery{lang: l, kind: "E2E_SPEC"})
		}
	case "java":
		for _, k := range []string{"E2E_SPEC", "PAGE_OBJECT", "USER_FLOW"} {
			out = append(out, e2eSymbolQuery{lang: "java", kind: k})
		}
	case "csharp", "cs":
		out = append(out, e2eSymbolQuery{lang: "csharp", kind: "E2E_SPEC"})
	default:
		return nil
	}
	return out
}

func e2eGapBaseReasonForSymbolKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "API_ROUTE":
		return "HTTP API route — no E2E or integration test targets this endpoint yet"
	case "PAGE_ROUTE":
		return "client/UI route — add or extend Playwright/Cypress coverage that exercises this path"
	case "PAGE_OBJECT":
		return "page object — extend or improve coverage"
	case "USER_FLOW":
		return "API/user flow test — extend or improve coverage"
	default:
		return "e2e spec — extend or improve coverage"
	}
}

// jstsLangsForAPIRouteQuery returns DB langs to scan for Nest/TS API_ROUTE / PAGE_ROUTE rows.
func jstsLangsForAPIRouteQuery(workflowLang string) []string {
	wl := strings.ToLower(strings.TrimSpace(workflowLang))
	switch wl {
	case "javascript", "js", "typescript", "ts":
		return []string{"typescript", "javascript"}
	default:
		return nil
	}
}

// e2eProfileWantsJSTSSupplement is true when retrieval.profile_e2e is full_stack, react_feature, or e2e_playwright.
// Then, after API routes are exhausted (or for Java/C# when every route is covered), gap listing continues with
// JS/TS symbols and with csharp/java E2E_SPEC fallthrough — matching Java monorepo behavior for C# + e2e_playwright.
func e2eProfileWantsJSTSSupplement(opts PlanOptions) bool {
	raw := strings.TrimSpace(opts.RetrievalProfileE2E)
	if raw == "" {
		return false
	}
	p := NormalizeRetrievalProfile(RetrievalProfile(raw))
	switch p {
	case ProfileFullStack, ProfileReactFeature, ProfileE2EPlaywright:
		return true
	default:
		return false
	}
}

// effectiveJSTSLangsForE2EQuery returns DB lang tags to scan for JS/TS API_ROUTE, E2E_SPEC, and PAGE_ROUTE.
// When workflow Lang is java or csharp/cs and profile_e2e is full_stack / react_feature / e2e_playwright,
// includes typescript+javascript so React/Node packages still produce E2E gaps in JVM/.NET monorepos.
func effectiveJSTSLangsForE2EQuery(opts PlanOptions) []string {
	if langs := jstsLangsForAPIRouteQuery(opts.Lang); len(langs) > 0 {
		return langs
	}
	wl := strings.ToLower(strings.TrimSpace(opts.Lang))
	if (wl == "java" || wl == "csharp" || wl == "cs") && e2eProfileWantsJSTSSupplement(opts) {
		return []string{"typescript", "javascript"}
	}
	return nil
}

// e2eSymbolQueriesForWorkflowLangWithSupplement extends e2eSymbolQueriesForWorkflowLang with TS/JS E2E_SPEC rows
// when Lang is java or csharp/cs and profile_e2e asks for UI+API (full_stack, react_feature, e2e_playwright).
func e2eSymbolQueriesForWorkflowLangWithSupplement(opts PlanOptions) []e2eSymbolQuery {
	seen := make(map[string]bool)
	var out []e2eSymbolQuery
	add := func(qs []e2eSymbolQuery) {
		for _, q := range qs {
			k := q.lang + "\x00" + q.kind
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, q)
		}
	}
	wl := strings.ToLower(strings.TrimSpace(opts.Lang))
	add(e2eSymbolQueriesForWorkflowLang(opts.Lang))
	if (wl == "java" || wl == "csharp" || wl == "cs") && e2eProfileWantsJSTSSupplement(opts) {
		add(e2eSymbolQueriesForWorkflowLang("typescript"))
	}
	return out
}

// pageRouteE2EGapsEnabledJS returns whether ListGapsE2E should add PAGE_ROUTE anchors from indexed JS/TS.
// Disable only for explicit java_unit-style profile_e2e names (java, java_unit, unit). Unknown profile strings
// no longer map to "disable PAGE_ROUTE" (NormalizeRetrievalProfile maps unknown → java_unit for expansion, but
// that must not hide React routes). Java or C# workflow + full_stack / react / e2e_playwright enables supplement scans.
func pageRouteE2EGapsEnabledJS(opts PlanOptions) bool {
	wl := strings.ToLower(strings.TrimSpace(opts.Lang))
	jsLike := wl == "javascript" || wl == "typescript" || wl == "js" || wl == "ts"
	jvmLikeSupplement := (wl == "java" || wl == "csharp" || wl == "cs") && e2eProfileWantsJSTSSupplement(opts)
	if !jsLike && !jvmLikeSupplement {
		return false
	}
	raw := strings.TrimSpace(opts.RetrievalProfileE2E)
	if raw == "" {
		return true
	}
	r := strings.ReplaceAll(strings.ToLower(raw), "_", "-")
	switch r {
	case "java", "java-unit", "unit":
		return false
	default:
		return true
	}
}

// listUncoveredAPIRouteE2EGaps returns API_ROUTE symbols in non-test code with no TARGETS_API_ROUTE from a test-scoped
// API_CLIENT_REQUEST (Spring, NestJS, …).
func listUncoveredAPIRouteE2EGaps(ctx context.Context, meta GapMetaReader, opts PlanOptions, routes []*metadata.Symbol) ([]*TestGap, error) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrencyListGaps)
	var mu sync.Mutex
	var list []*TestGap
	for _, route := range routes {
		route := route
		g.Go(func() error {
			if route == nil || route.ID == "" {
				return nil
			}
			if !gapSymbolUnderMonoScope(route.File, opts.MonoRepoGapPrefix) {
				return nil
			}
			if len(opts.SkipPathPrefixes) > 0 && indexer.PathMatchesSkipPrefix(route.File, opts.SkipPathPrefixes) {
				return nil
			}
			f, err := meta.GetFile(gctx, route.File)
			if err != nil || f == nil {
				return nil
			}
			edgesTo, err := meta.GetEdgesTo(gctx, route.ID)
			if err != nil {
				return nil
			}
			coveredByTest := false
			for _, e := range edgesTo {
				if e == nil || strings.TrimSpace(e.EdgeType) != "TARGETS_API_ROUTE" {
					continue
				}
				caller, err := meta.GetSymbolByID(gctx, e.CallerSymbolID)
				if err != nil || caller == nil || caller.File == "" {
					continue
				}
				cf, err := meta.GetFile(gctx, caller.File)
				if err != nil || cf == nil {
					continue
				}
				if cf.IsTest {
					coveredByTest = true
					break
				}
			}
			if coveredByTest {
				return nil
			}
			baseReason := e2eGapBaseReasonForSymbolKind("API_ROUTE")
			tg := &TestGap{Symbol: route, Module: f.Module, Kind: GapNoTests, Reason: baseReason, Priority: 5}
			if isCritical(route.File, f.Module, opts.CriticalModulePrefixes) {
				tg.Kind = GapBusinessCritical
				tg.Reason = "business-critical API route with no E2E/integration test targeting it"
				tg.Priority += 30
			}
			// Outbound ROUTE_TO_HANDLER (and similar): central entrypoints bubble up.
			edgesFrom, _ := meta.GetEdgesFrom(gctx, route.ID)
			if len(edgesFrom) >= 1 {
				tg.Priority += len(edgesFrom)
			}
			mu.Lock()
			list = append(list, tg)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return list, nil
}

// ListGapsE2E returns E2E-oriented gap anchors.
//
// **API_ROUTE (Java, C#, or JS/TS Nest):** When indexed API_ROUTE symbols exist, gaps are **uncovered HTTP entrypoints**
// (no TARGETS_API_ROUTE from a test-scoped API_CLIENT_REQUEST). **C#** uses **`lang=csharp`** routes like Java.
// When every C# route is covered, there are no API_ROUTE gaps unless **retrieval.profile_e2e** is **full_stack**,
// **react_feature**, or **e2e_playwright** — then listing continues to **csharp** **E2E_SPEC** and JS/TS supplement paths.
// When there are **no** **API_ROUTE** rows for C#, execution continues to **E2E_SPEC** test-file anchors.
// **Java:** if every route is covered, **Java** returns no gaps unless **retrieval.profile_e2e** is **full_stack**,
// **react_feature**, or **e2e_playwright** (then JS/TS-indexed PAGE_ROUTE / E2E_SPEC are scanned for monorepos).
// **JS/TS** workflow lang always **falls through** to E2E_SPEC / PAGE_ROUTE.
//
// **PAGE_ROUTE (JS/TS only):** Indexed **PAGE_ROUTE** symbols from non-test files are gap candidates unless
// **profile_e2e** is explicitly **java_unit**-shaped (alias **java**). Empty **profile_e2e** and profiles like
// **e2e_playwright**, **react** / **react_feature**, **http_api**, **nest_module**, **full_stack** all allow PAGE_ROUTE gaps.
// **retrieval.profile_e2e** still only affects graph expansion for a chosen gap, not this gate.
//
// **E2E_SPEC:** Playwright/Cypress spec files in test trees (always).
//
// Java fallback without API_ROUTE: PAGE_OBJECT, USER_FLOW, E2E_SPEC in test files.
//
// Requires MaxGapsE2E > 0; uses skip/critical heuristics and diversity caps.
func ListGapsE2E(ctx context.Context, meta GapMetaReader, opts PlanOptions) ([]*TestGap, error) {
	if opts.MaxGapsE2E <= 0 {
		return nil, nil
	}
	if !SupportsE2EGapListing(opts.Lang) {
		return nil, nil
	}
	maxPerFile := opts.MaxGapsPerFileE2E
	if maxPerFile < 0 {
		maxPerFile = 0
	}
	if maxPerFile == 0 {
		maxPerFile = 2
	}
	wl := strings.ToLower(strings.TrimSpace(opts.Lang))
	if wl == "java" {
		routes, err := meta.ListSymbolsInNonTestFiles(ctx, "java", "API_ROUTE")
		if err != nil {
			return nil, err
		}
		routes = filterSymbolsByMonoGapPrefix(routes, opts.MonoRepoGapPrefix)
		if len(routes) > 0 {
			list, err := listUncoveredAPIRouteE2EGaps(ctx, meta, opts, routes)
			if err != nil {
				return nil, err
			}
			if len(list) > 0 {
				list = sortByPriority(list)
				return selectGapsWithDiversity(list, opts.MaxGapsE2E, maxPerFile), nil
			}
			if !e2eProfileWantsJSTSSupplement(opts) {
				// Pure Java E2E: no "extend spec" gaps when every API route is targeted from tests.
				return nil, nil
			}
			// Monorepo: fall through so indexed JS/TS (React/Node) can still yield PAGE_ROUTE / E2E_SPEC when profile_e2e is full_stack / react / e2e_playwright.
		}
	}

	// C# ASP.NET Core: same uncovered API_ROUTE model as Java; same e2e_playwright / full_stack fallthrough when all covered.
	if wl == "csharp" || wl == "cs" {
		routes, err := meta.ListSymbolsInNonTestFiles(ctx, "csharp", "API_ROUTE")
		if err != nil {
			return nil, err
		}
		routes = filterSymbolsByMonoGapPrefix(routes, opts.MonoRepoGapPrefix)
		if len(routes) > 0 {
			apiList, err := listUncoveredAPIRouteE2EGaps(ctx, meta, opts, routes)
			if err != nil {
				return nil, err
			}
			if len(apiList) > 0 {
				apiList = sortByPriority(apiList)
				return selectGapsWithDiversity(apiList, opts.MaxGapsE2E, maxPerFile), nil
			}
			if !e2eProfileWantsJSTSSupplement(opts) {
				// http_api-style E2E: no further anchors when every API route is targeted from tests.
				return nil, nil
			}
			// e2e_playwright / full_stack / react_feature: fall through to csharp E2E_SPEC + JS/TS supplement.
		}
	}

	// JS/TS: Nest (and future) API_ROUTE — same uncovered-route model as Java, but fall through when all covered.
	if langs := effectiveJSTSLangsForE2EQuery(opts); len(langs) > 0 {
		seenR := make(map[string]bool)
		var apiRoutes []*metadata.Symbol
		for _, l := range langs {
			routes, err := meta.ListSymbolsInNonTestFiles(ctx, l, "API_ROUTE")
			if err != nil {
				return nil, err
			}
			for _, r := range routes {
				if r == nil || r.ID == "" || seenR[r.ID] {
					continue
				}
				if indexer.IsTypeScriptDeclarationPath(r.File) {
					continue
				}
				seenR[r.ID] = true
				apiRoutes = append(apiRoutes, r)
			}
		}
		apiRoutes = filterSymbolsByMonoGapPrefix(apiRoutes, opts.MonoRepoGapPrefix)
		if len(apiRoutes) > 0 {
			list, err := listUncoveredAPIRouteE2EGaps(ctx, meta, opts, apiRoutes)
			if err != nil {
				return nil, err
			}
			if len(list) > 0 {
				list = sortByPriority(list)
				return selectGapsWithDiversity(list, opts.MaxGapsE2E, maxPerFile), nil
			}
			// All API routes covered — continue to E2E_SPEC / PAGE_ROUTE (full-stack monorepos).
		}
	}

	queries := e2eSymbolQueriesForWorkflowLangWithSupplement(opts)
	var allSymbols []*metadata.Symbol
	seenID := make(map[string]bool)
	for _, q := range queries {
		symbols, err := meta.ListSymbolsInTestFiles(ctx, q.lang, q.kind)
		if err != nil {
			return nil, err
		}
		for _, s := range symbols {
			if s != nil && !seenID[s.ID] {
				seenID[s.ID] = true
				allSymbols = append(allSymbols, s)
			}
		}
	}
	allSymbols = filterSymbolsByMonoGapPrefix(allSymbols, opts.MonoRepoGapPrefix)
	var list []*TestGap
	if len(allSymbols) > 0 {
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(maxConcurrencyListGaps)
		var mu sync.Mutex
		for _, sym := range allSymbols {
			sym := sym
			g.Go(func() error {
				if len(opts.SkipPathPrefixes) > 0 && indexer.PathMatchesSkipPrefix(sym.File, opts.SkipPathPrefixes) {
					return nil
				}
				f, err := meta.GetFile(gctx, sym.File)
				if err != nil || f == nil {
					return nil
				}
				baseReason := e2eGapBaseReasonForSymbolKind(sym.Kind)
				gap := &TestGap{Symbol: sym, Module: f.Module, Kind: GapNoTests, Reason: baseReason, Priority: 0}
				if isCritical(sym.File, f.Module, opts.CriticalModulePrefixes) {
					gap.Kind = GapBusinessCritical
					gap.Reason = "business-critical e2e coverage"
					gap.Priority += 30
				}
				edgesToRaw, _ := meta.GetEdgesTo(gctx, sym.ID)
				edgesToCentrality := edgesExcludingTypes(edgesToRaw, metadata.EdgeTypeTestsSource)
				if len(edgesToCentrality) >= 2 {
					gap.Priority += len(edgesToCentrality)
					if gap.Kind == GapNoTests {
						gap.Kind = GapLowCoverageCentral
						if strings.TrimSpace(sym.Kind) == "PAGE_OBJECT" {
							gap.Reason = "page object linked to many symbols"
						} else if strings.TrimSpace(sym.Kind) == "USER_FLOW" {
							gap.Reason = "user flow linked to many symbols"
						} else {
							gap.Reason = "e2e spec linked to many symbols"
						}
					}
				}
				mu.Lock()
				list = append(list, gap)
				mu.Unlock()
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	// UI routes (React Router, Angular, …): gap anchors for JS/TS unless profile_e2e is explicitly java_unit-shaped.
	if pageRouteE2EGapsEnabledJS(opts) {
		if langs := effectiveJSTSLangsForE2EQuery(opts); len(langs) > 0 {
			seenID := make(map[string]bool)
			for _, g := range list {
				if g != nil && g.Symbol != nil && g.Symbol.ID != "" {
					seenID[g.Symbol.ID] = true
				}
			}
			for _, l := range langs {
				pages, err := meta.ListSymbolsInNonTestFiles(ctx, l, "PAGE_ROUTE")
				if err != nil {
					return nil, err
				}
				for _, sym := range pages {
					if sym == nil || sym.ID == "" || seenID[sym.ID] {
						continue
					}
					if !gapSymbolUnderMonoScope(sym.File, opts.MonoRepoGapPrefix) {
						continue
					}
					if indexer.IsTypeScriptDeclarationPath(sym.File) {
						continue
					}
					if len(opts.SkipPathPrefixes) > 0 && indexer.PathMatchesSkipPrefix(sym.File, opts.SkipPathPrefixes) {
						continue
					}
					f, err := meta.GetFile(ctx, sym.File)
					if err != nil || f == nil {
						continue
					}
					gap := &TestGap{
						Symbol: sym, Module: f.Module, Kind: GapNoTests,
						Reason: e2eGapBaseReasonForSymbolKind("PAGE_ROUTE"), Priority: 3,
					}
					if isCritical(sym.File, f.Module, opts.CriticalModulePrefixes) {
						gap.Kind = GapBusinessCritical
						gap.Reason = "business-critical UI route — add or extend E2E coverage"
						gap.Priority += 30
					}
					edgesToRaw, _ := meta.GetEdgesTo(ctx, sym.ID)
					edgesToCentrality := edgesExcludingTypes(edgesToRaw, metadata.EdgeTypeTestsSource)
					if len(edgesToCentrality) >= 2 && gap.Kind == GapNoTests {
						gap.Kind = GapLowCoverageCentral
						gap.Reason = "UI route linked to many graph symbols — prioritize E2E"
						gap.Priority += len(edgesToCentrality)
					}
					seenID[sym.ID] = true
					list = append(list, gap)
				}
			}
		}
	}

	if len(list) == 0 {
		return nil, nil
	}
	list = sortByPriority(list)
	list = selectGapsWithDiversity(list, opts.MaxGapsE2E, maxPerFile)
	return list, nil
}

func isCritical(filePath, module string, prefixes []string) bool {
	lower := strings.ToLower(filePath + " " + module)
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func sortByPriority(list []*TestGap) []*TestGap {
	// Sort by Priority descending; break ties by Symbol.ID for stable, deterministic order.
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			higher := list[j].Priority > list[j-1].Priority
			tie := list[j].Priority == list[j-1].Priority && list[j].Symbol != nil && list[j-1].Symbol != nil && list[j].Symbol.ID < list[j-1].Symbol.ID
			if higher || tie {
				list[j], list[j-1] = list[j-1], list[j]
			} else {
				break
			}
		}
	}
	return list
}

// selectGapsWithDiversity returns up to maxGaps from the sorted list, capping at maxPerFile gaps per file when maxPerFile > 0 so different files are picked across runs.
func selectGapsWithDiversity(list []*TestGap, maxGaps, maxPerFile int) []*TestGap {
	if maxGaps <= 0 || len(list) == 0 {
		return list
	}
	if maxPerFile <= 0 {
		if len(list) > maxGaps {
			return list[:maxGaps]
		}
		return list
	}
	var out []*TestGap
	perFile := make(map[string]int)
	for _, g := range list {
		if len(out) >= maxGaps {
			break
		}
		fileNorm := strings.TrimPrefix(filepath.ToSlash(g.Symbol.File), "/")
		if perFile[fileNorm] >= maxPerFile {
			continue
		}
		out = append(out, g)
		perFile[fileNorm]++
	}
	return out
}

// DefaultCriticalPrefixes returns common business-critical path/module keywords.
func DefaultCriticalPrefixes() []string {
	return []string{"payment", "auth", "security", "order", "billing", "user"}
}

// maxConcurrencyCreateTestPlan limits concurrent Retrieve calls in CreateTestPlan (each does metadata + chunk reads).
const maxConcurrencyCreateTestPlan = 8

type testPlanBuildParams struct {
	retrievalProfile string
	auditStartStep   string
	auditDoneStep    string
	layer            string
}

// CreateTestPlan picks test gaps (e.g. public methods with no tests, critical modules, central deps)
// then retrieves symbol-aware context for each: method + class, dependencies, domain models,
// similar tests, fixtures, config. Retrieve runs concurrently with bounded concurrency; results
// are assembled in gap order for stable audit and plan.Items.
func CreateTestPlan(ctx context.Context, gapMeta GapMetaReader, retrievalMeta MetaReader, chunks ChunkReader, opts PlanOptions) (*TestPlan, error) {
	gapList, err := ListGaps(ctx, gapMeta, opts)
	if err != nil {
		return nil, err
	}
	return createTestPlanFromGaps(ctx, gapMeta, retrievalMeta, chunks, opts, gapList, testPlanBuildParams{
		retrievalProfile: opts.RetrievalProfile,
		auditStartStep:   "retrieve.plan_start",
		auditDoneStep:    "retrieve.plan_done",
		layer:            "unit",
	})
}

// CreateE2ETestPlan builds a plan from E2E-oriented gaps (JS/TS: E2E_SPEC; Java: uncovered API_ROUTE, else test-file E2E symbols). No-op when MaxGapsE2E <= 0 or none found.
func CreateE2ETestPlan(ctx context.Context, gapMeta GapMetaReader, retrievalMeta MetaReader, chunks ChunkReader, opts PlanOptions) (*TestPlan, error) {
	if opts.MaxGapsE2E <= 0 {
		return &TestPlan{}, nil
	}
	gapList, err := ListGapsE2E(ctx, gapMeta, opts)
	if err != nil {
		return nil, err
	}
	if len(gapList) == 0 {
		if opts.Audit != nil && SupportsE2EGapListing(opts.Lang) {
			fields := map[string]interface{}{
				"message": fmt.Sprintf("E2E plan branch: no gaps for lang %s. Java: no uncovered API_ROUTE and no E2E_SPEC/PAGE_OBJECT/USER_FLOW. C#: no uncovered API_ROUTE and no E2E_SPEC (if all routes are covered, use profile_e2e e2e_playwright or full_stack to list E2E_SPEC). JS/TS: no uncovered API_ROUTE, no E2E_SPEC, and no usable indexed PAGE_ROUTE — or profile_e2e is java_unit, skip_path_prefixes, route files marked is_test (path heuristics), or indexer skipped router enrichers. Re-index if needed.", opts.Lang),
				"lang":    opts.Lang, "max_gaps_e2e": opts.MaxGapsE2E,
				// Note: unit-plan retrieval.profile is separate; E2E gaps use retrieval.profile_e2e (PlanOptions.RetrievalProfileE2E).
				"retrieval_profile_e2e":       strings.TrimSpace(opts.RetrievalProfileE2E),
				"page_route_e2e_gaps_enabled": pageRouteE2EGapsEnabledJS(opts),
			}
			wl := strings.ToLower(strings.TrimSpace(opts.Lang))
			if wl == "csharp" || wl == "cs" {
				routes, _ := gapMeta.ListSymbolsInNonTestFiles(ctx, "csharp", "API_ROUTE")
				fields["indexed_csharp_api_route_count"] = len(routes)
				if specs, err := gapMeta.ListSymbolsInTestFiles(ctx, "csharp", "E2E_SPEC"); err == nil {
					fields["indexed_csharp_e2e_spec_count"] = len(specs)
				}
			}
			if wl == "javascript" || wl == "typescript" || wl == "js" || wl == "ts" {
				pr := 0
				for _, l := range []string{"typescript", "javascript"} {
					syms, _ := gapMeta.ListSymbolsInNonTestFiles(ctx, l, "PAGE_ROUTE")
					pr += len(syms)
				}
				fields["indexed_page_route_non_test_count"] = pr
				e2eN := 0
				for _, l := range []string{"typescript", "javascript"} {
					syms, _ := gapMeta.ListSymbolsInTestFiles(ctx, l, "E2E_SPEC")
					e2eN += len(syms)
				}
				fields["indexed_e2e_spec_count"] = e2eN
			}
			opts.Audit.Log(ctx, "retrieve.e2e_gaps_none", fields)
		}
		return &TestPlan{}, nil
	}
	profE2E := strings.TrimSpace(opts.RetrievalProfileE2E)
	if profE2E == "" {
		if strings.ToLower(strings.TrimSpace(opts.Lang)) == "java" {
			profE2E = string(ProfileJavaUnit)
		} else {
			profE2E = string(ProfileE2EPlaywright)
		}
	}
	return createTestPlanFromGaps(ctx, gapMeta, retrievalMeta, chunks, opts, gapList, testPlanBuildParams{
		retrievalProfile: profE2E,
		auditStartStep:   "retrieve.e2e_plan_start",
		auditDoneStep:    "retrieve.e2e_plan_done",
		layer:            "e2e",
	})
}

func createTestPlanFromGaps(ctx context.Context, gapMeta GapMetaReader, retrievalMeta MetaReader, chunks ChunkReader, opts PlanOptions, gapList []*TestGap, p testPlanBuildParams) (*TestPlan, error) {
	if opts.Audit != nil {
		prof := string(NormalizeRetrievalProfile(RetrievalProfile(p.retrievalProfile)))
		payload := map[string]interface{}{
			"message":           fmt.Sprintf("Creating %s test plan: %d gaps to retrieve context for (repo %s, lang %s).", p.layer, len(gapList), opts.RepoID, opts.Lang),
			"gaps_count":        len(gapList),
			"repo_id":           opts.RepoID,
			"lang":              opts.Lang,
			"retrieval_profile": prof,
			"plan_layer":        p.layer,
		}
		if strings.TrimSpace(opts.E2EFramework) != "" {
			payload["e2e_framework"] = strings.TrimSpace(opts.E2EFramework)
		}
		opts.Audit.Log(ctx, p.auditStartStep, payload)
	}
	plan := &TestPlan{}
	abstained := 0
	retrieveCache := newWithinRunRetrieveCache()

	type indexedResult struct {
		index int
		item  *TestPlanItem
		err   error
	}
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(maxConcurrencyCreateTestPlan)
	var mu sync.Mutex
	results := make([]indexedResult, 0, len(gapList))
	profileForRetrieve := NormalizeRetrievalProfile(RetrievalProfile(p.retrievalProfile))
	for i, gap := range gapList {
		i, gap := i, gap
		eg.Go(func() error {
			sim, dep, fix := ResolveRetrievalBudgets(profileForRetrieve, opts.MaxSimilarTests, opts.MaxDependencyChunks, opts.MaxFixtures, opts.ProfileBudgets)
			req := ContextRequest{
				SymbolID:                  gap.Symbol.ID,
				Lang:                      gap.Symbol.Lang,
				RepoID:                    opts.RepoID,
				Profile:                   profileForRetrieve,
				MaxDependencyChunks:       dep,
				MaxSimilarTests:           sim,
				MaxFixtures:               fix,
				MaxConfigChunks:           opts.MaxConfigChunks,
				MaxContextChunks:          opts.MaxContextChunks,
				DependencyMaxDepth:        opts.DependencyMaxDepth,
				SimilarMMRLambda:          opts.SimilarMMRLambda,
				FailureHint:               opts.FailureHint,
				DisableHybridModuleFilter: opts.DisableHybridModuleFilter,
			}
			normReq := normalizeContextRequestForRetrieveKey(req)
			cacheKey := retrievalCacheKey(normReq)
			ctxBundle, err := retrieveCache.getOrRetrieve(gctx, cacheKey, func() (*RetrievalContext, error) {
				return Retrieve(gctx, retrievalMeta, chunks, normReq)
			})
			mu.Lock()
			results = append(results, indexedResult{index: i, item: &TestPlanItem{Gap: gap, Context: ctxBundle, Layer: p.layer}, err: err})
			mu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	sort.Slice(results, func(a, b int) bool { return results[a].index < results[b].index })
	for _, r := range results {
		if r.err != nil {
			if opts.Audit != nil && r.item != nil {
				lbl := planLayerGapAuditLabel(p.layer)
				opts.Audit.LogError(ctx, "retrieve.gap_error", map[string]interface{}{
					"message":    fmt.Sprintf("Retrieval failed for %s gap %s: %s", lbl, r.item.Gap.Symbol.FQName, r.err.Error()),
					"gap":        r.item.Gap.Symbol.FQName,
					"error":      r.err.Error(),
					"plan_layer": p.layer,
				})
			}
			continue
		}
		lbl := planLayerGapAuditLabel(p.layer)
		tgtEmb := TargetMethodEmbedding(r.item.Context)
		suffOK, suffReason, simN, maxCos, cosApplied := AssessSimilarReferenceSufficiency(
			tgtEmb, r.item.Context.SimilarTests, opts.MinSimilarTestsForGeneration, opts.MinSimilarityCosine)
		if !suffOK {
			abstained++
			if opts.Audit != nil {
				opts.Audit.Log(ctx, "retrieve.gap_abstained_retrieval", map[string]interface{}{
					"message": fmt.Sprintf("Abstaining from generation for %s gap %s: %s (manual follow-up).", lbl, r.item.Gap.Symbol.FQName, suffReason),
					"gap":     r.item.Gap.Symbol.FQName, "symbol_id": r.item.Gap.Symbol.ID,
					"plan_layer": p.layer, "reason_detail": suffReason,
					"similar_reference_count":          simN,
					"max_cosine_similarity":            maxCos,
					"cosine_criterion_applied":         cosApplied,
					"min_similar_tests_for_generation": opts.MinSimilarTestsForGeneration,
					"min_similarity_cosine":            opts.MinSimilarityCosine,
				})
			}
			continue
		}
		symFileNorm := filepath.ToSlash(strings.TrimSpace(r.item.Gap.Symbol.File))
		_, hasExistingTests := opts.SourceFilesWithExistingTest[symFileNorm]
		r.item.Context.ExistingTestCoverage = buildExistingTestCoverageHint(r.item.Context, hasExistingTests)
		if paths := opts.ExistingTestPathsBySource[symFileNorm]; len(paths) > 0 {
			// Copy + normalise so downstream sees slash-separated, sorted, dedup'd repo-relative paths.
			seen := make(map[string]struct{}, len(paths))
			cleaned := make([]string, 0, len(paths))
			for _, p := range paths {
				n := filepath.ToSlash(strings.TrimSpace(p))
				if n == "" {
					continue
				}
				if _, dup := seen[n]; dup {
					continue
				}
				seen[n] = struct{}{}
				cleaned = append(cleaned, n)
			}
			sort.Strings(cleaned)
			r.item.Context.ExistingTestPaths = cleaned
		}
		if opts.Audit != nil {
			prof := string(profileForRetrieve)
			payload := retrievalContextAuditSummary(r.item.Context, prof)
			payload["message"] = fmt.Sprintf("Retrieved context for %s gap %d: %s.", lbl, r.index+1, r.item.Gap.Symbol.FQName)
			payload["gap_index"] = r.index
			payload["symbol_id"] = r.item.Gap.Symbol.ID
			payload["fq_name"] = r.item.Gap.Symbol.FQName
			payload["plan_layer"] = p.layer
			opts.Audit.Log(ctx, "retrieve.gap_retrieved", payload)
		}
		plan.Items = append(plan.Items, r.item)
	}
	if opts.Audit != nil {
		prof := string(profileForRetrieve)
		sumDeps, sumSimilar, sumRelated := 0, 0, 0
		sumSimilarSegmented, sumSimilarReassembled := 0, 0
		sumExistingTestHints, sumMissingBranchIntents := 0, 0
		for _, it := range plan.Items {
			if it == nil || it.Context == nil {
				continue
			}
			sumDeps += len(it.Context.Dependencies)
			sumSimilar += len(it.Context.SimilarTests)
			sumRelated += len(it.Context.RelatedChunks)
			segmented, reassembled := similarSegmentationCounts(it.Context.SimilarTests)
			sumSimilarSegmented += segmented
			sumSimilarReassembled += reassembled
			if it.Context.ExistingTestCoverage != nil && it.Context.ExistingTestCoverage.HasExistingTests {
				sumExistingTestHints++
				sumMissingBranchIntents += len(it.Context.ExistingTestCoverage.MissingIntents)
			}
		}
		donePayload := map[string]interface{}{
			"message":                      fmt.Sprintf("%s test plan done: %d items with context (from %d gaps, %d abstained on retrieval sufficiency).", p.layer, len(plan.Items), len(gapList), abstained),
			"items_count":                  len(plan.Items),
			"gaps_total":                   len(gapList),
			"abstained_count":              abstained,
			"retrieval_profile":            prof,
			"plan_layer":                   p.layer,
			"total_deps":                   sumDeps,
			"total_similar":                sumSimilar,
			"total_similar_segmented":      sumSimilarSegmented,
			"total_similar_reassembled":    sumSimilarReassembled,
			"total_related":                sumRelated,
			"items_with_existing_tests":    sumExistingTestHints,
			"total_missing_branch_intents": sumMissingBranchIntents,
			// Within-run Retrieve memoization (see retrieve_cache.go): fast path = map hit after first success; coalesce = concurrent duplicate key waited on singleflight.
			"retrieve_within_run_cache_fast_hits":     retrieveCache.fastPathHits(),
			"retrieve_within_run_cache_coalesce_hits": retrieveCache.coalesceHits(),
		}
		if strings.TrimSpace(opts.E2EFramework) != "" {
			donePayload["e2e_framework"] = strings.TrimSpace(opts.E2EFramework)
		}
		opts.Audit.Log(ctx, p.auditDoneStep, donePayload)
	}
	return plan, nil
}

// TestPathToSourcePath returns the source file path for a test file path, or "" if path is not a recognized test file.
// Used to build SourceFilesWithExistingTest from CurrentFiles so we exclude those sources from the gap list.
// repoRoot is the repo root on disk; when set, C# and JS/TS tests under a dedicated root (tests/, UnitTests/, …)
// map back to src/… or other mirrored production paths (see internal/layout).
func TestPathToSourcePath(testFilePath string, lang string, testFramework string, repoRoot string) string {
	p := filepath.ToSlash(strings.TrimSpace(testFilePath))
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	lang = strings.ToLower(strings.TrimSpace(lang))
	testFramework = strings.ToLower(strings.TrimSpace(testFramework))

	switch lang {
	case "java":
		// Accept Tests, IT, Test suffixes (matches UnderTestClassFQNameFromTestClassFQ). When repoRoot
		// is set, prefer the candidate whose mapped src/main/java path exists on disk so a repo using
		// XTests.java doesn't silently fall through to XTest.java when both mappings are syntactically valid.
		javaSuffixes := []string{"Tests", "IT", "Test"}
		type javaCand struct{ stem, sourcePath string }
		var cands []javaCand
		for _, suf := range javaSuffixes {
			if !strings.HasSuffix(name, suf) || len(name) <= len(suf) {
				continue
			}
			stem := name[:len(name)-len(suf)]
			if stem == "" {
				continue
			}
			sourceName := stem + ext
			var sp string
			if strings.Contains(p, "src/test/java") {
				replaced := strings.Replace(p, "src/test/java", "src/main/java", 1)
				sp = filepath.Join(filepath.Dir(replaced), sourceName)
			} else {
				sp = filepath.Join(dir, sourceName)
			}
			cands = append(cands, javaCand{stem: stem, sourcePath: filepath.ToSlash(sp)})
		}
		if len(cands) == 0 {
			return ""
		}
		trimmedRepo := strings.TrimSpace(repoRoot)
		if trimmedRepo != "" {
			repoAbs := filepath.Clean(trimmedRepo)
			for _, c := range cands {
				full := filepath.Join(repoAbs, filepath.FromSlash(c.sourcePath))
				if st, err := os.Stat(full); err == nil && !st.IsDir() {
					return c.sourcePath
				}
			}
			// repoRoot was provided but no candidate maps to a real on-disk source. Bail out
			// instead of returning a syntactically valid but wrong mapping (e.g. OwnerControllerE2EIT.java
			// -> OwnerControllerE2E.java when only OwnerController.java exists). Saves callers from
			// polluting ExistingTestPathsBySource with phantom sources. Fallback to cands[0] still
			// applies when repoRoot is empty (caller didn't opt into on-disk verification).
			return ""
		}
		return cands[0].sourcePath
	case "csharp", "cs":
		if src := layout.SourcePathFromCSharpTestFile(p, repoRoot); src != "" {
			return src
		}
		// Sibling layout: FooTests.cs next to Foo.cs when not under a dedicated root segment.
		if strings.HasSuffix(name, "Tests") && len(name) > 5 {
			return filepath.Join(dir, name[:len(name)-5]+ext)
		}
		if strings.HasSuffix(name, "Test") && len(name) > 4 && !strings.EqualFold(name, "Test") {
			return filepath.Join(dir, name[:len(name)-4]+ext)
		}
		return ""
	case "javascript", "typescript", "js", "ts":
		if src := layout.SourcePathFromJSTSTestFile(p, repoRoot, testFramework); src != "" {
			return src
		}
		// Sibling layout: accept both .test and .spec regardless of the configured framework; prefer
		// whichever candidate exists on disk so a jest repo that happens to use x.spec.ts still maps
		// back to x.ts. Framework is only used as a tiebreaker when neither or both candidates exist.
		type jstsCand struct{ suffix, sourcePath string }
		var cands []jstsCand
		for _, suf := range []string{".test", ".spec"} {
			if !strings.HasSuffix(name, suf) || len(name) <= len(suf) {
				continue
			}
			sourceName := strings.TrimSuffix(name, suf) + ext
			cands = append(cands, jstsCand{suffix: suf, sourcePath: filepath.ToSlash(filepath.Join(dir, sourceName))})
		}
		if len(cands) == 0 {
			return ""
		}
		repoAbs := filepath.Clean(strings.TrimSpace(repoRoot))
		if repoAbs != "" {
			for _, c := range cands {
				full := filepath.Join(repoAbs, filepath.FromSlash(c.sourcePath))
				if st, err := os.Stat(full); err == nil && !st.IsDir() {
					return c.sourcePath
				}
			}
		}
		// No disk evidence: fall back to framework preference (.spec for jasmine, .test otherwise).
		preferred := ".test"
		if testFramework == "jasmine" {
			preferred = ".spec"
		}
		for _, c := range cands {
			if c.suffix == preferred {
				return c.sourcePath
			}
		}
		return cands[0].sourcePath
	default:
		if !strings.HasSuffix(name, "Test") || len(name) <= 4 {
			return ""
		}
		sourceName := name[:len(name)-4] + ext
		return filepath.Join(dir, sourceName)
	}
}
