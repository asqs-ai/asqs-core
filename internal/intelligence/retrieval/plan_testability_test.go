package retrieval

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func TestTestabilityScore(t *testing.T) {
	sig := func(s string) []byte { return []byte(`{"signature":"` + s + `"}`) }
	tests := []struct {
		name          string
		sym           *metadata.Symbol
		outboundCalls int
		intents       []string
		want          int
	}{
		{name: "nil symbol", sym: nil, want: 0},
		{
			name: "trivial getter: no branches, no calls, tiny span, no params",
			sym:  &metadata.Symbol{StartLine: 20, EndLine: 22, SignatureJSON: sig("LocalDate getDate()")},
			want: 0,
		},
		{
			name:          "branchy collaborator-heavy method",
			sym:           &metadata.Symbol{StartLine: 10, EndLine: 45, SignatureJSON: sig("Order place(Order o, User u)")},
			outboundCalls: 5,
			intents:       []string{"if_true_path", "if_false_path", "exception_path"},
			want:          9 + 15 + 6 + 4,
		},
		{
			name:          "signals are capped",
			sym:           &metadata.Symbol{StartLine: 1, EndLine: 500, SignatureJSON: sig("void f(A a, B b, C c, D d, E e)")},
			outboundCalls: 40,
			intents:       []string{"a", "b", "c", "d", "e", "f"},
			want:          MaxTestabilityScore,
		},
		{
			name: "generics in params do not inflate arity",
			sym:  &metadata.Symbol{StartLine: 1, EndLine: 2, SignatureJSON: sig("void f(Map<String, Integer> m)")},
			want: 2, // one parameter => 2 points, span 1 => 0
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TestabilityScore(tc.sym, tc.outboundCalls, tc.intents); got != tc.want {
				t.Fatalf("TestabilityScore = %d, want %d", got, tc.want)
			}
		})
	}
}

func javaMethod(id, fq, file string, start, end int, sig string) *metadata.Symbol {
	return &metadata.Symbol{
		ID: id, Lang: "java", Kind: "method", FQName: fq, File: file,
		StartLine: start, EndLine: end,
		SignatureJSON: []byte(`{"signature":"` + sig + `","visibility":"public"}`),
	}
}

func javaType(id, fq, file, kind string, annotations string) *metadata.Symbol {
	sig := `{"visibility":"public"}`
	if annotations != "" {
		sig = `{"visibility":"public","annotations":[` + annotations + `]}`
	}
	return &metadata.Symbol{
		ID: id, Lang: "java", Kind: kind, FQName: fq, File: file,
		StartLine: 1, EndLine: 200, SignatureJSON: []byte(sig),
	}
}

// THE F6 REGRESSION (interface members). Spring Data repository methods have no body; generating
// tests for them is what dragged @DataJpaTest / @MockBean into the failing run.
func TestListGaps_dropsInterfaceMethods(t *testing.T) {
	repoIface := javaType("t_repo", "p.OwnerRepository", "p/OwnerRepository.java", "interface", "")
	findById := javaMethod("m_find", "p.OwnerRepository#findById", "p/OwnerRepository.java", 10, 11, "Optional<Owner> findById(Integer id)")
	findBy := javaMethod("m_find2", "p.OwnerRepository#findByLastNameStartingWith", "p/OwnerRepository.java", 13, 14, "Page<Owner> findByLastNameStartingWith(String n, Pageable p)")
	real := javaMethod("m_real", "p.OrderService#place", "p/OrderService.java", 10, 40, "Order place(Order o)")
	realType := javaType("t_svc", "p.OrderService", "p/OrderService.java", "class", "")

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{findById, findBy, real, repoIface, realType},
		files: map[string]*metadata.File{
			"p/OwnerRepository.java": {File: "p/OwnerRepository.java"},
			"p/OrderService.java":    {File: "p/OrderService.java"},
		},
		edgesFrom: map[string][]*metadata.Edge{"m_real": {{CalleeSymbolID: "x"}, {CalleeSymbolID: "y"}}},
	}
	aud := &recordingPlanAuditor{}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{Lang: "java", MaxGaps: 10, Audit: aud})
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	for _, g := range gaps {
		if g.Symbol != nil && g.Symbol.File == "p/OwnerRepository.java" {
			t.Fatalf("interface member selected as a gap: %s", g.Symbol.FQName)
		}
	}
	if len(gaps) != 1 || gaps[0].Symbol.FQName != "p.OrderService#place" {
		t.Fatalf("expected only the class method; got %v", gapNames(gaps))
	}
	if n := aud.reasonCount("plan.gaps_filtered_ineligible", IneligibleInterfaceMember); n != 2 {
		t.Errorf("audit should report 2 interface_member drops, got %d", n)
	}
}

