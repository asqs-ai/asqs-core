package migrate

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// The runner's contract, against a real server: only unapplied migrations run, application order
// is slice order, a second invocation is a no-op, and a migration added later applies alone.
// Uses throwaway migration IDs namespaced to the test so re-runs against the shared scratch
// database stay independent of the real (currently empty) migration lists.
func TestRun_appliesOnlyPending(t *testing.T) {
	url, why := metadata.ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	// A deferred func, not t.Cleanup: cleanups run after the test function returns, which is after
	// the deferred pool.Close() — so a t.Cleanup here would execute against a closed pool and its
	// probe rows would leak into the shared scratch database.
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE id LIKE 'livetest_%'`)
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS livetest_migrate_probe`)
	}()

	var order []string
	mk := func(id string) Migration {
		return Migration{
			ID:          id,
			Description: "live-test probe",
			Apply: func(ctx context.Context, pool *pgxpool.Pool) error {
				order = append(order, id)
				_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS livetest_migrate_probe (id TEXT)`)
				return err
			},
		}
	}
	two := []Migration{mk("livetest_0001"), mk("livetest_0002")}

	res, err := Run(ctx, pool, two, nil)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(res.Applied) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("first run applied=%v skipped=%v, want both applied", res.Applied, res.Skipped)
	}
	if len(order) != 2 || order[0] != "livetest_0001" || order[1] != "livetest_0002" {
		t.Fatalf("application order = %v, want slice order", order)
	}

	// Second invocation: a no-op that records nothing new.
	order = nil
	res, err = Run(ctx, pool, two, nil)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(res.Applied) != 0 || len(res.Skipped) != 2 || len(order) != 0 {
		t.Fatalf("second run applied=%v skipped=%v ran=%v, want all skipped and nothing executed", res.Applied, res.Skipped, order)
	}

	// A migration added later applies alone.
	three := append(two, mk("livetest_0003"))
	res, err = Run(ctx, pool, three, nil)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "livetest_0003" || len(res.Skipped) != 2 {
		t.Fatalf("third run applied=%v skipped=%v, want only livetest_0003 applied", res.Applied, res.Skipped)
	}

	pending, err := Pending(ctx, pool, three)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want none", pending)
	}
}
