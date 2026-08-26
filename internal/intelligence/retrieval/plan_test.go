package retrieval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func TestIsPrivateJavaMethod(t *testing.T) {
	t.Run("nil_or_non_java", func(t *testing.T) {
		if isPrivateJavaMethod(nil) {
			t.Error("nil: want false")
		}
		if isPrivateJavaMethod(&metadata.Symbol{Lang: "csharp", Kind: "method"}) {
			t.Error("csharp: want false")
		}
		if isPrivateJavaMethod(&metadata.Symbol{Lang: "java", Kind: "class"}) {
			t.Error("java class: want false")
		}
	})
	t.Run("private", func(t *testing.T) {
		sym := &metadata.Symbol{Lang: "java", Kind: "method", SignatureJSON: []byte(`{"signature":"void run()","visibility":"private"}`)}
		if !isPrivateJavaMethod(sym) {
			t.Error("private method: want true")
		}
	})
	t.Run("public", func(t *testing.T) {
		sym := &metadata.Symbol{Lang: "java", Kind: "method", SignatureJSON: []byte(`{"signature":"void run()","visibility":"public"}`)}
		if isPrivateJavaMethod(sym) {
			t.Error("public method: want false")
		}
	})
	t.Run("no_visibility_in_json", func(t *testing.T) {
		sym := &metadata.Symbol{Lang: "java", Kind: "method", SignatureJSON: []byte(`{"signature":"void run()"}`)}
		if isPrivateJavaMethod(sym) {
			t.Error("no visibility: want false")
		}
	})
}

func TestGapSymbolKindsForLang(t *testing.T) {
	tests := []struct {
		lang string
		want []string
	}{
		{"java", []string{"method"}},
		{"csharp", []string{"method"}},
		{"javascript", []string{"FUNCTION", "METHOD", "VARIABLE"}},
		{"typescript", []string{"FUNCTION", "METHOD", "VARIABLE"}},
		{"js", []string{"FUNCTION", "METHOD", "VARIABLE"}},
		{"ts", []string{"FUNCTION", "METHOD", "VARIABLE"}},
	}
	for _, tt := range tests {
		got := gapSymbolKindsForLang(tt.lang)
		if len(got) != len(tt.want) {
			t.Errorf("gapSymbolKindsForLang(%q) = %v; want %v", tt.lang, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("gapSymbolKindsForLang(%q) = %v; want %v", tt.lang, got, tt.want)
				break
			}
		}
	}
}

func TestDefaultCriticalPrefixes(t *testing.T) {
	p := DefaultCriticalPrefixes()
	if len(p) == 0 {
		t.Fatal("DefaultCriticalPrefixes() returned empty slice")
	}
	want := map[string]bool{"payment": true, "auth": true, "security": true, "order": true, "billing": true, "user": true}
	for _, s := range p {
		if !want[s] {
			t.Errorf("unexpected prefix %q", s)
		}
	}
}

func TestIsCritical_match(t *testing.T) {
	prefixes := []string{"payment", "auth"}
	if !isCritical("src/payment/Handler.java", "payment-api", prefixes) {
		t.Error("isCritical(payment path): want true")
	}
	if !isCritical("AuthService.java", "auth", prefixes) {
		t.Error("isCritical(auth module): want true")
	}
	if !isCritical("PAYMENT/foo.java", "other", prefixes) {
		t.Error("isCritical(uppercase payment): want true")
	}
}

func TestIsCritical_noMatch(t *testing.T) {
	prefixes := []string{"payment", "auth"}
	if isCritical("src/user/Helper.java", "util", prefixes) {
		t.Error("isCritical(non-matching): want false")
	}
	if isCritical("", "", prefixes) {
		t.Error("isCritical(empty): want false")
	}
}

func TestIsCritical_emptyPrefixes(t *testing.T) {
	if isCritical("payment/foo.java", "payment", nil) {
		t.Error("isCritical with nil prefixes: want false")
	}
	if isCritical("payment/foo.java", "payment", []string{}) {
		t.Error("isCritical with empty prefixes: want false")
	}
}

func TestSortByPriority(t *testing.T) {
	low := &TestGap{Priority: 1}
	mid := &TestGap{Priority: 5}
	high := &TestGap{Priority: 10}
	list := []*TestGap{low, high, mid}
	list = sortByPriority(list)
	if list[0].Priority != 10 || list[1].Priority != 5 || list[2].Priority != 1 {
		t.Errorf("sortByPriority: want [10,5,1]; got [%d,%d,%d]", list[0].Priority, list[1].Priority, list[2].Priority)
	}
}

func TestSelectGapsWithDiversity(t *testing.T) {
	// 3 gaps from A, 3 from B; maxGaps=4, maxPerFile=2 -> expect 2 from A, 2 from B (spread)
	mk := func(file string, pri int) *TestGap {
		return &TestGap{Symbol: &metadata.Symbol{File: file}, Priority: pri}
	}
	list := []*TestGap{
		mk("a.go", 10), mk("a.go", 10), mk("a.go", 10),
		mk("b.go", 10), mk("b.go", 10), mk("b.go", 10),
	}
	got := selectGapsWithDiversity(list, 4, 2)
	if len(got) != 4 {
		t.Fatalf("selectGapsWithDiversity(maxGaps=4, maxPerFile=2): got len %d; want 4", len(got))
	}
	var countA, countB int
	for _, g := range got {
		if g.Symbol.File == "a.go" {
			countA++
		} else {
			countB++
		}
	}
	if countA != 2 || countB != 2 {
		t.Errorf("selectGapsWithDiversity: want 2 from a.go, 2 from b.go; got %d from a.go, %d from b.go", countA, countB)
	}
	// maxPerFile=0 -> no cap, take first maxGaps
	got2 := selectGapsWithDiversity(list, 4, 0)
	if len(got2) != 4 {
		t.Fatalf("selectGapsWithDiversity(maxPerFile=0): got len %d; want 4", len(got2))
	}
	if got2[0].Symbol.File != "a.go" || got2[1].Symbol.File != "a.go" {
		t.Errorf("selectGapsWithDiversity(maxPerFile=0): expected first 4 from list (all a.go); got %s, %s", got2[0].Symbol.File, got2[1].Symbol.File)
	}
}

func TestListGaps_mockMeta(t *testing.T) {
	// sym1: payment module -> business_critical; sym2: util module with 3 callers -> low_coverage_central
	sym1 := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", FQName: "com.payment.Handler.run", File: "payment/Handler.java"}
	sym2 := &metadata.Symbol{ID: "s2", Lang: "java", Kind: "method", FQName: "com.util.Helper.run", File: "util/Helper.java"}
	file1 := &metadata.File{File: "payment/Handler.java", Module: "payment-api", IsTest: false}
	file2 := &metadata.File{File: "util/Helper.java", Module: "util", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym1, sym2},
		files:   map[string]*metadata.File{"payment/Handler.java": file1, "util/Helper.java": file2},
		edgesTo: map[string][]*metadata.Edge{
			"s1": nil,
			"s2": {{CalleeSymbolID: "s2"}, {CalleeSymbolID: "s2"}, {CalleeSymbolID: "s2"}}, // 3 callers -> central
		},
	}
	opts := PlanOptions{Lang: "java", RepoID: "org/repo", MaxGaps: 10, CriticalModulePrefixes: []string{"payment"}}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("ListGaps: got %d gaps; want 2", len(gaps))
	}
	var sawCritical, sawCentral bool
	for _, g := range gaps {
		if g.Kind == GapBusinessCritical {
			sawCritical = true
		}
		if g.Kind == GapLowCoverageCentral {
			sawCentral = true
		}
	}
	if !sawCritical {
		t.Error("expected at least one GapBusinessCritical (payment module)")
	}
	if !sawCentral {
		t.Error("expected at least one GapLowCoverageCentral (3+ edges, non-critical)")
	}
}

