package dotnetproj

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// reProjectReference captures the path a <ProjectReference Include="..."/> points at.
	reProjectReference = regexp.MustCompile(`(?i)<ProjectReference\s+[^>]*Include\s*=\s*"([^"]+)"`)

	// reCompileInclude captures a <Compile Include="..."/>. A project can pull source in from
	// outside its own directory this way, and that source is as compilable from a test as anything
	// under the project — so an Include reaching outside means the closure is not the directory
	// tree this computes.
	reCompileInclude = regexp.MustCompile(`(?i)<Compile\s+[^>]*Include\s*=\s*"([^"]+)"`)
)

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
// Returns nil (meaning "no claim") whenever the closure cannot be read in full. An empty set and an
// unknown set look the same to a caller that filters on membership, and filtering everything out
// because a file was unreadable would turn a parse error into a run that plans nothing. Four things
// make it unreadable, and the first two are ordinary layouts rather than malformed ones:
//
//   - an Include built from an MSBuild property ($(SrcRoot)\Shop\Shop.csproj). Expanding it needs
//     the evaluated property bag, which is MSBuild's job, not this file's;
//   - a <Compile Include> reaching outside the project's own directory, which puts compilable
//     source somewhere no directory in the closure covers;
//   - an Include that resolves to nothing on disk, which means it was read wrong; and
//   - a reference chain deeper than the bound below.
//
// References are read from the Directory.Build.props / .targets chain as well as from the project,
// because factoring a shared ProjectReference into tests/Directory.Build.props is an ordinary
// layout — the same reason ResolveFacts reads PackageReference from that chain, and the same shape
// of miss that made the coverage gate report no coverlet in any project.
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
	complete := true
	var walk func(csprojAbs string, depth int)
	walk = func(csprojAbs string, depth int) {
		if depth > maxProjectReferenceDepth {
			complete = false
			return
		}
		dir := filepath.Dir(csprojAbs)
		relDir, err := filepath.Rel(root, dir)
		if err != nil || strings.HasPrefix(relDir, "..") {
			complete = false
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

		xmls := projectEvaluationXML(root, csprojAbs)
		if len(xmls) == 0 {
			complete = false
			return
		}
		for _, xml := range xmls {
			for _, m := range reCompileInclude.FindAllStringSubmatch(xml, -1) {
				if includeEscapesProjectDir(m[1]) {
					complete = false
				}
			}
			for _, m := range reProjectReference.FindAllStringSubmatch(xml, -1) {
				// The Include is relative to the PROJECT directory — MSBuild resolves an imported
				// file's item paths against $(MSBuildProjectDirectory), not against the importing
				// file — and uses Windows separators as a matter of course, even in a repository
				// that has never seen Windows.
				include := strings.ReplaceAll(m[1], `\`, "/")
				if strings.Contains(include, "$(") {
					complete = false
					continue
				}
				target := filepath.Join(dir, filepath.FromSlash(include))
				if st, serr := os.Stat(target); serr != nil || st.IsDir() {
					complete = false
					continue
				}
				walk(target, depth+1)
			}
		}
	}
	walk(abs, 0)
	if !complete {
		return nil
	}

	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// projectEvaluationXML returns the comment-stripped XML that contributes items to one project: the
// project itself and every Directory.Build.props / .targets it inherits from, inside the checkout.
func projectEvaluationXML(repoRoot, csprojAbs string) []string {
	b, err := os.ReadFile(csprojAbs)
	if err != nil {
		return nil
	}
	out := []string{StripXMLComments(string(b))}
	for _, name := range []string{"Directory.Build.props", "Directory.Build.targets"} {
		for _, path := range ancestorImportChain(repoRoot, csprojAbs, name) {
			if body, ok := readStrippedXML(path); ok {
				out = append(out, body)
			}
		}
	}
	return out
}

// includeEscapesProjectDir reports whether an item Include names a path outside the project's own
// directory. An absolute path and any `..` segment both do; a wildcard under the project does not.
func includeEscapesProjectDir(include string) bool {
	p := strings.TrimSpace(strings.ReplaceAll(include, `\`, "/"))
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, ":/") || strings.Contains(p, "$(") {
		return true
	}
	return p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../")
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
