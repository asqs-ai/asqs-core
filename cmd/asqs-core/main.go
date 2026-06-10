// Command asqs-core is the open-source CLI: a single `run` command that generates unit/E2E tests
// (and, optionally, ships them) for a local folder or a remote git repository.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/pipeline"
	"github.com/asqs/asqs-core/internal/repo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: %v\n", err)
		os.Exit(2)
	}
}

func run() error {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintf(os.Stderr, "usage: asqs-core run [flags] [<repo-path-or-git-url>]\n")
		return fmt.Errorf("the only supported command is `run`")
	}
	fs := flag.NewFlagSet("asqs-core run", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config YAML (database, llm, indexer, runner, vcs)")
	repoFlag := fs.String("repo", "", "repo path or git URL (may also be passed as a trailing argument)")
	lang := fs.String("lang", "", "language override: java|csharp|typescript|javascript (default: autodetect)")
	maxGaps := fs.Int("max-gaps", 10, "max unit gaps to generate")
	maxGapsE2E := fs.Int("max-gaps-e2e", 0, "max E2E gaps to generate (0 = skip E2E)")
	docs := fs.Bool("docs", false, "also generate per-symbol documentation (inserted above declarations)")
	sandbox := fs.String("sandbox", "", "sandbox type override: local|docker")
	ship := fs.Bool("ship", false, "after a stable run, commit+push a branch and open/update a PR/MR")
	shipBranch := fs.String("ship-branch", "", "branch to push when shipping (default: config or 'asqs-core')")
	baseBranch := fs.String("base-branch", "", "PR base branch (default: config or 'main')")
	dryRun := fs.Bool("dry-run", false, "generate + evaluate but never ship")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: asqs-core run [flags] [<repo-path-or-git-url>]\n\n")
		fs.PrintDefaults()
	}
	// Go's flag package stops parsing at the first non-flag token, so flags placed AFTER a trailing
	// repo argument (e.g. `run ./project --docs`) would otherwise be silently dropped. Re-parse,
	// pulling positionals out one at a time, so flags and the repo arg may appear in any order.
	var positionals []string
	rest := os.Args[2:]
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	repoArg := strings.TrimSpace(*repoFlag)
	if repoArg == "" && len(positionals) > 0 {
		repoArg = positionals[0]
	}
	if repoArg == "" {
		fs.Usage()
		return fmt.Errorf("missing repo: pass --repo <path-or-url> or as a trailing argument")
	}

	cfg, err := config.Load(config.LoadOptions{ConfigPath: *configPath, ValidateMode: "audit"})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if s := strings.TrimSpace(*sandbox); s != "" {
		cfg.Runner.Type = s
	}

	// --- Ship settings (resolved up front) ----------------------------------------------
	// Ship targets the REPO WE RUN AGAINST: the ship branch is prepared on that repo *before*
	// generation (asqs-go behaviour), so generated tests/docs are written and committed from it — and
	// an existing remote ship branch is reused so re-runs update its open PR instead of diverging.
	shipCfg := cfg.ActiveShip()
	shipEnabled := *ship && !*dryRun
	shipBranchName := firstNonEmpty(*shipBranch, shipCfg.Branch, "asqs-core")
	shipBaseName := firstNonEmpty(*baseBranch, shipCfg.BaseBranch, "main")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- Resolve repo (local folder or remote git URL) ---------------------------------
	var (
		repoDir   string
		repoID    string
		originURL string
		gitRepo   *repo.Repo
	)
	// cleanup removes the cloned temp folder. Set ONLY on the clone-URL path — never for a local
	// repo (we must not delete the user's folder). Called explicitly after ship below, with this
	// defer as a safety net for early-return/error paths.
	var cleanup func()
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	if isCloneURL(repoArg) {
		tmp, err := os.MkdirTemp("", "asqs-core-*")
		if err != nil {
			return fmt.Errorf("temp dir: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		token := cfg.CloneAuthTokenForURL(repoArg)
		var auth interface{}
		if token != "" {
			auth = &repo.TokenAuth{Token: token}
		}
		fmt.Fprintf(os.Stderr, "asqs-core: cloning %s …\n", repoArg)
		cloneOpts := repo.CloneOptions{URL: repoArg, Dir: tmp, Auth: auth}
		if shipEnabled {
			// Full clone of the base branch with all refs so an existing ship branch can be reused
			// (and a new one created) and the push is not a rejected shallow update.
			cloneOpts.Branch = shipBaseName
			cloneOpts.Depth = 0
			cloneOpts.FetchAllRefs = true
		}
		r, err := repo.Clone(ctx, cloneOpts)
		if err != nil {
			return fmt.Errorf("clone: %w", err)
		}
		gitRepo, repoDir, repoID, originURL = r, tmp, repoIDFromURL(repoArg), repoArg
	} else {
		abs, err := filepath.Abs(repoArg)
		if err != nil {
			return err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fmt.Errorf("%s is not a directory", abs)
		}
		repoDir, repoID = abs, repoIDFromPath(abs)
		if r, err := repo.Open(abs); err == nil {
			gitRepo = r
			originURL = r.RemoteURL("origin")
		}
	}

	// --- Prepare the ship branch on the TARGET repo BEFORE generation -------------------
	// So generated tests/docs land directly on the ship branch (and an existing remote ship branch
	// is reused, updating its open PR). Best-effort: a problem here just leaves the run on its
	// current branch — generation still proceeds, and the later push will report any failure.
	if shipEnabled {
		switch {
		case gitRepo == nil:
			fmt.Fprintln(os.Stderr, "asqs-core: --ship set but target is not a git repository — generating without shipping.")
		case cfg.CloneAuthTokenForURL(originURL) == "":
			fmt.Fprintf(os.Stderr, "asqs-core: --ship set but no VCS token configured for %q — generating without shipping.\n", originURL)
		default:
			prepareShipBranch(ctx, gitRepo, shipBranchName, shipBaseName, cfg.CloneAuthTokenForURL(originURL))
		}
	}

	// --- Run the pipeline ---------------------------------------------------------------
	sum, err := pipeline.Run(ctx, cfg, pipeline.Options{
		RepoPath:     repoDir,
		RepoID:       repoID,
		Lang:         *lang,
		MaxGaps:      *maxGaps,
		MaxGapsE2E:   *maxGapsE2E,
		GenerateDocs: *docs,
		Sandbox:      cfg.Runner.Type,
	})
	if err != nil {
		return err
	}
	printSummary(sum)

	// --- Ship (opt-in, gated on a stable run) ------------------------------------------
	if shipEnabled {
		if err := shipRun(ctx, cfg, gitRepo, originURL, sum, shipBranchName, shipBaseName); err != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: ship: %v\n", err)
		}
	}

	// Remove the cloned temp folder now that generation + ship are done. Done explicitly (not just
	// via the defer) so it also runs on the os.Exit path below, which would skip deferred cleanup.
	if cleanup != nil {
		cleanup()
		cleanup = nil
		fmt.Fprintln(os.Stderr, "asqs-core: removed temporary clone.")
	}

	if sum.GapsGenerated > 0 && !sum.Stable() {
		os.Exit(1) // the whole-project evaluation did not end green
	}
	return nil
}

