package testbootstrap

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/javaproj"
)

// JavaFramework is the application framework a Java module is built on.
//
// Bootstrap needs this because "the project has JUnit" is not the same question as "a generated
// test can compile here". A Spring Boot app whose POM carries only junit-jupiter cannot host the
// @SpringBootTest / Mockito / AssertJ style that generation reaches for, and the resulting compile
// errors live in the build manifest — which the fix loop is never allowed to write (see
// evaluator.fixOutputPathAllowed). Detecting the framework is what turns that terminal condition
// into a dependency this step can add before generation starts.
type JavaFramework string

const (
	// JavaFrameworkPlain covers libraries and applications with no framework-specific test support:
	// plain JUnit 5 plus the mock and assertion libraries generation uses regardless.
	JavaFrameworkPlain JavaFramework = "plain"
	// JavaFrameworkSpringBoot is detected from spring-boot-starter-parent, the spring-boot-dependencies
	// BOM, or the Gradle plugin.
	JavaFrameworkSpringBoot JavaFramework = "spring-boot"
	// JavaFrameworkQuarkus is detected from the io.quarkus plugin/BOM.
	JavaFrameworkQuarkus JavaFramework = "quarkus"
	// JavaFrameworkMicronaut is detected from the io.micronaut plugin/BOM or micronaut-parent.
	JavaFrameworkMicronaut JavaFramework = "micronaut"
	// JavaFrameworkAndroid is detected only so bootstrap can decline. Android unit tests need
	// `testOptions { unitTests.all { useJUnitPlatform() } }` in the module build file; adding
	// junit-jupiter without it yields a suite that silently runs zero tests, which is worse than
	// not bootstrapping at all.
	JavaFrameworkAndroid JavaFramework = "android"
)

// javaDep is one coordinate the profile requires on the test classpath.
type javaDep struct {
	GroupID    string
	ArtifactID string
	// Version empty means "let the framework's parent POM or BOM decide". Pinning a
	// framework-coupled artifact is how a Boot 3.2 app ends up compiling against Spring Test 6.0,
	// so only standalone libraries (Mockito, AssertJ) ever carry a version here.
	Version string
	// GradleOnly marks deps Maven gets for free from Surefire (junit-platform-launcher).
	GradleOnly bool
	// RuntimeOnly selects testRuntimeOnly over testImplementation in Gradle.
	RuntimeOnly bool
}

func (d javaDep) coord() string {
	if d.GroupID == "" {
		return d.ArtifactID
	}
	return d.GroupID + ":" + d.ArtifactID
}

func (d javaDep) coordWithVersion() string {
	if d.Version == "" {
		return d.coord() + " (version managed by the framework BOM/parent)"
	}
	return d.coord() + ":" + d.Version
}

// javaFrameworkSmoke identifies the framework-representative smoke test to run after the unit stack
// is proven. Empty means the profile has no integration entrypoint worth probing.
type javaFrameworkSmoke string

const (
	javaSmokeNone       javaFrameworkSmoke = ""
	javaSmokeSpringBoot javaFrameworkSmoke = "spring-boot"
	javaSmokeQuarkus    javaFrameworkSmoke = "quarkus"
	javaSmokeMicronaut  javaFrameworkSmoke = "micronaut"
)

// javaTestProfile is the full answer to "what does this module need in order to host a generated
// test", derived from the detected framework rather than from a substring search for "junit".
type javaTestProfile struct {
	Framework        JavaFramework
	FrameworkVersion string
	// VersionManaged reports whether a parent POM or BOM supplies versions for the framework's own
	// test artifacts.
	VersionManaged bool
	// Evidence is the concrete signal detection fired on, for the audit trail.
	Evidence string
	// Stack is a short label for audit payloads (mirrors the existing "junit5" / "xunit" values).
	Stack string
	// Deps is the complete required test classpath for this framework.
	Deps []javaDep
	// NeedsSurefirePlugin is false when a framework parent already configures Surefire; injecting a
	// second, differently-versioned plugin there is churn with a real chance of conflict.
	NeedsSurefirePlugin bool
	// FrameworkSmoke selects the integration smoke test, run after the mandatory unit smoke.
	FrameworkSmoke javaFrameworkSmoke
	// FrameworkSmokeRequired distinguishes confidence. For Spring Boot the dependency set is exact,
	// so a smoke test that will not COMPILE means the module cannot host generated tests and the run
	// should stop here. For Quarkus and Micronaut the annotation-processor wiring is beyond what a
	// manifest patcher can safely infer, so a compile failure downgrades to unit-only instead of
	// failing a run that would otherwise still produce useful unit tests.
	FrameworkSmokeRequired bool
	// Declined is set when bootstrap must not touch this module at all.
	Declined       bool
	DeclinedReason string
}

