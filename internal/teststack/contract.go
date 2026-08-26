// Package teststack defines the bootstrap → generation contract: what the test stack in a repository
// actually is, written once by test_framework_bootstrap and read by everything downstream.
//
// It is a LEAF package on purpose. internal/testbootstrap imports internal/orchestrator for the
// Auditor interface, so the contract cannot live in either of them without creating an import cycle.
//
// # Absence is normal
//
// The contract is advisory in the strict sense: every consumer must behave exactly as it did before
// this package existed when the file is missing, unreadable, or from a future schema version. That is
// not defensive coding for its own sake — it is the common case:
//
//   - bootstrap is off by default (bootstrap.policy.test_framework.enabled = false);
//   - a repository that already has a complete test stack is skipped;
//   - a run may start from a checkout produced before this file was ever written.
//
// Read therefore never returns an error. It returns ok=false and the caller carries on.
package teststack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is bumped only for changes readers cannot tolerate. A reader that sees a version it
// does not know treats the contract as absent rather than guessing at the shape.
const SchemaVersion = 1

// RelPath is the contract's location inside the workspace.
const RelPath = ".asqs/test-stack.json"

// SmokeStatus describes what happened to the framework-representative smoke test.
type SmokeStatus string

const (
	// SmokeNone means the profile has no framework smoke (a plain library, a Node package).
	SmokeNone SmokeStatus = "none"
	// SmokePassed means the framework's own entrypoint compiled and ran.
	SmokePassed SmokeStatus = "passed"
	// SmokeFailed means it did not, and the artifact was removed. The unit stack is still verified.
	SmokeFailed SmokeStatus = "failed"
	// SmokeSkipped means there was nothing to boot (no application class, no public entry type).
	SmokeSkipped SmokeStatus = "skipped"
	// SmokeNotRun means bootstrap did not execute anything — the stack was already complete.
	SmokeNotRun SmokeStatus = "not_run"
)

// Smoke is the framework smoke test outcome.
type Smoke struct {
	Kind   string      `json:"kind,omitempty"`
	Status SmokeStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// Contract is what bootstrap knows about a repository's test stack.
type Contract struct {
	Version     int    `json:"version"`
	GeneratedAt string `json:"generated_at"`

	// Language is java, csharp, javascript or typescript.
	Language string `json:"language"`
	// Framework is the detected application framework (spring-boot, aspnetcore, react, …).
	Framework        string `json:"framework"`
	FrameworkVersion string `json:"framework_version,omitempty"`
	// Runner is the test runner in effect (junit5, xunit, jest, vitest, …).
	Runner string `json:"runner"`
	// Stack is the short label bootstrap audits under.
	Stack string `json:"stack"`
	// TestEnvironment is node or jsdom. JS/TS only.
	TestEnvironment string `json:"test_environment,omitempty"`

	// AvailablePackages are the coordinates on the test classpath, in the ecosystem's own notation.
	AvailablePackages []string `json:"available_packages,omitempty"`
	// AvailableImports are the import roots a generated test may reference. This is the field that
	// matters most: the run this contract exists for had twenty generated candidates rejected for
	// importing org.mockito and org.assertj on a module that carried neither.
	AvailableImports []string `json:"available_imports,omitempty"`

	// CanonicalImports maps a framework test type's SIMPLE name to the fully-qualified name it has
	// on THIS project's compile classpath (AutoConfigureMockMvc ->
	// org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc).
	//
	// AvailableImports cannot answer this. Its roots come from a coordinate table that maps an
	// artifact to package prefixes, and a prefix is version-blind: spring-boot-starter-test yields
	// org.springframework.boot.test.* whether the project is on Boot 3 or Boot 4, so the same entry
	// licenses org.springframework.boot.test.autoconfigure.web.servlet.AutoConfigureMockMvc, which
	// exists in one and not the other. Run api-0c344e6bc0658e0db06506efb9d964f5 was a Boot 4.0.1
	// project told exactly that, in a block the prompt calls authoritative.
	//
	// These entries are resolved by looking the simple name up on the project's own classpath, so
	// they are version truth by construction and need no table. Empty when no classpath could be
	// resolved, when the language has no such list, or when a name resolved ambiguously.
	CanonicalImports map[string]string `json:"canonical_imports,omitempty"`

	// Verified reports whether a smoke test actually compiled AND ran against this stack during this
	// run. False when bootstrap skipped because the stack was already complete — the packages are
	// still real, they were simply not exercised.
	//
	// It says nothing about the DEPTH of AvailableImports. The Spring smoke test imports exactly one
	// class under org.springframework.boot.test.* (SpringBootTest), so a true value here has never
	// meant every package under a listed root exists — and the prompt block must not imply it does.
	Verified bool `json:"verified"`
	// VerifiedImports are the import lines the smoke test actually compiled. They bound what
	// Verified may be read to cover.
	VerifiedImports []string `json:"verified_imports,omitempty"`
	Smoke           Smoke    `json:"framework_smoke"`

	// Notes carry operator-facing caveats (a framework smoke that could not run here, for example).
	Notes []string `json:"notes,omitempty"`
}

// Path returns the absolute contract path for a workspace.
func Path(repoPath string) string {
	return filepath.Join(repoPath, filepath.FromSlash(RelPath))
}

// Write persists the contract, creating .asqs when needed.
//
// Callers should treat a failure here as non-fatal: an unwritten contract degrades generation to its
// previous behaviour, which is strictly better than failing a bootstrap that otherwise succeeded.
func Write(repoPath string, c Contract) error {
	c.Version = SchemaVersion
	if c.GeneratedAt == "" {
		c.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if c.Smoke.Status == "" {
		c.Smoke.Status = SmokeNone
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	path := Path(repoPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".test-stack-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Read loads the contract. ok is false whenever the caller must fall back to its previous behaviour:
// the file is absent, unreadable, malformed, or written by a schema version this build does not know.
//
// It deliberately returns no error. Every caller's correct response to every failure mode is the
// same — carry on without it — and an error return invites a caller to propagate one.
func Read(repoPath string) (c Contract, ok bool) {
	b, err := os.ReadFile(Path(repoPath))
	if err != nil {
		return Contract{}, false
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Contract{}, false
	}
	if c.Version != SchemaVersion {
		return Contract{}, false
	}
	if c.Language == "" && c.Runner == "" && len(c.AvailableImports) == 0 {
		// A structurally valid but empty contract tells a consumer nothing; treat it as absent so no
		// prompt renders an empty allowlist that reads as "nothing is available".
		return Contract{}, false
	}
	return c, true
}
