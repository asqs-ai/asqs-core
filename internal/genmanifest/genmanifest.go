// Package genmanifest records which test artifacts ASQS itself authored.
//
// ASQS cannot recognise its own output, and that turns out to be load-bearing rather than a nicety.
// A run redirects a new test into an existing on-disk file rather than creating a duplicate, and it
// picks between candidates partly on which naming convention the repository uses. Both decisions
// degrade as the tool writes more files: on the fixture repo the upstream convention was 14
// `*Tests.java` against 1 `*Test.java` (93% — unambiguous), but after a single ASQS run committed
// seven `*Test.java` artifacts it read 14 against 9 (61%), and one more run would invert it. Left
// alone, the tool's own output progressively outvotes the repository it is meant to follow.
//
// The manifest is therefore the ground truth for "did we write this file", used to
//   - exclude ASQS-authored files from the convention vote, and
//   - rank a human-authored test above a generated one when both back-link to the same source.
//
// It deliberately does NOT infer provenance from git history. That only works when ASQS committed
// its output, which is a policy decision the project has explicitly left open, and a run that has
// not committed yet would read as having authored nothing.
//
// The file lives at .asqs/generated-artifacts.json alongside the existing project-intel cache. A
// missing or unreadable manifest is not an error: callers degrade to "provenance unknown", which
// every consumer must already handle for repositories ASQS has never touched.
package genmanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RelPath is the manifest location relative to the repository root.
const RelPath = ".asqs/generated-artifacts.json"

// Entry records one artifact ASQS wrote.
type Entry struct {
	// Path is repo-relative, forward-slashed.
	Path string `json:"path"`
	// RunID is the run that FIRST wrote the path. Later runs extending the same file do not
	// overwrite it — provenance is about authorship, not last touch.
	RunID string `json:"run_id"`
	// FirstWrittenAt is RFC3339 UTC, set when the path is first recorded.
	FirstWrittenAt string `json:"first_written_at"`
}

// Manifest is the on-disk document.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

const currentVersion = 1

// nowFunc is the clock seam for tests.
var nowFunc = time.Now

// mu guards read-modify-write of the manifest file. Generation writes tests concurrently (the gap
// fan-out runs at gap_concurrency, 8 by default), and every one of them may append here.
var mu sync.Mutex

// Normalize returns the canonical key form for a repo-relative path.
func Normalize(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "/")
}

// Load reads the manifest for repoRoot. A missing, empty, or malformed file yields an empty
// manifest and no error: provenance is advisory, and a corrupt manifest must not fail a run.
func Load(repoRoot string) Manifest {
	if strings.TrimSpace(repoRoot) == "" {
		return Manifest{Version: currentVersion}
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(RelPath)))
	if err != nil {
		return Manifest{Version: currentVersion}
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{Version: currentVersion}
	}
	if m.Version == 0 {
		m.Version = currentVersion
	}
	return m
}

// Set is a fast membership view over a manifest.
type Set map[string]Entry

// Has reports whether ASQS authored p.
func (s Set) Has(p string) bool {
	if len(s) == 0 {
		return false
	}
	_, ok := s[Normalize(p)]
	return ok
}

// Known reports whether the manifest carries any provenance at all. Callers use this to tell
// "ASQS wrote nothing here" apart from "we have no idea", which are different rankings.
func (s Set) Known() bool { return len(s) > 0 }

// AsSet indexes the manifest by normalized path.
func (m Manifest) AsSet() Set {
	out := make(Set, len(m.Entries))
	for _, e := range m.Entries {
		out[Normalize(e.Path)] = e
	}
	return out
}

// LoadSet is Load followed by AsSet.
func LoadSet(repoRoot string) Set { return Load(repoRoot).AsSet() }

// Record adds paths authored by runID, preserving the existing entry for any path already present,
// and writes the manifest back. Paths already recorded keep their original run id and timestamp.
// Returns the number of newly recorded paths.
//
// Safe to call concurrently within a process; the mutex serialises the read-modify-write. Cross
// process concurrency on one workspace is out of scope — a workspace belongs to one run.
func Record(repoRoot, runID string, paths []string) (int, error) {
	if strings.TrimSpace(repoRoot) == "" || len(paths) == 0 {
		return 0, nil
	}
	mu.Lock()
	defer mu.Unlock()

	m := Load(repoRoot)
	have := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		have[Normalize(e.Path)] = true
	}
	now := nowFunc().UTC().Format(time.RFC3339)
	added := 0
	for _, p := range paths {
		key := Normalize(p)
		if key == "" || have[key] {
			continue
		}
		have[key] = true
		m.Entries = append(m.Entries, Entry{Path: key, RunID: strings.TrimSpace(runID), FirstWrittenAt: now})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	m.Version = currentVersion

	full := filepath.Join(repoRoot, filepath.FromSlash(RelPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, err
	}
	// Write via a temp file in the same directory so a crash cannot leave a truncated manifest that
	// Load would silently read as "ASQS authored nothing".
	tmp, err := os.CreateTemp(filepath.Dir(full), ".generated-artifacts-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	return added, nil
}
