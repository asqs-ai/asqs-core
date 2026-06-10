package errloc

import (
	"os"
	"path/filepath"
	"strings"
)

// ExtraReadableRepoPaths returns up to max additional repo-relative paths cited in the log that exist under repoRoot
// and are not already in alreadyHave (keys normalized like NormalizePath).
func ExtraReadableRepoPaths(log string, repoRoot string, alreadyHave map[string]bool, max int) []string {
	if max <= 0 || log == "" || repoRoot == "" {
		return nil
	}
	repoRoot = filepath.Clean(repoRoot)
	seen := make(map[string]bool)
	for k := range alreadyHave {
		if n := NormalizePath(k); n != "" {
			seen[n] = true
		}
	}
	locs := ParseLocations(log)
	var out []string
	for _, loc := range locs {
		if len(out) >= max {
			break
		}
		rel, ok := tryResolveUnderRepo(repoRoot, loc.File)
		if !ok || rel == "" {
			continue
		}
		n := NormalizePath(rel)
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func tryResolveUnderRepo(repoRoot, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "file://")
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" {
		return "", false
	}
	var candidates []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "/")
		if s != "" {
			candidates = append(candidates, s)
		}
	}
	add(raw)
	// Ephemeral Docker eval mounts the repo at /workspace; MSBuild/dotnet often log absolute paths there.
	if after, ok := strings.CutPrefix(raw, "/workspace/"); ok {
		add(after)
	}
	if i := strings.Index(raw, "src/"); i >= 0 {
		add(raw[i:])
	}
	lowRaw := strings.ToLower(raw)
	if i := strings.Index(lowRaw, "/tests/"); i >= 0 {
		add(raw[i+1:]) // tests/... repo-relative
	}
	for _, c := range candidates {
		full := filepath.Join(repoRoot, filepath.FromSlash(c))
		full = filepath.Clean(full)
		rel, err := filepath.Rel(repoRoot, full)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}
