package embeddings

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// TestInitSchema_appliesAgainstALiveServer is the embeddings twin of the metadata live test
// (core-own; upstream covers this store through its callers): the real schema.sql, the dimension
// rewrite for a non-default model, and the idempotent re-run. Routed through the scratch-database
// guard because alignChunksEmbeddingColumn can TRUNCATE chunks on a dimension change — exactly the
// destructive write the guard exists to keep away from a real corpus.
func TestInitSchema_appliesAgainstALiveServer(t *testing.T) {
	url, why := metadata.ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	st, err := Open(ctx, Config{ConnString: url, Dimension: 768})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := st.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema (second run): %v", err)
	}
}
