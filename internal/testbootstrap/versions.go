package testbootstrap

import "strconv"

// Pinned dependency versions for reproducible Jest bootstrap (update periodically).
const (
	VersionJest      = "29.7.0"
	VersionTSJest    = "29.4.12" // peer jest ^29 || ^30, so one pin serves both Jest lines
	VersionTypesJest = "29.5.14"
	VersionTypesNode = "22.10.0"
	// VersionJest30 is required by jest-preset-angular >= 15 (Angular 18+); the Angular profile
	// selects it, everything else stays on the proven 29 line.
	VersionJest30      = "30.4.2"
	VersionTypesJest30 = "30.0.0"
	// jest-environment-jsdom ships in lockstep with Jest: a jsdom environment from the wrong major
	// fails at runtime with an unhelpful "Test environment not found".
	VersionJestEnvironmentJsdom   = "29.7.0"
	VersionJestEnvironmentJsdom30 = "30.4.1"
	// @playwright/test (pin for e2e_framework_bootstrap).
	VersionPlaywrightTest = "1.49.1"
	// DefaultPlaywrightDockerImage is mcr.microsoft.com/playwright for JS/TS E2E bootstrap in Docker (keep patch in sync with VersionPlaywrightTest).
	DefaultPlaywrightDockerImage = "mcr.microsoft.com/playwright:v1.49.1-jammy"
	// cypress (pin for e2e_framework_bootstrap).
	VersionCypress = "13.16.1"
)

// Pinned JUnit 5 / Surefire / Platform for Java bootstrap.
const (
	VersionJUnitJupiter        = "5.11.3"
	VersionJUnitPlatform       = "1.11.3"
	VersionMavenSurefirePlugin = "3.5.2"
	// Playwright Java (e2e_framework_bootstrap for JVM); align major with JS @playwright/test when possible.
	VersionPlaywrightJava = "1.49.0"
	// DefaultPlaywrightJavaDockerImage is mcr.microsoft.com/playwright/java for Java E2E bootstrap in Docker
	// (JDK + Maven + baked browsers + OS libs). Keep tag aligned with VersionPlaywrightJava.
	DefaultPlaywrightJavaDockerImage = "mcr.microsoft.com/playwright/java:v1.49.0-jammy"
)

// Vitest lines. Vitest carries Vite as a dependency, so it also works in repos with no Vite at all,
// but v4 additionally declares Vite as a PEER (^6 || ^7 || ^8) — installing it beside Vite 5 breaks.
// The profile picks the line from the repo's Vite major.
const (
	VersionVitest4 = "4.1.11" // Vite 6+
	VersionVitest3 = "3.2.4"  // Vite 5, or no Vite at all (self-contained)
	VersionVitest1 = "1.6.1"  // Vite 4 and older
)

// jsdom lines. jsdom is the only pin in this package constrained by the RUNTIME rather than by the
// repo's own manifest, so it cannot be a single constant.
//
// jsdom 30 moved to undici 8, which calls worker_threads.markAsUncloneable — a symbol that does not
// exist before Node 22.10. npm treats `engines` as advisory (EBADENGINE is a warning and the install
// still exits 0), so on Node 20 the install succeeds and jsdom then fails at require() time with
// "webidl.util.markAsUncloneable is not a function". The Vitest worker never starts, no test runs,
// and the mandatory smoke gate aborts the whole run — which is exactly what a React/Vite repository
// did on node:20-bookworm-slim before jsdomVersionForNode was derived from the registry:
//
//	jsdom 30.x  node ^22.22.2 || ^24.15.0 || >=26.0.0   (undici ^8, itself node >=22.19.0)
//	jsdom 29.x  node ^20.19.0 || ^22.13.0 || >=24.0.0   (undici ^7, itself node >=20.18.1)
//	jsdom 26.x  node >=18                               (last line with no undici dependency)
const (
	VersionJsdom30 = "30.0.1"
	VersionJsdom29 = "29.1.1"
	VersionJsdom26 = "26.1.0"
)

