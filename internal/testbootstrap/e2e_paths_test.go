package testbootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

// stepRecorder captures the audit steps a bootstrap emits.
type stepRecorder struct{ steps []string }

func (r *stepRecorder) Log(_ context.Context, step string, _ interface{}) {
	r.steps = append(r.steps, step)
}
func (r *stepRecorder) LogError(_ context.Context, step string, _ interface{}) {
	r.steps = append(r.steps, step)
}

// GUARD: an absent GitRepoRoot means "the workspace is the repository", not "the process's
// current directory".
//
// filepath.Clean("") returns ".", so testing the CLEANED string for emptiness can never detect an
// absent path. This disabled E2E bootstrap outright for the only caller there is: pipeline.go does
// not set GitRepoRoot, so gitRoot resolved to asqs-core's own working directory and the containment
// check rejected the target repository — before the first audit event, leaving one stderr line as
// the sole trace. Runs then generated Playwright tests against repositories with no Playwright.
func TestRunE2EBootstrap_absentGitRootDefaultsToTheWorkspace(t *testing.T) {
	repo := t.TempDir()
	cfg := config.E2EFrameworkBootstrapConfig{Enabled: true, Mode: "auto", Execution: "local"}
	rc := config.RunnerConfig{Type: "local", Timeout: "1m"}
	rec := &stepRecorder{}

	err := RunE2EBootstrap(context.Background(), E2EParams{
		RepoPath:      repo,
		Lang:          "kotlin", // no apply path: stops right after start, touching nothing
		Config:        &cfg,
		MaxGapsE2E:    5,
		RunnerTimeout: "1m",
		Runner:        &rc,
		RunnerType:    "local",
	}, rec)

	if err != nil && strings.Contains(err.Error(), "must be under git root") {
		t.Fatalf("absent GitRepoRoot was treated as the working directory: %v", err)
	}
	// Reaching the start event is the whole point: the bug returned before any event was logged.
	var started bool
	for _, s := range rec.steps {
		if s == "e2e_bootstrap.start" {
			started = true
		}
	}
	if !started {
		t.Fatalf("e2e_bootstrap.start never fired; steps were %v (err=%v)", rec.steps, err)
	}
}

// An explicitly wrong git root is still rejected — the containment check is the point of the
// parameter, and the fix above must not have removed it.
func TestRunE2EBootstrap_workspaceOutsideGitRootIsRejected(t *testing.T) {
	cfg := config.E2EFrameworkBootstrapConfig{Enabled: true, Mode: "auto", Execution: "local"}
	rc := config.RunnerConfig{Type: "local", Timeout: "1m"}
	err := RunE2EBootstrap(context.Background(), E2EParams{
		RepoPath:    t.TempDir(),
		GitRepoRoot: t.TempDir(), // a sibling, not an ancestor
		Lang:        "java",
		Config:      &cfg,
		MaxGapsE2E:  5,
		Runner:      &rc,
		RunnerType:  "local",
	}, &stepRecorder{})
	if err == nil || !strings.Contains(err.Error(), "must be under git root") {
		t.Fatalf("a workspace outside the git root must be rejected; got %v", err)
	}
}

// An empty RepoPath must be an error, not a silent fallback to the working directory — bootstrap
// PATCHES what it is pointed at, so "." would edit whatever directory asqs-core was started in.
func TestRunE2EBootstrap_emptyRepoPathIsAnError(t *testing.T) {
	cfg := config.E2EFrameworkBootstrapConfig{Enabled: true, Mode: "auto", Execution: "local"}
	rc := config.RunnerConfig{Type: "local", Timeout: "1m"}
	err := RunE2EBootstrap(context.Background(), E2EParams{
		RepoPath:   "   ",
		Lang:       "java",
		Config:     &cfg,
		MaxGapsE2E: 5,
		Runner:     &rc,
		RunnerType: "local",
	}, &stepRecorder{})
	if err == nil || !strings.Contains(err.Error(), "empty repo path") {
		t.Fatalf("an empty repo path must be rejected rather than resolved to the CWD; got %v", err)
	}
}
