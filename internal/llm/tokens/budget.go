package tokens

import "strings"

// SectionKind identifies a context section for budgeting.
type SectionKind string

const (
	SectionIntro        SectionKind = "intro"
	SectionTarget       SectionKind = "target"       // the target method itself — the code under test
	SectionTargetClass  SectionKind = "target_class" // enclosing class/container: context, not the unit under test
	SectionDependencies SectionKind = "dependencies" // dependency graph
	SectionSimilar      SectionKind = "similar"      // similar reference chunks
	SectionDomain       SectionKind = "domain"       // domain models / collaborators
	SectionFixtures     SectionKind = "fixtures"     // fixtures + config
	SectionProjectIntel SectionKind = "project_intel"
	SectionExisting     SectionKind = "existing" // existing test file (extend mode)
	SectionContract     SectionKind = "contract" // output contract
)

// reservation is the share of the budget a section may claim, and whether it can be cut.
type reservation struct {
	share       float64
	truncatable bool
}

// defaultReservations allocates the budget across sections.
//
// Two entries are never truncated. The **target** is the code under test — truncating it defeats
// the entire task. The **contract** is the required output shape; it sits last in the prompt, which
// means a provider-side input truncation silently removes it first, and the model then emits
// free-form prose where a JSON object was required. That failure mode presents as "the LLM produced
// garbage" and is exactly what a budget exists to prevent.
//
// The enclosing class is deliberately NOT in that group. It is *context* — the field set, the
// constructor, the sibling signatures — not the unit under test, and it is the single largest
// unbounded input in practice: a gap whose enclosing class is a 3000-line God object emitted that
// class in full into every one of its methods' prompts. Leaving it untruncatable would mean the
// budget cannot be enforced at all on exactly the repos that most need it. It gets a generous share
// so it is only clamped when it would otherwise consume the whole allowance.
var defaultReservations = map[SectionKind]reservation{
	SectionIntro:        {share: 0.03, truncatable: false},
	SectionContract:     {share: 0.05, truncatable: false},
	SectionTarget:       {share: 0.10, truncatable: false},
	SectionTargetClass:  {share: 0.20, truncatable: true},
	SectionDependencies: {share: 0.27, truncatable: true},
	SectionSimilar:      {share: 0.22, truncatable: true},
	SectionDomain:       {share: 0.08, truncatable: true},
	SectionFixtures:     {share: 0.03, truncatable: true},
	SectionProjectIntel: {share: 0.02, truncatable: true},
}

// Budget allocates a total token allowance across sections, releasing unspent allowance downward.
type Budget struct {
	total   int
	counter Counter
	alloc   map[SectionKind]int
	used    map[SectionKind]int
	order   []SectionKind
	// closed marks sections that can no longer spend, because a later section has started. Only
	// closed sections release their leftovers — see Remaining.
	closed map[SectionKind]bool
}

// releaseOrder is the order sections are emitted into the prompt, and therefore the order in which
// unspent allowance flows: a section releases its leftover only once it is finished, to sections
// that come after it.
//
// Getting this wrong is subtle and expensive. Crediting *not-yet-rendered* sections to the current
// one means the first section sees essentially the whole budget and clamping never bites — which is
// how a 3000-token budget produced a 12000-token prompt during development.
var releaseOrder = []SectionKind{
	SectionIntro,
	SectionTarget,
	SectionTargetClass,
	SectionDependencies,
	SectionDomain,
	SectionSimilar,
	SectionFixtures,
	SectionProjectIntel,
	SectionExisting,
	SectionContract,
}

// NewBudget returns a Budget over total tokens. A non-positive total means "unbounded": every
// Allow call returns the requested amount, so callers need no special case and existing behaviour
// is preserved when no budget is configured.
func NewBudget(total int, c Counter) *Budget {
	b := &Budget{total: total, counter: c, alloc: map[SectionKind]int{}, used: map[SectionKind]int{},
		closed: map[SectionKind]bool{}, order: releaseOrder}
	if total <= 0 {
		return b
	}
	for _, k := range releaseOrder {
		r := defaultReservations[k]
		b.alloc[k] = int(float64(total) * r.share)
	}
	return b
}

// Counter returns the token counter this budget uses.
func (b *Budget) Counter() Counter {
	if b == nil || b.counter == nil {
		return For("", "")
	}
	return b.counter
}

// Unbounded reports whether this budget imposes no limit.
func (b *Budget) Unbounded() bool { return b == nil || b.total <= 0 }

// Total returns the overall token allowance (0 when unbounded).
func (b *Budget) Total() int {
	if b == nil {
		return 0
	}
	return b.total
}

// Remaining returns the tokens still available to kind, including anything released from earlier
// sections that did not spend their share.
func (b *Budget) Remaining(kind SectionKind) int {
	if b.Unbounded() {
		return 1 << 30
	}
	avail := b.alloc[kind] - b.used[kind]
	if avail < 0 {
		avail = 0
	}
	return avail + b.released(kind)
}

