package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShipRoundTrip exercises the asqs-go-style ship flow against a LOCAL bare remote (no network):
// clone → prepare ship branch → "generate" a file → add → commit → push, then a SECOND run that
// reuses the existing remote ship branch and adds another file. It proves the repo primitives the
// CLI's prepareShipBranch/shipRun compose actually land the generated files on the ship branch.
func TestShipRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not on PATH")
	}
	ctx := context.Background()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")

	// Seed a bare "origin" with one commit on main.
	seed := filepath.Join(root, "seed")
	runGit(t, "", "init", "-q", "--bare", origin)
	runGit(t, "", "init", "-q", seed)
	runGit(t, seed, "config", "user.email", "t@t")
	runGit(t, seed, "config", "user.name", "t")
	runGit(t, seed, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-q", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "-q", "origin", "main")

	const shipBranch, base = "asqs-core", "main"

	shipOnce := func(genFile, content string) {
		work := filepath.Join(root, "work-"+genFile)
		r, err := Clone(ctx, CloneOptions{URL: origin, Dir: work, Branch: base, Depth: 0, FetchAllRefs: true})
		if err != nil {
			t.Fatalf("clone: %v", err)
		}
		// prepareShipBranch logic: reuse existing remote ship branch, else create from base.
		if err := r.Fetch(ctx, FetchOptions{RemoteName: "origin"}); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		exists, err := r.HasRemoteBranch("origin", shipBranch)
		if err != nil {
			t.Fatalf("has-remote-branch: %v", err)
		}
		if exists {
			if err := r.CheckoutBranchFromRemote("origin", shipBranch); err != nil {
				t.Fatalf("checkout remote: %v", err)
			}
		} else if err := r.CreateAndCheckoutBranch(shipBranch); err != nil {
			t.Fatalf("create branch: %v", err)
		}
		// "generate" artifacts: a NEW test file AND a modification to a TRACKED file. The latter
		// mirrors --docs inserting into existing sources (and the fixer rewriting tracked files) — it
		// is what makes a same-branch re-checkout fail with go-git's "unstaged changes".
		if err := os.WriteFile(filepath.Join(work, genFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"+content), 0o644); err != nil {
			t.Fatal(err)
		}
		// Regression guard: with a dirty TRACKED change, re-checking-out the SAME branch MUST fail —
		// which is why shipRun skips the checkout when CurrentBranch already equals the ship branch.
		if err := r.CheckoutBranch(shipBranch); err == nil {
			t.Fatalf("expected dirty-tree checkout of %q to fail", shipBranch)
		}
		if cur, _ := r.CurrentBranch(); cur != shipBranch {
			t.Fatalf("expected to still be on %q, got %q", shipBranch, cur)
		}
		// add/commit/push (shipRun mechanics — no re-checkout).
		if err := r.Add("."); err != nil {
			t.Fatalf("add: %v", err)
		}
		if err := r.Commit("asqs-core: generated", nil); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := r.Push(ctx, PushOptions{RemoteName: "origin", Branch: shipBranch}); err != nil {
			t.Fatalf("push: %v", err)
		}
	}

	// First run: branch does not exist yet → created from base, file pushed.
	shipOnce("FooTest.java", "// foo\n")
	if got := lsTree(t, origin, shipBranch); !strings.Contains(got, "FooTest.java") {
		t.Fatalf("first run: ship branch missing FooTest.java; tree=\n%s", got)
	}

	// Second run: branch exists on origin → reused, second file added on top (PR update, not divergent).
	shipOnce("BarTest.java", "// bar\n")
	got := lsTree(t, origin, shipBranch)
	if !strings.Contains(got, "BarTest.java") || !strings.Contains(got, "FooTest.java") {
		t.Fatalf("second run: ship branch should hold both files; tree=\n%s", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func lsTree(t *testing.T, bare, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", bare, "ls-tree", "-r", "--name-only", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-tree %s: %v\n%s", branch, err, out)
	}
	return string(out)
}
