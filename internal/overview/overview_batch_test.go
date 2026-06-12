package overview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// stubOverviewMeta implements overviewMetaReader for unit tests.
type stubOverviewMeta struct {
	files   []*metadata.File
	classes []*metadata.Symbol
	methods []*metadata.Symbol
}

func (s *stubOverviewMeta) ListFiles(ctx context.Context, lang string, isTest *bool) ([]*metadata.File, error) {
	if isTest != nil && *isTest {
		return nil, nil
	}
	return s.files, nil
}

func (s *stubOverviewMeta) ListSymbolsByLang(ctx context.Context, lang, kind string) ([]*metadata.Symbol, error) {
	switch kind {
	case "class":
		return s.classes, nil
	case "method":
		return s.methods, nil
	default:
		return nil, nil
	}
}

func TestPartitionOverviewFileBatches_ordersAndSplits(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "gamma/z.go", Lang: "go", Module: "m2"},
			{File: "alpha/a.go", Lang: "go", Module: "m1"},
			{File: "alpha/b.go", Lang: "go", Module: "m1"},
			{File: "alpha/c.go", Lang: "go", Module: "m1"},
			{File: "beta/x.go", Lang: "go", Module: "m1"},
		},
	}
	batches, err := partitionOverviewFileBatches(ctx, st, "go", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatalf("len(batches)=%d; want 3 (m1 alpha 2+1, m1 beta 1, m2 gamma 1)", len(batches))
	}
	if got := strings.Join(batches[0], ","); got != "alpha/a.go,alpha/b.go" {
		t.Errorf("batch0: %q", got)
	}
	if got := strings.Join(batches[1], ","); got != "alpha/c.go,beta/x.go" {
		t.Errorf("batch1: %q", got)
	}
	if got := strings.Join(batches[2], ","); got != "gamma/z.go" {
		t.Errorf("batch2: %q", got)
	}
}

func TestPartitionOverviewFileBatches_skipsIgnoredPaths(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "src/a.go", Lang: "go", Module: ""},
			{File: "dist/bundle.js", Lang: "go", Module: ""},
		},
	}
	batches, err := partitionOverviewFileBatches(ctx, st, "go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0]) != 1 || batches[0][0] != "src/a.go" {
		t.Fatalf("batches=%v; want one batch [src/a.go]", batches)
	}
}

func TestBuildOverviewContextForSourceFiles_filtersFilesAndSymbols(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "keep/p1.go", Lang: "go", Module: "modA"},
			{File: "drop/p2.go", Lang: "go", Module: "modB"},
		},
		classes: []*metadata.Symbol{
			{FQName: "pkg.T", File: "keep/p1.go", Kind: "class"},
			{FQName: "pkg.U", File: "drop/p2.go", Kind: "class"},
		},
		methods: []*metadata.Symbol{
			{FQName: "pkg.T.M", File: "keep/p1.go", Kind: "method"},
			{FQName: "pkg.U.N", File: "drop/p2.go", Kind: "method"},
		},
	}
	out, err := buildOverviewContextForSourceFiles(ctx, st, "go", []string{"keep/p1.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "p2.go") || strings.Contains(out, "pkg.U") {
		t.Fatalf("expected only keep/p1.go symbols; got:\n%s", out)
	}
	if !strings.Contains(out, "keep/p1.go") || !strings.Contains(out, "pkg.T") || !strings.Contains(out, "pkg.T.M") {
		t.Fatalf("missing expected content:\n%s", out)
	}
}

type sliceIndexCompleter struct{ i int }

func (s *sliceIndexCompleter) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	s.i++
	if s.i == 1 {
		return &model.CompleteResult{Content: fmt.Sprintf("# Repo\n\nfirst slice %d", s.i)}, nil
	}
	return &model.CompleteResult{Content: fmt.Sprintf("## More\n\ncontinuation %d", s.i)}, nil
}

// eofSplitTwoFileBatchCompleter returns unexpected EOF when the user message still lists two indexed files
// in one slice; succeeds with smaller single-file payloads (simulates transport limit on large POST body).
type eofSplitTwoFileBatchCompleter struct{ calls int }

func (e *eofSplitTwoFileBatchCompleter) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	e.calls++
	var u string
	for _, m := range messages {
		if m.Role == "user" {
			u += m.Content
		}
	}
	if strings.Contains(u, "a.go") && strings.Contains(u, "b.go") {
		return nil, errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
	}
	if strings.Contains(u, "a.go") && !strings.Contains(u, "b.go") {
		return &model.CompleteResult{Content: "## From A\nalpha"}, nil
	}
	if strings.Contains(u, "b.go") && !strings.Contains(u, "a.go") {
		return &model.CompleteResult{Content: "## From B\nbeta"}, nil
	}
	return &model.CompleteResult{Content: "## Fallback\nx"}, nil
}