func TestListGaps_excludesTypeScriptDeclarationFiles(t *testing.T) {
	symDTS := &metadata.Symbol{ID: "d1", Lang: "typescript", Kind: "FUNCTION", FQName: "X", File: "apps/web/src/compat.d.ts"}
	symTS := &metadata.Symbol{ID: "s1", Lang: "typescript", Kind: "FUNCTION", FQName: "Y", File: "apps/web/src/util.ts"}
	fileDTS := &metadata.File{File: "apps/web/src/compat.d.ts", Module: "web", IsTest: false}
	fileTS := &metadata.File{File: "apps/web/src/util.ts", Module: "web", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{symDTS, symTS},
		files:   map[string]*metadata.File{"apps/web/src/compat.d.ts": fileDTS, "apps/web/src/util.ts": fileTS},
		edgesTo: map[string][]*metadata.Edge{"d1": nil, "s1": nil},
	}
	opts := PlanOptions{Lang: "typescript", RepoID: "r", MaxGaps: 10}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("ListGaps: got %d gaps; want 1 (.d.ts symbol excluded)", len(gaps))
	}
	if gaps[0].Symbol.File != "apps/web/src/util.ts" {
		t.Errorf("gap file = %q; want util.ts", gaps[0].Symbol.File)
	}
}

func TestListGaps_excludesPrivateMethods(t *testing.T) {
	symPublic := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", FQName: "com.example.Foo.bar", File: "Foo.java", SignatureJSON: []byte(`{"visibility":"public"}`)}
	symPrivate := &metadata.Symbol{ID: "s2", Lang: "java", Kind: "method", FQName: "com.example.Foo.privateBar", File: "Foo.java", SignatureJSON: []byte(`{"visibility":"private"}`)}
	file1 := &metadata.File{File: "Foo.java", Module: "example", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{symPublic, symPrivate},
		files:   map[string]*metadata.File{"Foo.java": file1},
		edgesTo: map[string][]*metadata.Edge{"s1": nil, "s2": nil},
	}
	opts := PlanOptions{Lang: "java", RepoID: "org/repo", MaxGaps: 10}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("ListGaps: got %d gaps; want 1 (private method excluded)", len(gaps))
	}
	if gaps[0].Symbol.FQName != "com.example.Foo.bar" {
		t.Errorf("remaining gap should be public method; got %s", gaps[0].Symbol.FQName)
	}
}

func TestListGaps_maxGaps(t *testing.T) {
	symbols := make([]*metadata.Symbol, 5)
	files := make(map[string]*metadata.File)
	for i := range symbols {
		symbols[i] = &metadata.Symbol{ID: fmt.Sprintf("s%d", i), Lang: "java", Kind: "method", File: "F.java"}
		files["F.java"] = &metadata.File{File: "F.java", Module: "m"}
	}
	meta := &mockGapMetaReader{symbols: symbols, files: files, edgesTo: map[string][]*metadata.Edge{}}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGaps: 2}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Errorf("ListGaps with MaxGaps=2: got %d; want 2", len(gaps))
	}
}

func TestListGaps_getFileError_skipped(t *testing.T) {
	sym := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", File: "Missing.java"}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym},
		files:   map[string]*metadata.File{}, // no file -> GetFile returns nil
		edgesTo: map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGaps: 10}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("ListGaps with missing file: got %d gaps; want 0", len(gaps))
	}
}

func TestListGaps_excludesSkipPathPrefixes(t *testing.T) {
	// Use java + method so mock returns one set of symbols (no duplicate kinds like JS FUNCTION+METHOD).
	symSkipped := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", File: "app/lib/angular/angular.js"}
	symSkippedFQ := &metadata.Symbol{ID: "s2", Lang: "java", Kind: "method", File: "app.lib.angular.angular"} // FQName-style path
	symKept := &metadata.Symbol{ID: "s3", Lang: "java", Kind: "method", File: "app/src/bar.js"}
	file := &metadata.File{File: "app/src/bar.js", Module: "app", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{symSkipped, symSkippedFQ, symKept},
		files: map[string]*metadata.File{
			"app/lib/angular/angular.js": {File: "app/lib/angular/angular.js", Module: "app.lib.angular", IsTest: false},
			"app.lib.angular.angular":    {File: "app.lib.angular.angular", Module: "app.lib.angular", IsTest: false},
			"app/src/bar.js":             file,
		},
		edgesTo: map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGaps: 10, SkipPathPrefixes: []string{"app/lib"}}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 1 {
		var files []string
		for _, g := range gaps {
			files = append(files, g.Symbol.File)
		}
		t.Fatalf("ListGaps with SkipPathPrefixes [app/lib]: got %d gaps %v; want 1 (only app/src/bar.js)", len(gaps), files)
	}
	if gaps[0].Symbol.File != "app/src/bar.js" {
		t.Errorf("remaining gap File = %q; want app/src/bar.js", gaps[0].Symbol.File)
	}
}

type mockGapMetaReader struct {
	symbols     []*metadata.Symbol
	testSymbols []*metadata.Symbol // E2E_SPEC etc. from "test" files
	files       map[string]*metadata.File
	edgesTo     map[string][]*metadata.Edge
	edgesFrom   map[string][]*metadata.Edge
	// symbolsByID optional; otherwise built from symbols + testSymbols
	symbolsByID map[string]*metadata.Symbol
}

func (m *mockGapMetaReader) symbolIndex() map[string]*metadata.Symbol {
	if m.symbolsByID != nil {
		return m.symbolsByID
	}
	out := make(map[string]*metadata.Symbol)
	for _, s := range m.symbols {
		if s != nil && s.ID != "" {
			out[s.ID] = s
		}
	}
	for _, s := range m.testSymbols {
		if s != nil && s.ID != "" {
			out[s.ID] = s
		}
	}
	return out
}

