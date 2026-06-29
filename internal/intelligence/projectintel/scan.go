package projectintel

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxWalkFiles = 4000

// maxAPIFileBytes caps how many bytes are read from YAML files to check for OpenAPI markers.
const maxAPIFileBytes = 4096

var ignoreDirNames = map[string]struct{}{
	"node_modules": {}, ".git": {}, "vendor": {}, "dist": {}, "coverage": {},
	"__pycache__": {}, ".next": {}, "build": {}, ".cache": {}, "tmp": {}, "temp": {},
	"target": {},
}

func normalizeMonoPrefix(mono string) string {
	return strings.Trim(filepath.ToSlash(strings.TrimSpace(mono)), "/")
}

func inMonoScope(rel, monoNorm string) bool {
	if monoNorm == "" {
		return true
	}
	r := filepath.ToSlash(rel)
	return r == monoNorm || strings.HasPrefix(r, monoNorm+"/")
}

// isSkillPath reports Cursor / Agent skill markdown paths.
func isSkillPath(rel string) bool {
	rl := strings.ToLower(filepath.ToSlash(rel))
	rl = strings.TrimPrefix(rl, "./")
	if !strings.HasSuffix(rl, "skill.md") {
		return false
	}
	return strings.HasPrefix(rl, ".cursor/skills/") ||
		strings.Contains(rl, "/.cursor/skills/") ||
		strings.HasPrefix(rl, ".agent/skills/") ||
		strings.Contains(rl, "/.agent/skills/")
}

func isDocPath(rel string) bool {
	rl := filepath.ToSlash(rel)
	if !strings.HasSuffix(strings.ToLower(rl), ".md") {
		return false
	}
	if isSkillPath(rel) {
		return false
	}
	if strings.Contains(rl, "/node_modules/") {
		return false
	}
	return true
}

// isSchemaPath reports SQL schema files.
func isSchemaPath(rel string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.ToSlash(rel)), ".sql")
}

// isAPIPath reports OpenAPI/Swagger YAML files by reading the first few KB.
func isAPIPath(absPath, rel string) bool {
	rl := strings.ToLower(filepath.ToSlash(rel))
	if !strings.HasSuffix(rl, ".yaml") && !strings.HasSuffix(rl, ".yml") {
		return false
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return false
	}
	if len(b) > maxAPIFileBytes {
		b = b[:maxAPIFileBytes]
	}
	return bytes.Contains(b, []byte("openapi:")) || bytes.Contains(b, []byte("swagger:"))
}

// Discover walks the repo for markdown docs, SKILL.md files, OpenAPI specs, and SQL schemas.
func Discover(repoAbs, monoPrefix string, extraDocGlobs, extraSkillGlobs []string) ([]Candidate, error) {
	monoNorm := normalizeMonoPrefix(monoPrefix)
	repoAbs = filepath.Clean(repoAbs)

	var out []Candidate
	n := 0
	err := filepath.WalkDir(repoAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n >= maxWalkFiles {
			return fs.SkipAll
		}
		if d.IsDir() {
			if _, skip := ignoreDirNames[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoAbs, path)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, "..") {
			return nil
		}
		if !inMonoScope(rel, monoNorm) {
			return nil
		}
		var kind DocKind
		switch {
		case isSkillPath(rel):
			kind = DocKindSkill
		case isDocPath(rel):
			kind = DocKindDoc
		case isSchemaPath(rel):
			kind = DocKindSchema
		default:
			// YAML check requires reading the file header; do stat first.
		}
		st, stErr := os.Stat(path)
		if stErr != nil || !st.Mode().IsRegular() {
			return nil
		}
		if st.Size() > 2<<20 {
			return nil
		}
		if kind == "" {
			if isAPIPath(path, rel) {
				kind = DocKindAPI
			} else {
				return nil
			}
		}
		out = append(out, Candidate{
			RelPath: rel,
			Kind:    kind,
			Size:    st.Size(),
			ModTime: st.ModTime(),
		})
		n++
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, pat := range extraDocGlobs {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(repoAbs, filepath.FromSlash(pat)))
		for _, p := range matches {
			rel, e := filepath.Rel(repoAbs, p)
			if e != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !inMonoScope(rel, monoNorm) || !isDocPath(rel) {
				continue
			}
			st, e := os.Stat(p)
			if e != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
				continue
			}
			out = append(out, Candidate{RelPath: rel, Kind: DocKindDoc, Size: st.Size(), ModTime: st.ModTime()})
		}
	}
	for _, pat := range extraSkillGlobs {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(repoAbs, filepath.FromSlash(pat)))
		for _, p := range matches {
			rel, e := filepath.Rel(repoAbs, p)
			if e != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !inMonoScope(rel, monoNorm) {
				continue
			}
			st, e := os.Stat(p)
			if e != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
				continue
			}
			out = append(out, Candidate{RelPath: rel, Kind: DocKindSkill, Size: st.Size(), ModTime: st.ModTime()})
		}
	}
	return dedupeCandidates(out), nil
}

func dedupeCandidates(in []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(in))
	var out []Candidate
	for _, c := range in {
		k := string(c.Kind) + ":" + c.RelPath
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}
