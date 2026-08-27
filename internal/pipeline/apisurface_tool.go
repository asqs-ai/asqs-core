package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/apisurface"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// thirdPartySurfaceFunc adapts the evaluator's classpath surface provider to get_symbol's miss
// ladder: a fully-qualified name that is not in the repository index is resolved against the build
// classpath and rendered as a verbatim member list — the same compiler-authoritative facts
// evaluator.writeAPISurfaceBlock injects prompt-side, now available at the moment the model asks.
//
// The motivating miss: run api-eb300211385b9616dc6cf81bd513369b asked get_symbol for
// com.microsoft.playwright.Route.FulfillOptions, was told "not indexed", and guessed setJson —
// while javap against the already-resolved classpath knew the real member list the whole time.
func thirdPartySurfaceFunc(surface apisurface.Provider, repoPath string) func(ctx context.Context, fq string) (string, bool) {
	if surface == nil || strings.TrimSpace(repoPath) == "" {
		return nil
	}
	return func(ctx context.Context, fq string) (string, bool) {
		fq = metadata.BareFQName(fq)
		member := ""
		if i := strings.Index(fq, "#"); i >= 0 {
			member = strings.TrimSpace(fq[i+1:])
			fq = strings.TrimSpace(fq[:i])
		}
		if fq == "" {
			return "", false
		}
		// A dotted name is a type to javap/d.ts-resolve; a bare simple name is a symbol whose
		// providing type (and import line) the class index can name.
		kind := apisurface.KindType
		if !strings.Contains(fq, ".") {
			kind = apisurface.KindSymbol
		}
		surfaces, err := surface.Lookup(ctx, repoPath, []apisurface.Target{{Kind: kind, Name: fq, Member: member}})
		if err != nil {
			// The provider could not check at all (no classpath, no declarations, no docs). It has
			// nothing to say either way, so the ladder continues to the web.
			return "", false
		}
		if len(surfaces) == 0 {
			// Upstream additionally renders a proof-of-absence answer here (classpathAbsenceAnswer);
			// it needs the absence-prover plumbing that arrives with its own bundle. Until then an
			// empty result means "checked, not found", and the ladder simply continues.
			return "", false
		}
		var b strings.Builder
		for _, s := range surfaces {
			if len(s.Members) == 0 {
				fmt.Fprintf(&b, "%s resolves on the build classpath (not in the repository index); import %s\n", s.FQCN, s.FQCN)
				continue
			}
			origin := ""
			if strings.TrimSpace(s.Origin) != "" {
				origin = " [" + s.Origin + "]"
			}
			fmt.Fprintf(&b, "%s%s — members read from the build classpath (not in the repository index):\n", s.FQCN, origin)
			for _, m := range s.Members {
				b.WriteString("  " + m + "\n")
			}
			if s.Truncated {
				// Members is a rendering budget; the underlying read was complete. Give the full
				// name set so absence stays provable even when signatures were cut.
				b.WriteString("  … (signature list truncated; complete member NAMES: " + strings.Join(s.AllMemberNames, ", ") + ")\n")
			} else {
				b.WriteString("  (complete member list: a member not shown above does not exist)\n")
			}
		}
		if b.Len() == 0 {
			return "", false
		}
		return b.String(), true
	}
}