// jsdomLines are the releases bootstrap may install, newest first. Supported mirrors the release's
// own engines.node, so selection follows the declared contract rather than what happens to load:
// on Node 24.14.0 this picks the 29 line even though 30 runs there, because 30 declares ^24.15.0.
var jsdomLines = []struct {
	Version   string
	Supported func(major, minor, patch int) bool
}{
	{VersionJsdom30, func(major, minor, patch int) bool {
		switch major {
		case 22:
			return minor > 22 || (minor == 22 && patch >= 2)
		case 24:
			return minor >= 15
		default:
			return major >= 26
		}
	}},
	{VersionJsdom29, func(major, minor, patch int) bool {
		switch major {
		case 20:
			return minor >= 19
		case 22:
			return minor >= 13
		default:
			return major >= 24
		}
	}},
	{VersionJsdom26, func(major, minor, patch int) bool { return major >= 18 }},
}

// jsdomVersionForNode picks the newest jsdom the given Node runtime can actually load. ok is false
// only when no current line supports it at all, which the caller turns into a declined profile.
//
// An empty or unparseable nodeVersion means the runtime probe failed; that falls back to the
// widest-support line rather than the newest, because the two errors are not symmetric: a too-new
// jsdom aborts the run at the smoke gate, while an older one still hosts generated tests.
func jsdomVersionForNode(nodeVersion string) (version string, ok bool) {
	major, minor, patch := nodeSemver(nodeVersion)
	if major == 0 {
		return VersionJsdom26, true
	}
	for _, l := range jsdomLines {
		if l.Supported(major, minor, patch) {
			return l.Version, true
		}
	}
	return "", false
}

// Vue and Svelte component-testing libraries.
//
// @vue/test-utils is hard-split by Vue major: the 2 line declares peer vue 3.x, the 1 line declares
// 2.x. @testing-library/svelte 5 declares svelte ^3 || ^4 || ^5, so one pin covers every current
// Svelte line.
const (
	VersionVueTestUtils         = "2.4.11" // Vue 3
	VersionVueTestUtilsLegacy   = "1.3.6"  // Vue 2
	VersionTestingLibrarySvelte = "5.4.2"
)

// React Testing Library tracks the React major: the 16 line declares react ^18 || ^19, the 12 line
// declares react <18. Installing 16 beside React 17 fails peer resolution.
const (
	VersionTestingLibraryReact       = "16.3.2"
	VersionTestingLibraryReactLegacy = "12.1.5"
	VersionTestingLibraryJestDom     = "7.0.1"
	// user-event is what the React generation hint (reactTSXUnitTestHint) tells the model to reach
	// for, so the profile must install it: run api-72dad6bb281cacee338f43c48432a780 lost two whole
	// suites to `Failed to resolve import "@testing-library/user-event"`. Its only peer is
	// @testing-library/dom >=7.21.4, which both RTL lines already bring in (16 as a peer that npm
	// auto-installs, 12 as a dependency). Latest 14.x on npm as of 2026-09-03.
	VersionTestingLibraryUserEvent = "14.6.7"
)

// jestPresetAngularForMajor maps an Angular major to a jest-preset-angular release, plus the Jest
// major that release requires.
//
// It deliberately does NOT pick the newest preset. @angular-devkit/build-angular — present in every
// Angular CLI project — declares a peerOptional on Jest, and through Angular 19 that peer is
// `^29.5.0` only. Installing jest-preset-angular 15/16 there pulls Jest 30 and npm fails the whole
// install with ERESOLVE, which is exactly what an Angular 19 fixture did before this mapping was
// derived from the registry:
//
//	build-angular 16.x-19.x  jest ^29.5.0
//	build-angular 20.x       jest ^29.5.0 || ^30.2.0
//	build-angular 21.x       jest ^30.2.0
//
// So the Jest 29 preset line (14.6.2, @angular/core >=15 <21) is used for as long as it covers the
// Angular major, and only Angular 21+ moves to the Jest 30 line.
func jestPresetAngularForMajor(ngMajor int) (preset string, jestMajor int) {
	switch {
	case ngMajor >= 21:
		return "17.0.0", 30
	case ngMajor >= 15:
		return "14.6.2", 29
	default:
		// Older Angular is outside every current preset's peer range; the caller declines instead.
		return "", 0
	}
}

