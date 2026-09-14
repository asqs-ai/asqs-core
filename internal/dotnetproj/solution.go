package dotnetproj

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Solution files name the projects that actually get built and tested, which is a different set
// from "every .csproj on disk". Reading them lives here, in the leaf package, because two callers
// need the answer and used to have only one: the bootstrap picked its target from the solution while
// generation walked the filesystem, and in a repository with four test projects under tests/ they
// disagreed — see layout.DetectCSharpUnitTestProjectDir.

// reSlnProjectCsproj matches a top-level Project(...) line whose second field is a .csproj path.
var reSlnProjectCsproj = regexp.MustCompile(`(?i)Project\("\{[0-9A-Fa-f-]+\}"\)\s*=\s*"[^"]*",\s*"([^"]+\.csproj)"\s*,`)

// RootSolutionFilePaths returns sorted *.sln and *.slnx paths directly under repo (non-recursive).
func RootSolutionFilePaths(repo string) ([]string, error) {
	repo = filepath.Clean(repo)
	ents, err := os.ReadDir(repo)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".sln" || ext == ".slnx" {
			out = append(out, filepath.Join(repo, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// solutionReferencedCsprojRelPaths extracts project-relative .csproj paths from a .sln file (UTF-8 BOM ok).
func solutionReferencedCsprojRelPaths(slnAbs string) ([]string, error) {
	b, err := os.ReadFile(slnAbs)
	if err != nil {
		return nil, err
	}
	s := string(b)
	if len(s) >= 3 && s[0] == '\xef' && s[1] == '\xbb' && s[2] == '\xbf' {
		s = s[3:]
	}
	m := reSlnProjectCsproj.FindAllStringSubmatch(s, -1)
	seen := make(map[string]bool)
	var out []string
	for _, sub := range m {
		if len(sub) < 2 {
			continue
		}
		rel := strings.TrimSpace(sub[1])
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out, nil
}

// slnxReferencedCsprojRelPaths extracts project-relative .csproj paths from an XML .slnx file.
// It collects every <Project Path="..."/> anywhere under <Solution> (including inside <Folder>), ignoring XML namespaces.
func slnxReferencedCsprojRelPaths(slnxAbs string) ([]string, error) {
	b, err := os.ReadFile(slnxAbs)
	if err != nil {
		return nil, err
	}
	s := string(b)
	if len(s) >= 3 && s[0] == '\xef' && s[1] == '\xbb' && s[2] == '\xbf' {
		s = s[3:]
	}

	dec := xml.NewDecoder(strings.NewReader(s))
	dec.Strict = false
	seen := make(map[string]bool)
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Project" {
			continue
		}
		for _, a := range se.Attr {
			if a.Name.Local != "Path" {
				continue
			}
			rel := strings.TrimSpace(a.Value)
			if rel == "" || seen[rel] {
				break
			}
			if !strings.HasSuffix(strings.ToLower(rel), ".csproj") {
				break
			}
			seen[rel] = true
			out = append(out, rel)
			break
		}
	}
	return out, nil
}

func pathIsUnderRepo(repo, abs string) bool {
	repo = filepath.Clean(repo)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(repo, abs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CsprojPathsFromRootSolutions returns unique absolute paths to SDK-style .csproj files listed in any
// root-level .sln or .slnx under repo. Paths must exist on disk and lie under repo. Order is undefined; caller sorts.
func CsprojPathsFromRootSolutions(repo string) ([]string, error) {
	solutions, err := RootSolutionFilePaths(repo)
	if err != nil {
		return nil, err
	}
	repo = filepath.Clean(repo)
	seen := make(map[string]bool)
	var out []string
	for _, sln := range solutions {
		var rels []string
		var err error
		switch strings.ToLower(filepath.Ext(sln)) {
		case ".slnx":
			rels, err = slnxReferencedCsprojRelPaths(sln)
		default:
			rels, err = solutionReferencedCsprojRelPaths(sln)
		}
		if err != nil {
			continue
		}
		slnDir := filepath.Dir(sln)
		for _, rel := range rels {
			rel = filepath.FromSlash(strings.ReplaceAll(rel, `\`, `/`))
			abs := filepath.Clean(filepath.Join(slnDir, rel))
			if !pathIsUnderRepo(repo, abs) {
				continue
			}
			st, err := os.Stat(abs)
			if err != nil || st.IsDir() {
				continue
			}
			if seen[abs] {
				continue
			}
			b, err := os.ReadFile(abs)
			if err != nil || !IsSDKStyle(string(b)) {
				continue
			}
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out, nil
}
