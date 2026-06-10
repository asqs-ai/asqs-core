package javaindexer

import (
	"path/filepath"
	"strings"
)

// PathNormalizer maps file paths emitted by the Java indexer JAR into repo-relative paths.
// Used when the JAR ran inside Docker (paths under the container workdir) or when it echoed absolute host paths.
type PathNormalizer struct {
	HostRepoAbs      string // absolute host repo path (clean)
	ContainerWorkDir string // e.g. /workspace — repo mount inside the container
}

// NormalizeJavaIndexerPath converts a path from JSONL into a repo-relative key (forward slashes, no leading slash).
func NormalizeJavaIndexerPath(p string, norm *PathNormalizer) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if norm != nil {
		if h := filepath.ToSlash(filepath.Clean(norm.HostRepoAbs)); h != "" && h != "." {
			if strings.HasPrefix(p, h+"/") {
				p = p[len(h)+1:]
			} else if p == h {
				p = ""
			}
		}
		if c := filepath.ToSlash(filepath.Clean(norm.ContainerWorkDir)); c != "" && c != "." {
			if strings.HasPrefix(p, c+"/") {
				p = p[len(c)+1:]
			} else if p == c {
				p = ""
			}
		}
	}
	p = strings.TrimPrefix(p, "/")
	return p
}