// javaMockitoVersion picks a Mockito line the module's Java level can actually load. Mockito 5
// requires Java 11; on Java 8 it fails at class-load time with an UnsupportedClassVersionError that
// looks nothing like a dependency problem.
func javaMockitoVersion(javaVersion string) string {
	if javaMajor(javaVersion) > 0 && javaMajor(javaVersion) < 11 {
		return VersionMockitoJava8
	}
	return VersionMockito
}

// javaMajor parses "17", "1.8", "21.0.1" into a major version number; 0 when unknown.
func javaMajor(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if strings.HasPrefix(v, "1.") {
		v = v[2:]
	}
	if i := strings.IndexAny(v, ".-_"); i > 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// mockitoDeps returns the mock stack for profiles whose framework does not already bundle one.
func mockitoDeps(javaVersion string) []javaDep {
	v := javaMockitoVersion(javaVersion)
	return []javaDep{
		{GroupID: "org.mockito", ArtifactID: "mockito-core", Version: v},
		{GroupID: "org.mockito", ArtifactID: "mockito-junit-jupiter", Version: v},
	}
}

func assertJDep() javaDep {
	return javaDep{GroupID: "org.assertj", ArtifactID: "assertj-core", Version: VersionAssertJ}
}

func junit5Deps() []javaDep {
	return []javaDep{
		{GroupID: "org.junit.jupiter", ArtifactID: "junit-jupiter", Version: VersionJUnitJupiter},
		{GroupID: "org.junit.platform", ArtifactID: "junit-platform-launcher", Version: VersionJUnitPlatform, GradleOnly: true, RuntimeOnly: true},
	}
}

// buildJavaTestProfile turns a framework detection into the required test stack.
func buildJavaTestProfile(det javaFrameworkDetection, javaVersion string) javaTestProfile {
	p := javaTestProfile{
		Framework:        det.Framework,
		FrameworkVersion: det.Version,
		VersionManaged:   det.VersionManaged,
		Evidence:         det.Evidence,
	}

	switch det.Framework {
	case JavaFrameworkAndroid:
		p.Declined = true
		p.DeclinedReason = "Android modules need `testOptions { unitTests.all { useJUnitPlatform() } }` for JUnit 5 to run at all; " +
			"adding dependencies without it produces a suite that silently executes zero tests. Configure the Android test options manually."
		p.Stack = "android-declined"
		return p

	case JavaFrameworkSpringBoot:
		// spring-boot-starter-test is the whole stack: JUnit 5, Mockito, AssertJ, Hamcrest,
		// JSONassert, JsonPath, spring-test and spring-boot-test. Adding junit-jupiter alongside it
		// only creates a chance of two Jupiter versions on one classpath.
		dep := javaDep{GroupID: "org.springframework.boot", ArtifactID: "spring-boot-starter-test"}
		if !det.VersionManaged {
			dep.Version = det.Version
		}
		p.Deps = []javaDep{dep}
		p.Stack = "spring-boot-test"
		p.NeedsSurefirePlugin = !det.VersionManaged
		p.FrameworkSmoke = javaSmokeSpringBoot
		p.FrameworkSmokeRequired = true

	case JavaFrameworkQuarkus:
		p.Deps = []javaDep{
			{GroupID: "io.quarkus", ArtifactID: "quarkus-junit5", Version: quarkusPin(det)},
			{GroupID: "io.quarkus", ArtifactID: "quarkus-junit5-mockito", Version: quarkusPin(det)},
			assertJDep(),
		}
		p.Stack = "quarkus-junit5"
		p.NeedsSurefirePlugin = false
		p.FrameworkSmoke = javaSmokeQuarkus
		p.FrameworkSmokeRequired = false

	case JavaFrameworkMicronaut:
		p.Deps = append([]javaDep{
			{GroupID: "io.micronaut.test", ArtifactID: "micronaut-test-junit5", Version: micronautPin(det)},
		}, mockitoDeps(javaVersion)...)
		p.Deps = append(p.Deps, assertJDep())
		p.Stack = "micronaut-test-junit5"
		p.NeedsSurefirePlugin = false
		p.FrameworkSmoke = javaSmokeMicronaut
		p.FrameworkSmokeRequired = false

	default:
		// Plain Java still gets Mockito and AssertJ. The audit.log post-mortem this profile exists
		// for shows generation reaching for both on a module that had neither — twenty rejected
		// candidates, nine written anyway, none repairable.
		p.Framework = JavaFrameworkPlain
		p.Deps = append(junit5Deps(), mockitoDeps(javaVersion)...)
		p.Deps = append(p.Deps, assertJDep())
		p.Stack = "junit5-mockito-assertj"
		p.NeedsSurefirePlugin = true
		p.FrameworkSmoke = javaSmokeNone
	}
	return p
}

// quarkusPin / micronautPin return "" whenever a BOM is in play, so the platform decides.
func quarkusPin(det javaFrameworkDetection) string {
	if det.VersionManaged {
		return ""
	}
	return det.Version
}

func micronautPin(det javaFrameworkDetection) string {
	if det.VersionManaged {
		return ""
	}
	return det.Version
}

// mavenDeps returns the deps that belong in a pom.xml.
func (p javaTestProfile) mavenDeps() []javaDep {
	out := make([]javaDep, 0, len(p.Deps))
	for _, d := range p.Deps {
		if d.GradleOnly {
			continue
		}
		out = append(out, d)
	}
	return out
}

// missingDeps returns the profile deps whose artifactId does not already appear in the build file.
//
// Substring matching is deliberate and sufficient: the question is "would this coordinate resolve",
// and an artifactId mentioned anywhere in the manifest (direct, dependencyManagement, or a BOM line)
// answers yes. It is the *set* that changed — asking about each required artifact instead of asking
// whether the word "junit" appears anywhere.
func (p javaTestProfile) missingDeps(buildSrc string, maven bool) []javaDep {
	hay := strings.ToLower(buildSrc)
	deps := p.Deps
	if maven {
		deps = p.mavenDeps()
	}
	var out []javaDep
	for _, d := range deps {
		if strings.Contains(hay, strings.ToLower(d.ArtifactID)) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// describeDeps renders coordinates for audit payloads.
func describeJavaDeps(deps []javaDep) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.coordWithVersion())
	}
	return out
}

