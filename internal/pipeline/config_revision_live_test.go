package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// TestEnsureConfigRevisionForRun_reusesUnchangedAppendsChanged is live because the reuse contract
// is a body comparison against what Postgres stored: re-running one configuration N times is the
// normal A/B shape and must reuse one revision, while an edited file must append a new one.
func TestEnsureConfigRevisionForRun_reusesUnchangedAppendsChanged(t *testing.T) {
	url, why := metadata.ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := metadata.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		configs, _ := s.ListConfigs(bg)
		for _, c := range configs {
			if c.Name == "cli" {
				_, _ = s.DeleteConfig(bg, c.ID)
			}
		}
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  provider: openai\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No file → no revision, no error: env-only configs have no body to version.
	if id, err := ensureConfigRevisionForRun(ctx, s, ""); err != nil || id != "" {
		t.Fatalf("empty path: id=%q err=%v, want empty and nil", id, err)
	}

	first, err := ensureConfigRevisionForRun(ctx, s, path)
	if err != nil || first == "" {
		t.Fatalf("first: id=%q err=%v", first, err)
	}
	again, err := ensureConfigRevisionForRun(ctx, s, path)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("unchanged file minted a new revision: %s then %s — N runs of one configuration must share one revision", first, again)
	}

	if err := os.WriteFile(path, []byte("llm:\n  provider: anthropic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ensureConfigRevisionForRun(ctx, s, path)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed file reused the old revision; the A/B report would merge two different configurations into one row")
	}
}
