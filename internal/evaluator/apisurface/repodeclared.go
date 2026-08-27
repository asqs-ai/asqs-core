package apisurface

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RepoDeclaredSimpleNames returns the simple type names the REPOSITORY declares in source, and
// whether the language supports the question at all.
//
// It exists to bound a negative claim. A classpath lookup answers "is this name in a compiled
// artifact", not "does this project have such a type" — the Java classpath is jars plus
// target/classes and target/test-classes (see JavaProvider.classpath), so a repo class that has not
// been compiled resolves to nothing. On the fixer's path that is the normal state: the fixer runs
// because compile FAILED, so the very classes involved are the ones missing from target/.
//
// Reporting such a name as absent is not a harmless miss. The prompt block that consumes it says
// "No import makes these resolve … delete the code that uses them", in a section framed as verified
// — a false statement attached to a destructive instruction. The sibling checker in this package
// already guards exactly this case (UnresolvedDependencyRefs skips repo-domain packages: "repo
// domain; file-level absence proves nothing"); this is that guard for bare-symbol lookups.
//
// supported=false means no guard exists for the language and the caller must make NO absence claim.
// Java is the only language with source roots fixed by convention; C# sources live anywhere under
// the repo and TS/JS module names are not filenames, so for those the honest answer is silence
// rather than a scan that could miss and license the same destructive directive.
//
// A walk error also yields supported=false. An incomplete set is worse than none: every name the
// walk failed to see would read as proof of absence.
func RepoDeclaredSimpleNames(lang Lang, repoRoot string) (map[string]bool, bool) {
	if lang != LangJava || strings.TrimSpace(repoRoot) == "" {
		return nil, false
	}
	out := map[string]bool{}
	sawRoot := false
	for _, root := range javaSourceRoots {
		dir := filepath.Join(repoRoot, filepath.FromSlash(root))
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			continue // a project without this root simply declares nothing under it.
		}
		sawRoot = true
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".java") {
				return nil
			}
			out[strings.TrimSuffix(d.Name(), ".java")] = true
			return nil
		})
		if err != nil {
			return nil, false
		}
	}
	if !sawRoot {
		// No Maven/Gradle source root at all: this is not a layout the scan understands, so it
		// cannot testify about what the repository declares.
		return nil, false
	}
	return out, true
}