func (m *mockGapMetaReader) ListSymbolsInNonTestFiles(ctx context.Context, repoID, lang, kind string) ([]*metadata.Symbol, error) {
	var out []*metadata.Symbol
	langWant := strings.ToLower(strings.TrimSpace(lang))
	kindWant := strings.TrimSpace(kind)
	for _, s := range m.symbols {
		if s == nil {
			continue
		}
		if langWant != "" && strings.ToLower(strings.TrimSpace(s.Lang)) != langWant {
			continue
		}
		if kindWant != "" && s.Kind != kindWant {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *mockGapMetaReader) ListSymbolsInTestFiles(ctx context.Context, repoID, lang, kind string) ([]*metadata.Symbol, error) {
	if m.testSymbols == nil {
		return nil, nil
	}
	var out []*metadata.Symbol
	langWant := strings.ToLower(strings.TrimSpace(lang))
	kindWant := strings.TrimSpace(kind)
	for _, s := range m.testSymbols {
		if s == nil {
			continue
		}
		if langWant != "" && strings.ToLower(strings.TrimSpace(s.Lang)) != langWant {
			continue
		}
		if kindWant != "" && s.Kind != kindWant {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *mockGapMetaReader) GetFile(ctx context.Context, repoID, file string) (*metadata.File, error) {
	return m.files[file], nil
}

func (m *mockGapMetaReader) GetSymbolByID(ctx context.Context, repoID, id string) (*metadata.Symbol, error) {
	return m.symbolIndex()[id], nil
}

func (m *mockGapMetaReader) GetEdgesFrom(ctx context.Context, repoID, callerSymbolID string) ([]*metadata.Edge, error) {
	if m.edgesFrom == nil {
		return nil, nil
	}
	return m.edgesFrom[callerSymbolID], nil
}

func (m *mockGapMetaReader) GetEdgesTo(ctx context.Context, repoID, calleeSymbolID string) ([]*metadata.Edge, error) {
	return m.edgesTo[calleeSymbolID], nil
}

func (m *mockGapMetaReader) ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*metadata.Symbol, error) {
	var out []*metadata.Symbol
	for _, s := range m.symbols {
		if s != nil && s.FQName == fqName {
			out = append(out, s)
		}
	}
	for _, s := range m.testSymbols {
		if s != nil && s.FQName == fqName {
			out = append(out, s)
		}
	}
	return out, nil
}

// TestCreateTestPlan_withMocks runs CreateTestPlan with gap meta + retrieval meta + chunk reader mocks.
// Retrieve gets minimal context (target symbol only) so plan should have one item.
func TestCreateTestPlan_abstainsRetrievalSufficiency(t *testing.T) {
	sym := &metadata.Symbol{ID: "sym1", Lang: "java", Kind: "method", FQName: "com.Example.run", File: "Example.java", StartLine: 10, EndLine: 15}
	file := &metadata.File{File: "Example.java", Module: "app", IsTest: false}
	gapMeta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym},
		files:   map[string]*metadata.File{"Example.java": file},
		edgesTo: map[string][]*metadata.Edge{},
	}
	retrievalMeta := &mockMetaReaderForPlan{
		symbols:   map[string]*metadata.Symbol{sym.ID: sym},
		byFile:    map[string][]*metadata.Symbol{"Example.java": {sym}},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	chunks := &mockChunkReaderForPlan{}
	aud := &sliceAuditor{}
	opts := PlanOptions{
		Lang: "java", RepoID: "org/repo", MaxGaps: 5,
		MinSimilarTestsForGeneration: 1,
		Audit:                        aud,
	}
	plan, err := CreateTestPlan(context.Background(), gapMeta, retrievalMeta, chunks, opts)
	if err != nil {
		t.Fatalf("CreateTestPlan: %v", err)
	}
	if len(plan.Items) != 0 {
		t.Fatalf("want no plan items when similar-reference count is 0 and min_similar_tests_for_generation=1; got %d", len(plan.Items))
	}
	aud.mu.Lock()
	defer aud.mu.Unlock()
	var sawAbstain, sawDone bool
	for i, step := range aud.steps {
		if i < len(aud.payloads) && aud.payloads[i] != nil {
			if step == "retrieve.gap_abstained_retrieval" {
				sawAbstain = true
			}
			if step == "retrieve.plan_done" {
				if ac, ok := aud.payloads[i]["abstained_count"].(int); ok && ac == 1 {
					sawDone = true
				}
			}
		}
	}
	if !sawAbstain || !sawDone {
		t.Fatalf("audit: abstain=%v done_with_abstained_count=%v steps=%v", sawAbstain, sawDone, aud.steps)
	}
}

func TestCreateTestPlan_withMocks(t *testing.T) {
	sym := &metadata.Symbol{ID: "sym1", Lang: "java", Kind: "method", FQName: "com.Example.run", File: "Example.java", StartLine: 10, EndLine: 15}
	file := &metadata.File{File: "Example.java", Module: "app", IsTest: false}
	gapMeta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym},
		files:   map[string]*metadata.File{"Example.java": file},
		edgesTo: map[string][]*metadata.Edge{},
	}
	// Same mock implements MetaReader for Retrieve: GetSymbolByID, ListSymbolsByFile, GetEdgesFrom
	retrievalMeta := &mockMetaReaderForPlan{
		symbols:   map[string]*metadata.Symbol{sym.ID: sym},
		byFile:    map[string][]*metadata.Symbol{"Example.java": {sym}},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	chunks := &mockChunkReaderForPlan{} // List returns empty
	opts := PlanOptions{Lang: "java", RepoID: "org/repo", MaxGaps: 5}
	plan, err := CreateTestPlan(context.Background(), gapMeta, retrievalMeta, chunks, opts)
	if err != nil {
		t.Fatalf("CreateTestPlan: %v", err)
	}
	if plan == nil {
		t.Fatal("CreateTestPlan: plan is nil")
	}
	if len(plan.Items) != 1 {
		t.Errorf("plan.Items length = %d; want 1", len(plan.Items))
	}
	if plan.Items[0].Gap == nil || plan.Items[0].Gap.Symbol.ID != "sym1" {
		t.Errorf("plan.Items[0].Gap.Symbol.ID = %v; want sym1", plan.Items[0].Gap)
	}
	if plan.Items[0].Context == nil {
		t.Error("plan.Items[0].Context is nil")
	}
}

type sliceAuditor struct {
	mu       sync.Mutex
	steps    []string
	payloads []map[string]interface{}
}

func (a *sliceAuditor) Log(ctx context.Context, step string, payload interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps = append(a.steps, step)
	if m, ok := payload.(map[string]interface{}); ok {
		cp := make(map[string]interface{}, len(m))
		for k, v := range m {
			cp[k] = v
		}
		a.payloads = append(a.payloads, cp)
	} else {
		a.payloads = append(a.payloads, nil)
	}
}

func (a *sliceAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	a.Log(ctx, step, payload)
}

func TestCreateTestPlan_auditPayloads(t *testing.T) {
	sym := &metadata.Symbol{ID: "sym1", Lang: "java", Kind: "method", FQName: "com.Example.run", File: "Example.java", StartLine: 10, EndLine: 15}
	file := &metadata.File{File: "Example.java", Module: "app", IsTest: false}
	gapMeta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym},
		files:   map[string]*metadata.File{"Example.java": file},
		edgesTo: map[string][]*metadata.Edge{},
	}
	retrievalMeta := &mockMetaReaderForPlan{
		symbols:   map[string]*metadata.Symbol{sym.ID: sym},
		byFile:    map[string][]*metadata.Symbol{"Example.java": {sym}},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	chunks := &mockChunkReaderForPlanWithChunks{
		bySymbol: map[string][]embeddings.Chunk{
			sym.ID: {{ID: "c1", Content: "void run(){}", SymbolID: sym.ID, File: "Example.java", RepoID: "org/repo", Lang: "java"}},
		},
	}
	aud := &sliceAuditor{}
	opts := PlanOptions{
		Lang: "java", RepoID: "org/repo", MaxGaps: 5,
		RetrievalProfile: "http_api",
		Audit:            aud,
		SourceFilesWithExistingTest: map[string]struct{}{
			"Example.java": {},
		},
	}
	_, err := CreateTestPlan(context.Background(), gapMeta, retrievalMeta, chunks, opts)
	if err != nil {
		t.Fatalf("CreateTestPlan: %v", err)
	}
	aud.mu.Lock()
	defer aud.mu.Unlock()
	var sawStart, sawGap, sawDone bool
	for i, step := range aud.steps {
		if i >= len(aud.payloads) || aud.payloads[i] == nil {
			continue
		}
		p := aud.payloads[i]
		switch step {
		case "retrieve.plan_start":
			if p["retrieval_profile"] == "http_api" && p["gaps_count"] != nil {
				sawStart = true
			}
		case "retrieve.gap_retrieved":
			if p["deps_count"] != nil && p["similar_chunks_count"] != nil && p["existing_tests_detected"] != nil {
				sawGap = true
			}
		case "retrieve.plan_done":
			if p["total_deps"] != nil &&
				p["total_similar_segmented"] != nil &&
				p["total_similar_reassembled"] != nil &&
				p["items_with_existing_tests"] != nil &&
				p["total_missing_branch_intents"] != nil &&
				p["retrieval_profile"] == "http_api" {
				sawDone = true
			}
		}
	}
	if !sawStart || !sawGap || !sawDone {
		t.Fatalf("audit payloads: start=%v gap=%v done=%v steps=%v", sawStart, sawGap, sawDone, aud.steps)
	}
}

