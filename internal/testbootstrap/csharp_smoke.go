package testbootstrap

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed testdata/AsqsBootstrapSmokeTest.xunit.cs.template
var csharpUnitSmokeXunit string

//go:embed testdata/AsqsBootstrapSmokeTest.nunit.cs.template
var csharpUnitSmokeNUnit string

//go:embed testdata/AsqsBootstrapSmokeTest.mstest.cs.template
var csharpUnitSmokeMSTest string

//go:embed testdata/AsqsAspNetCoreFrameworkSmokeTest.cs.template
var csharpAspNetCoreSmoke string

const (
	csharpSmokeNamespace                = "Asqs.Bootstrap"
	csharpSmokeClassSimpleName          = "AsqsBootstrapSmokeTest"
	csharpFrameworkSmokeClassSimpleName = "AsqsFrameworkSmokeTest"

	csharpEntryPointToken = "__ASQS_ENTRYPOINT__"
	csharpTestUsingToken  = "__ASQS_TEST_USING__"
	csharpClassAttrToken  = "__ASQS_TEST_CLASSATTR__"
	csharpMethodAttrToken = "__ASQS_TEST_METHODATTR__"
)

// csharpSmokeFile is a smoke test staged on disk.
type csharpSmokeFile struct {
	Abs string
	// FullyQualifiedName drives `dotnet test --filter FullyQualifiedName~…`.
	FullyQualifiedName string
	Wrote              bool
}

// csharpTestFrameworkTokens returns the runner-specific substitutions for the shared templates.
func csharpTestFrameworkTokens(tf CSharpTestFramework) (usingLine, classAttr, methodAttr string) {
	switch tf {
	case CSharpTestNUnit:
		return "using NUnit.Framework;", "[TestFixture]\n    ", "[Test]"
	case CSharpTestMSTest:
		return "using Microsoft.VisualStudio.TestTools.UnitTesting;", "[TestClass]\n    ", "[TestMethod]"
	default:
		return "using Xunit;", "", "[Fact]"
	}
}

// writeCSharpSmokeSource materialises one smoke class in the test project directory.
func writeCSharpSmokeSource(testProjectDir, simpleName, source string) (csharpSmokeFile, error) {
	abs := filepath.Join(testProjectDir, simpleName+".cs")
	f := csharpSmokeFile{Abs: abs, FullyQualifiedName: csharpSmokeNamespace + "." + simpleName}
	if fileExists(abs) {
		return f, nil
	}
	if err := os.MkdirAll(testProjectDir, 0o755); err != nil {
		return f, fmt.Errorf("mkdir smoke test dir: %w", err)
	}
	if err := atomicWrite(abs, []byte(source)); err != nil {
		return f, fmt.Errorf("write smoke test: %w", err)
	}
	f.Wrote = true
	return f, nil
}

// writeCSharpUnitSmokeTest stages the mandatory smoke test proving the runner, Moq and
// FluentAssertions all restore and execute.
func writeCSharpUnitSmokeTest(testProjectDir string, tf CSharpTestFramework) (csharpSmokeFile, error) {
	src := csharpUnitSmokeXunit
	switch tf {
	case CSharpTestNUnit:
		src = csharpUnitSmokeNUnit
	case CSharpTestMSTest:
		src = csharpUnitSmokeMSTest
	}
	return writeCSharpSmokeSource(testProjectDir, csharpSmokeClassSimpleName, src)
}

// writeCSharpFrameworkSmokeTest stages the ASP.NET Core host smoke test.
//
// Returns staged=false when no usable entry-point type can be found in the web project, which is not
// an error: WebApplicationFactory<T> needs a *public* type from that assembly, and a web project
// whose only types are internal cannot be booted from a separate test project without an
// InternalsVisibleTo edit that bootstrap deliberately does not make to production code.
func writeCSharpFrameworkSmokeTest(testProjectDir string, prof csharpTestProfile) (csharpSmokeFile, bool, error) {
	if prof.FrameworkSmoke != csharpSmokeAspNetCore || prof.WebProjectAbs == "" {
		return csharpSmokeFile{}, false, nil
	}
	entry, ok := csharpEntryPointType(filepath.Dir(prof.WebProjectAbs))
	if !ok {
		return csharpSmokeFile{}, false, nil
	}
	usingLine, classAttr, methodAttr := csharpTestFrameworkTokens(prof.TestFramework)
	src := csharpAspNetCoreSmoke
	src = strings.ReplaceAll(src, csharpEntryPointToken, entry)
	src = strings.ReplaceAll(src, csharpTestUsingToken, usingLine)
	src = strings.ReplaceAll(src, csharpClassAttrToken, classAttr)
	src = strings.ReplaceAll(src, csharpMethodAttrToken, methodAttr)

	f, err := writeCSharpSmokeSource(testProjectDir, csharpFrameworkSmokeClassSimpleName, src)
	return f, true, err
}

