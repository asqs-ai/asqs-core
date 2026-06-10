package indexer

import (
	"path/filepath"
	"strings"
)

// PrefixParsedMapPathsToGitRoot rewrites ParsedFile.Path and map keys so paths are repo-relative to the git root
// when language indexers were run with cwd at a mono-repo workspace subdirectory (monoPrefix e.g. "apps/api").
func PrefixParsedMapPathsToGitRoot(m map[string]*ParsedFile, monoPrefix string) {
	if m == nil {
		return
	}
	p := strings.Trim(filepath.ToSlash(strings.TrimSpace(monoPrefix)), "/")
	if p == "" {
		return
	}
	out := make(map[string]*ParsedFile, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		slashK := filepath.ToSlash(strings.TrimSpace(k))
		slashK = strings.TrimPrefix(slashK, "/")
		var newK string
		if slashK == p || strings.HasPrefix(slashK, p+"/") {
			newK = slashK
		} else {
			newK = p + "/" + slashK
		}
		v.Path = newK
		out[newK] = v
	}
	for k := range m {
		delete(m, k)
	}
	for k, v := range out {
		m[k] = v
	}
}
