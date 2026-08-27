package indexer

import (
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// The B25 forced reindex hangs off an OPTIONAL interface assertion on the metadata writer, which
// means a store that stops implementing it does not break the build — the reindex just silently
// never fires, and the repository keeps a half-legacy, half-parameterized symbol graph. That is
// precisely the failure this bundle exists to prevent, so the satisfaction is asserted here rather
// than left to a runtime type switch nobody watches.
func TestStoreSatisfiesLegacyCSharpFQCounter(t *testing.T) {
	var _ legacyCSharpFQCounter = (*metadata.Store)(nil)
}
