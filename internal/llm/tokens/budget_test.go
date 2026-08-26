package tokens

import (
	"strings"
	"testing"
)

func body(lines int) string {
	return strings.Repeat("some source line of roughly typical width here\n", lines)
}

// TestBudget_neverTruncatesTargetOrContract is the most important property in this package.
//
// The target is the code under test — truncating it defeats the task. The contract is the required
// output shape and it sits LAST in the prompt, so a provider-side input truncation removes it
// first; the model then emits prose where a JSON object was required, which presents as "the LLM
// produced garbage". A budget that cut either to satisfy arithmetic would have optimized the wrong
// thing.
func TestBudget_neverTruncatesTargetOrContract(t *testing.T) {
	c := For("", "")
	b := NewBudget(100, c) // deliberately far too small
	huge := body(200)

	for _, kind := range []SectionKind{SectionTarget, SectionContract, SectionIntro} {
		out, elided := b.Fit(kind, huge)
		if out != huge {
			t.Errorf("%s was truncated; it must be emitted whole", kind)
		}
		if elided != 0 {
			t.Errorf("%s reported %d elided lines; it must never be cut", kind, elided)
		}
	}
}

func TestBudget_truncatesTruncatableSections(t *testing.T) {
	c := For("", "")
	b := NewBudget(2000, c)
	huge := body(500)

	out, elided := b.Fit(SectionDependencies, huge)
	if len(out) >= len(huge) {
		t.Fatal("dependencies should have been clamped")
	}
	if elided == 0 {
		t.Error("clamping should report the number of elided lines so the model can be told")
	}
}

// Unspent allowance flows FORWARD, and only from sections that are finished.
//
// A section is finished once a later one starts emitting. A repo with a tiny enclosing class
// therefore hands that leftover to dependencies and exemplars rather than wasting it — but a
// section that has not rendered yet contributes nothing, because it may still spend its whole
// share. Crediting not-yet-rendered sections is what made a 3000-token budget produce a
// 12000-token prompt during development.
func TestBudget_releasesUnspentAllowanceDownward(t *testing.T) {
	c := For("", "")

	// Nothing rendered yet: dependencies see only their own share.
	fresh := NewBudget(10000, c)
	bare := fresh.Remaining(SectionDependencies)

	// Earlier sections rendered cheaply, so their leftovers are released forward.
	used := NewBudget(10000, c)
	used.Fit(SectionTarget, "tiny")
	used.Fit(SectionTargetClass, "tiny")
	afterCheapEarlySections := used.Remaining(SectionDependencies)

	if afterCheapEarlySections <= bare {
		t.Errorf("dependencies should gain the leftovers of finished earlier sections: %d vs bare %d",
			afterCheapEarlySections, bare)
	}
}

// A section that has not rendered yet must NOT inflate an earlier section's allowance.
func TestBudget_doesNotCreditUnrenderedSections(t *testing.T) {
	c := For("", "")
	b := NewBudget(10000, c)

	// TargetClass renders before Dependencies/Similar/Fixtures. Its allowance must not include
	// theirs, or the first big section swallows the whole budget and clamping never bites.
	got := b.Remaining(SectionTargetClass)
	share := int(10000 * defaultReservations[SectionTargetClass].share)
	if got > share+int(10000*defaultReservations[SectionTarget].share)+int(10000*defaultReservations[SectionIntro].share) {
		t.Errorf("TargetClass remaining %d exceeds its own share plus preceding sections; later sections were credited early", got)
	}
}

// A zero/negative total means unbounded, which is what preserves pre-budget behaviour when no
// model window is known and no cap is configured.
func TestBudget_unboundedIsANoOp(t *testing.T) {
	c := For("", "")
	for _, total := range []int{0, -1} {
		b := NewBudget(total, c)
		if !b.Unbounded() {
			t.Fatalf("total %d should be unbounded", total)
		}
		huge := body(1000)
		out, elided := b.Fit(SectionDependencies, huge)
		if out != huge || elided != 0 {
			t.Errorf("unbounded budget modified content (total=%d)", total)
		}
	}
}

func TestBudget_nilSafe(t *testing.T) {
	var b *Budget
	if !b.Unbounded() {
		t.Error("nil budget should report unbounded")
	}
	if b.Total() != 0 || b.UsedTotal() != 0 || b.Used(SectionTarget) != 0 {
		t.Error("nil budget accessors should return zero")
	}
	if b.Breakdown() != nil {
		t.Error("nil budget breakdown should be nil")
	}
	b.Spend(SectionTarget, "x") // must not panic
}

func TestBudget_breakdownReportsPerSectionSpend(t *testing.T) {
	c := For("", "")
	b := NewBudget(100000, c)
	b.Fit(SectionTarget, body(10))
	b.Fit(SectionDependencies, body(20))

	bd := b.Breakdown()
	if bd["target"] == 0 || bd["dependencies"] == 0 {
		t.Fatalf("breakdown missing sections: %+v", bd)
	}
	if bd["similar"] != 0 {
		t.Errorf("unspent sections should be absent from the breakdown, got %+v", bd)
	}
	if b.UsedTotal() != bd["target"]+bd["dependencies"] {
		t.Errorf("UsedTotal %d != sum of breakdown %+v", b.UsedTotal(), bd)
	}
}

func TestResolve(t *testing.T) {
	// Known model: window minus output reservation minus safety margin.
	got := Resolve("openai", "gpt-4o", 8192, 0)
	if got <= 0 || got >= 128000 {
		t.Errorf("Resolve for gpt-4o = %d, want a positive value below the 128k window", got)
	}

	// A configured cap wins when it is tighter.
	if got := Resolve("openai", "gpt-4o", 8192, 20000); got != 20000 {
		t.Errorf("configured cap should win: got %d, want 20000", got)
	}

	// Unknown model and no cap = unbounded, NOT a guessed limit. Inventing a cap here would
	// silently truncate a perfectly valid prompt on any model not in the table.
	if got := Resolve("ollama", "some-unlisted-model", 8192, 0); got != 0 {
		t.Errorf("unknown model with no configured cap should be unbounded, got %d", got)
	}

	// Unknown model WITH a cap uses the cap.
	if got := Resolve("ollama", "some-unlisted-model", 8192, 16000); got != 16000 {
		t.Errorf("unknown model with a cap should use it, got %d", got)
	}
}

func TestModelWindow(t *testing.T) {
	if ModelWindow("openai", "gpt-4o-mini") != 128000 {
		t.Error("gpt-4o family should be 128k")
	}
	if ModelWindow("anthropic", "claude-sonnet-4-20250514") != 200000 {
		t.Error("claude family should be 200k")
	}
	// Local models are bounded by llm.ollama_num_ctx, not by a guess from the model name.
	if ModelWindow("ollama", "qwen2.5-coder:32b") != 0 {
		t.Error("local models should report unknown so num_ctx / max_context_tokens decides")
	}
	if ModelWindow("", "") != 0 {
		t.Error("unknown model should report 0")
	}
}
