package indexer

import (
	"context"
)

// DetectChanges compares the current file set with the last indexed state in metadata and returns a ChangeSet.
// currentFiles: files present in the repo (path + SHA). meta: MetadataWriter to read last known files.
func DetectChanges(ctx context.Context, currentFiles []FileVersion, meta MetadataWriter) (*ChangeSet, error) {
	// Load last indexed state: all files from metadata (we need ListFiles without lang filter to get all).
	stored, err := meta.ListFiles(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]string) // path -> sha
	for _, f := range stored {
		byPath[f.File] = f.SHA
	}
	currentByPath := make(map[string]FileVersion)
	for _, f := range currentFiles {
		currentByPath[f.Path] = f
	}

	var added, changed []FileVersion
	var removed []string
	for path, fv := range currentByPath {
		prevSHA, ok := byPath[path]
		if !ok {
			added = append(added, fv)
			continue
		}
		if prevSHA != fv.SHA {
			changed = append(changed, fv)
		}
	}
	for path := range byPath {
		if _, ok := currentByPath[path]; !ok {
			removed = append(removed, path)
		}
	}
	return &ChangeSet{Added: added, Changed: changed, Removed: removed}, nil
}