func shipRun(ctx context.Context, cfg *config.Config, gitRepo *repo.Repo, originURL string, sum pipeline.Summary, branch, base string) error {
	if !sum.Stable() {
		fmt.Fprintln(os.Stderr, "asqs-core: run not stable — not shipping.")
		return nil
	}
	if gitRepo == nil {
		return fmt.Errorf("target is not a git repository (cannot ship)")
	}
	token := cfg.CloneAuthTokenForURL(originURL)
	if token == "" {
		return fmt.Errorf("no VCS token configured for %s", originURL)
	}

	// prepareShipBranch already checked out the ship branch before generation (while the worktree was
	// clean). Generation then left unstaged changes, so re-checking-out now would fail with
	// "worktree contains unstaged changes" — switch only when we are NOT already on the branch
	// (the prep-skipped/failed fallback).
	if cur, _ := gitRepo.CurrentBranch(); cur != branch {
		if err := gitRepo.CreateAndCheckoutBranch(branch); err != nil {
			if err2 := gitRepo.CheckoutBranch(branch); err2 != nil {
				return fmt.Errorf("checkout %s: %w", branch, err)
			}
		}
	}
	if err := gitRepo.Add("."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := gitRepo.Commit("asqs-core: generated tests and docs", nil); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// Resolve owner/repo: config defaults first, then parse from the origin URL — so a plain
	// `git clone`d repo ships without needing vcs.<provider>.default_owner/default_repo set.
	owner, repoName := cfg.ActiveDefaultOwnerRepo()
	if owner == "" || repoName == "" {
		if o, rp, perr := cfg.ParseRepoFromCloneURL(originURL); perr == nil {
			if owner == "" {
				owner = o
			}
			if repoName == "" {
				repoName = rp
			}
		}
	}

	// Push to a canonical HTTPS URL (rewriting an SSH origin) so the token authenticates even when
	// origin is git@host:owner/repo. Empty pushURL => push to the configured origin remote as-is.
	pushURL := toHTTPSRemoteURL(originURL)
	if err := gitRepo.Push(ctx, repo.PushOptions{RemoteName: "origin", Branch: branch, RemoteURL: pushURL, Auth: &repo.TokenAuth{Token: token}}); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	fmt.Fprintf(os.Stderr, "asqs-core: pushed branch %q.\n", branch)

	if owner == "" || repoName == "" {
		fmt.Fprintf(os.Stderr, "asqs-core: branch pushed, but could not determine owner/repo for a PR "+
			"(set vcs.%s.default_owner/default_repo, or use a repo whose origin is a recognized URL).\n",
			firstNonEmpty(cfg.VCS.Provider, "github"))
		return nil
	}
	prURL, ok, err := cfg.ShipEnsureOpenPullRequest(ctx, originURL, owner, repoName, branch, base)
	if err != nil {
		return fmt.Errorf("open PR for %s/%s: %w", owner, repoName, err)
	}
	if ok && prURL != "" {
		fmt.Fprintf(os.Stderr, "asqs-core: shipped → %s\n", prURL)
	} else {
		fmt.Fprintf(os.Stderr, "asqs-core: pushed branch %s (no PR URL returned)\n", branch)
	}
	return nil
}

// prepareShipBranch checks out the ship branch on the TARGET repo *before* generation (asqs-go
// behaviour): fetch origin, reuse an existing remote ship branch — so a re-run updates its open PR
// instead of diverging — or create the branch from the base. Best-effort: failures are logged and
// the run continues on the current branch (the later push then surfaces any real problem).
func prepareShipBranch(ctx context.Context, gitRepo *repo.Repo, branch, base, token string) {
	auth := &repo.TokenAuth{Token: token}
	if err := gitRepo.Fetch(ctx, repo.FetchOptions{RemoteName: "origin", Auth: auth}); err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: ship fetch: %v (continuing)\n", err)
	}
	exists, err := gitRepo.HasRemoteBranch("origin", branch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: ship has-remote-branch: %v (continuing)\n", err)
	}
	if err == nil && exists {
		if cerr := gitRepo.CheckoutBranchFromRemote("origin", branch); cerr != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: ship checkout remote %q: %v (continuing on current branch)\n", branch, cerr)
			return
		}
		// Best-effort: bring the reused branch up to date with base so its PR is not behind.
		if merr := gitRepo.MergeBranch(ctx, "origin/"+base); merr != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: ship merge origin/%s: %v (continuing)\n", base, merr)
		}
		fmt.Fprintf(os.Stderr, "asqs-core: checked out existing ship branch %q (its open PR will be updated).\n", branch)
		return
	}
	if cerr := gitRepo.CreateAndCheckoutBranch(branch); cerr != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: ship create branch %q: %v (continuing on current branch)\n", branch, cerr)
		return
	}
	fmt.Fprintf(os.Stderr, "asqs-core: created ship branch %q from %s.\n", branch, base)
}

