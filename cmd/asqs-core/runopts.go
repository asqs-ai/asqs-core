package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// Built-in gap caps, used only when neither the command line nor the config file supplies one.
const (
	defaultMaxGaps    = 10
	defaultMaxGapsE2E = 0
)

// Where an effective value came from (gap caps, audit log). Reported on stderr so a run is
// self-explaining when a flag and its config key disagree.
const (
	gapSourceFlag    = "flag"
	gapSourceConfig  = "config"
	gapSourceDefault = "default"
)

// runFlags is the parsed `asqs-core run` command line.
type runFlags struct {
	configPath string
	repo       string // --repo, or the first positional argument
	lang       string
	maxGaps    int
	maxGapsE2E int
	docs       bool
	sandbox    string
	ship       bool
	shipBranch string
	baseBranch string
	dryRun     bool
	auditLog   string

	// setFlags holds the names of the flags that were actually present on the command line, so
	// resolveMaxGaps / resolveMaxGapsE2E can tell "user asked for this value" apart from "the
	// flag was left at its default" and only then fall back to the config file.
	setFlags map[string]bool

	// usage prints the command's help text (bound to the underlying FlagSet).
	usage func()
}

// parseRunFlags parses the arguments after the `run` subcommand. Go's flag package stops at the
// first non-flag token, so flags placed AFTER a trailing repo argument (e.g. `run ./project --docs`)
// would otherwise be silently dropped. We re-parse, pulling positionals out one at a time, so flags
// and the repo argument may appear in any order. FlagSet.Visit accumulates across those rounds, so
// setFlags sees flags from every pass.
func parseRunFlags(args []string, out io.Writer) (*runFlags, error) {
	fs := flag.NewFlagSet("asqs-core run", flag.ContinueOnError)
	if out != nil {
		fs.SetOutput(out)
	}
	configPath := fs.String("config", "", "path to config YAML (database, llm, indexer, runner, vcs)")
	repoFlag := fs.String("repo", "", "repo path or git URL (may also be passed as a trailing argument)")
	lang := fs.String("lang", "", "language override: java|csharp|typescript|javascript (default: autodetect)")
	maxGaps := fs.Int("max-gaps", defaultMaxGaps, "max unit gaps to generate (default: indexer.max_gaps, else 10)")
	maxGapsE2E := fs.Int("max-gaps-e2e", defaultMaxGapsE2E, "max E2E gaps to generate, 0 = skip E2E (default: indexer.max_gaps_e2e, else 0)")
	docs := fs.Bool("docs", false, "also generate per-symbol documentation (inserted above declarations)")
	sandbox := fs.String("sandbox", "", "sandbox type override: local|docker")
	ship := fs.Bool("ship", false, "after a stable run, commit+push a branch and open/update a PR/MR")
	shipBranch := fs.String("ship-branch", "", "branch to push when shipping (default: config or 'asqs-core')")
	baseBranch := fs.String("base-branch", "", "PR base branch (default: config or 'main')")
	dryRun := fs.Bool("dry-run", false, "generate + evaluate but never ship")
	auditLog := fs.String("audit-log", "", "append structured audit JSONL (step + payload per line) to this file (default: audit.file_path from config; empty = no audit file)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: asqs-core run [flags] [<repo-path-or-git-url>]\n\n")
		fs.PrintDefaults()
	}

	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	setFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	repoArg := strings.TrimSpace(*repoFlag)
	if repoArg == "" && len(positionals) > 0 {
		repoArg = positionals[0]
	}

	return &runFlags{
		configPath: strings.TrimSpace(*configPath),
		repo:       repoArg,
		lang:       *lang,
		maxGaps:    *maxGaps,
		maxGapsE2E: *maxGapsE2E,
		docs:       *docs,
		sandbox:    *sandbox,
		ship:       *ship,
		shipBranch: *shipBranch,
		baseBranch: *baseBranch,
		dryRun:     *dryRun,
		auditLog:   *auditLog,
		setFlags:   setFlags,
		usage:      fs.Usage,
	}, nil
}

// resolveAuditLogPath picks the audit JSONL path. Precedence: an explicitly passed --audit-log —
// including an explicit empty value, which disables a config-set path for one run — then
// audit.file_path from the config file (or ASQS_AUDIT_FILE_PATH, which config.Load has already
// folded into cfgVal), then empty = no audit file, today's behaviour exactly.
func resolveAuditLogPath(flagSet bool, flagVal, cfgVal string) (string, string) {
	if flagSet {
		return strings.TrimSpace(flagVal), gapSourceFlag
	}
	if v := strings.TrimSpace(cfgVal); v != "" {
		return v, gapSourceConfig
	}
	return "", gapSourceDefault
}

// resolveMaxGaps picks the effective unit-gap cap. Precedence: an explicitly passed --max-gaps,
// then indexer.max_gaps from the config file (or ASQS_INDEXER_MAX_GAPS, which config.Load has
// already folded into cfgVal), then the built-in default.
//
// A non-positive value is treated as "not configured" and falls through to the next source: a run
// capped at zero unit gaps generates nothing, and the planner clamps <= 0 to its own default anyway
// (see retrieval.CreateTestPlan), so honouring it would only produce a confusing no-op. A NEGATIVE
// value is instead rejected outright — it is always a mistake, and silently reinterpreting it is
// exactly the kind of surprise this resolution is meant to remove.
func resolveMaxGaps(flagSet bool, flagVal, cfgVal int) (int, string, error) {
	if flagSet && flagVal < 0 {
		return 0, "", fmt.Errorf("--max-gaps must be >= 0, got %d", flagVal)
	}
	if cfgVal < 0 {
		return 0, "", fmt.Errorf("indexer.max_gaps must be >= 0, got %d", cfgVal)
	}
	if flagSet && flagVal > 0 {
		return flagVal, gapSourceFlag, nil
	}
	if cfgVal > 0 {
		return cfgVal, gapSourceConfig, nil
	}
	return defaultMaxGaps, gapSourceDefault, nil
}

// resolveMaxGapsE2E picks the effective E2E-gap cap. Precedence: an explicitly passed
// --max-gaps-e2e, then indexer.max_gaps_e2e (or ASQS_INDEXER_MAX_GAPS_E2E), then 0.
//
// Unlike the unit cap, 0 is MEANINGFUL here: it means "skip the E2E plan branch entirely". So an
// explicit `--max-gaps-e2e 0` wins over a non-zero config value — that is how a user turns E2E off
// for a single run without editing the config file.
func resolveMaxGapsE2E(flagSet bool, flagVal, cfgVal int) (int, string, error) {
	if flagSet && flagVal < 0 {
		return 0, "", fmt.Errorf("--max-gaps-e2e must be >= 0, got %d", flagVal)
	}
	if cfgVal < 0 {
		return 0, "", fmt.Errorf("indexer.max_gaps_e2e must be >= 0, got %d", cfgVal)
	}
	if flagSet {
		return flagVal, gapSourceFlag, nil
	}
	if cfgVal > 0 {
		return cfgVal, gapSourceConfig, nil
	}
	return defaultMaxGapsE2E, gapSourceDefault, nil
}
