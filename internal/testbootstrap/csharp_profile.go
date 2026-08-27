package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CSharpFramework is the application framework a .NET project is built on.
//
// The reason this exists mirrors the Java case: "the solution has a test project" is not the same
// question as "a generated test can compile". An ASP.NET Core API whose test project carries only
// xUnit cannot host the WebApplicationFactory / Moq style generation produces, and the resulting
// errors live in a .csproj — which the fix loop is never allowed to write.
type CSharpFramework string

const (
	// CSharpFrameworkPlain covers libraries, console apps and anything with no web host.
	CSharpFrameworkPlain CSharpFramework = "plain"
	// CSharpFrameworkAspNetCore is detected from the Microsoft.NET.Sdk.Web project SDK.
	CSharpFrameworkAspNetCore CSharpFramework = "aspnetcore"
	// CSharpFrameworkBlazorWasm is a web SDK without a server host: WebApplicationFactory does not
	// apply, so it takes the plain package set and no framework smoke.
	CSharpFrameworkBlazorWasm CSharpFramework = "blazor-wasm"
	// CSharpFrameworkWorkload is detected only so bootstrap can decline. MAUI / iOS / Android /
	// Windows-desktop projects need `dotnet workload install` before they build at all, which a
	// stock SDK container does not have.
	CSharpFrameworkWorkload CSharpFramework = "workload"
)

// CSharpTestFramework is the xUnit/NUnit/MSTest choice already established in the repo.
type CSharpTestFramework string

const (
	CSharpTestXunit  CSharpTestFramework = "xunit"
	CSharpTestNUnit  CSharpTestFramework = "nunit"
	CSharpTestMSTest CSharpTestFramework = "mstest"
)

// csharpPkg is one PackageReference the profile requires.
type csharpPkg struct {
	ID      string
	Version string
	// PrivateRunner renders the IncludeAssets/PrivateAssets block test adapters need so they are not
	// propagated to consumers of the test assembly.
	PrivateRunner bool
}

// csharpFrameworkSmoke selects the framework-representative smoke test.
type csharpFrameworkSmoke string

const (
	csharpSmokeNone       csharpFrameworkSmoke = ""
	csharpSmokeAspNetCore csharpFrameworkSmoke = "aspnetcore"
)

// csharpTestProfile is the complete answer to "what does this solution need in order to host a
// generated test".
type csharpTestProfile struct {
	Framework     CSharpFramework
	TestFramework CSharpTestFramework
	// TargetFramework is the TFM the test project will use (e.g. net8.0); NetMajor is its major.
	TargetFramework string
	NetMajor        int
	// WebProjectAbs is the ASP.NET Core project the framework smoke boots, when there is one.
	WebProjectAbs string
	UsesEFCore    bool
	EFCoreMajor   int
	Evidence      string
	Stack         string
	Packages      []csharpPkg

	FrameworkSmoke csharpFrameworkSmoke
	// FrameworkSmokeRequired is false for ASP.NET Core.
	//
	// Unlike Spring Boot, where @SpringBootTest resolves configuration deterministically from the
	// package tree, WebApplicationFactory<T> needs a *type from the web assembly*, and top-level
	// statements make the generated Program class internal. Bootstrap picks a public type instead
	// (see csharpEntryPointType), which works but is an inference — so a failure downgrades the run
	// to unit-only rather than aborting it.
	FrameworkSmokeRequired bool

	Declined       bool
	DeclinedReason string
}

// csharpFrameworkCoupledVersion returns the runtime-matched version for Mvc.Testing / EF InMemory.
// Unknown majors fall back to "<major>.0.0", which these Microsoft packages always publish at GA.
func csharpFrameworkCoupledVersion(major int) string {
	if v, ok := csharpFrameworkCoupledVersions[major]; ok {
		return v
	}
	if major > 0 {
		return strconv.Itoa(major) + ".0.0"
	}
	return ""
}

// csharpBaseTestPackages returns the runner packages for the established test framework.
func csharpBaseTestPackages(tf CSharpTestFramework) []csharpPkg {
	sdk := csharpPkg{ID: "Microsoft.NET.Test.Sdk", Version: VersionDotNetTestSDK}
	switch tf {
	case CSharpTestNUnit:
		return []csharpPkg{
			sdk,
			{ID: "NUnit", Version: VersionNUnit},
			{ID: "NUnit3TestAdapter", Version: VersionNUnitTestAdapter, PrivateRunner: true},
		}
	case CSharpTestMSTest:
		return []csharpPkg{
			sdk,
			{ID: "MSTest.TestFramework", Version: VersionMSTestFramework},
			{ID: "MSTest.TestAdapter", Version: VersionMSTestTestAdapter, PrivateRunner: true},
		}
	default:
		return []csharpPkg{
			sdk,
			{ID: "xunit", Version: VersionXunit},
			{ID: "xunit.runner.visualstudio", Version: VersionXunitRunnerVS, PrivateRunner: true},
		}
	}
}

