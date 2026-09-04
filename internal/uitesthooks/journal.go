package uitesthooks

import (
	"os"
	"sort"
)

// Journal keeps the pre-write bytes of every file the pass changes so a caller without a seam
// journal of its own (asqs-core's pipeline) can restore the tree when the compile check fails.
// asqs-go's seam phase snapshots into its own journal through Apply's beforeWrite callback and
// does not use this type.
type Journal struct {
	order []string
	full  map[string]string
	body  map[string][]byte
}

// NewJournal returns an empty journal.
func NewJournal() *Journal {
	return &Journal{full: map[string]string{}, body: map[string][]byte{}}
}

// Snapshot records the current bytes of full (keyed by rel) unless already recorded. Safe to use
// directly as Apply's beforeWrite callback.
func (j *Journal) Snapshot(full, rel string) {
	if j == nil || rel == "" {
		return
	}
	if _, seen := j.body[rel]; seen {
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return
	}
	j.order = append(j.order, rel)
	j.full[rel] = full
	j.body[rel] = b
}

// Rels lists the journalled repo-relative paths, sorted.
func (j *Journal) Rels() []string {
	if j == nil {
		return nil
	}
	out := append([]string(nil), j.order...)
	sort.Strings(out)
	return out
}

// Empty reports whether nothing was journalled.
func (j *Journal) Empty() bool { return j == nil || len(j.order) == 0 }

// RestoreAll writes every journalled file back and returns the paths restored, sorted.
func (j *Journal) RestoreAll() []string {
	if j == nil {
		return nil
	}
	var restored []string
	for _, rel := range j.order {
		if err := os.WriteFile(j.full[rel], j.body[rel], 0o644); err == nil {
			restored = append(restored, rel)
		}
	}
	sort.Strings(restored)
	return restored
}
