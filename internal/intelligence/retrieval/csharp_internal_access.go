package retrieval

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/langid"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// maxGrantScanFiles bounds the search for an [assembly: InternalsVisibleTo] attribute inside a
// project. The grant lives in the project file or in AssemblyInfo.cs by convention; a project whose
// root holds more files than this is not one this needs to read exhaustively.
const maxGrantScanFiles = 32

// internalAccessFilter answers whether a C# member declared `internal` can be reached by a test.
//
// `internal` means "this assembly". In the ordinary .NET layout the tests are their own project, so
// an internal member is exactly as unreachable from them as a private one — unless the production
// project grants InternalsVisibleTo to the test assembly. isPrivateMethod screens `private` and
// nothing screened this.
//
// Run api-2555a79ee2660a8a5cd95c8c860090f5 paid for the gap: a gap was planned against
// `internal static string DescribeBlockingLegacy(...)`, the generated test failed CS0117 for three
// iterations, the fixer's only move was deleting tests until the coverage gate stopped it, and the
// loop ended on no_accepted_writes. Fifty minutes and two of nine gaps, on a target no test could
// ever have called.
//
// A zero value allows everything. So does every branch that cannot answer: no repository path, a
// file under no project, a row with no recorded visibility. A wrong exclusion here silently removes
// work the run should have done and nothing reports it — the rule csharpReachableFilter follows.
type internalAccessFilter struct {
	repoRoot       string
	testAssemblies []string
	testProjectDir map[string]bool
	grantCache     map[string]bool
}

func newInternalAccessFilter(opts PlanOptions) internalAccessFilter {
	root := strings.TrimSpace(opts.RepoPath)
	if root == "" || !langid.IsCSharp(opts.Lang) {
		return internalAccessFilter{}
	}
	f := internalAccessFilter{
		repoRoot:       root,
		testProjectDir: map[string]bool{},
		grantCache:     map[string]bool{},
	}
	for _, proj := range dotnetproj.FindTestProjects(root) {
		f.testProjectDir[filepath.ToSlash(filepath.Dir(proj))] = true
		if name := dotnetproj.AssemblyNameFor(filepath.Join(root, filepath.FromSlash(proj))); name != "" {
			f.testAssemblies = append(f.testAssemblies, name)
		}
	}
	// No test project means the bootstrap case, where one is about to be created and nothing can be
	// said about what it will be granted.
	if len(f.testAssemblies) == 0 {
		return internalAccessFilter{}
	}
	return f
}

// allows reports whether a gap against this symbol is worth planning.
func (f internalAccessFilter) allows(sym *metadata.Symbol) bool {
	if f.repoRoot == "" || sym == nil {
		return true
	}
	if !langid.IsCSharp(sym.Lang) {
		return true
	}
	if !dotnetproj.AccessIsAssemblyScoped(symbolVisibility(sym)) {
		return true
	}
	projRel, ok := dotnetproj.NearestCsprojRel(f.repoRoot, sym.File)
	if !ok || strings.TrimSpace(projRel) == "" {
		return true
	}
	projDir := filepath.ToSlash(filepath.Dir(projRel))
	// Declared inside a test project: same assembly, no boundary to cross.
	if f.testProjectDir[projDir] {
		return true
	}
	return f.projectGrantsAnyTestAssembly(projRel)
}

// projectGrantsAnyTestAssembly reports whether the production project opens its internals to at
// least one of the repository's test assemblies.
//
// Any, not all: the gap will be written into one test project, and being permissive across them is
// the safe direction for a rule whose false positives delete work.
func (f internalAccessFilter) projectGrantsAnyTestAssembly(projRel string) bool {
	if got, ok := f.grantCache[projRel]; ok {
		return got
	}
	text := f.grantSearchText(projRel)
	granted := false
	for _, asm := range f.testAssemblies {
		if dotnetproj.GrantsInternalsVisibleTo(text, asm) {
			granted = true
			break
		}
	}
	f.grantCache[projRel] = granted
	return granted
}

// grantSearchText concatenates the places a grant is written: the project file (the MSBuild item
// form) and the project's own C# at its root and under Properties/ (the assembly-attribute form,
// which by convention lives in AssemblyInfo.cs).
func (f internalAccessFilter) grantSearchText(projRel string) string {
	var b strings.Builder
	projAbs := filepath.Join(f.repoRoot, filepath.FromSlash(projRel))
	if raw, err := os.ReadFile(projAbs); err == nil {
		b.Write(raw)
		b.WriteString("\n")
	}
	projDirAbs := filepath.Dir(projAbs)
	scanned := 0
	for _, dir := range []string{projDirAbs, filepath.Join(projDirAbs, "Properties")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".cs") {
				continue
			}
			scanned++
			if scanned > maxGrantScanFiles {
				return b.String()
			}
			if raw, rerr := os.ReadFile(filepath.Join(dir, e.Name())); rerr == nil {
				b.Write(raw)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}
