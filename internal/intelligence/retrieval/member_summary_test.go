package retrieval

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/llm/tokens"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// visitTypeWithMembers models the shape that broke in the observed run: a small domain type whose
// real accessor is getDate(), where the generated test called getVisitDate().
func visitTypeWithMembers() (*metadata.Symbol, *mockMetaReaderForRetrieve) {
	visit := &metadata.Symbol{
		ID: "s_visit", Kind: "class", Lang: "java",
		FQName: "p.Visit", File: "src/main/java/p/Visit.java",
		StartLine: 10, EndLine: 60,
	}
	members := []*metadata.Symbol{
		visit,
		{ID: "s_getdate", Kind: "method", FQName: "p.Visit#getDate", File: visit.File, StartLine: 20, EndLine: 22,
			SignatureJSON: []byte(`{"signature":"LocalDate getDate()","visibility":"public"}`)},
		{ID: "s_setdate", Kind: "method", FQName: "p.Visit#setDate", File: visit.File, StartLine: 24, EndLine: 26,
			SignatureJSON: []byte(`{"signature":"void setDate(LocalDate date)","visibility":"public"}`)},
		{ID: "s_desc", Kind: "field", FQName: "p.Visit#description", File: visit.File, StartLine: 15, EndLine: 15,
			SignatureJSON: []byte(`{"type":"String","name":"description","visibility":"public"}`)},
		{ID: "s_secret", Kind: "method", FQName: "p.Visit#internalOnly", File: visit.File, StartLine: 40, EndLine: 42,
			SignatureJSON: []byte(`{"signature":"void internalOnly()","visibility":"private"}`)},
		// Outside the type's line range: belongs to another class in the same file.
		{ID: "s_other", Kind: "method", FQName: "p.Other#stray", File: visit.File, StartLine: 80, EndLine: 82,
			SignatureJSON: []byte(`{"signature":"void stray()","visibility":"public"}`)},
	}
	return visit, &mockMetaReaderForRetrieve{
		symbols: map[string]*metadata.Symbol{"s_visit": visit},
		byFile:  map[string][]*metadata.Symbol{visit.File: members},
	}
}

func TestMemberSummaryForType(t *testing.T) {
	visit, meta := visitTypeWithMembers()
	got := memberSummaryForType(context.Background(), meta, "", visit, maxMembersPerType)

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "getDate()") {
		t.Fatalf("the real accessor is missing from the member list:\n%s", joined)
	}
	if !strings.Contains(joined, "String description") {
		t.Fatalf("public field missing from the member list:\n%s", joined)
	}
	if strings.Contains(joined, "internalOnly") {
		t.Fatalf("private member leaked into the list:\n%s", joined)
	}
	if strings.Contains(joined, "stray") {
		t.Fatalf("a member of a different type in the same file leaked in:\n%s", joined)
	}
	// Source order (by start line): description (15) before getDate (20) before setDate (24).
	if len(got) != 3 {
		t.Fatalf("got %d members, want 3: %v", len(got), got)
	}
	if !strings.Contains(got[0], "description") {
		t.Fatalf("members not in source order: %v", got)
	}
}

func TestMemberSummaryForType_capsLength(t *testing.T) {
	owner := &metadata.Symbol{ID: "s_big", Kind: "class", FQName: "p.Big", File: "p/Big.java", StartLine: 1, EndLine: 1000}
	syms := []*metadata.Symbol{owner}
	for i := 0; i < 40; i++ {
		syms = append(syms, &metadata.Symbol{
			ID: "m" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Kind: "method",
			FQName: "p.Big#m", File: owner.File, StartLine: 10 + i, EndLine: 10 + i,
			SignatureJSON: []byte(`{"signature":"void m` + string(rune('a'+i%26)) + string(rune('0'+i/26)) + `()","visibility":"public"}`),
		})
	}
	meta := &mockMetaReaderForRetrieve{byFile: map[string][]*metadata.Symbol{owner.File: syms}}
	got := memberSummaryForType(context.Background(), meta, "", owner, maxMembersPerType)
	if len(got) != maxMembersPerType {
		t.Fatalf("got %d members, want the cap of %d", len(got), maxMembersPerType)
	}
}

func TestMemberSummaryForType_nilInputs(t *testing.T) {
	if got := memberSummaryForType(context.Background(), nil, "", &metadata.Symbol{}, 5); got != nil {
		t.Fatalf("nil meta: got %v", got)
	}
	meta := &mockMetaReaderForRetrieve{}
	if got := memberSummaryForType(context.Background(), meta, "", nil, 5); got != nil {
		t.Fatalf("nil symbol: got %v", got)
	}
}

// The rendered block must be unambiguous that the list is exhaustive; a softer phrasing leaves the
// model free to assume it is a sample and invent the member it expected.
func TestMemberListBlock_rendersExhaustiveWording(t *testing.T) {
	out := memberListBlock([]string{"LocalDate getDate()"}, nil, tokens.SectionDomain)
	if !strings.Contains(out, "the only members that exist") {
		t.Fatalf("member block wording is not exhaustive:\n%s", out)
	}
	if !strings.Contains(out, "LocalDate getDate()") {
		t.Fatalf("member block lost its content:\n%s", out)
	}
	if memberListBlock(nil, nil, tokens.SectionDomain) != "" {
		t.Fatal("empty member list must render nothing")
	}
}