// nestTestingForMajor maps a @nestjs/core major to the matching @nestjs/testing release. They are
// released together and a mismatched pair fails at module-resolution time.
func nestTestingForMajor(major int) string {
	switch major {
	case 8:
		return "8.4.7"
	case 9:
		return "9.4.3"
	case 10:
		return "10.4.22"
	case 11:
		return "11.2.1"
	default:
		if major > 11 {
			return strconv.Itoa(major) + ".0.0"
		}
		return ""
	}
}

// Pinned standalone Java test libraries. These are deliberately NOT framework-coupled: Mockito and
// AssertJ ship independently of Spring Boot / Quarkus / Micronaut, so pinning them is safe where
// pinning spring-test or quarkus-junit5 would silently mismatch the app's framework major.
const (
	// VersionMockito requires Java 11+.
	VersionMockito = "5.14.2"
	// VersionMockitoJava8 is the last Mockito line that loads on Java 8; Mockito 5 fails there with
	// an UnsupportedClassVersionError that looks nothing like a dependency problem.
	VersionMockitoJava8 = "4.11.0"
	VersionAssertJ      = "3.26.3"
)

// Microsoft.Playwright NuGet (e2e_framework_bootstrap for .NET). Keep aligned with runner.DefaultPlaywrightDotnetDockerImage tag.
const VersionMicrosoftPlaywrightNuGet = "1.49.0"

// Pinned .NET test packages for C# bootstrap (SDK-style csproj).
const (
	VersionDotNetTestSDK = "17.12.0"
	VersionXunit         = "2.9.2"
	VersionXunitRunnerVS = "2.8.2"
	// NUnit / MSTest pins are fallbacks: those branches are only reached on repos that already use
	// them, so the framework packages are normally present and only the adapter can be missing.
	VersionNUnit             = "4.6.1"
	VersionNUnitTestAdapter  = "6.2.0"
	VersionMSTestFramework   = "4.3.3"
	VersionMSTestTestAdapter = "4.3.3"
)

// Standalone .NET test libraries — not coupled to the app's TargetFramework, so pinning is safe.
const (
	// VersionMoq is BSD-3-Clause. Keep at or above 4.20.2: 4.20.0/4.20.1 shipped SponsorLink, which
	// harvested developer email addresses at build time.
	VersionMoq = "4.20.72"
	// VersionFluentAssertions MUST STAY ON THE 7.x LINE.
	//
	// 7.2.2 declares `<license type="expression">Apache-2.0</license>`. FluentAssertions 8.0.0
	// switched to a custom licence file (Xceed) that requires a PAID licence for commercial use.
	// ASQS writes this PackageReference into customer repositories, so bumping this to 8.x would
	// impose a commercial licensing obligation on every client the bootstrap touches.
	VersionFluentAssertions = "7.2.2"
)

// csharpFrameworkCoupledVersions maps a .NET major to the matching version of packages that ship in
// lockstep with the runtime. Microsoft.AspNetCore.Mvc.Testing 10.x simply cannot be referenced from
// a net8.0 project, so these are chosen from the TargetFramework rather than pinned globally.
var csharpFrameworkCoupledVersions = map[int]string{
	6:  "6.0.36",
	7:  "7.0.20",
	8:  "8.0.30",
	9:  "9.0.19",
	10: "10.0.11",
}
