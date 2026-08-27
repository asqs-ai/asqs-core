package apisurface

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// classEntriesMatching returns the class entries inside a classpath element whose path ends with
// suffix (e.g. "/RuntimeHints.class"). Handles both jar files and exploded class directories.
//
// This is what turns "cannot find symbol: class RuntimeHints" into
// "org/springframework/aot/hint/RuntimeHints.class", i.e. the exact import line to add. javap
// cannot help there — it needs a fully-qualified name, which is precisely what the diagnostic is
// missing.
func classEntriesMatching(entry, suffix string) []string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	st, err := os.Stat(entry)
	if err != nil {
		return nil
	}
	if st.IsDir() {
		return dirEntriesMatching(entry, suffix)
	}
	if !strings.HasSuffix(strings.ToLower(entry), ".jar") {
		return nil
	}
	return jarEntriesMatching(entry, st, suffix)
}

type jarIndex struct {
	names   []string
	modTime time.Time
	size    int64
}

var (
	jarMu    sync.Mutex
	jarCache = map[string]jarIndex{}
)

// jarEntriesMatching reads a jar's central directory once and caches the class list. A Maven test
// classpath routinely carries 80+ jars and the fixer looks names up every round; re-opening each
// jar per lookup would dominate the cost of the whole feature.
func jarEntriesMatching(path string, st os.FileInfo, suffix string) []string {
	jarMu.Lock()
	idx, ok := jarCache[path]
	jarMu.Unlock()

	if !ok || idx.modTime != st.ModTime() || idx.size != st.Size() {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(zr.File))
		for _, f := range zr.File {
			if strings.HasSuffix(f.Name, ".class") {
				names = append(names, f.Name)
			}
		}
		zr.Close()
		idx = jarIndex{names: names, modTime: st.ModTime(), size: st.Size()}
		jarMu.Lock()
		jarCache[path] = idx
		jarMu.Unlock()
	}
	var out []string
	for _, n := range idx.names {
		if strings.HasSuffix(n, suffix) && !strings.Contains(n[strings.LastIndex(n, "/")+1:], "$") {
			out = append(out, n)
		}
	}
	return out
}

// dirEntriesMatching walks an exploded classes directory. Bounded by the directory itself, which
// for a project's own target/classes is small.
func dirEntriesMatching(root, suffix string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix("/"+rel, suffix) && !strings.Contains(filepath.Base(rel), "$") {
			out = append(out, rel)
		}
		return nil
	})
	return out
}
