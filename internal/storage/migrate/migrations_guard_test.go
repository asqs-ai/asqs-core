package migrate

import (
	"os"
	"strings"
	"testing"
)

// migrationSource reads the migration definitions as text. These guards are about SQL that only
// fails against a real server, which no unit test in this package can reach — so they assert on the
// source instead. Both invariants below were violated in shipped code and both cost a full
// round-trip against a live corpus to discover.
// Comments are stripped: these guards explain the wrong SQL in prose, and a guard that matched its
// own rationale would fire on every correct version of the file.
func migrationSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("migrations.go")
	if err != nil {
		t.Fatalf("read migrations.go: %v", err)
	}
	var code []string
	for _, ln := range strings.Split(string(b), "\n") {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		code = append(code, ln)
	}
	return strings.Join(code, "\n")
}

// pgvector defines l2_norm for halfvec and sparsevec but NOT for vector. Calling it on a vector
// column does not fail with "function does not exist" — it fails with "function l2_norm(vector) is
// not unique" (SQLSTATE 42725), because the planner finds two candidates and can implicitly cast to
// neither. The error names the function, which makes it read like a missing extension rather than a
// wrong type, and it aborts the whole migration run.
//
// The norm of a vector is sqrt(inner_product(v, v)); scaling is l2_normalize(v).
func TestMigrations_doNotCallL2NormOnVector(t *testing.T) {
	if strings.Contains(migrationSource(t), "l2_norm(") {
		t.Error("a migration calls l2_norm(), which pgvector does not define for the vector type; " +
			"use sqrt(inner_product(v, v)) for the norm and l2_normalize(v) to scale")
	}
}

// `asqs-core migrate` connects with a raw pgxpool and never runs schema.sql — that is deliberate,
// since InitSchema also aligns the embedding column and can truncate. The consequence is that a
// migration may not assume any DDL from schema.sql has been applied: on a corpus indexed before a
// column existed, and not restarted through the pipeline since, it has not been.
//
// This bit migration 0003, which indexed chunks.content_tsv while schema.sql was the only thing
// that created it.
func TestMigrations_indexingContentTSVAlsoCreatesIt(t *testing.T) {
	src := migrationSource(t)
	i := strings.Index(src, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chunks_content_tsv")
	if i < 0 {
		t.Skip("content_tsv index migration not present")
	}
	add := strings.Index(src, "ADD COLUMN IF NOT EXISTS content_tsv")
	if add < 0 || add > i {
		t.Error("the content_tsv index is created without the migration first adding the column; " +
			"migrate never runs schema.sql, so the column may not exist")
	}
}

// IDs are the primary key of schema_migrations. Reusing one across the two migration sets would
// make the second silently skip when both point at the same database — which is the default
// single-database deployment.
func TestMigrations_idsAreUniqueAcrossSets(t *testing.T) {
	seen := map[string]string{}
	for _, set := range []struct {
		name string
		ms   []Migration
	}{{"metadata", MetadataMigrations()}, {"embeddings", EmbeddingsMigrations()}} {
		for _, m := range set.ms {
			if prev, dup := seen[m.ID]; dup {
				t.Errorf("migration id %q used by both %s and %s; schema_migrations is keyed by id "+
					"and both sets share a database by default", m.ID, prev, set.name)
			}
			seen[m.ID] = set.name
		}
	}
}