// released sums the unspent allowance of sections that are finished (closed) and come before kind.
func (b *Budget) released(kind SectionKind) int {
	spare := 0
	for _, k := range b.order {
		if k == kind {
			break
		}
		if !b.closed[k] {
			continue
		}
		if s := b.alloc[k] - b.used[k]; s > 0 {
			spare += s
		}
	}
	return spare
}

// closeBefore marks every section emitted before kind as finished, so their leftovers become
// available to kind and to everything after it.
func (b *Budget) closeBefore(kind SectionKind) {
	for _, k := range b.order {
		if k == kind {
			return
		}
		b.closed[k] = true
	}
}

// Truncatable reports whether kind may be cut to fit.
func (b *Budget) Truncatable(kind SectionKind) bool {
	return defaultReservations[kind].truncatable
}

// Fit clamps s to what kind can still afford and records the spend.
//
// Non-truncatable sections are always emitted whole: their cost is recorded (so later sections see
// less headroom) but they are never cut. That is deliberate — a budget that truncates the code
// under test or the output contract to satisfy an arithmetic constraint has optimized the wrong
// thing.
func (b *Budget) Fit(kind SectionKind, s string) (out string, elidedLines int) {
	if s == "" {
		return "", 0
	}
	b.closeBefore(kind)
	cost := b.counter.Count(s)
	if b.Unbounded() || !b.Truncatable(kind) {
		b.used[kind] += cost
		return s, 0
	}
	avail := b.Remaining(kind)
	if cost <= avail {
		b.used[kind] += cost
		return s, 0
	}
	kept, elided := ClampToTokens(s, avail, b.counter)
	b.used[kind] += b.counter.Count(kept)
	return kept, elided
}

// Spend records tokens consumed by kind without clamping (for text emitted outside Fit).
func (b *Budget) Spend(kind SectionKind, s string) {
	if b == nil || s == "" {
		return
	}
	b.closeBefore(kind)
	b.used[kind] += b.counter.Count(s)
}

// Used returns tokens consumed by kind.
func (b *Budget) Used(kind SectionKind) int {
	if b == nil {
		return 0
	}
	return b.used[kind]
}

// UsedTotal returns tokens consumed across all sections.
func (b *Budget) UsedTotal() int {
	if b == nil {
		return 0
	}
	t := 0
	for _, v := range b.used {
		t += v
	}
	return t
}

// Breakdown returns per-section token usage for telemetry.
func (b *Budget) Breakdown() map[string]int {
	if b == nil {
		return nil
	}
	out := make(map[string]int, len(b.used))
	for k, v := range b.used {
		if v > 0 {
			out[string(k)] = v
		}
	}
	return out
}

// ModelWindow returns the input context window for a model, or 0 when unknown.
//
// Deliberately conservative and small: an unknown model returns 0, which means "no budget", which
// means today's unbounded behaviour rather than a guessed cap that could silently truncate a
// perfectly valid prompt. Configure retrieval.context.max_tokens to bound an unlisted model.
func ModelWindow(provider, model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-4.1"), strings.Contains(m, "gpt-5"):
		return 128000
	case strings.Contains(m, "gpt-4-turbo"):
		return 128000
	case strings.Contains(m, "gpt-4"):
		return 8192
	case strings.Contains(m, "gpt-3.5"):
		return 16385
	case strings.Contains(m, "claude"):
		return 200000
	case strings.Contains(m, "qwen"), strings.Contains(m, "devstral"), strings.Contains(m, "codestral"):
		// Local models vary widely by tag and are additionally bounded by llm.ollama_num_ctx,
		// which is the real constraint. Return 0 so the configured num_ctx / max_context_tokens
		// decides rather than a guess derived from a model name.
		return 0
	default:
		return 0
	}
}

// Resolve computes the input budget for a request: the model window minus the output reservation
// and a safety margin, further capped by an explicit configured limit.
//
// Returns 0 (unbounded) when neither a known window nor a configured cap is available — preserving
// current behaviour rather than inventing a limit.
func Resolve(provider, model string, outputTokens, configuredMax int) int {
	budget := 0
	if w := ModelWindow(provider, model); w > 0 {
		budget = w - outputTokens - safetyMargin(w)
		if budget < 0 {
			budget = 0
		}
	}
	if configuredMax > 0 && (budget == 0 || configuredMax < budget) {
		budget = configuredMax
	}
	return budget
}

// safetyMargin reserves headroom for the parts of a request nobody counts: chat-template overhead,
// role markers, and the difference between an estimate and the provider's real tokenizer.
func safetyMargin(window int) int {
	m := window / 20 // 5%
	if m < 512 {
		m = 512
	}
	if m > 8000 {
		m = 8000
	}
	return m
}