// removeCSharpSmokeFile deletes a smoke test this run created, so a failed framework smoke never
// becomes a permanently broken file the evaluator inherits.
func removeCSharpSmokeFile(f csharpSmokeFile) {
	if !f.Wrote || f.Abs == "" {
		return
	}
	_ = os.Remove(f.Abs)
}

var (
	csharpFileScopedNamespaceRE = regexp.MustCompile(`(?m)^\s*namespace\s+([\w.]+)\s*;`)
	csharpBlockNamespaceRE      = regexp.MustCompile(`(?m)^\s*namespace\s+([\w.]+)\s*(?:\{|$)`)
	// A public, non-static, non-generic class or record. Generic types cannot be used as a bare type
	// argument and static classes cannot be type arguments at all.
	csharpPublicTypeRE    = regexp.MustCompile(`(?m)^\s*public\s+(?:sealed\s+|abstract\s+|partial\s+)*(?:class|record)\s+(\w+)\s*(?:\(|:|\{|$)`)
	csharpPublicProgramRE = regexp.MustCompile(`(?m)^\s*public\s+(?:sealed\s+|partial\s+)*class\s+Program\b`)
	csharpStaticClassRE   = regexp.MustCompile(`(?m)^\s*public\s+static\s+(?:partial\s+)*class\s+(\w+)\b`)
)

// csharpEntryPointType returns the type name to hand to WebApplicationFactory<T>.
//
// Preference order:
//  1. an explicitly public Program class — the documented entry point, present when the project uses
//     a classic Main or declares `public partial class Program { }` for testability;
//  2. any other public non-static, non-generic class or record in the project, shallowest namespace
//     first so the result is stable across runs.
//
// A project using top-level statements gets an INTERNAL generated Program, which is exactly why
// case 2 exists — and why the ASP.NET Core framework smoke is advisory rather than required.
func csharpEntryPointType(projectDir string) (string, bool) {
	type candidate struct {
		fq    string
		depth int
		path  string
	}
	var programCandidates, others []candidate

	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is not a reason to abandon the scan
		}
		if d.IsDir() {
			if migrateWalkSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".cs") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := stripCSharpComments(string(b))
		ns := csharpNamespaceOf(src)

		if csharpPublicProgramRE.MatchString(src) {
			programCandidates = append(programCandidates, candidate{fq: qualify(ns, "Program"), depth: strings.Count(ns, "."), path: path})
		}

		static := map[string]bool{}
		for _, m := range csharpStaticClassRE.FindAllStringSubmatch(src, -1) {
			static[m[1]] = true
		}
		for _, m := range csharpPublicTypeRE.FindAllStringSubmatch(src, -1) {
			name := m[1]
			if static[name] || name == "Program" {
				continue
			}
			others = append(others, candidate{fq: qualify(ns, name), depth: strings.Count(ns, "."), path: path})
		}
		return nil
	})

	pick := func(cs []candidate) (string, bool) {
		if len(cs) == 0 {
			return "", false
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].depth != cs[j].depth {
				return cs[i].depth < cs[j].depth
			}
			if cs[i].path != cs[j].path {
				return cs[i].path < cs[j].path
			}
			return cs[i].fq < cs[j].fq
		})
		return cs[0].fq, true
	}
	if fq, ok := pick(programCandidates); ok {
		return fq, true
	}
	return pick(others)
}

func qualify(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "." + name
}

func csharpNamespaceOf(src string) string {
	if m := csharpFileScopedNamespaceRE.FindStringSubmatch(src); m != nil {
		return m[1]
	}
	if m := csharpBlockNamespaceRE.FindStringSubmatch(src); m != nil {
		return m[1]
	}
	return ""
}

var (
	csharpLineCommentRE  = regexp.MustCompile(`(?m)//.*$`)
	csharpBlockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// stripCSharpComments keeps a commented-out class from being picked as the entry point.
func stripCSharpComments(src string) string {
	return csharpLineCommentRE.ReplaceAllString(csharpBlockCommentRE.ReplaceAllString(src, ""), "")
}