func TestListGaps_dropsConfigurationBeanMethods(t *testing.T) {
	cfgType := javaType("t_cfg", "p.WebConfiguration", "p/WebConfiguration.java", "class", `"@Configuration"`)
	bean := javaMethod("m_bean", "p.WebConfiguration#localeChangeInterceptor", "p/WebConfiguration.java", 20, 24, "LocaleChangeInterceptor localeChangeInterceptor()")
	realType := javaType("t_svc", "p.OrderService", "p/OrderService.java", "class", "")
	real := javaMethod("m_real", "p.OrderService#place", "p/OrderService.java", 10, 40, "Order place(Order o)")

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{bean, real, cfgType, realType},
		files: map[string]*metadata.File{
			"p/WebConfiguration.java": {File: "p/WebConfiguration.java"},
			"p/OrderService.java":     {File: "p/OrderService.java"},
		},
	}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{Lang: "java", MaxGaps: 10})
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	for _, g := range gaps {
		if g.Symbol != nil && g.Symbol.FQName == bean.FQName {
			t.Fatal("@Configuration @Bean factory selected as a gap")
		}
	}
}

func TestListGaps_dropsTrivialAccessors(t *testing.T) {
	petType := javaType("t_pet", "p.Pet", "p/Pet.java", "class", "")
	getter := javaMethod("m_get", "p.Pet#getBirthDate", "p/Pet.java", 30, 32, "LocalDate getBirthDate()")
	// Same prefix but real behaviour: must survive.
	computed := javaMethod("m_calc", "p.Pet#getAgeInMonths", "p/Pet.java", 40, 60, "int getAgeInMonths()")

	meta := &mockGapMetaReader{
		symbols:   []*metadata.Symbol{getter, computed, petType},
		files:     map[string]*metadata.File{"p/Pet.java": {File: "p/Pet.java"}},
		edgesFrom: map[string][]*metadata.Edge{"m_calc": {{CalleeSymbolID: "x"}}},
	}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{Lang: "java", MaxGaps: 10})
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 1 || gaps[0].Symbol.FQName != "p.Pet#getAgeInMonths" {
		t.Fatalf("expected only the computed accessor; got %v", gapNames(gaps))
	}
}

// THE F6 REGRESSION (alphabetical degeneration). With every candidate at Priority 0, sortByPriority
// falls back to the FQName tie-break and selection becomes alphabetical. The positive score must
// let a branchy method outrank an alphabetically-earlier trivial one.
func TestListGaps_branchyMethodOutranksAlphabeticallyEarlierTrivialOne(t *testing.T) {
	aType := javaType("t_a", "a.Aaa", "a/Aaa.java", "class", "")
	simple := javaMethod("m_simple", "a.Aaa#simple", "a/Aaa.java", 10, 14, "void simple()")
	zType := javaType("t_z", "z.Zzz", "z/Zzz.java", "class", "")
	complexM := javaMethod("m_complex", "z.Zzz#complex", "z/Zzz.java", 10, 45, "Result complex(Input in, Options o)")

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{simple, complexM, aType, zType},
		files: map[string]*metadata.File{
			"a/Aaa.java": {File: "a/Aaa.java"},
			"z/Zzz.java": {File: "z/Zzz.java"},
		},
		edgesFrom: map[string][]*metadata.Edge{
			"m_complex": {{CalleeSymbolID: "c1"}, {CalleeSymbolID: "c2"}, {CalleeSymbolID: "c3"}, {CalleeSymbolID: "c4"}, {CalleeSymbolID: "c5"}},
		},
	}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{Lang: "java", MaxGaps: 10})
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("got %d gaps, want 2: %v", len(gaps), gapNames(gaps))
	}
	if gaps[0].Symbol.FQName != "z.Zzz#complex" {
		t.Fatalf("ranking is still alphabetical: got %v", gapNames(gaps))
	}
	if gaps[0].TestabilityScore <= gaps[1].TestabilityScore {
		t.Fatalf("testability score did not separate the two: %d vs %d", gaps[0].TestabilityScore, gaps[1].TestabilityScore)
	}
}

// Branch evidence is the strongest signal but needs a chunk; it is applied to the shortlist only.
func TestListGapsWithChunks_branchIntentsAddedForShortlist(t *testing.T) {
	tType := javaType("t_t", "p.T", "p/T.java", "class", "")
	plain := javaMethod("m_plain", "p.T#aPlain", "p/T.java", 10, 20, "void aPlain()")
	branchy := javaMethod("m_branchy", "p.T#zBranchy", "p/T.java", 30, 40, "void zBranchy()")

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{plain, branchy, tType},
		files:   map[string]*metadata.File{"p/T.java": {File: "p/T.java"}},
	}
	chunks := &mockChunkReaderForPlanWithChunks{bySymbol: map[string][]embeddings.Chunk{
		"m_branchy": {{SymbolID: "m_branchy", Content: "if (x == null) { throw new IllegalStateException(); } else { switch (y) { case 1: break; } }"}},
		"m_plain":   {{SymbolID: "m_plain", Content: "return 1;"}},
	}}

	gaps, err := ListGapsWithChunks(context.Background(), meta, chunks, PlanOptions{Lang: "java", MaxGaps: 10, MaxGapsPerFile: 5})
	if err != nil {
		t.Fatalf("ListGapsWithChunks: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("got %d gaps, want 2: %v", len(gaps), gapNames(gaps))
	}
	if gaps[0].Symbol.FQName != "p.T#zBranchy" {
		t.Fatalf("branch evidence did not reorder the shortlist: %v", gapNames(gaps))
	}
	if len(gaps[0].BranchIntents) == 0 {
		t.Fatal("BranchIntents not recorded on the gap; createTestPlanFromGaps would recompute them")
	}
}

