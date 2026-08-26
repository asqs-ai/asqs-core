package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// cliConfigName is the configs row under which CLI runs auto-record their configuration.
// API-triggered deployments name configs themselves; the CLI has exactly one config source — the
// YAML file — so one well-known row keeps every CLI revision in a single version history.
const cliConfigName = "cli"

// ensureConfigRevisionForRun records the run's configuration as a config revision and returns its
// id, so index_runs.config_revision_id joins the run to the exact settings that produced it.
//
// Upstream fills config_revision_id only on API-triggered runs; CLI runs left it NULL, which makes
// them invisible to the A/B report — the report's whole premise is grouping runs by revision. The
// CLI edition therefore version-controls its own config file: an unchanged file reuses the latest
// revision (re-running a configuration N times is the normal A/B shape and must not mint N
// revisions), and a changed file appends a new one.
//
// Returns "" without error when the config came from environment only (no file): there is no body
// to version, and inventing one from the resolved struct would leak resolved secrets into the
// database. The body stored is the operator's file verbatim, same as API-posted revisions.
func ensureConfigRevisionForRun(ctx context.Context, meta *metadata.Store, sourcePath string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if meta == nil || sourcePath == "" {
		return "", nil
	}
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read config for revision: %w", err)
	}

	configs, err := meta.ListConfigs(ctx)
	if err != nil {
		return "", fmt.Errorf("list configs: %w", err)
	}
	var cliID string
	for _, c := range configs {
		if c.Name == cliConfigName {
			cliID = c.ID
			break
		}
	}
	if cliID == "" {
		_, revID, _, err := meta.CreateConfigWithInitialRevision(ctx, cliConfigName,
			"asqs-core run configurations (auto-recorded per run)", string(body), "asqs-core")
		if err != nil {
			return "", fmt.Errorf("create cli config: %w", err)
		}
		return revID, nil
	}
	latest, err := meta.GetLatestConfigRevision(ctx, cliID)
	if err != nil {
		return "", fmt.Errorf("latest cli revision: %w", err)
	}
	if latest != nil && latest.YAMLBody == string(body) {
		return latest.ID, nil
	}
	revID, _, err := meta.AppendConfigRevision(ctx, cliID, string(body), "asqs-core")
	if err != nil {
		return "", fmt.Errorf("append cli revision: %w", err)
	}
	return revID, nil
}