type mockMetaReaderForPlan struct {
	symbols   map[string]*metadata.Symbol
	byFile    map[string][]*metadata.Symbol
	edgesFrom map[string][]*metadata.Edge
	edgesTo   map[string][]*metadata.Edge
}

func (m *mockMetaReaderForPlan) GetSymbolByID(ctx context.Context, repoID, id string) (*metadata.Symbol, error) {
	return m.symbols[id], nil
}

func (m *mockMetaReaderForPlan) ListSymbolsByFile(ctx context.Context, repoID, file string) ([]*metadata.Symbol, error) {
	return m.byFile[file], nil
}

func (m *mockMetaReaderForPlan) GetEdgesFrom(ctx context.Context, repoID, callerSymbolID string) ([]*metadata.Edge, error) {
	return m.edgesFrom[callerSymbolID], nil
}

func (m *mockMetaReaderForPlan) GetEdgesTo(ctx context.Context, repoID, calleeSymbolID string) ([]*metadata.Edge, error) {
	if m.edgesTo == nil {
		return nil, nil
	}
	return m.edgesTo[calleeSymbolID], nil
}

func (m *mockMetaReaderForPlan) GetFile(ctx context.Context, repoID, file string) (*metadata.File, error) {
	return nil, nil
}

func (m *mockMetaReaderForPlan) ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*metadata.Symbol, error) {
	return nil, nil
}

type mockChunkReaderForPlan struct{}

func (m *mockChunkReaderForPlan) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	return nil, nil
}

func (m *mockChunkReaderForPlan) Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	return nil, nil
}

// mockChunkReaderForPlanWithChunks supports List by symbol_id (and optional parent) for CreateTestPlan audit tests.
type mockChunkReaderForPlanWithChunks struct {
	bySymbol    map[string][]embeddings.Chunk
	byParent    map[string][]embeddings.Chunk
	listAll     []embeddings.Chunk
	byChunkType map[string][]embeddings.Chunk
}

func (m *mockChunkReaderForPlanWithChunks) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	if opts.ParentSymbolID != "" && m.byParent != nil {
		return m.byParent[opts.ParentSymbolID], nil
	}
	if opts.SymbolID != "" && m.bySymbol != nil {
		return m.bySymbol[opts.SymbolID], nil
	}
	if opts.ChunkType != "" && m.byChunkType != nil {
		return m.byChunkType[opts.ChunkType], nil
	}
	if m.listAll != nil {
		return m.listAll, nil
	}
	return nil, nil
}

func (m *mockChunkReaderForPlanWithChunks) Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	return nil, nil
}

