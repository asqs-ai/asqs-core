package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// permittedDifferences enumerates every way the two sandbox targets are still allowed to disagree.
//
// This map is the progress meter for the whole unification port (CP30–CP35). It starts full and
// every bundle deletes entries from it; when it holds only the structural §1 rows, the targets are
// unified. Two rules keep it honest:
//
//   - An unlisted difference fails the test, so no new divergence can be added silently.
//   - A LISTED difference that no longer occurs also fails the test, so a fixed gap cannot be left
//     rotting in the whitelist and a bundle cannot claim to have closed something it did not.
//
// Keys are "<fixture>/<field>". Values name either a permanent structural row (§1-N, matching the
// upstream unification plan's four rows: 1 toolchain provenance, 2 filesystem isolation, 4
// credential location, 5 machine-global blast radius) or the CP bundle that removes the entry.
var permittedDifferences = map[string]string{
	// PERMITTED (§1 row 5, machine-global blast radius): the Docker target appends
	// `dotnet build-server shutdown` after `dotnet test`. That kills EVERY MSBuild/Roslyn node on
	// the machine — in a container that is the container, on a host it would reach a concurrent
	// run or the operator's IDE. Everything else in these argv is byte-identical since CP31.
	// Permanent: no bundle removes these entries.
	"dotnet-multitarget/Argv[coverage]": "§1-5",
	"dotnet-multitarget/Argv[test]":     "§1-5",
	"dotnet/Argv[coverage]":             "§1-5",
	"dotnet/Argv[test]":                 "§1-5",

	// CP32 — build-tool resolution honoured on both targets; wrapper-free argv (U3/U3b). The
	// forced-gradle restore row is the build_tool half: Docker restores from the Maven profile
	// while local obeys build_tool and restores with Gradle.
	"gradle/Argv[compile]":                               "CP32",
	"gradle/Argv[coverage]":                              "CP32",
	"gradle/Argv[test]":                                  "CP32",
	"java-both-build-files-forced-gradle/Argv[compile]":  "CP32",
	"java-both-build-files-forced-gradle/Argv[coverage]": "CP32",
	"java-both-build-files-forced-gradle/Argv[test]":     "CP32",
	"java-both-build-files-forced-gradle/Restore":        "CP32",
	"java-both-build-files-forced-gradle/Toolchain":      "CP32",

	// CP33 — step environment parity: Docker sets CI=true (+ .NET hygiene vars) on every step,
	// local adds CI=true only on test/coverage and nothing on compile (U5).
	"dotnet-multitarget/Env[compile]":                   "CP33",
	"dotnet-multitarget/Env[coverage]":                  "CP33",
	"dotnet-multitarget/Env[test]":                      "CP33",
	"dotnet-overrides/Env[compile]":                     "CP33",
	"dotnet-overrides/Env[coverage]":                    "CP33",
	"dotnet-overrides/Env[test]":                        "CP33",
	"dotnet/Env[compile]":                               "CP33",
	"dotnet/Env[coverage]":                              "CP33",
	"dotnet/Env[test]":                                  "CP33",
	"gradle/Env[compile]":                               "CP33",
	"gradle/Env[coverage]":                              "CP33",
	"gradle/Env[test]":                                  "CP33",
	"java-both-build-files-forced-gradle/Env[compile]":  "CP33",
	"java-both-build-files-forced-gradle/Env[coverage]": "CP33",
	"java-both-build-files-forced-gradle/Env[test]":     "CP33",
	"java-no-jacoco/Env[compile]":                       "CP33",
	"java-no-jacoco/Env[coverage]":                      "CP33",
	"java-no-jacoco/Env[test]":                          "CP33",
	"js-build-runs-start/Env[compile]":                  "CP33",
	"js-nest-no-build/Env[compile]":                     "CP33",
	"js-no-package-json/Env[compile]":                   "CP33",
	"js-no-test-script/Env[compile]":                    "CP33",
	"js-test-coverage-only/Env[compile]":                "CP33",
	"maven-overrides/Env[compile]":                      "CP33",
	"maven-overrides/Env[coverage]":                     "CP33",
	"maven-overrides/Env[test]":                         "CP33",
	"maven/Env[compile]":                                "CP33",
	"maven/Env[coverage]":                               "CP33",
	"maven/Env[test]":                                   "CP33",
	"mono-repo-subpath/Env[compile]":                    "CP33",
	"mono-repo-subpath/Env[coverage]":                   "CP33",
	"mono-repo-subpath/Env[test]":                       "CP33",
	"npm/Env[compile]":                                  "CP33",
	"pnpm/Env[compile]":                                 "CP33",
	"yarn/Env[compile]":                                 "CP33",
}

// planParityFixture is one repository shape, planned for both targets from one Sandbox config.
type planParityFixture struct {
	name  string
	lang  string
	files map[string]string
	exec  map[string]bool
	// mutate adjusts the shared Sandbox config before planning (overrides, mono-repo subpath).
	mutate func(*Sandbox)
}

