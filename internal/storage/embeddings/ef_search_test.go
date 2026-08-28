package embeddings

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestEFSearchFor(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{0, defaultEFSearch},  // never below pgvector's default
		{1, defaultEFSearch},  // 4 would be worse than the default
		{5, defaultEFSearch},  // 20 -> floored to 40
		{10, defaultEFSearch}, // 40
		{20, 80},              // 4x
		{100, 400},            // 4x, at the ceiling
		{1000, maxEFSearch},   // clamped
		{-5, defaultEFSearch}, // defensive
	}
	for _, c := range cases {
		if got := efSearchFor(c.limit); got != c.want {
			t.Errorf("efSearchFor(%d) = %d, want %d", c.limit, got, c.want)
		}
	}
}

// fakeConn records the statements executed so the test can assert SET hnsw.ef_search was issued on
// the same connection that runs the query — SET LOCAL/SET is per-session, so using a pooled
// connection for the SET and a different one for the query would silently lose the setting.
type fakeConn struct {
	execs    []string
	execErr  error
	queryErr error
}

func (f *fakeConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	return pgconn.CommandTag{}, f.execErr
}

func (f *fakeConn) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, f.queryErr
}

func TestSetEFSearch_issuesSetOnTheConnection(t *testing.T) {
	c := &fakeConn{}
	if err := setEFSearch(context.Background(), c, 160); err != nil {
		t.Fatal(err)
	}
	if len(c.execs) != 1 {
		t.Fatalf("got %d statements, want 1: %v", len(c.execs), c.execs)
	}
	if c.execs[0] != "SET hnsw.ef_search = 160" {
		t.Errorf("statement = %q", c.execs[0])
	}
}

// An older pgvector (or a fixture Postgres without the extension) does not know the GUC. Worse
// recall is acceptable there; failing every search is not.
func TestSetEFSearch_toleratesUnknownGUC(t *testing.T) {
	c := &fakeConn{execErr: errors.New(`unrecognized configuration parameter "hnsw.ef_search"`)}
	if err := setEFSearch(context.Background(), c, 80); err != nil {
		t.Fatalf("setEFSearch should degrade rather than fail: %v", err)
	}
}

func TestSetEFSearch_nilConnIsSafe(t *testing.T) {
	if err := setEFSearch(context.Background(), nil, 40); err != nil {
		t.Fatal(err)
	}
}

// The formatted value must always be one efSearchFor produced, so no caller-controlled string can
// reach the SET statement.
func TestSetEFSearch_valueIsAlwaysClamped(t *testing.T) {
	for _, limit := range []int{-1, 0, 7, 999999} {
		ef := efSearchFor(limit)
		if ef < defaultEFSearch || ef > maxEFSearch {
			t.Fatalf("efSearchFor(%d) = %d escapes [%d,%d]", limit, ef, defaultEFSearch, maxEFSearch)
		}
	}
}

func TestANNWidenHook(t *testing.T) {
	s := &Store{}
	var got ANNWidenEvent
	var calls int
	s.SetANNWidenHook(func(_ context.Context, e ANNWidenEvent) {
		calls++
		got = e
	})
	before := ANNWidenCount()
	s.noteANNWidened(context.Background(), 10, 2, 9)

	if calls != 1 {
		t.Fatalf("hook called %d times, want 1", calls)
	}
	if got.Limit != 10 || got.Got != 2 || got.WidenedTo != 9 || got.EFSearch != maxEFSearch {
		t.Errorf("event = %+v", got)
	}
	if ANNWidenCount() != before+1 {
		t.Error("widen counter did not advance")
	}
}

func TestANNWidenHook_nilIsSafe(t *testing.T) {
	s := &Store{}
	s.noteANNWidened(context.Background(), 5, 1, 4) // must not panic with no hook installed
}