func TestTestPathToSourcePath(t *testing.T) {
	tests := []struct {
		name          string
		testFilePath  string
		lang          string
		testFramework string
		repoRoot      string
		want          string
	}{
		{"java src/test/java", "src/test/java/pkg/FooTest.java", "java", "", "", "src/main/java/pkg/Foo.java"},
		{"java same dir", "pkg/FooTest.java", "java", "", "", "pkg/Foo.java"},
		{"java invalid no Test suffix", "pkg/Foo.java", "java", "", "", ""},
		{"java Tests suffix", "src/test/java/pkg/FooTests.java", "java", "", "", "src/main/java/pkg/Foo.java"},
		{"java Tests suffix same dir", "pkg/FooTests.java", "java", "", "", "pkg/Foo.java"},
		{"java IT suffix", "src/test/java/pkg/PaymentIT.java", "java", "", "", "src/main/java/pkg/Payment.java"},
		{"java IT suffix same dir", "pkg/PaymentIT.java", "java", "", "", "pkg/Payment.java"},
		{"java Tests vs Test prefers longer suffix", "src/test/java/pkg/CustomerTests.java", "java", "", "", "src/main/java/pkg/Customer.java"},
		{"java too short to match", "src/test/java/pkg/Test.java", "java", "", "", ""},
		{"csharp Tests suffix", "pkg/FooTests.cs", "csharp", "", "", "pkg/Foo.cs"},
		{"csharp Test singular suffix", "pkg/HandlerTest.cs", "csharp", "", "", "pkg/Handler.cs"},
		{"csharp invalid", "pkg/Foo.cs", "csharp", "", "", ""},
		{"csharp dedicated root heuristic", "tests/Api/HandlersTests.cs", "csharp", "", "", "src/Api/Handlers.cs"},
		{"javascript dedicated root heuristic", "tests/components/Widget.test.tsx", "javascript", "", "", "src/components/Widget.tsx"},
		{"jasmine dedicated root heuristic", "tests/m.spec.ts", "javascript", "jasmine", "", "src/m.ts"},
		{"javascript .test", "src/foo.test.ts", "javascript", "", "", "src/foo.ts"},
		{"typescript .test", "lib/bar.test.js", "typescript", "", "", "lib/bar.js"},
		{"jasmine .spec", "src/baz.spec.ts", "javascript", "jasmine", "", "src/baz.ts"},
		{"jest .spec stray accepted", "src/baz.spec.ts", "javascript", "jest", "", "src/baz.ts"},
		{"jasmine .test stray accepted", "src/foo.test.ts", "javascript", "jasmine", "", "src/foo.ts"},
		{"javascript invalid", "src/foo.ts", "javascript", "", "", ""},
		{"empty path", "", "java", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TestPathToSourcePath(tt.testFilePath, tt.lang, tt.testFramework, tt.repoRoot)
			// Normalize for cross-platform (forward slashes)
			got = filepath.ToSlash(got)
			want := filepath.ToSlash(tt.want)
			if got != want {
				t.Errorf("TestPathToSourcePath(%q, %q, %q, %q) = %q; want %q", tt.testFilePath, tt.lang, tt.testFramework, tt.repoRoot, got, want)
			}
		})
	}
	t.Run("javascript dedicated stat prefers existing source tree", func(t *testing.T) {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "lib", "z.js")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("//"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := filepath.ToSlash(TestPathToSourcePath("tests/lib/z.test.js", "javascript", "", tmp))
		want := filepath.ToSlash(filepath.Join("lib", "z.js"))
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("java on-disk prefers Tests stem when both exist", func(t *testing.T) {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "src", "main", "java", "pkg", "Customer.java")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// src"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := filepath.ToSlash(TestPathToSourcePath("src/test/java/pkg/CustomerTests.java", "java", "", tmp))
		want := "src/main/java/pkg/Customer.java"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("java on-disk prefers IT stem when source exists", func(t *testing.T) {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "src", "main", "java", "pkg", "PaymentService.java")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// src"), 0o644); err != nil {
			t.Fatal(err)
		}
		// With no file for "PaymentServiceI.java" the IT stem should win on disk.
		got := filepath.ToSlash(TestPathToSourcePath("src/test/java/pkg/PaymentServiceIT.java", "java", "", tmp))
		want := "src/main/java/pkg/PaymentService.java"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("java IT file with no matching source returns empty when repoRoot set", func(t *testing.T) {
		tmp := t.TempDir()
		// Repo has OwnerController.java but no OwnerControllerE2E.java; the E2EIT test must not
		// be reverse-mapped to the phantom "OwnerControllerE2E.java" just because the IT suffix
		// happens to match syntactically. Real petclinic-style layout regression test.
		p := filepath.Join(tmp, "src", "main", "java", "pkg", "OwnerController.java")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// src"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := filepath.ToSlash(TestPathToSourcePath("src/test/java/pkg/OwnerControllerE2EIT.java", "java", "", tmp))
		if got != "" {
			t.Fatalf("expected empty (no phantom mapping); got %q", got)
		}
	})
	t.Run("java IT with repoRoot empty still falls back to first candidate", func(t *testing.T) {
		// Compatibility: the repoRoot == "" fallback path (per plan spec) still returns cands[0]
		// even if the candidate would point at a non-existent source, since the caller has opted
		// out of on-disk verification.
		got := filepath.ToSlash(TestPathToSourcePath("src/test/java/pkg/OwnerControllerE2EIT.java", "java", "", ""))
		want := "src/main/java/pkg/OwnerControllerE2E.java"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("jest repo with only x.spec.ts on disk maps to x.ts", func(t *testing.T) {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "src", "x.ts")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("// src"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := filepath.ToSlash(TestPathToSourcePath("src/x.spec.ts", "typescript", "jest", tmp))
		want := "src/x.ts"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("csharp dedicated stat prefers existing source tree", func(t *testing.T) {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "source", "Api", "Handlers.cs")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("//"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := filepath.ToSlash(TestPathToSourcePath("UnitTests/Api/HandlersTests.cs", "csharp", "", tmp))
		want := filepath.ToSlash(filepath.Join("source", "Api", "Handlers.cs"))
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

func TestListGaps_javascriptAndTypescript(t *testing.T) {
	// ListGaps(lang "javascript") queries both "javascript" and "typescript"; symbols are deduped by ID.
	symJS := &metadata.Symbol{ID: "s1", Lang: "javascript", Kind: "FUNCTION", FQName: "foo", File: "src/foo.js"}
	symTS := &metadata.Symbol{ID: "s2", Lang: "typescript", Kind: "METHOD", FQName: "Bar.run", File: "src/Bar.ts"}
	fileJS := &metadata.File{File: "src/foo.js", Module: "app", IsTest: false}
	fileTS := &metadata.File{File: "src/Bar.ts", Module: "app", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{symJS, symTS},
		files:   map[string]*metadata.File{"src/foo.js": fileJS, "src/Bar.ts": fileTS},
		edgesTo: map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{Lang: "javascript", RepoID: "r", MaxGaps: 10}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("ListGaps(javascript) got %d gaps; want 2 (combined from js+ts)", len(gaps))
	}
	seen := make(map[string]bool)
	for _, g := range gaps {
		if seen[g.Symbol.ID] {
			t.Errorf("duplicate symbol ID %s", g.Symbol.ID)
		}
		seen[g.Symbol.ID] = true
	}
}

func TestListGaps_priorityOrder(t *testing.T) {
	// Order: critical > central > plain > extend-existing.
	symCritical := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", File: "payment/Handler.java"}
	symCentral := &metadata.Symbol{ID: "s2", Lang: "java", Kind: "method", File: "util/Helper.java"}
	symPlain := &metadata.Symbol{ID: "s3", Lang: "java", Kind: "method", File: "other/Foo.java"}
	symExtend := &metadata.Symbol{ID: "s4", Lang: "java", Kind: "method", File: "src/Foo.java"}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{symCritical, symCentral, symPlain, symExtend},
		files: map[string]*metadata.File{
			"payment/Handler.java": {File: "payment/Handler.java", Module: "payment-api"},
			"util/Helper.java":     {File: "util/Helper.java", Module: "util"},
			"other/Foo.java":       {File: "other/Foo.java", Module: "other"},
			"src/Foo.java":         {File: "src/Foo.java", Module: "app"},
		},
		edgesTo: map[string][]*metadata.Edge{
			"s2": {{}, {}, {}}, // 3 callers -> central
		},
	}
	opts := PlanOptions{
		Lang: "java", RepoID: "r", MaxGaps: 10,
		CriticalModulePrefixes:      []string{"payment"},
		SourceFilesWithExistingTest: map[string]struct{}{"src/Foo.java": {}},
	}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 4 {
		t.Fatalf("ListGaps: got %d; want 4", len(gaps))
	}
	// First should be critical (payment), then central (util), then plain (other), then extend (src/Foo).
	if gaps[0].Kind != GapBusinessCritical {
		t.Errorf("first gap Kind = %v; want GapBusinessCritical", gaps[0].Kind)
	}
	if gaps[1].Kind != GapLowCoverageCentral {
		t.Errorf("second gap Kind = %v; want GapLowCoverageCentral", gaps[1].Kind)
	}
	// Third is plain (no tests). Fourth has existing test (deprioritized).
	if gaps[3].Reason != "extend existing test file (add test for this symbol)" {
		t.Errorf("fourth gap Reason = %q; want extend existing test file reason", gaps[3].Reason)
	}
}

func TestCreateTestPlan_preservesGapOrder(t *testing.T) {
	syms := []*metadata.Symbol{
		{ID: "a", Lang: "java", Kind: "method", FQName: "pkg.A.a", File: "A.java"},
		{ID: "b", Lang: "java", Kind: "method", FQName: "pkg.B.b", File: "B.java"},
		{ID: "c", Lang: "java", Kind: "method", FQName: "pkg.C.c", File: "C.java"},
	}
	files := map[string]*metadata.File{"A.java": {File: "A.java", Module: "pkg"}, "B.java": {File: "B.java", Module: "pkg"}, "C.java": {File: "C.java", Module: "pkg"}}
	gapMeta := &mockGapMetaReader{symbols: syms, files: files, edgesTo: map[string][]*metadata.Edge{}}
	retrievalMeta := &mockMetaReaderForPlan{
		symbols:   map[string]*metadata.Symbol{"a": syms[0], "b": syms[1], "c": syms[2]},
		byFile:    map[string][]*metadata.Symbol{"A.java": {syms[0]}, "B.java": {syms[1]}, "C.java": {syms[2]}},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	chunks := &mockChunkReaderForPlan{}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGaps: 5}
	plan, err := CreateTestPlan(context.Background(), gapMeta, retrievalMeta, chunks, opts)
	if err != nil {
		t.Fatalf("CreateTestPlan: %v", err)
	}
	if len(plan.Items) != 3 {
		t.Fatalf("plan.Items length = %d; want 3", len(plan.Items))
	}
	for i, wantID := range []string{"a", "b", "c"} {
		if plan.Items[i].Gap.Symbol.ID != wantID {
			t.Errorf("plan.Items[%d].Gap.Symbol.ID = %s; want %s", i, plan.Items[i].Gap.Symbol.ID, wantID)
		}
	}
}

func TestListGaps_deprioritizesExistingTestFiles(t *testing.T) {
	// Two symbols in same file; that file has an existing test -> deprioritize, reason "extend existing test file"
	sym := &metadata.Symbol{ID: "s1", Lang: "java", Kind: "method", FQName: "com.example.Foo.run", File: "src/main/java/com/example/Foo.java"}
	file := &metadata.File{File: "src/main/java/com/example/Foo.java", Module: "example", IsTest: false}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{sym},
		files:   map[string]*metadata.File{"src/main/java/com/example/Foo.java": file},
		edgesTo: map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{
		Lang: "java", RepoID: "r", MaxGaps: 10,
		SourceFilesWithExistingTest: map[string]struct{}{"src/main/java/com/example/Foo.java": {}},
	}
	gaps, err := ListGaps(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("ListGaps: got %d gaps; want 1", len(gaps))
	}
	if gaps[0].Reason != "extend existing test file (add test for this symbol)" {
		t.Errorf("ListGaps with existing test file: Reason = %q; want extend existing test file reason", gaps[0].Reason)
	}
	if gaps[0].Priority >= 0 {
		t.Errorf("ListGaps with existing test file: Priority should be deprioritized (negative or low); got %d", gaps[0].Priority)
	}
}

func TestListGapsE2E_respectsMaxAndSkipsPrefixes(t *testing.T) {
	e2eSyms := []*metadata.Symbol{
		{ID: "e1", Lang: "typescript", Kind: "E2E_SPEC", FQName: "E2E_SPEC:e2e/a.spec.ts", File: "e2e/a.spec.ts"},
		{ID: "e2", Lang: "typescript", Kind: "E2E_SPEC", FQName: "E2E_SPEC:e2e/b.spec.ts", File: "e2e/b.spec.ts"},
		{ID: "e3", Lang: "typescript", Kind: "E2E_SPEC", FQName: "E2E_SPEC:skip/x.spec.ts", File: "skip/x.spec.ts"},
	}
	files := map[string]*metadata.File{
		"e2e/a.spec.ts":  {File: "e2e/a.spec.ts", Module: "e2e", IsTest: true},
		"e2e/b.spec.ts":  {File: "e2e/b.spec.ts", Module: "e2e", IsTest: true},
		"skip/x.spec.ts": {File: "skip/x.spec.ts", Module: "skip", IsTest: true},
	}
	meta := &mockGapMetaReader{
		testSymbols: e2eSyms,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 5, SkipPathPrefixes: []string{"skip/"}}
	gaps, err := ListGapsE2E(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("ListGapsE2E: got %d gaps; want 2 (skipped prefix)", len(gaps))
	}
	opts.MaxGapsE2E = 1
	gaps2, err := ListGapsE2E(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps2) != 1 {
		t.Fatalf("ListGapsE2E max: got %d; want 1", len(gaps2))
	}
}

func TestListGapsE2E_java_pageObjectUserFlow(t *testing.T) {
	po := &metadata.Symbol{ID: "po1", Lang: "java", Kind: "PAGE_OBJECT", FQName: "PAGE_OBJECT:com.LoginPage@src/test/java/LoginPage.java", File: "src/test/java/LoginPage.java"}
	uf := &metadata.Symbol{ID: "uf1", Lang: "java", Kind: "USER_FLOW", FQName: "USER_FLOW:com.ApiFlow@src/test/java/ApiIT.java", File: "src/test/java/ApiIT.java"}
	files := map[string]*metadata.File{
		"src/test/java/LoginPage.java": {File: "src/test/java/LoginPage.java", Module: "app", IsTest: true},
		"src/test/java/ApiIT.java":     {File: "src/test/java/ApiIT.java", Module: "app", IsTest: true},
	}
	meta := &mockGapMetaReader{
		testSymbols: []*metadata.Symbol{po, uf},
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGapsE2E: 10}
	gaps, err := ListGapsE2E(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("ListGapsE2E(java): got %d gaps; want 2", len(gaps))
	}
	kinds := map[string]bool{}
	for _, g := range gaps {
		if g.Symbol != nil {
			kinds[g.Symbol.Kind] = true
		}
	}
	if !kinds["PAGE_OBJECT"] || !kinds["USER_FLOW"] {
		t.Errorf("want PAGE_OBJECT and USER_FLOW kinds; kinds=%v", kinds)
	}
}

func TestListGapsE2E_java_uncoveredApiRoutes(t *testing.T) {
	rOpen := &metadata.Symbol{ID: "rOpen", Lang: "java", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/api/open@com.Ctrl#x", File: "src/main/java/com/Ctrl.java"}
	rHit := &metadata.Symbol{ID: "rHit", Lang: "java", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/api/hit@com.Ctrl#y", File: "src/main/java/com/Ctrl.java"}
	client := &metadata.Symbol{ID: "cli1", Lang: "java", Kind: "API_CLIENT_REQUEST", FQName: "API_CLIENT_REQUEST:GET:/api/hit@com.Test#L1", File: "src/test/java/com/Test.java"}
	files := map[string]*metadata.File{
		"src/main/java/com/Ctrl.java": {File: "src/main/java/com/Ctrl.java", Module: "app", IsTest: false},
		"src/test/java/com/Test.java": {File: "src/test/java/com/Test.java", Module: "app", IsTest: true},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{rOpen, rHit},
		testSymbols: []*metadata.Symbol{client},
		files:       files,
		edgesTo: map[string][]*metadata.Edge{
			"rHit": {{CallerSymbolID: "cli1", CalleeSymbolID: "rHit", EdgeType: "TARGETS_API_ROUTE"}},
		},
	}
	opts := PlanOptions{Lang: "java", RepoID: "r", MaxGapsE2E: 10}
	gaps, err := ListGapsE2E(context.Background(), meta, opts)
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("ListGapsE2E(java routes): got %d gaps; want 1 uncovered route", len(gaps))
	}
	if gaps[0].Symbol == nil || gaps[0].Symbol.ID != "rOpen" {
		t.Fatalf("want gap for rOpen; got %+v", gaps[0].Symbol)
	}
	if gaps[0].Symbol.Kind != "API_ROUTE" {
		t.Errorf("Kind = %q; want API_ROUTE", gaps[0].Symbol.Kind)
	}
}

func TestListGapsE2E_java_allRoutesCoveredNoFallbackToTestSymbols(t *testing.T) {
	r1 := &metadata.Symbol{ID: "r1", Lang: "java", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/x@com.C#a", File: "src/main/java/C.java"}
	cli := &metadata.Symbol{ID: "c1", Lang: "java", Kind: "API_CLIENT_REQUEST", FQName: "API_CLIENT_REQUEST:GET:/x@T#L1", File: "src/test/java/T.java"}
	po := &metadata.Symbol{ID: "po1", Lang: "java", Kind: "PAGE_OBJECT", FQName: "PAGE_OBJECT:com.P@src/test/java/P.java", File: "src/test/java/P.java"}
	files := map[string]*metadata.File{
		"src/main/java/C.java": {File: "src/main/java/C.java", Module: "m", IsTest: false},
		"src/test/java/T.java": {File: "src/test/java/T.java", Module: "m", IsTest: true},
		"src/test/java/P.java": {File: "src/test/java/P.java", Module: "m", IsTest: true},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{r1},
		testSymbols: []*metadata.Symbol{cli, po},
		files:       files,
		edgesTo: map[string][]*metadata.Edge{
			"r1": {{CallerSymbolID: "c1", CalleeSymbolID: "r1", EdgeType: "TARGETS_API_ROUTE"}},
		},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "java", RepoID: "r", MaxGapsE2E: 10})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("when all API routes covered, expect 0 E2E gaps (no fallback to PAGE_OBJECT); got %d", len(gaps))
	}
}

func TestListGapsE2E_csharp_allRoutesCovered_http_api_noFallbackE2eSpec(t *testing.T) {
	r1 := &metadata.Symbol{ID: "r1", Lang: "csharp", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/x@Ctrl#a", File: "src/Api.cs"}
	cli := &metadata.Symbol{ID: "c1", Lang: "csharp", Kind: "API_CLIENT_REQUEST", FQName: "API_CLIENT_REQUEST:GET:/x@Test#L1", File: "tests/Client.cs"}
	e2e := &metadata.Symbol{ID: "e1", Lang: "csharp", Kind: "E2E_SPEC", FQName: "E2E_SPEC:tests/Smoke.cs", File: "tests/Smoke.cs"}
	files := map[string]*metadata.File{
		"src/Api.cs":      {File: "src/Api.cs", Module: "m", IsTest: false},
		"tests/Client.cs": {File: "tests/Client.cs", Module: "m", IsTest: true},
		"tests/Smoke.cs":  {File: "tests/Smoke.cs", Module: "m", IsTest: true},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{r1},
		testSymbols: []*metadata.Symbol{cli, e2e},
		files:       files,
		edgesTo: map[string][]*metadata.Edge{
			"r1": {{CallerSymbolID: "c1", CalleeSymbolID: "r1", EdgeType: "TARGETS_API_ROUTE"}},
		},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{
		Lang: "csharp", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "http_api",
	})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("csharp + http_api + all routes covered: want 0 gaps (no E2E_SPEC fallthrough); got %d", len(gaps))
	}
}

func TestListGapsE2E_csharp_allRoutesCovered_e2ePlaywright_fallsThroughToE2eSpec(t *testing.T) {
	r1 := &metadata.Symbol{ID: "r1", Lang: "csharp", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/x@Ctrl#a", File: "src/Api.cs"}
	cli := &metadata.Symbol{ID: "c1", Lang: "csharp", Kind: "API_CLIENT_REQUEST", FQName: "API_CLIENT_REQUEST:GET:/x@Test#L1", File: "tests/Client.cs"}
	e2e := &metadata.Symbol{ID: "e1", Lang: "csharp", Kind: "E2E_SPEC", FQName: "E2E_SPEC:tests/Smoke.cs", File: "tests/Smoke.cs"}
	files := map[string]*metadata.File{
		"src/Api.cs":      {File: "src/Api.cs", Module: "m", IsTest: false},
		"tests/Client.cs": {File: "tests/Client.cs", Module: "m", IsTest: true},
		"tests/Smoke.cs":  {File: "tests/Smoke.cs", Module: "m", IsTest: true},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{r1},
		testSymbols: []*metadata.Symbol{cli, e2e},
		files:       files,
		edgesTo: map[string][]*metadata.Edge{
			"r1": {{CallerSymbolID: "c1", CalleeSymbolID: "r1", EdgeType: "TARGETS_API_ROUTE"}},
		},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{
		Lang: "csharp", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "e2e_playwright",
	})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "E2E_SPEC" {
		t.Fatalf("want 1 csharp E2E_SPEC gap when profile_e2e=e2e_playwright; got %+v", gaps)
	}
}

func TestListGapsE2E_csharp_e2ePlaywright_listsTypeScriptPageRouteWhenRoutesCovered(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/app@m:L1", File: "apps/web/routes.tsx"}
	r1 := &metadata.Symbol{ID: "r1", Lang: "csharp", Kind: "API_ROUTE", FQName: "GET /api/x", File: "api/Controller.cs"}
	cli := &metadata.Symbol{ID: "c1", Lang: "csharp", Kind: "API_CLIENT_REQUEST", FQName: "client", File: "api/ControllerTests.cs"}
	files := map[string]*metadata.File{
		"apps/web/routes.tsx":    {File: "apps/web/routes.tsx", Module: "web", IsTest: false},
		"api/Controller.cs":      {File: "api/Controller.cs", Module: "api", IsTest: false},
		"api/ControllerTests.cs": {File: "api/ControllerTests.cs", Module: "api", IsTest: true},
	}
	edge := &metadata.Edge{CallerSymbolID: "c1", CalleeSymbolID: "r1", EdgeType: "TARGETS_API_ROUTE"}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr, r1},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{"r1": {edge}},
		symbolsByID: map[string]*metadata.Symbol{"r1": r1, "c1": cli},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{
		Lang: "csharp", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "e2e_playwright",
	})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "PAGE_ROUTE" {
		t.Fatalf("want 1 PAGE_ROUTE from TS supplement; got %+v", gaps)
	}
}

func TestListGapsE2E_typescript_uncoveredNestApiRoute(t *testing.T) {
	rOpen := &metadata.Symbol{ID: "r1", Lang: "typescript", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/api/x@src.app.Ctrl#m", File: "src/app.controller.ts"}
	files := map[string]*metadata.File{
		"src/app.controller.ts": {File: "src/app.controller.ts", Module: "app", IsTest: false},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{rOpen},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "API_ROUTE" {
		t.Fatalf("want 1 API_ROUTE gap; got %+v", gaps)
	}
}

func TestListGapsE2E_typescript_allApiCoveredFallsThroughToE2Espec(t *testing.T) {
	r1 := &metadata.Symbol{ID: "r1", Lang: "typescript", Kind: "API_ROUTE", FQName: "API_ROUTE:GET:/x@C#m", File: "src/c.ts"}
	cli := &metadata.Symbol{ID: "c1", Lang: "typescript", Kind: "API_CLIENT_REQUEST", FQName: "API_CLIENT_REQUEST:GET:/x@T#L1", File: "test/t.ts"}
	e2e := &metadata.Symbol{ID: "e1", Lang: "typescript", Kind: "E2E_SPEC", FQName: "E2E_SPEC:e2e/a.spec.ts", File: "e2e/a.spec.ts"}
	files := map[string]*metadata.File{
		"src/c.ts":      {File: "src/c.ts", Module: "m", IsTest: false},
		"test/t.ts":     {File: "test/t.ts", Module: "m", IsTest: true},
		"e2e/a.spec.ts": {File: "e2e/a.spec.ts", Module: "e2e", IsTest: true},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{r1},
		testSymbols: []*metadata.Symbol{cli, e2e},
		files:       files,
		edgesTo: map[string][]*metadata.Edge{
			"r1": {{CallerSymbolID: "c1", CalleeSymbolID: "r1", EdgeType: "TARGETS_API_ROUTE"}},
		},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "E2E_SPEC" {
		t.Fatalf("want fallthrough to E2E_SPEC; got %d gaps %+v", len(gaps), gaps)
	}
}

func TestListGapsE2E_typescript_pageRouteWithDefaultE2EProfile(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/settings@app.routes:L10", File: "src/routes.tsx"}
	files := map[string]*metadata.File{
		"src/routes.tsx": {File: "src/routes.tsx", Module: "app", IsTest: false},
	}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: ""})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "PAGE_ROUTE" {
		t.Fatalf("want 1 PAGE_ROUTE gap with default profile; got %+v", gaps)
	}
}

func TestListGapsE2E_typescript_noPageRouteWhenProfileJavaUnit(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/x@m:L1", File: "src/r.tsx"}
	files := map[string]*metadata.File{"src/r.tsx": {File: "src/r.tsx", Module: "app", IsTest: false}}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "java_unit"})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("with profile_e2e=java_unit, expect no PAGE_ROUTE gaps; got %d", len(gaps))
	}
}

func TestListGapsE2E_java_fullStack_listsTypeScriptPageRouteWhenJavaRoutesCovered(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/app@m:L1", File: "apps/web/routes.tsx"}
	jar := &metadata.Symbol{ID: "a1", Lang: "java", Kind: "API_ROUTE", FQName: "GET /api/x", File: "api/Api.java"}
	tclient := &metadata.Symbol{ID: "t1", Lang: "java", Kind: "API_CLIENT_REQUEST", FQName: "client", File: "api/ApiTest.java"}
	files := map[string]*metadata.File{
		"apps/web/routes.tsx": {File: "apps/web/routes.tsx", Module: "web", IsTest: false},
		"api/Api.java":        {File: "api/Api.java", Module: "api", IsTest: false},
		"api/ApiTest.java":    {File: "api/ApiTest.java", Module: "api", IsTest: true},
	}
	edge := &metadata.Edge{CallerSymbolID: "t1", CalleeSymbolID: "a1", EdgeType: "TARGETS_API_ROUTE"}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr, jar},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{"a1": {edge}},
		symbolsByID: map[string]*metadata.Symbol{"a1": jar, "t1": tclient},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "java", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "full_stack"})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "PAGE_ROUTE" {
		t.Fatalf("java+full_stack: want 1 PAGE_ROUTE after covered Java API_ROUTE; got %+v", gaps)
	}
}

func TestListGapsE2E_java_http_api_doesNotListTypeScriptPageRoute(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/app@m:L1", File: "apps/web/routes.tsx"}
	files := map[string]*metadata.File{"apps/web/routes.tsx": {File: "apps/web/routes.tsx", Module: "web", IsTest: false}}
	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{pr}, files: files, edgesTo: map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "java", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "http_api"})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("java+http_api: want no TS PAGE_ROUTE supplement; got %d gaps", len(gaps))
	}
}