const jacocoPom = `<project><build><plugins><plugin>
  <groupId>org.jacoco</groupId><artifactId>jacoco-maven-plugin</artifactId>
</plugin></plugins></build></project>`

func planParityFixtures() []planParityFixture {
	const csproj = `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`
	const multiTargetCsproj = `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFrameworks>net8.0;net9.0</TargetFrameworks></PropertyGroup></Project>`
	const pkgJSON = `{"scripts":{"build":"tsc","test":"jest","coverage":"jest --coverage"}}`

	// Upstream's matrix additionally carries two private-registry fixtures (a generated Maven
	// settings.xml and a NuGet credential envelope). Core's credential seam is inert until CP33
	// ports it compile-only, so those fixtures arrive with CP33.
	return []planParityFixture{
		{name: "maven", lang: "java", files: map[string]string{"pom.xml": jacocoPom}},
		{
			// A repo carrying both build files, with build_tool forcing the non-default one. This
			// is the only shape that reveals whether the Docker target honours runner.build_tool
			// at all: with "auto" both targets independently prefer Maven and agree by accident.
			name:   "java-both-build-files-forced-gradle",
			lang:   "java",
			files:  map[string]string{"pom.xml": jacocoPom, "build.gradle": "plugins { id 'jacoco' }"},
			mutate: func(s *Sandbox) { s.BuildTool = "gradle" },
		},
		{name: "gradle", lang: "java", files: map[string]string{"build.gradle": "plugins { id 'jacoco' }"}},
		{name: "npm", lang: "typescript", files: map[string]string{"package.json": pkgJSON}},
		{name: "pnpm", lang: "typescript", files: map[string]string{"package.json": pkgJSON, "pnpm-lock.yaml": "lockfileVersion: '9.0'"}},
		{name: "yarn", lang: "typescript", files: map[string]string{"package.json": pkgJSON, "yarn.lock": "# yarn lockfile v1"}},
		{name: "dotnet", lang: "csharp", files: map[string]string{"App.csproj": csproj}},
		{name: "dotnet-multitarget", lang: "csharp", files: map[string]string{"App.csproj": multiTargetCsproj}},
		{
			// Overrides take completely different routes on the two targets today, which makes this
			// the highest-value row in the matrix.
			name:  "maven-overrides",
			lang:  "java",
			files: map[string]string{"pom.xml": jacocoPom},
			mutate: func(s *Sandbox) {
				s.CompileCommand = "mvn -q -B -DskipTests test-compile"
				s.TestCommand = "mvn -q -B test"
			},
		},
		{
			name:  "dotnet-overrides",
			lang:  "csharp",
			files: map[string]string{"App.csproj": csproj},
			mutate: func(s *Sandbox) {
				s.CompileCommand = "dotnet build -c Release"
				s.TestCommand = "dotnet test -c Release --no-build"
			},
		},
		{
			// A pom that never declares JaCoCo. Local skips nothing here (it runs the plain test
			// goal for coverage); Docker appends jacoco goals unconditionally.
			name:  "java-no-jacoco",
			lang:  "java",
			files: map[string]string{"pom.xml": "<project/>"},
		},
		{
			// Nothing to run should be a skip. This shape produces different answers per target
			// and per package manager today.
			name:  "js-no-test-script",
			lang:  "typescript",
			files: map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`},
		},
		{
			// The Vitest layout: only `test:coverage`. Package managers disagree about which
			// script name they look for.
			name:  "js-test-coverage-only",
			lang:  "typescript",
			files: map[string]string{"package.json": `{"scripts":{"build":"tsc","test":"vitest run","test:coverage":"vitest run --coverage"}}`},
		},
		{
			// NestJS with no build script: local falls back to `npx nest build`, Docker runs the
			// profile's npm script.
			name:  "js-nest-no-build",
			lang:  "typescript",
			files: map[string]string{"package.json": `{"dependencies":{"@nestjs/core":"10"},"scripts":{"test":"jest"}}`},
		},
		{
			// angular-seed shape: "build" runs start, which triggers prestart, which runs npm
			// install. Compiling is not what would happen, so local substitutes a no-op.
			name:  "js-build-runs-start",
			lang:  "typescript",
			files: map[string]string{"package.json": `{"scripts":{"build":"npm run start","start":"ng serve","test":"jest"}}`},
		},
		{
			// A JS-tagged repo with no package.json at all.
			name:  "js-no-package-json",
			lang:  "typescript",
			files: map[string]string{"README.md": "x"},
		},
		{
			// Mono-repo: local pre-resolves the cwd, docker mounts the git root and sets -w. The
			// most likely place for a silent divergence to reappear.
			name:   "mono-repo-subpath",
			lang:   "java",
			files:  map[string]string{"services/api/pom.xml": jacocoPom},
			mutate: func(s *Sandbox) { s.EvalWorkSubpath = "services/api" },
		},
	}
}

// stubToolsOnPATH puts executable stubs for the named binaries at the front of PATH so the argv
// tests do not depend on whether this machine happens to have Maven or Gradle installed.
func stubToolsOnPATH(t *testing.T, names ...string) {
	t.Helper()
	bin := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestStepPlanParity(t *testing.T) {
	stubToolsOnPATH(t, "mvn", "gradle", "npm", "pnpm", "yarn", "dotnet", "node")

	used := map[string]bool{}
	for _, fx := range planParityFixtures() {
		t.Run(fx.name, func(t *testing.T) {
			repo := writeRepoTree(t, fx.files, fx.exec)

			base := &Sandbox{Timeout: "30s"}
			if fx.mutate != nil {
				fx.mutate(base)
			}
			local := base.clone()
			local.Type = string(TargetLocal)
			docker := base.clone()
			docker.Type = string(TargetDocker)

			lp, err := local.buildStepPlan(repo, fx.lang, "")
			if err != nil {
				t.Fatalf("local plan: %v", err)
			}
			dp, err := docker.buildStepPlan(repo, fx.lang, "")
			if err != nil {
				t.Fatalf("docker plan: %v", err)
			}

			for _, d := range diffPlans(fx.name, lp, dp) {
				if _, ok := permittedDifferences[d.key]; ok {
					used[d.key] = true
					continue
				}
				t.Errorf("unpermitted divergence %s\n  local  %s\n  docker %s", d.key, d.local, d.docker)
			}
		})
	}

	// A whitelist entry that no longer fires is a lie about the current state of the code.
	var stale []string
	for key := range permittedDifferences {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("permittedDifferences has %d stale entr%s — these no longer differ and must be deleted:\n  %s",
			len(stale), plural(len(stale)), strings.Join(stale, "\n  "))
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

type planDiff struct {
	key           string
	local, docker string
}

// diffPlans compares the two plans field by field. Image is never compared: the toolchain and its
// version come from the image tag under Docker and from the host PATH under local, which is the
// first of the permitted §1 differences and is structural, not a gap to close.
func diffPlans(fixture string, lp, dp StepPlan) []planDiff {
	var out []planDiff
	add := func(field, l, d string) {
		if l != d {
			out = append(out, planDiff{key: fixture + "/" + field, local: l, docker: d})
		}
	}

	add("Toolchain", string(lp.Toolchain), string(dp.Toolchain))
	add("Restore", strings.Join(lp.Restore, " "), strings.Join(dp.Restore, " "))
	add("CoverageReportPaths", strings.Join(lp.CoverageReportPaths, ","), strings.Join(dp.CoverageReportPaths, ","))

	for _, step := range planSteps {
		add(fmt.Sprintf("Argv[%s]", step),
			strings.Join(lp.ArgvFor(step), " "), strings.Join(dp.ArgvFor(step), " "))
		add(fmt.Sprintf("Env[%s]", step),
			strings.Join(lp.EnvFor(step), " "), strings.Join(dp.EnvFor(step), " "))
		add(fmt.Sprintf("Decision[%s]", step),
			describeDecision(lp.DecisionFor(step)), describeDecision(dp.DecisionFor(step)))
	}
	return out
}

func describeDecision(d StepDecision) string {
	if d.Action == ActionRun {
		return string(d.Action)
	}
	return string(d.Action) + ": " + d.Reason
}

// writeRepoTree writes a fixture repository, supporting nested paths for the mono-repo fixture.
func writeRepoTree(t *testing.T, files map[string]string, exec map[string]bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeNested(t, dir, name, body, exec[name])
	}
	return dir
}

func writeNested(t *testing.T, root, rel, body string, executable bool) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if err := os.WriteFile(full, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// Every whitelist row must be attributable: either to a permanent §1 structural difference, or to
// the open CP bundle (CP31–CP35) that removes it. CP35 closes the whitelist and tightens this test
// to §1-only — the upstream end-state form — at which point "the two targets are unified" becomes
// a fact about the code rather than a claim in a document.
func TestPermittedDifferences_containOnlyStructuralRows(t *testing.T) {
	if len(permittedDifferences) == 0 {
		t.Fatal("the whitelist is empty; CP30 lands with core's local/docker disagreements enumerated, and the §1 structural rows cannot vanish even at the end state")
	}
	validOwner := func(owner string) bool {
		if strings.HasPrefix(owner, "§1-") {
			return true
		}
		switch owner {
		case "CP31", "CP32", "CP33", "CP34", "CP35":
			return true
		}
		return false
	}
	for key, owner := range permittedDifferences {
		if !validOwner(owner) {
			t.Errorf("%s is attributed to %q; every difference must name a §1 structural row or the CP31–CP35 bundle that removes it", key, owner)
		}
	}
}