// csharpRunnerOnlyProfile is the minimal profile: just the test runner packages, no mocking,
// assertion or framework-integration libraries. The E2E bootstrap uses it — a Playwright project has
// no use for Moq or Mvc.Testing, and pulling them in would widen its restore for nothing.
func csharpRunnerOnlyProfile(tf CSharpTestFramework) csharpTestProfile {
	return csharpTestProfile{
		Framework:     CSharpFrameworkPlain,
		TestFramework: tf,
		Stack:         string(tf) + "-runner-only",
		Packages:      csharpBaseTestPackages(tf),
	}
}

// buildCSharpTestProfile turns a detection into the required package set.
func buildCSharpTestProfile(det csharpFrameworkDetection) csharpTestProfile {
	p := csharpTestProfile{
		Framework:       det.Framework,
		TestFramework:   det.TestFramework,
		TargetFramework: det.TargetFramework,
		NetMajor:        det.NetMajor,
		WebProjectAbs:   det.WebProjectAbs,
		UsesEFCore:      det.UsesEFCore,
		EFCoreMajor:     det.EFCoreMajor,
		Evidence:        det.Evidence,
	}

	if det.Framework == CSharpFrameworkWorkload {
		p.Declined = true
		p.DeclinedReason = "Every production project in this solution targets an optional .NET workload (MAUI / iOS / Android / Windows desktop). " +
			"Those need `dotnet workload install` before they build, which stock SDK containers do not have, so a test project referencing them could not compile."
		p.Stack = "workload-declined"
		return p
	}

	// Mocking has no built-in equivalent in .NET, and the generation contract explicitly steers the
	// model to Moq ("Mocking: Moq if present; otherwise hand-rolled fakes"). FluentAssertions is the
	// form generated C# assertions idiomatically take. Both are absent from a stock test project.
	p.Packages = append(csharpBaseTestPackages(det.TestFramework),
		csharpPkg{ID: "Moq", Version: VersionMoq},
		csharpPkg{ID: "FluentAssertions", Version: VersionFluentAssertions},
	)

	switch det.Framework {
	case CSharpFrameworkAspNetCore:
		p.Packages = append(p.Packages, csharpPkg{
			ID:      "Microsoft.AspNetCore.Mvc.Testing",
			Version: csharpFrameworkCoupledVersion(det.NetMajor),
		})
		p.Stack = string(det.TestFramework) + "-aspnetcore"
		p.FrameworkSmoke = csharpSmokeAspNetCore
		p.FrameworkSmokeRequired = false
	case CSharpFrameworkBlazorWasm:
		p.Stack = string(det.TestFramework) + "-blazor-wasm"
	default:
		p.Stack = string(det.TestFramework) + "-moq-fluentassertions"
	}

	if det.UsesEFCore {
		major := det.EFCoreMajor
		if major == 0 {
			major = det.NetMajor
		}
		p.Packages = append(p.Packages, csharpPkg{
			ID:      "Microsoft.EntityFrameworkCore.InMemory",
			Version: csharpFrameworkCoupledVersion(major),
		})
	}
	return p
}

// missingPackages returns the profile packages absent from a .csproj.
//
// Matching is on the exact Include attribute, not a substring: "xunit" appears inside
// "xunit.runner.visualstudio", and a substring test would report the runner as satisfying xunit.
func (p csharpTestProfile) missingPackages(csprojContent string) []csharpPkg {
	lower := strings.ToLower(csprojContent)
	var out []csharpPkg
	for _, pkg := range p.Packages {
		if strings.Contains(lower, `include="`+strings.ToLower(pkg.ID)+`"`) {
			continue
		}
		out = append(out, pkg)
	}
	return out
}

func describeCSharpPackages(pkgs []csharpPkg) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		if p.Version == "" {
			out = append(out, p.ID)
			continue
		}
		out = append(out, p.ID+" "+p.Version)
	}
	return out
}

// cpmVersionMap renders the profile for ensureCentralPackageVersions.
func cpmVersionMap(pkgs []csharpPkg) map[string]string {
	m := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		m[p.ID] = p.Version
	}
	return m
}

// renderCSharpPackageRef emits one PackageReference. Under Central Package Management the version
// lives in Directory.Packages.props and must NOT appear here.
func renderCSharpPackageRef(p csharpPkg, useCPM bool) string {
	ver := ""
	if !useCPM && p.Version != "" {
		ver = fmt.Sprintf(` Version="%s"`, p.Version)
	}
	if !p.PrivateRunner {
		return fmt.Sprintf(`    <PackageReference Include="%s"%s />`, p.ID, ver)
	}
	return fmt.Sprintf(`    <PackageReference Include="%s"%s>
      <IncludeAssets>runtime; build; native; contentfiles; analyzers; buildtransitive</IncludeAssets>
      <PrivateAssets>all</PrivateAssets>
    </PackageReference>`, p.ID, ver)
}

