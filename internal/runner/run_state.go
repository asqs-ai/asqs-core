package runner

import "sync"

// Per-run Sandbox state, held behind a pointer so that clones share it.
//
// Four Sandbox methods — TestWithCommand, CompileWithCommand, TestE2EPass, CoverageWithCommand —
// override a single command by taking a shallow copy of the receiver. While the state lived
// directly on Sandbox as individual `*sync.Once` fields, whether a clone SHARED or FORKED state
// depended entirely on the field's Go type: a copied pointer happened to share, but a `bool` or a
// lazily-created `nil` map would have forked. Nothing enforced the distinction, and getting it
// wrong is invisible — the restore memo (CP31) would simply re-run restore per clone and quietly
// give up the whole point of memoising.
//
// Moving the state behind one pointer makes sharing structural instead of type-dependent, and —
// the part that matters for what comes next — means any mutex added to this struct is shared by
// every clone rather than copied into independent locks.
type sandboxRunState struct {
	// dockerEvalEnvOnce logs the full docker eval environment once per run.
	dockerEvalEnvOnce sync.Once
	// localEvalEnvOnce logs the local eval environment once per run.
	localEvalEnvOnce sync.Once
	// nugetPluginWarnOnce reports a missing Artifacts credential provider once per run (CP33).
	nugetPluginWarnOnce sync.Once
	// e2eBrowserWarnOnce reports a missing local browser cache once per run (CP34).
	e2eBrowserWarnOnce sync.Once

	// mu guards restoredKeys. It is the concrete payoff of this file: a mutex declared HERE is
	// shared by every clone, whereas the same field on Sandbox would be copied into four
	// independent locks by clone() — with no new `go vet` finding to say so.
	mu sync.Mutex
	// restoredKeys records which dependency-restore fingerprints have already run this round (CP31).
	restoredKeys map[string]bool
}

// restoreOnce runs fn unless a restore with this fingerprint already ran, and records it.
//
// The lock is held across fn deliberately. Restore is a heavyweight, side-effecting step and two
// concurrent `npm install` runs in one working tree corrupt node_modules; serialising is the point,
// not a detail. Nothing drives the Sandbox concurrently today, so there is no contention to lose.
func (r *sandboxRunState) restoreOnce(key string, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == "" {
		fn()
		return
	}
	if r.restoredKeys[key] {
		return
	}
	if r.restoredKeys == nil {
		r.restoredKeys = map[string]bool{}
	}
	r.restoredKeys[key] = true
	fn()
}

// runState returns the shared per-run state, allocating it if the Sandbox was built as a struct
// literal rather than through NewSandboxFromConfig (which allocates eagerly).
//
// Not safe for concurrent first use. That is not a live constraint: the eval Sandbox is driven
// sequentially by the evaluator. Should concurrent use ever be introduced, the fix is to rely on
// NewSandboxFromConfig's eager allocation and add a mutex to sandboxRunState — which, being behind
// this pointer, would then correctly be shared by every clone.
func (s *Sandbox) runState() *sandboxRunState {
	if s.run == nil {
		s.run = &sandboxRunState{}
	}
	return s.run
}

// clone returns a shallow copy of the Sandbox that SHARES its per-run state.
//
// The runState call before the copy is the load-bearing part: it guarantees the pointer is
// non-nil at copy time, so the clone cannot end up with state of its own. Callers must use this
// rather than a bare `*s`, which would share only when the receiver happened to be initialised
// already — exactly the accidental invariant this file exists to remove.
func (s *Sandbox) clone() Sandbox {
	s.runState()
	return *s
}
