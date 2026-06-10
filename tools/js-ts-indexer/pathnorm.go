package jstindexer

import (
	"path/filepath"
	"strings"
)

// PathNormalizer maps paths emitted by the Node indexer (absolute host or container paths) to repo-relative keys.
type PathNormalizer struct {
	HostRepoAbs      string
	ContainerWorkDir string // e.g. /workspace
}

// NormalizeJSTIndexerPath returns a repo-relative path (forward slashes, no leading slash).
func NormalizeJSTIndexerPath(p string, norm *PathNormalizer) string {
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