// csharpFrameworkDetection is the raw detection result before it becomes a profile.
type csharpFrameworkDetection struct {
	Framework       CSharpFramework
	TestFramework   CSharpTestFramework
	TargetFramework string
	NetMajor        int
	WebProjectAbs   string
	UsesEFCore      bool
	EFCoreMajor     int
	Evidence        string
}

var (
	csharpWebSDKMarkers    = []string{`sdk="microsoft.net.sdk.web"`, `sdk='microsoft.net.sdk.web'`}
	csharpBlazorSDKMarkers = []string{`sdk="microsoft.net.sdk.blazorwebassembly"`, `sdk='microsoft.net.sdk.blazorwebassembly'`}
)

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// packageReferenceVersionMajor returns the major of the pinned Version on a PackageReference whose
// Include starts with prefix, or 0.
func packageReferenceVersionMajor(csprojLower, prefix string) int {
	idx := strings.Index(csprojLower, `include="`+strings.ToLower(prefix))
	if idx < 0 {
		return 0
	}
	rest := csprojLower[idx:]
	if end := strings.Index(rest, "/>"); end > 0 {
		rest = rest[:end]
	}
	vi := strings.Index(rest, `version="`)
	if vi < 0 {
		return 0
	}
	rest = rest[vi+len(`version="`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0
	}
	return netMajorFromTFM("net" + strings.TrimSpace(rest[:end]))
}

// detectCSharpFramework classifies a solution from its production and test projects.
func detectCSharpFramework(repo, fallbackTFM string) (csharpFrameworkDetection, error) {
	prod, test, err := splitCSharpProdAndTestCsprojs(repo)
	if err != nil {
		return csharpFrameworkDetection{}, err
	}

	det := csharpFrameworkDetection{
		Framework:     CSharpFrameworkPlain,
		TestFramework: csharpEstablishedTestFramework(test),
		Evidence:      "no web SDK found in the production projects",
	}

	if len(prod) == 0 {
		// splitCSharpProdAndTestCsprojs drops workload projects from prod; a solution that is nothing
		// but MAUI/mobile/desktop therefore lands here with something still on disk.
		if paths, derr := discoverSDKStyleCsprojPaths(repo); derr == nil && len(paths) > 0 {
			for _, p := range paths {
				if csprojRequiresOptionalDotnetWorkload(p) {
					det.Framework = CSharpFrameworkWorkload
					det.Evidence = "every production project requires an optional .NET workload"
					return det, nil
				}
			}
		}
	}

	det.TargetFramework = inferCSharpTestTFM(prod, fallbackTFM)
	det.NetMajor = netMajorFromTFM(det.TargetFramework)

	sort.Strings(prod)
	for _, abs := range prod {
		b, rerr := os.ReadFile(abs)
		if rerr != nil {
			continue
		}
		lower := strings.ToLower(string(b))

		if strings.Contains(lower, `include="microsoft.entityframeworkcore`) {
			det.UsesEFCore = true
			if m := packageReferenceVersionMajor(lower, "microsoft.entityframeworkcore"); m > 0 {
				det.EFCoreMajor = m
			}
		}
		switch {
		case containsAny(lower, csharpWebSDKMarkers):
			// The first web project wins; sorting makes that deterministic.
			if det.Framework != CSharpFrameworkAspNetCore {
				det.Framework = CSharpFrameworkAspNetCore
				det.WebProjectAbs = abs
				det.Evidence = "Microsoft.NET.Sdk.Web in " + filepath.Base(abs)
			}
		case containsAny(lower, csharpBlazorSDKMarkers):
			if det.Framework == CSharpFrameworkPlain {
				det.Framework = CSharpFrameworkBlazorWasm
				det.Evidence = "Microsoft.NET.Sdk.BlazorWebAssembly in " + filepath.Base(abs)
			}
		}
	}
	return det, nil
}

// csharpEstablishedTestFramework reads the repo's existing choice so bootstrap completes the stack
// the team already uses instead of dragging a second runner into the solution. xUnit is the default
// only when nothing is established.
func csharpEstablishedTestFramework(testCsprojs []string) CSharpTestFramework {
	for _, abs := range testCsprojs {
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(b))
		switch {
		case strings.Contains(lower, `include="nunit"`), strings.Contains(lower, "nunit3testadapter"):
			return CSharpTestNUnit
		case strings.Contains(lower, "mstest.testframework"), strings.Contains(lower, "mstest.testadapter"):
			return CSharpTestMSTest
		case strings.Contains(lower, `include="xunit"`), strings.Contains(lower, "xunit.runner"):
			return CSharpTestXunit
		}
	}
	return CSharpTestXunit
}

// summarizeCSharpProfile renders a one-line description for stderr and audit messages.
func summarizeCSharpProfile(p csharpTestProfile) string {
	tfm := p.TargetFramework
	if tfm == "" {
		tfm = "TFM unknown"
	}
	ef := ""
	if p.UsesEFCore {
		ef = " + EF Core"
	}
	return fmt.Sprintf("%s (%s)%s → %s", p.Framework, tfm, ef, p.Stack)
}