func TestGenerateOverviewWithMeta_fullBatchedEOFResplitsTwoFileBatch(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "a.go", Lang: "go", Module: "m"},
			{File: "b.go", Lang: "go", Module: "m"},
		},
	}
	llm := &eofSplitTwoFileBatchCompleter{}
	g := &LLMOverviewDocGenerator{
		LLM:         llm,
		FullRewrite: true,
	}
	content, _, stat, err := g.GenerateOverviewWithMeta(ctx, st, "go", 2, -1, OverviewGenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 3 {
		t.Fatalf("LLM Complete calls=%d want 3 (EOF on combined index then one call per file)", llm.calls)
	}
	if !strings.Contains(content, "alpha") || !strings.Contains(content, "beta") {
		t.Fatalf("expected merged fragments, got:\n%s", content)
	}
	if stat.TotalIndexRunes <= 0 {
		t.Fatalf("TotalIndexRunes=%d", stat.TotalIndexRunes)
	}
}

type eofSplitIncrementalCompleter struct{ calls int }

func (e *eofSplitIncrementalCompleter) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	e.calls++
	u := messages[len(messages)-1].Content
	if strings.Contains(u, "a.go") && strings.Contains(u, "b.go") {
		return nil, errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
	}
	if strings.Contains(u, "a.go") && !strings.Contains(u, "b.go") {
		return &model.CompleteResult{Content: "patch-left"}, nil
	}
	return &model.CompleteResult{Content: "patch-right"}, nil
}

func TestCompleteOverviewIncrementalSliceWithEOFResplit_splits(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "a.go", Lang: "go", Module: "m"},
			{File: "b.go", Lang: "go", Module: "m"},
		},
	}
	llm := &eofSplitIncrementalCompleter{}
	g := &LLMOverviewDocGenerator{LLM: llm}
	stat := &OverviewLLMStats{}
	delta, err := g.completeOverviewIncrementalSliceWithEOFResplit(ctx, st, "go", 0, 1, []string{"a.go", "b.go"},
		"# Title\n\nexisting", "system prompt", "2099-01-01", 0, false, 0, stat)
	if err != nil {
		t.Fatal(err)
	}
	if llm.calls != 3 {
		t.Fatalf("calls=%d want 3", llm.calls)
	}
	if !strings.Contains(delta, "patch-left") || !strings.Contains(delta, "patch-right") {
		t.Fatalf("delta=%q", delta)
	}
}

func TestGenerateOverviewWithMeta_fullBatchedMergesSlices(t *testing.T) {
	ctx := context.Background()
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "a.go", Lang: "go", Module: "m"},
			{File: "b.go", Lang: "go", Module: "m"},
			{File: "c.go", Lang: "go", Module: "m"},
		},
	}
	g := &LLMOverviewDocGenerator{
		LLM:         &sliceIndexCompleter{},
		FullRewrite: true,
	}
	content, _, stat, err := g.GenerateOverviewWithMeta(ctx, st, "go", 2, -1, OverviewGenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if stat.Partitions != 2 {
		t.Fatalf("Partitions=%d; want 2", stat.Partitions)
	}
	if stat.TotalSourceFiles != 3 {
		t.Fatalf("TotalSourceFiles=%d; want 3", stat.TotalSourceFiles)
	}
	if !strings.Contains(content, "first slice 1") || !strings.Contains(content, "continuation 2") {
		t.Fatalf("merged content missing slices:\n%s", content)
	}
}

func TestResolveOverviewMaxIndexRunesPerSlice(t *testing.T) {
	if lim, on := resolveOverviewMaxIndexRunesPerSlice(-1); on || lim != 0 {
		t.Fatalf("-1: got lim=%d on=%v", lim, on)
	}
	if lim, on := resolveOverviewMaxIndexRunesPerSlice(0); !on || lim != defaultOverviewMaxIndexRunesPerSlice {
		t.Fatalf("0: got lim=%d on=%v", lim, on)
	}
	if lim, on := resolveOverviewMaxIndexRunesPerSlice(50_000); !on || lim != 50_000 {
		t.Fatalf("50k: got lim=%d on=%v", lim, on)
	}
}

func TestRefineOverviewBatchesByIndexRunes_splitsHeavyBatch(t *testing.T) {
	ctx := context.Background()
	var methods []*metadata.Symbol
	for i := 0; i < 2000; i++ {
		methods = append(methods, &metadata.Symbol{FQName: fmt.Sprintf("pkg.A.M_%d", i), File: "a.go", Kind: "method"})
	}
	for i := 0; i < 2000; i++ {
		methods = append(methods, &metadata.Symbol{FQName: fmt.Sprintf("pkg.B.M_%d", i), File: "b.go", Kind: "method"})
	}
	st := &stubOverviewMeta{
		files: []*metadata.File{
			{File: "a.go", Lang: "go", Module: "m"},
			{File: "b.go", Lang: "go", Module: "m"},
		},
		methods: methods,
	}
	batches := [][]string{{"a.go", "b.go"}}
	out, err := refineOverviewBatchesByIndexRunes(ctx, st, "go", batches, 12_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 2 {
		t.Fatalf("expected split into at least 2 batches, got %d: %v", len(out), out)
	}
}

func TestTruncateUTF8ToMaxRunesWithTrailer_overviewSlice(t *testing.T) {
	s := strings.Repeat("x", 500)
	out := truncateUTF8ToMaxRunesWithTrailer(s, 120, "ENDMARK")
	if utf8.RuneCountInString(out) > 120 {
		t.Fatalf("len runes %d > 120", utf8.RuneCountInString(out))
	}
	if !strings.Contains(out, "ENDMARK") {
		t.Fatalf("missing trailer: %q", out)
	}
}
