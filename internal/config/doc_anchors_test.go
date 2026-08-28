package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var reDocAnchor = regexp.MustCompile(`docs/([A-Z][A-Za-z0-9_.-]*\.md)#([a-z0-9_-]+)`)

// A citation with a #anchor must land somewhere. A dangling anchor is worse than a dangling file:
// the link opens, the reader lands at the top of a long document, and the specific thing they were
// sent to find is not obviously absent — so they scroll, conclude they missed it, and give up.
//
// Anchors are derived from headings the way GitHub derives them: lowercased, punctuation dropped,
// spaces to hyphens.
func TestCitedDocAnchorsResolve(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	headings := map[string]map[string]bool{} // file -> anchor set

	var dangling []string
	for _, path := range livingDocs(t, root) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, m := range reDocAnchor.FindAllStringSubmatch(string(b), -1) {
			doc, anchor := "docs/"+m[1], m[2]
			if _, loaded := headings[doc]; !loaded {
				headings[doc] = anchorsIn(filepath.Join(root, filepath.FromSlash(doc)))
			}
			if headings[doc] == nil {
				continue // the file itself is the dangling-reference guard's problem
			}
			if !headings[doc][anchor] {
				dangling = append(dangling, rel+" links to "+doc+"#"+anchor)
			}
		}
	}
	// Go sources cite anchors too, and those are the ones a reader follows from a code comment.
	for _, sub := range []string{"internal", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || (!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sql")) {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, m := range reDocAnchor.FindAllStringSubmatch(string(b), -1) {
				doc, anchor := "docs/"+m[1], m[2]
				if _, loaded := headings[doc]; !loaded {
					headings[doc] = anchorsIn(filepath.Join(root, filepath.FromSlash(doc)))
				}
				if headings[doc] != nil && !headings[doc][anchor] {
					dangling = append(dangling, filepath.ToSlash(rel)+" links to "+doc+"#"+anchor)
				}
			}
			return nil
		})
	}

	if len(dangling) > 0 {
		sort.Strings(dangling)
		t.Errorf("these citations point at a heading that does not exist:\n  %s\n\n"+
			"Add the section, or repoint the link at one that is there.", strings.Join(unique(dangling), "\n  "))
	}
}

var reHeading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)

// anchorsIn returns the GitHub-style anchors a markdown file defines, or nil when it does not exist.
func anchorsIn(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, m := range reHeading.FindAllStringSubmatch(string(b), -1) {
		out[slugify(m[1])] = true
	}
	return out
}

// slugify mirrors GitHub's heading-to-anchor rule closely enough for this check: lowercase, drop
// anything that is not a letter, digit, space or hyphen, then spaces to hyphens.
func slugify(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			// GitHub keeps underscores in anchors; dropping them would make this check disagree
			// with the renderer and report working links as broken.
			b.WriteRune('_')
		case r == ' ' || r == '-':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
