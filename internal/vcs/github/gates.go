package github

import (
	"context"
	"strings"
)

// GateResult is the result of running all gating rules.
type GateResult struct {
	Pass   bool     // true only if all gates pass
	Reason string   // human-readable reason when Pass is false (e.g. first failed gate)
	Failed []string // list of failed gate names for logging
}

// GateRunner runs gating rules against a PR context and returns whether to proceed.
type GateRunner interface {
	RunGates(ctx context.Context, pr *PRContext) GateResult
}

// GateOptions configures gating rules (thresholds, allowed branches, etc.).
type GateOptions struct {
	// AllowedBaseBranches: if non-empty, PR base must be in this list (e.g. ["main", "master"]).
	AllowedBaseBranches []string
	// RejectDraft: if true, draft PRs are rejected.
	RejectDraft bool
	// MaxRepoSizeKB: skip if repo size (from API) exceeds this; 0 = no limit.
	MaxRepoSizeKB int
	// MaxIndexFiles: skip if number of indexable files would exceed this (estimate); 0 = no limit.
	MaxIndexFiles int
	// SupportedLanguages: e.g. ["java", "csharp"]. If repo has no detected lang in this set, fail; empty = allow all.
	SupportedLanguages []string
	// RequireBuildToolchain: if true, gate fails when no build file found (pom.xml, build.gradle, .csproj).
	RequireBuildToolchain bool
	// MaxFailingTests: gate fails if existing failing tests in repo exceed this; 0 = skip this gate.
	MaxFailingTests int
}

// DefaultGateOptions returns sensible defaults: main/master allowed, reject draft, Java/C# supported.
func DefaultGateOptions() GateOptions {
	return GateOptions{
		AllowedBaseBranches:   []string{"main", "master"},
		RejectDraft:           true,
		MaxRepoSizeKB:         0,
		MaxIndexFiles:         0,
		SupportedLanguages:    []string{"java", "csharp"},
		RequireBuildToolchain: true,
		MaxFailingTests:       0,
	}
}

// Gates implements GateRunner using GitHub API and GateOptions.
type Gates struct {
	Client  *Client
	Options GateOptions
	// RepoInspector is optional: used to count indexable files and detect toolchain/language.
	RepoInspector RepoInspector
	// TestStatus is optional: used to count failing tests (e.g. run tests or read CI status).
	TestStatus TestStatusChecker
}

// RepoInspector inspects the repo (e.g. after clone) for file count and toolchain/language.
type RepoInspector interface {
	// CountIndexableFiles returns approximate number of source files to index (or 0 if unknown).
	CountIndexableFiles(ctx context.Context, cloneURL, headSHA string) (int, error)
	// DetectLanguage returns primary language (e.g. "java", "csharp") or "" if unknown.
	DetectLanguage(ctx context.Context, cloneURL, headSHA string) (string, error)
	// HasBuildToolchain returns true if repo has a recognized build file (pom.xml, build.gradle, .csproj, etc.).
	HasBuildToolchain(ctx context.Context, cloneURL, headSHA string) (bool, error)
}

// TestStatusChecker returns the number of failing tests (e.g. from last CI run or local run); 0 if unknown.
type TestStatusChecker interface {
	FailingTestCount(ctx context.Context, owner, repo, headSHA string) (int, error)
}

// RunGates runs all configured gates and returns a combined result.
func (g *Gates) RunGates(ctx context.Context, pr *PRContext) GateResult {
	var failed []string

	// Gate 1: PR is draft or target is not main/master
	if g.Options.RejectDraft && pr.Draft {
		failed = append(failed, "pr_draft")
		return GateResult{Pass: false, Reason: "PR is draft", Failed: failed}
	}
	if len(g.Options.AllowedBaseBranches) > 0 {
		base := strings.ToLower(pr.BaseRef)
		allowed := false
		for _, b := range g.Options.AllowedBaseBranches {
			if strings.ToLower(b) == base {
				allowed = true
				break
			}
		}
		if !allowed {
			failed = append(failed, "base_branch")
			return GateResult{Pass: false, Reason: "target branch is not main or master", Failed: failed}
		}
	}

	// Gate 2: repo too large (indexing would take too long)
	if g.Options.MaxRepoSizeKB > 0 && pr.RepoSizeKB > g.Options.MaxRepoSizeKB {
		failed = append(failed, "repo_too_large")
		return GateResult{Pass: false, Reason: "repo is too large for indexing", Failed: failed}
	}

	// Gate 3 & 4: language/framework and build toolchain (need repo inspection)
	if g.RepoInspector != nil {
		if g.Options.MaxIndexFiles > 0 {
			n, err := g.RepoInspector.CountIndexableFiles(ctx, pr.CloneURL, pr.HeadSHA)
			if err == nil && n > g.Options.MaxIndexFiles {
				failed = append(failed, "index_too_large")
				return GateResult{Pass: false, Reason: "too many files to index", Failed: failed}
			}
		}
		if len(g.Options.SupportedLanguages) > 0 {
			lang, err := g.RepoInspector.DetectLanguage(ctx, pr.CloneURL, pr.HeadSHA)
			if err == nil && lang != "" {
				ok := false
				for _, l := range g.Options.SupportedLanguages {
					if l == lang {
						ok = true
						break
					}
				}
				if !ok {
					failed = append(failed, "unsupported_language")
					return GateResult{Pass: false, Reason: "language/framework not supported", Failed: failed}
				}
			}
		}
		if g.Options.RequireBuildToolchain {
			has, err := g.RepoInspector.HasBuildToolchain(ctx, pr.CloneURL, pr.HeadSHA)
			if err == nil && !has {
				failed = append(failed, "missing_build_toolchain")
				return GateResult{Pass: false, Reason: "missing build toolchain in CI environment", Failed: failed}
			}
		}
	}

	// Gate 5: too many failing tests already
	if g.Options.MaxFailingTests > 0 && g.TestStatus != nil {
		count, err := g.TestStatus.FailingTestCount(ctx, pr.Owner, pr.Repo, pr.HeadSHA)
		if err == nil && count > g.Options.MaxFailingTests {
			failed = append(failed, "too_many_failing_tests")
			return GateResult{Pass: false, Reason: "too many failing tests already", Failed: failed}
		}
	}

	return GateResult{Pass: true, Failed: nil}
}
