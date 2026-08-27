package tools

import (
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// resolveSymbol's bare-name fallback is reached through a type assertion on r.Meta, so if the
// concrete store does not implement the lookup the assertion simply fails and get_symbol answers
// "no symbol" for every parameterless C# name a model quotes from prose — with no build error and
// no log line. The fallback shipped ahead of the store method and was inert exactly this way until
// this bundle landed ListSymbolsByBareFQName, so the wiring is pinned here.
func TestStoreSatisfiesBareFQLookup(t *testing.T) {
	var _ bareFQLookup = (*metadata.Store)(nil)
}