// toHTTPSRemoteURL converts an SSH git remote to its HTTPS form so a token can authenticate the push:
// git@host:owner/repo(.git) and ssh://git@host[:port]/owner/repo become https://host/owner/repo.
// HTTPS/HTTP URLs are returned unchanged; "" stays "" (the caller then pushes to the origin remote).
func toHTTPSRemoteURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
		return s
	}
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		if host, path, found := strings.Cut(rest, ":"); found {
			return "https://" + host + "/" + strings.TrimPrefix(path, "/")
		}
		return s
	}
	if rest, ok := strings.CutPrefix(s, "ssh://"); ok {
		rest = strings.TrimPrefix(rest, "git@")
		if host, path, found := strings.Cut(rest, "/"); found {
			if h, _, hasPort := strings.Cut(host, ":"); hasPort {
				host = h
			}
			return "https://" + host + "/" + path
		}
		return s
	}
	return s
}

func printSummary(s pipeline.Summary) {
	stable := "no"
	if s.ProjectStable {
		stable = "yes"
		if s.Discarded > 0 {
			stable = "yes (after discard)"
		}
	}
	fmt.Printf("\nasqs-core summary (%s): %d files indexed, %d gaps planned, %d generated, %d stable, %d discarded, %d docs | project: %s (%d fix iters)\n",
		s.Lang, s.FilesIndexed, s.GapsPlanned, s.GapsGenerated, s.GapsStable, s.Discarded, s.DocsWritten, stable, s.Iterations)
	for _, o := range s.Outcomes {
		status := "skipped"
		switch {
		case o.Discarded:
			status = "discarded"
		case o.Stable:
			status = "stable"
		case o.Generated:
			status = "unstable"
		}
		line := fmt.Sprintf("  - [%s] %s", status, o.Symbol)
		if o.Path != "" {
			line += " → " + o.Path
		}
		if o.Err != "" {
			line += "  (" + o.Err + ")"
		}
		fmt.Println(line)
	}
}

func isCloneURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://") || strings.HasSuffix(s, ".git")
}

func repoIDFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), ".git")
	u = strings.TrimSuffix(u, "/")
	parts := strings.Split(u, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return u
}

func repoIDFromPath(p string) string { return filepath.Base(filepath.Clean(p)) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
