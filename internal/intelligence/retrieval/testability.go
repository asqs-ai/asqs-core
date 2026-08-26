package retrieval

import (
	"encoding/json"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// Testability scoring exists because ListGaps had no *positive* signal at all. Priority was built
// only from penalties and two narrow bonuses (+30 business-critical, +N inbound edges when ≥3, −20
// existing test file, −38 TESTS_SOURCE trace). On a repo where none of those apply — the common
// case — every candidate scores 0, and sortByPriority's FQName tie-break then makes selection
// effectively **alphabetical**.
//
// That is not a hypothetical: in the run this was written for, the ten selected Java gaps were in
// exact alphabetical FQName order (owner.OwnerRepository ×2 → owner.Pet ×2 → owner.PetTypeRepository
// → owner.Visit ×2 → system.CacheConfiguration → system.WebConfiguration ×2), and consisted almost
// entirely of trivial getters, Spring `@Bean` factories, and Spring Data repository *interface*
// methods with no body. All ten reported `missing_branch_intents: 0` — the branch-intent extractor
// was right; the targets were simply not worth testing.
const (
	// maxBranchIntentPoints etc. bound each signal's contribution. The weights are ordered so
	// branch evidence dominates, then real collaboration, then size — a heavily-branched method in
	// a plain module should outrank a getter in a "business-critical" one, which is why the ceiling
	// (40) deliberately exceeds the +30 critical-module bonus.
	maxBranchIntentPoints = 12
	maxOutboundCallPoints = 18
	maxSpanPoints         = 6
	maxArityPoints        = 4
	// MaxTestabilityScore is the highest value TestabilityScore can return.
	MaxTestabilityScore = maxBranchIntentPoints + maxOutboundCallPoints + maxSpanPoints + maxArityPoints
)

// TestabilityScore returns a 0..MaxTestabilityScore positive signal for how much *behaviour* a
// symbol has to test. Added to TestGap.Priority, so the existing bands keep their relative meaning.
//
// branchIntents may be nil: it is only available for the shortlist that ListGapsWithChunks fetches
// chunks for (fetching a chunk per candidate across a large repo is not affordable). Everything
// else is derived from data ListGaps already has in hand.
func TestabilityScore(sym *metadata.Symbol, outboundCalls int, branchIntents []string) int {
	if sym == nil {
		return 0
	}
	score := 0

	if n := len(branchIntents); n > 0 {
		if n > 4 {
			n = 4
		}
		score += 3 * n
	}

	if outboundCalls > 0 {
		n := outboundCalls
		if n > 6 {
			n = 6
		}
		score += 3 * n
	}

	switch span := sym.EndLine - sym.StartLine; {
	case span >= 25:
		score += 6
	case span >= 12:
		score += 4
	case span >= 5:
		score += 2
	}

	if arity := signatureArity(sym); arity > 0 {
		if arity > 2 {
			arity = 2
		}
		score += arity * 2
	}

	return score
}

// signatureArity counts declared parameters from signature_json's "signature" text. Returns 0 when
// the signature is absent or has no parameter list — a conservative default, since over-counting
// would inflate the score of symbols we know least about.
func signatureArity(sym *metadata.Symbol) int {
	if sym == nil || len(sym.SignatureJSON) == 0 {
		return 0
	}
	var parsed struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(sym.SignatureJSON, &parsed); err != nil {
		return 0
	}
	sig := strings.TrimSpace(parsed.Signature)
	open := strings.Index(sig, "(")
	closeIdx := strings.LastIndex(sig, ")")
	if open < 0 || closeIdx <= open {
		return 0
	}
	inner := strings.TrimSpace(sig[open+1 : closeIdx])
	if inner == "" {
		return 0
	}
	// Commas inside generics (Map<String, Integer>) must not inflate the count.
	depth := 0
	count := 1
	for _, r := range inner {
		switch r {
		case '<', '[', '(':
			depth++
		case '>', ']', ')':
			depth--
		case ',':
			if depth == 0 {
				count++
			}
		}
	}
	return count
}

// Ineligibility reasons, surfaced in the plan.gaps_filtered_ineligible audit payload. Without a
// per-reason breakdown a mis-tuned filter is invisible: the plan just silently gets smaller.
const (
	IneligibleInterfaceMember = "interface_member"
	IneligibleFrameworkConfig = "framework_config_bean"
	IneligibleTrivialAccessor = "trivial_accessor"
	IneligibleNoBody          = "no_body"
)

// gapEligibility decides whether a candidate symbol represents testable behaviour.
//
// Everything here is derived from data the index already persists — deliberately not from a new
// indexer field. Adding `isAbstract`/`hasBody` to JavaIndexer.java would need a jar rebuild, a
// Docker image ship, `integration`-tagged tests outside the default `go test ./...` gate, three
// language implementations, a chunk-metadata allowlist change, and a full reindex of every existing
// repo before it did anything. The enclosing type's Kind is already persisted and already reachable
// via classFQFromMethodOrType + ListSymbolsByFQName.
//
// enclosing may be nil when the declaring type cannot be resolved; in that case type-derived rules
// are skipped rather than guessed.
func gapEligibility(sym *metadata.Symbol, enclosing *metadata.Symbol, outboundCalls int) (ok bool, reason string) {
	if sym == nil {
		return false, IneligibleNoBody
	}
	span := sym.EndLine - sym.StartLine

	if enclosing != nil {
		if strings.EqualFold(strings.TrimSpace(enclosing.Kind), "interface") {
			// Spring Data derived queries (OwnerRepository#findByLastNameStartingWith) have no
			// body to test; generating for them is what dragged @DataJpaTest and @MockBean into
			// the failing run.
			return false, IneligibleInterfaceMember
		}
		if hasFrameworkConfigAnnotation(enclosing) {
			// @Configuration @Bean factories assert the framework wires itself up, not our logic.
			return false, IneligibleFrameworkConfig
		}
	}

	// Span-derived rules require *positive evidence* of a short body. Not every indexer populates
	// EndLine (and older rows predate it), so an unknown span must never be read as "bodyless" —
	// that would silently drop real methods wherever line data is missing.
	if hasKnownSpan(sym) {
		// A bodyless declaration (abstract method, one-line record accessor) that calls nothing.
		if span <= 1 && outboundCalls == 0 {
			return false, IneligibleNoBody
		}
		if isTrivialAccessorName(sym.FQName) && span <= 3 && outboundCalls == 0 {
			return false, IneligibleTrivialAccessor
		}
	}

	return true, ""
}

var frameworkConfigAnnotations = map[string]bool{
	"configuration":           true,
	"springbootapplication":   true,
	"enableautoconfiguration": true,
	"testconfiguration":       true,
	"autoconfiguration":       true,
}

// hasFrameworkConfigAnnotation reads the class-level annotations the indexer already persists in
// signature_json (JavaIndexer emits them for class-like symbols).
func hasFrameworkConfigAnnotation(sym *metadata.Symbol) bool {
	if sym == nil || len(sym.SignatureJSON) == 0 {
		return false
	}
	var parsed struct {
		Annotations []string `json:"annotations"`
	}
	if err := json.Unmarshal(sym.SignatureJSON, &parsed); err != nil {
		return false
	}
	for _, a := range parsed.Annotations {
		name := strings.TrimSpace(a)
		name = strings.TrimPrefix(name, "@")
		if i := strings.Index(name, "("); i >= 0 {
			name = name[:i]
		}
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		if frameworkConfigAnnotations[strings.ToLower(name)] {
			return true
		}
	}
	return false
}

// isTrivialAccessorName matches get/set/is/has-prefixed member names. Name alone is never enough —
// callers combine it with a short span and zero outbound calls, so a getter that actually computes
// something still qualifies as a gap.
func isTrivialAccessorName(fqName string) bool {
	name := metadata.BareFQName(fqName)
	if i := strings.LastIndex(name, "#"); i >= 0 {
		name = name[i+1:]
	} else if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	for _, p := range []string{"get", "set", "is", "has"} {
		if len(name) > len(p) && strings.HasPrefix(name, p) {
			r := rune(name[len(p)])
			if r >= 'A' && r <= 'Z' {
				return true
			}
		}
	}
	return false
}

// hasKnownSpan reports whether the symbol carries usable line-range data. StartLine is 1-based, so
// a zero on either end means "not recorded" rather than "empty body".
func hasKnownSpan(sym *metadata.Symbol) bool {
	return sym != nil && sym.StartLine > 0 && sym.EndLine > 0 && sym.EndLine >= sym.StartLine
}
