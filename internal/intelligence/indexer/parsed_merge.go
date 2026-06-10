package indexer

// MergeParsedMapsNoOverwrite copies entries from src into dst when dst does not already contain the key.
// Primary / first-writer wins. Call PrefixParsedMapPathsToGitRoot on src before merge when the indexer
// was run from a subdirectory of the git root.
func MergeParsedMapsNoOverwrite(dst map[string]*ParsedFile, src map[string]*ParsedFile) {
	if dst == nil || len(src) == 0 {
		return
	}
	for k, v := range src {
		if v == nil {
			continue
		}
		if _, exists := dst[k]; exists {
			continue
		}
		dst[k] = v
	}
}