// javaFrameworkDetection is the raw detection result before it becomes a profile.
type javaFrameworkDetection struct {
	Framework      JavaFramework
	Version        string
	VersionManaged bool
	Evidence       string
}

// detectJavaFrameworkMaven classifies a pom.xml.
func detectJavaFrameworkMaven(pom string) javaFrameworkDetection {
	clean := javaproj.StripXMLComments(pom)
	low := strings.ToLower(clean)
	props := javaproj.ParseProperties(clean)
	parent := javaproj.ParseParent(clean)

	if v := javaproj.ParseSpringBootVersion(clean, props); v != "" {
		managed := strings.EqualFold(parent.ArtifactID, "spring-boot-starter-parent") ||
			strings.Contains(low, "spring-boot-dependencies")
		evidence := "spring-boot-dependencies BOM in <dependencyManagement>"
		if strings.EqualFold(parent.ArtifactID, "spring-boot-starter-parent") {
			evidence = "<parent> is spring-boot-starter-parent"
		} else if !managed {
			evidence = "Spring Boot version property"
		}
		return javaFrameworkDetection{Framework: JavaFrameworkSpringBoot, Version: v, VersionManaged: managed, Evidence: evidence}
	}

	if strings.Contains(low, "io.quarkus") {
		managed := strings.Contains(low, "quarkus-bom") || strings.Contains(low, "quarkus-universe-bom") ||
			strings.Contains(low, "quarkus-platform")
		v := firstNonEmpty(
			javaproj.ResolveProperty(props["quarkus.platform.version"], props),
			javaproj.ResolveProperty(props["quarkus.version"], props),
		)
		return javaFrameworkDetection{Framework: JavaFrameworkQuarkus, Version: v, VersionManaged: managed, Evidence: "io.quarkus coordinates in pom.xml"}
	}

	if strings.Contains(low, "io.micronaut") {
		managed := strings.Contains(low, "micronaut-bom") || strings.Contains(low, "micronaut-platform") ||
			strings.EqualFold(parent.ArtifactID, "micronaut-parent")
		v := firstNonEmpty(
			javaproj.ResolveProperty(props["micronaut.version"], props),
			javaproj.ResolveProperty(parent.Version, props),
		)
		return javaFrameworkDetection{Framework: JavaFrameworkMicronaut, Version: v, VersionManaged: managed, Evidence: "io.micronaut coordinates in pom.xml"}
	}

	return javaFrameworkDetection{Framework: JavaFrameworkPlain, Evidence: "no framework parent, BOM or plugin found in pom.xml"}
}

