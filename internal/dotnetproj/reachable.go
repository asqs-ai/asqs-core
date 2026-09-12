package dotnetproj

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// reProjectReference captures the path a <ProjectReference Include="..."/> points at.
var reProjectReference = regexp.MustCompile(`(?i)<ProjectReference\s+[^>]*Include\s*=\s*"([^"]+)"`)

// maxProjectReferenceDepth bounds the transitive walk. A reference chain deeper than this is a
// solution whose graph nobody could hold in their head either.
const maxProjectReferenceDepth = 16

// TestProjectReachableDirs returns the repo-relative directories whose code a test project can
// reference: its own, plus every project reachable through ProjectReference, transitively.
//
// This answers a question the pipeline never asked, and the §4.1.1 validation run is what made it
// visible. Four of nine gaps were discarded for one reason: Shop.Tests references Shop.Core and NOT
// Shop, so a test for a controller or a Razor Pages model cannot name the type it is testing
// however well it is written. Nothing noticed until the compiler said so — after the gap had spent
// its generation and its fix rounds.
//
// ProjectReference is transitive for compilation in SDK-style projects, which is why the walk
// follows the chain rather than reading one level.
//
// Returns nil (meaning "no claim") when the test project cannot be read. An empty set and an
// unknown set look the same to a caller that filters on membership, and filtering everything out
// because a file was unreadable would turn a parse error into a run that plans nothing.
func TestProjectReachableDirs(repoRoot, testCsprojRel string) []string {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	rel := strings.TrimSpace(filepath.ToSlash(testCsprojRel))
	if root == "" || rel == "" {
		return nil
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(csprojAbs string, depth int)
	walk = func(csprojAbs string, depth int) {
		if depth > maxProjectReferenceDepth {
			return
		}
		dir := filepath.Dir(csprojAbs)
		relDir, err := filepath.Rel(root, dir)
		if err != nil || strings.HasPrefix(relDir, "..") {
			return
		}
		relDir = filepath.ToSlash(relDir)
		if relDir == "." {
			relDir = ""
		}
		if seen[relDir] {
			return
		}
		seen[relDir] = true

		b, rerr := os.ReadFile(csprojAbs)
		if rerr != nil {
			return
		}
		for _, m := range reProjectReference.FindAllStringSubmatch(string(b), -1) {
			// The Include is relative to the referencing project and uses Windows separators as a
			// matter of course, even in a repository that has never seen Windows.
			target := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(m[1], `\`, "/")))
			if _, err := os.Stat(target); err != nil {
				continue
			}
			walk(target, depth+1)
		}
	}
	walk(abs, 0)

	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// FindTestProjects returns the repo-relative .csproj paths of every project that references a test
// framework, which is what makes a project a test project rather than where it sits.
func FindTestProjects(repoRoot string) []string {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	if root == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && WalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			return nil
		}
		facts, ferr := ResolveFacts(root, path)
		if ferr != nil {
			return nil
		}
		if !referencesTestFramework(facts) {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// testFrameworkPackagePrefixes are the packages whose presence makes a project a test project.
var testFrameworkPackagePrefixes = []string{
	"microsoft.net.test.sdk", "xunit", "nunit", "mstest", "microsoft.visualstudio.testtools",
}

func referencesTestFramework(f Facts) bool {
	for _, id := range f.PackageIDs() {
		lower := strings.ToLower(id)
		for _, p := range testFrameworkPackagePrefixes {
			if lower == p || strings.HasPrefix(lower, p+".") {
				return true
			}
		}
	}
	return false
}

// PathIsReachableFrom reports whether a repo-relative source path lies inside one of the given
// project directories.
func PathIsReachableFrom(sourceRel string, projectDirs []string) bool {
	p := filepath.ToSlash(strings.TrimSpace(sourceRel))
	if p == "" || len(projectDirs) == 0 {
		return false
	}
	for _, dir := range projectDirs {
		if dir == "" {
			return true // the repository root owns everything under it
		}
		if strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
}
