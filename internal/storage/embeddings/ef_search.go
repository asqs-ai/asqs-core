package embeddings

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ef_search bounds. pgvector's default is 40, which is too low once several selective equality
// predicates post-filter the candidate set. 4x the requested limit is the standard operational
// guidance; the floor keeps small-limit queries from degrading below the default, and the ceiling
// bounds worst-case traversal cost (ef_search 40 -> 80 roughly doubles HNSW traversal, which is
// single-digit milliseconds at this corpus size — strongly worth it for recall).
const (
	defaultEFSearch = 40
	maxEFSearch     = 400
	efSearchPerItem = 4
)

// pooledConn is the subset of *pgxpool.Conn the search path uses, so it can be faked in tests.
type pooledConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// efSearchFor returns the hnsw.ef_search value for a query returning limit rows.
func efSearchFor(limit int) int {
	ef := limit * efSearchPerItem
	if ef < defaultEFSearch {
		ef = defaultEFSearch
	}
	if ef > maxEFSearch {
		ef = maxEFSearch
	}
	return ef
}

// setEFSearch applies hnsw.ef_search to the pinned connection for the current transaction/session.
//
// The value is formatted into the statement rather than parameterized because SET does not accept
// bind parameters. efSearchFor clamps it to [40, 400], so it is never attacker-influenced.
func setEFSearch(ctx context.Context, conn pooledConn, ef int) error {
	if conn == nil {
		return nil
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET hnsw.ef_search = %d", ef)); err != nil {
		// An older pgvector (or a non-pgvector Postgres in a unit-test fixture) may not know the
		// GUC. Recall is worse without it, but failing the search would be worse still.
		return nil
	}
	return nil
}

// annWidenCount counts filtered-ANN widen retries. Exposed for tests and for operators via
// ANNWidenCount; the audit-log surfacing lives in the retrieval layer, which owns the audit sink.
var annWidenCount atomic.Int64

// noteANNWidened records that a search under-returned at the computed ef_search and was retried at
// the ceiling. A non-zero count means the filtered-recall problem is real on this corpus.
func (s *Store) noteANNWidened(ctx context.Context, limit, got, widened int) {
	annWidenCount.Add(1)
	if s.onANNWiden != nil {
		s.onANNWiden(ctx, ANNWidenEvent{Limit: limit, Got: got, WidenedTo: widened, EFSearch: maxEFSearch})
	}
}

// ANNWidenEvent describes one filtered-recall widen retry.
type ANNWidenEvent struct {
	Limit     int // rows the caller asked for
	Got       int // rows returned at the computed ef_search
	WidenedTo int // rows returned at the ceiling
	EFSearch  int
}

// ANNWidenCount returns the process-wide number of widen retries since start.
func ANNWidenCount() int64 { return annWidenCount.Load() }

// SetANNWidenHook installs a callback invoked on every widen retry. The retrieval layer wires this
// to `retrieve.ann_widened` in the audit log.
func (s *Store) SetANNWidenHook(fn func(context.Context, ANNWidenEvent)) { s.onANNWiden = fn }
