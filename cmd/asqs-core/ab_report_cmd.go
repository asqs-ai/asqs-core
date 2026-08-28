package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
)

// minRunsForSignal is the run count below which a comparison is reported as insufficient.
//
// It is deliberately visible in the output rather than a silent threshold: an average over 3 runs
// displayed identically to one over 30 actively misleads, and a UI or a person reading a single
// number is exactly how an unmeasured change gets called an improvement.
const minRunsForSignal = 10

// runABReport prints outcome metrics grouped by config revision.
//
// This turns the measurement loop from "a SQL query someone has to remember" into a command. The
// underlying data — index_runs.first_wave_metrics joined to index_runs.config_revision_id — has
// existed all along and nobody was running the query.
func runABReport(args []string) error {
	fs := flag.NewFlagSet("asqs-core ab-report", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to config YAML (or ASQS_CONFIG_PATH)")
	repoID := fs.String("repo", "", "restrict to one repo id (empty = all repos)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(config.LoadOptions{
		ConfigPath:   strings.TrimSpace(*cfgPath),
		ValidateMode: "audit", // needs the database URL, not VCS credentials
	})
	if err != nil {
		return err
	}
	meta, err := cfg.OpenMetadataStore()
	if err != nil {
		return fmt.Errorf("metadata store: %w", err)
	}
	defer func() { _ = meta.Close() }()

	rep, err := meta.ABReportForRepo(context.Background(), strings.TrimSpace(*repoID))
	if err != nil {
		return err
	}
	if len(rep.Rows) == 0 {
		fmt.Fprintln(os.Stdout, "No completed runs with first-wave metrics and a config revision.")
		fmt.Fprintln(os.Stdout, "Run the pipeline at least twice per configuration before comparing.")
		return nil
	}

	fmt.Fprintf(os.Stdout, "%-38s %7s %5s %10s %10s %12s %10s\n",
		"config_revision", "version", "runs", "pass@1", "compile", "tok→stable", "iters")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 100))
	weak := 0
	for _, r := range rep.Rows {
		note := ""
		if r.Runs < minRunsForSignal {
			note = "  ← too few runs"
			weak++
		}
		fmt.Fprintf(os.Stdout, "%-38s %7d %5d %9.1f%% %9.1f%% %12.0f %10.2f%s\n",
			r.ConfigRevisionID, r.Version, r.Runs,
			r.PassWithoutFixRate*100, r.FirstCompileRate*100,
			r.AvgTokensToStable, r.AvgIterations, note)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Decision criterion: compare pass@1 AND tok→stable together. A change that raises\n"+
		"pass@1 while substantially increasing tokens-to-stable is not obviously an improvement.\n")
	if weak > 0 {
		fmt.Fprintf(os.Stdout, "\n%d revision(s) have fewer than %d runs. Those rows are not evidence — gather more\n"+
			"runs of the same repo under the same configuration before drawing a conclusion.\n", weak, minRunsForSignal)
	}
	fmt.Fprintf(os.Stdout, "\nNote: this comparison assumes deterministic gap selection, otherwise the revisions are\n"+
		"scored against different gap sets.\n")
	return nil
}