func TestPageRouteE2EGapsEnabledJS_unknownProfileDoesNotDisable(t *testing.T) {
	opts := PlanOptions{Lang: "typescript", RetrievalProfileE2E: "typo_profile_e2e"}
	if !pageRouteE2EGapsEnabledJS(opts) {
		t.Fatal("unknown profile_e2e should not disable PAGE_ROUTE gaps on JS/TS")
	}
}

func TestListGapsE2E_typescript_pageRouteWithFullStackProfile(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/app@m:L1", File: "src/routes.tsx"}
	files := map[string]*metadata.File{"src/routes.tsx": {File: "src/routes.tsx", Module: "app", IsTest: false}}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "full_stack"})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "PAGE_ROUTE" {
		t.Fatalf("profile_e2e=full_stack must list PAGE_ROUTE gaps; got %+v", gaps)
	}
}

func TestListGapsE2E_typescript_pageRouteWithReactFeatureProfile(t *testing.T) {
	pr := &metadata.Symbol{ID: "p1", Lang: "typescript", Kind: "PAGE_ROUTE", FQName: "PAGE_ROUTE:/dash@m:L1", File: "src/routes.tsx"}
	files := map[string]*metadata.File{"src/routes.tsx": {File: "src/routes.tsx", Module: "app", IsTest: false}}
	meta := &mockGapMetaReader{
		symbols:     []*metadata.Symbol{pr},
		testSymbols: nil,
		files:       files,
		edgesTo:     map[string][]*metadata.Edge{},
	}
	gaps, err := ListGapsE2E(context.Background(), meta, PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 10, RetrievalProfileE2E: "react"})
	if err != nil {
		t.Fatalf("ListGapsE2E: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol == nil || gaps[0].Symbol.Kind != "PAGE_ROUTE" {
		t.Fatalf("profile_e2e=react must still list PAGE_ROUTE gaps; got %+v", gaps)
	}
}

