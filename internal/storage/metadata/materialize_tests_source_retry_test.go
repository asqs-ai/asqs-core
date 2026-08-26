package metadata

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// badPool fails Begin for the first failCount attempts, then behaves. It is a deliberately minimal
// querier so the retry policy can be unit-tested with no Postgres and no build tag — the retry loop
// previously had no coverage at all because it needs a live handle.
//
// This used to be a registered database/sql driver returning driver.ErrBadConn, which pgxpool
// cannot be backed by. The replacement fails with SQLSTATE 57P01 (admin shutdown) instead, which is
// a strictly better fixture: it is what a backend restarting underneath an open transaction
// actually sends, i.e. the one failure this retry loop exists for, rather than a driver-layer
// sentinel that native pgx never produces.
//
// SCOPE, stated because these tests were once mistaken for coverage of the thing they surround.
// This fake fails at Begin, so materializeTestsSourceEdgesOnce never runs a single statement and
// insertTestsSourceFromNamingConvention is never reached. These tests therefore verify the retry
// POLICY (backoff schedule, ping between attempts, no retry on a non-transient error) and nothing
// about materialization itself. They passed throughout the period when materialization failed on
// 100% of real runs. The behaviour they cannot see is covered by the live tests in
// materialize_tests_source_test.go, which is where a nested-cursor regression would surface.
type badPool struct {
	mu        sync.Mutex
	failCount int
	begins    int
	pings     int
}

var _ querier = (*badPool)(nil)

func (p *badPool) Begin(ctx context.Context) (pgx.Tx, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.begins++
	if p.begins <= p.failCount {
		return nil, &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}
	}
	// A successful Begin is enough to prove the retry reached a live connection; this
	// distinguishable non-transient error is what the caller must NOT retry.
	return nil, errors.New("metadata-test: reached live connection")
}

func (p *badPool) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return p.Begin(ctx)
}

func (p *badPool) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pings++
	return nil
}

func (p *badPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("metadata-test: Query not implemented")
}

func (p *badPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return errRow{errors.New("metadata-test: QueryRow not implemented")}
}

func (p *badPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("metadata-test: Exec not implemented")
}

// errRow is a pgx.Row that fails on Scan, for the fake's unused query methods.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func newBadPool(t *testing.T, failCount int) *badPool {
	t.Helper()
	return &badPool{failCount: failCount}
}

func captureSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	prev := sleepFn
	sleepFn = func(d time.Duration) { got = append(got, d) }
	t.Cleanup(func() { sleepFn = prev })
	return &got
}

// The observed failure: three attempts completing in microseconds against the same dead pooled
// connection, failing 3/3 on four consecutive index passes. Each retry must back off and ping.
func TestMaterializeTestsSourceEdges_backsOffAndPingsBetweenAttempts(t *testing.T) {
	sleeps := captureSleeps(t)
	drv := newBadPool(t, materializeTestsSourceMaxAttempts*4) // always transient
	s := &Store{db: drv}

	if _, err := s.MaterializeTestsSourceEdges(context.Background(), "retry/repo"); err == nil {
		t.Fatal("expected an error when every attempt fails")
	}
	if len(*sleeps) != materializeTestsSourceMaxAttempts-1 {
		t.Fatalf("slept %d times (%v), want %d — one backoff between each pair of attempts",
			len(*sleeps), *sleeps, materializeTestsSourceMaxAttempts-1)
	}
	want := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}
	for i, d := range *sleeps {
		if d != want[i] {
			t.Errorf("backoff[%d] = %v, want %v", i, d, want[i])
		}
	}
	drv.mu.Lock()
	pings := drv.pings
	drv.mu.Unlock()
	if pings == 0 {
		t.Error("no Ping between retries; pgxpool will keep handing back the same dead connection")
	}
}

// A non-transient error must fail immediately: retrying a constraint violation or syntax error is
// pure waste and hides the real cause.
func TestMaterializeTestsSourceEdges_doesNotRetryNonTransient(t *testing.T) {
	sleeps := captureSleeps(t)
	s := &Store{db: newBadPool(t, 0)} // first Begin already returns the non-transient sentinel

	if _, err := s.MaterializeTestsSourceEdges(context.Background(), "retry/repo"); err == nil {
		t.Fatal("expected an error")
	}
	if len(*sleeps) != 0 {
		t.Fatalf("slept %v; a non-transient error must not be retried", *sleeps)
	}
}

func TestMaterializeTestsSourceEdges_respectsCancelledContext(t *testing.T) {
	captureSleeps(t)
	s := &Store{db: newBadPool(t, 99)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.MaterializeTestsSourceEdges(ctx, "retry/repo"); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestMaterializeTestsSourceBackoff(t *testing.T) {
	if got := materializeTestsSourceBackoff(1); got != 100*time.Millisecond {
		t.Errorf("first backoff = %v, want 100ms", got)
	}
	for _, attempt := range []int{2, 3, 9} {
		if got := materializeTestsSourceBackoff(attempt); got != 500*time.Millisecond {
			t.Errorf("backoff(%d) = %v, want 500ms", attempt, got)
		}
	}
}
