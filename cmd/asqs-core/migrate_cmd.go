package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/storage/metadata"
	"github.com/asqs/asqs-core/internal/storage/migrate"
)

// runMigrate applies one-shot schema and data migrations that cannot live in schema.sql — data
// backfills (which would re-run on every process start) and CREATE INDEX CONCURRENTLY (which cannot
// run inside a transaction). See internal/storage/migrate for the full rationale.
//
// Safe to run repeatedly: applied migrations are recorded in schema_migrations and skipped.
func runMigrate(args []string) error {
	fs := flag.NewFlagSet("asqs-core migrate", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to config YAML (or ASQS_CONFIG_PATH)")
	dryRun := fs.Bool("dry-run", false, "list pending migrations without applying them")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(config.LoadOptions{
		ConfigPath:   strings.TrimSpace(*cfgPath),
		ValidateMode: "audit", // migrate needs the database URLs, not VCS credentials
	})
	if err != nil {
		return err
	}

	ctx := context.Background()
	metaURL := strings.TrimSpace(cfg.Database.MetadataURL)
	embURL := strings.TrimSpace(cfg.Database.EmbeddingsURL)
	if embURL == "" {
		embURL = metaURL
	}

	targets := []struct {
		name       string
		connString string
		migrations []migrate.Migration
	}{
		{"metadata", metaURL, migrate.MetadataMigrations()},
		{"embeddings", embURL, migrate.EmbeddingsMigrations()},
	}

	for _, tgt := range targets {
		if tgt.connString == "" {
			fmt.Fprintf(os.Stderr, "%s: no connection string configured; skipping\n", tgt.name)
			continue
		}
		pool, err := pgxpool.New(ctx, tgt.connString)
		if err != nil {
			return fmt.Errorf("%s: connect: %w", tgt.name, err)
		}
		if *dryRun {
			pending, err := migrate.Pending(ctx, pool, tgt.migrations)
			pool.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", tgt.name, err)
			}
			if len(pending) == 0 {
				fmt.Fprintf(os.Stderr, "%s: up to date\n", tgt.name)
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: %d pending migration(s):\n", tgt.name, len(pending))
			for _, id := range pending {
				fmt.Fprintf(os.Stderr, "  %s\n", id)
			}
			continue
		}
		fmt.Fprintf(os.Stderr, "%s:\n", tgt.name)
		res, err := migrate.Run(ctx, pool, tgt.migrations, os.Stderr)
		pool.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", tgt.name, err)
		}
		fmt.Fprintf(os.Stderr, "%s: %d applied, %d already up to date\n", tgt.name, len(res.Applied), len(res.Skipped))
	}

	// The migration backfills what it can prove. Anything it could not — a path claimed by more
	// than one repository, or a database whose index_runs name several repositories and whose
	// symbols predate repo_id — stays at repo_id = '' and is invisible to every scoped read. Say so
	// here, where the operator is already looking.
	if metaURL != "" && !*dryRun {
		store, err := metadata.Open(metaURL)
		if err == nil {
			defer func() { _ = store.Close() }()
			if counts, cerr := store.CountUnscopedRows(ctx); cerr == nil {
				if w := metadata.ReindexRequiredWarning(counts); w != "" {
					fmt.Fprintf(os.Stderr, "\n%s\n", w)
				}
			}
			if repos, rerr := store.ReposMissingFileRows(ctx); rerr == nil {
				if w := metadata.MissingFileRowsWarning(repos); w != "" {
					fmt.Fprintf(os.Stderr, "\n%s\n", w)
				}
			}
		}
	}
	return nil
}
