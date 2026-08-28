package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// Dependency restore: the argv, the once-per-round memo, and the fingerprint that decides when
// "once" has expired.
//
// # Why local needed a restore stage (D1)
//
// The Docker target has always restored before compile/test/coverage; the local target never did.
// That is not a cosmetic gap — local `npm run build` ran against whatever node_modules happened to
// be on disk, and a fresh clone had none.
//
// # Why the memo is not an optimisation
//
// Restore ran once per STEP, not once per round: compile, test and coverage each triggered it, plus
// the E2E pass and every scoped-compile fallback — five to six `npm install` / `dotnet restore`
// invocations per fix-loop iteration. Giving local the same stage without memoising would have made
// the local runner several times slower in exchange for parity, so the two changes ship together.
//
// # Why the memo key is a fingerprint and not a repo path
//
// The fix loop EDITS manifests mid-round: the LLM fixer adds a missing test dependency to pom.xml,
// package.json or a .csproj and the suite is re-run. A memo keyed on (sandbox, repo, ecosystem)
// would remember the restore from before that edit and test against stale dependencies — a wrong
// answer that looks like a flaky failure. The key therefore includes a content hash of the
// manifests the ecosystem actually restores from, so an edit invalidates it automatically.

// restoreManifests lists the files whose content decides whether a restore is still valid, per
// ecosystem. Lockfiles are included because a lockfile change is exactly a dependency change.
func restoreManifests(id profile.ToolchainID) []string {
	switch id {
	case profile.JavaMaven, profile.JavaMaven11, profile.JavaMaven21:
		return []string{"pom.xml"}
	case profile.JavaGradle, profile.JavaGradle11, profile.JavaGradle21:
		return []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradle/libs.versions.toml"}
	case profile.TypeScriptNPM:
		return []string{"package.json", "package-lock.json"}
	case profile.TypeScriptPNPM:
		return []string{"package.json", "pnpm-lock.yaml"}
	case profile.TypeScriptYarn:
		return []string{"package.json", "yarn.lock"}
	case profile.CSharpDotnet:
		return []string{"Directory.Packages.props", "Directory.Build.props", "nuget.config", "NuGet.config"}
	default:
		return nil
	}
}

// restoreKeyFor fingerprints the ecosystem plus the content of its manifests.
//
// Absent files contribute a marker rather than being skipped, so ADDING a lockfile invalidates the
// memo just as editing one does. A manifest that cannot be read contributes its error, which is
// conservative: an unreadable manifest produces a key that will not match the next attempt, so the
// restore re-runs rather than being wrongly skipped.
func restoreKeyFor(absCwd string, id profile.ToolchainID, extra ...string) string {
	h := sha256.New()
	fmt.Fprintf(h, "toolchain=%s\n", id)
	for _, e := range extra {
		fmt.Fprintf(h, "extra=%s\n", e)
	}
	names := append([]string(nil), restoreManifests(id)...)
	if id == profile.CSharpDotnet {
		// The fixer adds PackageReference entries to the test project, so every project file is a
		// dependency manifest here. A static list cannot know their paths.
		names = append(names, dotnetProjectManifests(absCwd)...)
	}
	sort.Strings(names)
	for _, rel := range names {
		p := filepath.Join(absCwd, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		switch {
		case err != nil && os.IsNotExist(err):
			fmt.Fprintf(h, "%s=<absent>\n", rel)
		case err != nil:
			fmt.Fprintf(h, "%s=<unreadable:%v>\n", rel, err)
		default:
			fmt.Fprintf(h, "%s=%x\n", rel, sha256.Sum256(b))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// restoreArgvFor returns the dependency-restore argv for a toolchain.
//
// Both targets read it from the same toolchain profile, so they cannot drift. The images passed to
// BuiltinToolchain are irrelevant here — only the Restore field is used — which is what lets the
// local target share this without pretending to have an image.
func restoreArgvFor(id profile.ToolchainID) []string {
	if id == profile.UnsupportedDocker {
		return nil
	}
	return append([]string(nil), profile.BuiltinToolchain(id, "", "", "", "").Restore...)
}

// runLocalRestoreOnce runs the plan's restore argv on the host, at most once per fingerprint.
//
// Best-effort by contract, matching the Docker path: a restore that fails is logged and the step
// proceeds, because the step's own failure is the more useful diagnostic. A missing package manager
// is reported as a skip rather than an error — on a host that is a provisioning fact, and failing
// here would mask it behind a confusing restore error.
func (s *Sandbox) runLocalRestoreOnce(ctx context.Context, plan StepPlan, absCwd string) {
	if len(plan.Restore) == 0 {
		return
	}
	s.runState().restoreOnce(plan.RestoreKey, func() {
		bin := plan.Restore[0]
		if err := requireLocalToolchain(bin); err != nil {
			fmt.Fprintf(os.Stderr, "  local restore: skipped (%v)\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "[asqs-eval] step=restore-deps phase=restore-deps argv=[%s] cwd=%s\n",
			strings.Join(plan.Restore, " "), absCwd)
		cmd := newLocalBuildCmd(absCwd, plan.Restore)
		if out, err := runCommand(ctx, cmd, s.timeoutDuration()); err != nil {
			fmt.Fprintf(os.Stderr, "  local restore: %v (continuing)\n%s\n", err, firstLines(out, 5))
		}
	})
}

// dotnetProjectManifests returns repo-relative .csproj/.sln/.slnx paths under dir, skipping build
// output and vendor directories. Bounded: it stops after a generous cap so a pathological tree
// cannot turn fingerprinting into a full-repo walk on every step.
func dotnetProjectManifests(dir string) []string {
	const cap = 200
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bin", "obj", "out", "build", "dist", "target", "packages", ".nuget":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".csproj", ".sln", ".slnx":
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		if len(out) >= cap {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}