func TestSupportsE2EGapListing(t *testing.T) {
	if !SupportsE2EGapListing("java") || !SupportsE2EGapListing("typescript") {
		t.Error("java/ts should support E2E gap listing")
	}
	if !SupportsE2EGapListing("csharp") || !SupportsE2EGapListing("cs") {
		t.Error("csharp should support E2E gap listing")
	}
}

func TestCreateE2ETestPlan_setsLayer(t *testing.T) {
	sym := &metadata.Symbol{ID: "e1", Lang: "typescript", Kind: "E2E_SPEC", FQName: "E2E_SPEC:e2e/login.spec.ts", File: "e2e/login.spec.ts"}
	file := &metadata.File{File: "e2e/login.spec.ts", Module: "e2e", IsTest: true}
	gapMeta := &mockGapMetaReader{
		testSymbols: []*metadata.Symbol{sym},
		files:       map[string]*metadata.File{"e2e/login.spec.ts": file},
		edgesTo:     map[string][]*metadata.Edge{},
	}
	retrievalMeta := &mockMetaReaderForPlan{
		symbols:   map[string]*metadata.Symbol{sym.ID: sym},
		byFile:    map[string][]*metadata.Symbol{"e2e/login.spec.ts": {sym}},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	chunks := &mockChunkReaderForPlan{}
	opts := PlanOptions{Lang: "typescript", RepoID: "r", MaxGapsE2E: 5, E2EFramework: "playwright"}
	plan, err := CreateE2ETestPlan(context.Background(), gapMeta, retrievalMeta, chunks, opts)
	if err != nil {
		t.Fatalf("CreateE2ETestPlan: %v", err)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("items = %d; want 1", len(plan.Items))
	}
	if plan.Items[0].Layer != "e2e" {
		t.Errorf("Layer = %q; want e2e", plan.Items[0].Layer)
	}
}

func TestCreateTestPlanFromGaps_retrieveWithinRunCacheReducesChunkIO(t *testing.T) {
	sym := &metadata.Symbol{ID: "symdup", Lang: "java", Kind: "method", FQName: "com.Example.run", File: "Example.java", StartLine: 10, EndLine: 15}
	sym2 := &metadata.Symbol{ID: "sym2", Lang: "java", Kind: "method", FQName: "com.Example.other", File: "Other.java", StartLine: 1, EndLine: 5}
	retrievalMeta := &mockMetaReaderForPlan{
		symbols: map[string]*metadata.Symbol{sym.ID: sym, sym2.ID: sym2},
		byFile: map[string][]*metadata.Symbol{
			"Example.java": {sym},
			"Other.java":   {sym2},
		},
		edgesFrom: map[string][]*metadata.Edge{},
	}
	params := testPlanBuildParams{
		retrievalProfile: string(ProfileJavaUnit),
		auditStartStep:   "retrieve.plan_start",
		auditDoneStep:    "retrieve.plan_done",
		layer:            "unit",
	}
	opts := PlanOptions{
		Lang:                         "java",
		RepoID:                       "org/repo",
		MaxGaps:                      5,
		MinSimilarTestsForGeneration: 0,
		MinSimilarityCosine:          0,
	}

	countSingle := &countingChunkReader{inner: &mockChunkReaderForPlan{}}
	_, err := createTestPlanFromGaps(context.Background(), nil, retrievalMeta, countSingle, opts, []*TestGap{{Symbol: sym, Kind: GapNoTests, Reason: "r"}}, params)
	if err != nil {
		t.Fatalf("createTestPlanFromGaps single: %v", err)
	}
	opsSingle := countSingle.listCalls.Load() + countSingle.searchCalls.Load()

	countDup := &countingChunkReader{inner: &mockChunkReaderForPlan{}}
	_, err = createTestPlanFromGaps(context.Background(), nil, retrievalMeta, countDup, opts, []*TestGap{
		{Symbol: sym, Kind: GapNoTests, Reason: "r1"},
		{Symbol: sym, Kind: GapNoTests, Reason: "r2"},
	}, params)
	if err != nil {
		t.Fatalf("createTestPlanFromGaps dup: %v", err)
	}
	opsDup := countDup.listCalls.Load() + countDup.searchCalls.Load()

	countTwo := &countingChunkReader{inner: &mockChunkReaderForPlan{}}
	_, err = createTestPlanFromGaps(context.Background(), nil, retrievalMeta, countTwo, opts, []*TestGap{
		{Symbol: sym, Kind: GapNoTests, Reason: "a"},
		{Symbol: sym2, Kind: GapNoTests, Reason: "b"},
	}, params)
	if err != nil {
		t.Fatalf("createTestPlanFromGaps two symbols: %v", err)
	}
	opsTwo := countTwo.listCalls.Load() + countTwo.searchCalls.Load()

	if opsSingle <= 0 {
		t.Fatalf("expected chunk ops for single gap, got %d", opsSingle)
	}
	if opsDup >= 2*opsSingle {
		t.Fatalf("duplicate symbol gaps should reuse Retrieve (chunk ops < 2× single): single_ops=%d dup_ops=%d", opsSingle, opsDup)
	}
	if opsTwo <= opsDup {
		t.Fatalf("two distinct symbols should do more chunk work than duplicate-symbol run: two_ops=%d dup_ops=%d", opsTwo, opsDup)
	}
}
