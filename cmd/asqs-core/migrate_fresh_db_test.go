package main

import (
	"os"
	"strings"
	"testing"
)

// `asqs-core migrate` must work on a database that has never been indexed into.
//
// Every migration assumes its tables exist, so on a brand-new database the first one failed with
// `relation "symbols" does not exist` — and "run migrate" is the documented FIRST step of an
// install, which made the documented order impossible to follow. Found while setting up a
// measurement run, not by a test, which is why there is one now.
//
// The two stores are also not independent: metadata's repo-scoping migration reads `chunks`, which
// the embeddings schema owns. When both share one database — the default, since an empty
// embeddings_url falls back to the metadata URL — initialising each target just before its own
// migrations still fails. The schemas must ALL exist before ANY migration runs, which is why this
// is a separate pass rather than a line inside the loop.
func TestMigrateInitialisesEverySchemaBeforeAnyMigration(t *testing.T) {
	b, err := os.ReadFile("migrate_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	initPass := strings.Index(src, "initSchemaForTarget(ctx, tgt.name")
	migrateRun := strings.Index(src, "migrate.Run(ctx, pool")
	if initPass < 0 {
		t.Fatal("migrate never creates the schema; it fails on a fresh database, which is the " +
			"documented first step of an install")
	}
	if migrateRun < 0 {
		t.Fatal("migrate.Run is gone")
	}
	if initPass > migrateRun {
		t.Error("schemas are created after migrations run; a fresh database still fails")
	}
	// It must be its own pass, ahead of the loop that RUNS the migrations — found by walking back
	// from migrate.Run, since the pre-pass has a loop of its own and a naive first-match would point
	// at that instead.
	migrateLoopStart := strings.LastIndex(src[:migrateRun], "for _, tgt := range targets {")
	if migrateLoopStart >= 0 && initPass > migrateLoopStart {
		t.Error("schema creation happens inside the per-target migration loop; when metadata and " +
			"embeddings share one database, metadata's migrations then run before the embeddings " +
			"schema exists and fail on `chunks`")
	}
	// The embeddings width comes from config: creating it at the wrong dimension would build a
	// corpus the configured model is then refused against.
	if !strings.Contains(src, "cfg.Database.EmbeddingsDimension") {
		t.Error("the embeddings schema is created without the configured dimension; the vector " +
			"column would be built at the wrong width")
	}
}
