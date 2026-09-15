package runner

import "testing"

// The restore memo is keyed on a fingerprint of the dependency manifests, so it cannot tell
// "the derived state matches these manifests" from "the derived state was built from different
// manifests that have since been reverted". Those are the same fingerprint and different trees.
//
// Run api-bdf7539b296a0df65a7cf1bf2bf2739b walked straight through the gap. The baseline compile
// restored at 10:58:33 and recorded the key. One second later the pre-generate pass added
// cross-tree ProjectReferences to five .csproj files, and the seam verification restored against
// them — writing a project.assets.json in which Clean.Architecture.Infrastructure depends on
// NimblePros.SampleToDo.Web. The seam was rolled back at 10:58:58 and all five files restored. At
// 13:11:36 the evaluation compiled with --no-restore, found the recorded key unchanged, skipped the
// restore and read the poisoned graph: NU1109, naming a dependency edge that existed nowhere on
// disk. The fixer may only write test artifacts, so the whole first iteration went on an error
// nothing was allowed to repair.
func TestRestoreMemo_invalidationForcesTheNextRestore(t *testing.T) {
	st := &sandboxRunState{}
	const key = "manifest-fingerprint"

	runs := 0
	record := func() { runs++ }

	st.restoreOnce(key, record)
	if runs != 1 {
		t.Fatalf("first restore did not run: %d", runs)
	}
	st.restoreOnce(key, record)
	if runs != 1 {
		t.Fatalf("a repeat of the same fingerprint restored again: %d", runs)
	}

	st.invalidateRestoreMemo()

	st.restoreOnce(key, record)
	if runs != 2 {
		t.Fatalf("after invalidation the same fingerprint must restore again, ran %d time(s)", runs)
	}
	// And the memo is live again from there: invalidation forces one restore, not every restore.
	st.restoreOnce(key, record)
	if runs != 2 {
		t.Fatalf("invalidation disabled the memo permanently, ran %d time(s)", runs)
	}
}

// Invalidating a memo that never recorded anything is not an error, and must not restore twice for
// the first caller either.
func TestRestoreMemo_invalidationOnAnEmptyMemoIsHarmless(t *testing.T) {
	st := &sandboxRunState{}
	st.invalidateRestoreMemo()

	runs := 0
	st.restoreOnce("k", func() { runs++ })
	st.restoreOnce("k", func() { runs++ })
	if runs != 1 {
		t.Fatalf("want one restore, got %d", runs)
	}
}

// The memo lives behind a pointer precisely so the four cloning methods share it. An invalidation
// on a clone that did not reach the original would leave the stale derived state in place for
// whichever of them ran the next compile.
func TestRestoreMemo_invalidationReachesClones(t *testing.T) {
	s := &Sandbox{}
	clone := s.clone()

	runs := 0
	s.runState().restoreOnce("k", func() { runs++ })
	clone.runState().restoreOnce("k", func() { runs++ })
	if runs != 1 {
		t.Fatalf("clone did not share the memo: %d restores", runs)
	}

	clone.InvalidateRestoreMemo()

	s.runState().restoreOnce("k", func() { runs++ })
	if runs != 2 {
		t.Fatalf("invalidating through a clone did not reach the shared memo: %d restores", runs)
	}
}