// detectJavaFrameworkGradle classifies a build.gradle(.kts) from its plugin block.
func detectJavaFrameworkGradle(src string) javaFrameworkDetection {
	low := strings.ToLower(src)

	if strings.Contains(low, "com.android.application") || strings.Contains(low, "com.android.library") {
		return javaFrameworkDetection{Framework: JavaFrameworkAndroid, Evidence: "Android Gradle plugin"}
	}
	if v := javaproj.GradleSpringBootVersion(src); v != "" {
		// The Boot Gradle plugin does not manage versions on its own; io.spring.dependency-management
		// or an explicit platform() does. Without one, spring-boot-starter-test needs a version.
		managed := strings.Contains(low, "io.spring.dependency-management") ||
			strings.Contains(low, "spring-boot-dependencies")
		return javaFrameworkDetection{Framework: JavaFrameworkSpringBoot, Version: v, VersionManaged: managed, Evidence: "org.springframework.boot Gradle plugin"}
	}
	if strings.Contains(low, "io.quarkus") {
		return javaFrameworkDetection{Framework: JavaFrameworkQuarkus, Version: gradlePluginVersion(src, "io.quarkus"), VersionManaged: strings.Contains(low, "quarkus-bom") || strings.Contains(low, "quarkus-platform"), Evidence: "io.quarkus Gradle plugin"}
	}
	if strings.Contains(low, "io.micronaut.application") || strings.Contains(low, "io.micronaut.library") {
		return javaFrameworkDetection{Framework: JavaFrameworkMicronaut, VersionManaged: true, Evidence: "io.micronaut Gradle plugin"}
	}
	return javaFrameworkDetection{Framework: JavaFrameworkPlain, Evidence: "no framework plugin found in the Gradle build file"}
}

// gradlePluginVersion extracts `id("x") version "1.2.3"` for the named plugin.
func gradlePluginVersion(src, pluginID string) string {
	idx := strings.Index(src, pluginID)
	if idx < 0 {
		return ""
	}
	rest := src[idx:]
	if nl := strings.IndexByte(rest, '\n'); nl > 0 {
		rest = rest[:nl]
	}
	vi := strings.Index(rest, "version")
	if vi < 0 {
		return ""
	}
	rest = rest[vi:]
	start := strings.IndexAny(rest, `"'`)
	if start < 0 {
		return ""
	}
	rest = rest[start+1:]
	end := strings.IndexAny(rest, `"'`)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// summarizeJavaProfile renders a one-line description for stderr and audit messages.
func summarizeJavaProfile(p javaTestProfile) string {
	v := p.FrameworkVersion
	if v == "" {
		v = "version unknown"
	}
	return fmt.Sprintf("%s (%s) → %s", p.Framework, v, p.Stack)
}
