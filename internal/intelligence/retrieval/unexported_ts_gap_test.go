package retrieval

import (
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func tsSymbol(kind, fq, sig string) *metadata.Symbol {
	return &metadata.Symbol{
		ID: fq, Lang: "typescript", Kind: kind, FQName: fq, File: "src/main.ts",
		StartLine: 8, EndLine: 21, SignatureJSON: []byte(sig),
	}
}

// Run api-a716cb7b25db5a880b4675a04b0b08c3 planned `src.main.bootstrap`: an unexported function in
// a NestJS entry point that calls app.listen at module load. Nothing can import it, so the
// generated test failed with "bootstrap is not a function" in all nine fix rounds and was one of the
// three artifacts the discard removed. The indexer already records `exported: false` for it.
func TestGapEligibility_dropsUnexportedTypeScriptFunctions(t *testing.T) {
	cases := []struct {
		name     string
		sym      *metadata.Symbol
		eligible bool
		reason   string
	}{
		{
			name:   "unexported module-level function",
			sym:    tsSymbol("function", "src.main.bootstrap", `{"exported":false,"visibility":"internal"}`),
			reason: IneligibleUnexported,
		},
		{
			name:   "unexported arrow function const",
			sym:    tsSymbol("function", "src.util.helper", `{"exported":false,"visibility":"internal","type":"() => void"}`),
			reason: IneligibleUnexported,
		},
		{
			name:     "exported function stays",
			sym:      tsSymbol("function", "src.util.parse", `{"exported":true,"visibility":"public"}`),
			eligible: true,
		},
		{
			name:     "no export information stays (older index rows)",
			sym:      tsSymbol("function", "src.util.legacy", `{"signature":"legacy()"}`),
			eligible: true,
		},
		{
			name: "class member visibility is not this rule's business",
			sym: &metadata.Symbol{
				ID: "m", Lang: "typescript", Kind: "method", FQName: "src.svc.Svc#run", File: "src/svc.ts",
				StartLine: 3, EndLine: 12, SignatureJSON: []byte(`{"exported":false,"visibility":"protected"}`),
			},
			eligible: true,
		},
		{
			// The indexer's flag is ESM syntax; a CommonJS `module.exports = { helper }` reads as
			// unexported, so JavaScript is left alone until the flag understands it.
			name: "javascript is not gated",
			sym: &metadata.Symbol{
				ID: "j", Lang: "javascript", Kind: "function", FQName: "lib.helper", File: "lib/helper.js",
				StartLine: 3, EndLine: 12, SignatureJSON: []byte(`{"exported":false,"visibility":"internal"}`),
			},
			eligible: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := gapEligibility(tc.sym, nil, 2)
			if ok != tc.eligible {
				t.Fatalf("eligible = %v (reason %q), want %v", ok, reason, tc.eligible)
			}
			if !ok && reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}