// The filter must never produce an empty plan: a run that generates nothing is worse than a run
// that generates weak tests and says so.
func TestListGaps_fallsBackWhenAllCandidatesFiltered(t *testing.T) {
	iface := javaType("t_i", "p.Repo", "p/Repo.java", "interface", "")
	m1 := javaMethod("m1", "p.Repo#findById", "p/Repo.java", 10, 11, "Optional<X> findById(Long id)")
	m2 := javaMethod("m2", "p.Repo#findAll", "p/Repo.java", 13, 14, "List<X> findAll()")

	meta := &mockGapMetaReader{
		symbols: []*metadata.Symbol{m1, m2, iface},
		files:   map[string]*metadata.File{"p/Repo.java": {File: "p/Repo.java"}},
	}
	aud := &recordingPlanAuditor{}
	gaps, err := ListGaps(context.Background(), meta, PlanOptions{Lang: "java", MaxGaps: 10, MaxGapsPerFile: 5, Audit: aud})
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(gaps) == 0 {
		t.Fatal("filter emptied the plan; the fallback to the unfiltered ranking did not fire")
	}
	if !aud.has("plan.all_candidates_filtered") {
		t.Error("expected plan.all_candidates_filtered audit event so the operator knows why the plan is weak")
	}
}

// MinGapTestabilityScore defaults to 0 (rank only). Raising it turns the filter into an abstention.
func TestListGaps_minTestabilityScoreDropsZeroScorers(t *testing.T) {
	tType := javaType("t_t", "p.T", "p/T.java", "class", "")
	weak := javaMethod("m_weak", "p.T#aWeak", "p/T.java", 10, 12, "void aWeak()")
	strong := javaMethod("m_strong", "p.T#zStrong", "p/T.java", 20, 60, "R zStrong(A a)")

	meta := &mockGapMetaReader{
		symbols:   []*metadata.Symbol{weak, strong, tType},
		files:     map[string]*metadata.File{"p/T.java": {File: "p/T.java"}},
		edgesFrom: map[string][]*metadata.Edge{"m_strong": {{CalleeSymbolID: "c"}}},
	}
	base := PlanOptions{Lang: "java", MaxGaps: 10, MaxGapsPerFile: 5}

	all, err := ListGaps(context.Background(), meta, base)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("default must not drop low scorers; got %v", gapNames(all))
	}

	base.MinGapTestabilityScore = 1
	filtered, err := ListGaps(context.Background(), meta, base)
	if err != nil {
		t.Fatalf("ListGaps: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Symbol.FQName != "p.T#zStrong" {
		t.Fatalf("MinGapTestabilityScore did not drop the zero scorer; got %v", gapNames(filtered))
	}
}

func TestGapEligibility_unknownSpanIsNotTreatedAsBodyless(t *testing.T) {
	// Rows without line data (older indexes, indexers that do not emit EndLine) must not be
	// silently dropped.
	sym := &metadata.Symbol{Kind: "method", FQName: "p.T#getThing"}
	if ok, reason := gapEligibility(sym, nil, 0); !ok {
		t.Fatalf("symbol with unknown span was dropped as %q", reason)
	}
}

func gapNames(gaps []*TestGap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		if g != nil && g.Symbol != nil {
			out = append(out, g.Symbol.FQName)
		}
	}
	return out
}

type recordingPlanAuditor struct {
	steps    []string
	payloads []map[string]interface{}
}

func (a *recordingPlanAuditor) Log(_ context.Context, step string, payload interface{}) {
	a.steps = append(a.steps, step)
	p, _ := payload.(map[string]interface{})
	a.payloads = append(a.payloads, p)
}
func (a *recordingPlanAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	a.Log(ctx, step, payload)
}

func (a *recordingPlanAuditor) has(step string) bool {
	for _, s := range a.steps {
		if s == step {
			return true
		}
	}
	return false
}

func (a *recordingPlanAuditor) reasonCount(step, reason string) int {
	for i, s := range a.steps {
		if s != step || a.payloads[i] == nil {
			continue
		}
		byReason, _ := a.payloads[i]["by_reason"].(map[string]int)
		return byReason[reason]
	}
	return 0
}
