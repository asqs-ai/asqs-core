package testbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// JSFramework is the application framework a JS/TS package is built on.
//
// Same reasoning as the Java and C# profiles: "the package has a test runner" is not the same
// question as "a generated test can run". A React package with plain Jest gets `testEnvironment:
// 'node'`, so every component test dies on `document is not defined`; an Angular package with plain
// ts-jest cannot compile a single component; a NestJS package without @nestjs/testing cannot call
// Test.createTestingModule. None of those are repairable from a test file.
type JSFramework string

const (
	JSFrameworkPlain   JSFramework = "plain"
	JSFrameworkNodeESM JSFramework = "node-esm"
	JSFrameworkReact   JSFramework = "react"
	JSFrameworkVue     JSFramework = "vue"
	JSFrameworkSvelte  JSFramework = "svelte"
	JSFrameworkAngular JSFramework = "angular"
	JSFrameworkNest    JSFramework = "nestjs"
)

// JSRunner is the test runner the profile installs.
type JSRunner string

const (
	JSRunnerJest   JSRunner = "jest"
	JSRunnerVitest JSRunner = "vitest"
)

// jsDep is one devDependency the profile requires.
type jsDep struct {
	Name    string
	Version string
}

func (d jsDep) String() string { return d.Name + "@" + d.Version }

// jsFrameworkSmoke selects the framework-representative smoke test.
type jsFrameworkSmoke string

const (
	jsSmokeNone    jsFrameworkSmoke = ""
	jsSmokeReact   jsFrameworkSmoke = "react"
	jsSmokeVue     jsFrameworkSmoke = "vue"
	jsSmokeSvelte  jsFrameworkSmoke = "svelte"
	jsSmokeAngular jsFrameworkSmoke = "angular"
	jsSmokeNest    jsFrameworkSmoke = "nestjs"
)

// jsTestProfile is the complete answer to "what does this package need to host a generated test".
type jsTestProfile struct {
	Framework        JSFramework
	FrameworkVersion string
	Runner           JSRunner
	IsTS             bool
	IsESM            bool
	ViteMajor        int
	Evidence         string
	Stack            string
	Deps             []jsDep

	// TestEnvironment is 'node' or 'jsdom'. Getting this wrong is the single most common reason a
	// generated component test fails: the default is 'node', where there is no document.
	TestEnvironment string
	// ConfigFile is the runner config bootstrap writes (jest.config.cjs / vitest.config.ts).
	ConfigFile string
	// SetupFile is the per-runner setup module, when the framework needs one.
	SetupFile string
	// ResolveConditions is emitted as Vite's resolve.conditions in the runner config.
	//
	// Svelte needs ["browser"]: its package exports map resolves to the SERVER build under Vitest's
	// default conditions, and render() then throws "mount(...) is not available on the server" even
	// though the jsdom environment is correct. It lives only in the generated Vitest config, so the
	// app's own `vite build` is unaffected.
	ResolveConditions []string
	// TestScript is the package.json script bootstrap adds. It is deliberately NOT "test": the
	// previous behaviour replaced whatever the repo already had there.
	TestScript string

	FrameworkSmoke         jsFrameworkSmoke
	FrameworkSmokeRequired bool

	Declined       bool
	DeclinedReason string
}

const (
	// jsAsqsTestScript is where bootstrap puts its runner invocation. `npm test` is the repo's, not
	// ours: overwriting it destroyed `ng test` on Angular repos and any custom harness elsewhere.
	jsAsqsTestScript = "test:asqs"
	// jsStackJsdomDeclined marks a profile declined for the runtime rather than for the repo.
	jsStackJsdomDeclined = "jsdom-declined"
)

// semverMajor extracts the leading major from a package.json range ("^19.2.0" → 19). 0 when unknown.
func semverMajor(v string) int {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "^~>=<v ")
	if i := strings.IndexAny(v, ". -+|"); i > 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// nodeSemver splits a Node runtime version ("v20.20.2", "22.23.2") into major, minor and patch.
// Zeros when unknown — every caller treats that as "runtime not determined".
func nodeSemver(v string) (major, minor, patch int) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+ "); i > 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	at := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return at(0), at(1), at(2)
}

// jsPackageJSON is the subset of package.json detection needs.
type jsPackageJSON struct {
	Type            string            `json:"type"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p jsPackageJSON) dep(name string) (string, bool) {
	if v, ok := p.Dependencies[name]; ok {
		return v, true
	}
	v, ok := p.DevDependencies[name]
	return v, ok
}

func readJSPackageJSON(pkgDir string) (jsPackageJSON, error) {
	b, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return jsPackageJSON{}, err
	}
	var p jsPackageJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return jsPackageJSON{}, fmt.Errorf("package.json: %w", err)
	}
	return p, nil
}

// jsFrameworkDetection is the raw detection result before it becomes a profile.
type jsFrameworkDetection struct {
	Framework        JSFramework
	FrameworkVersion string
	FrameworkMajor   int
	IsTS             bool
	IsESM            bool
	ViteMajor        int
	// BrowserLike reports whether the package renders to a DOM. It decides jsdom vs node for Vite
	// projects that are not one of the frameworks handled explicitly — a Vue or Svelte app needs a
	// document just as much as a React one does.
	BrowserLike bool
	Evidence    string
}

// jsUILibraries are packages whose presence means the code under test touches the DOM.
var jsUILibraries = []string{
	"react", "react-dom", "vue", "svelte", "solid-js", "preact", "lit", "@lit/reactive-element",
	"alpinejs", "@stencil/core",
}

// detectJSFramework classifies one package directory.
func detectJSFramework(pkgDir string, lang string) (jsFrameworkDetection, error) {
	pkg, err := readJSPackageJSON(pkgDir)
	if err != nil {
		return jsFrameworkDetection{}, err
	}
	det := jsFrameworkDetection{
		Framework: JSFrameworkPlain,
		IsESM:     strings.EqualFold(strings.TrimSpace(pkg.Type), "module"),
		IsTS:      detectJestBootstrapIsTS(pkgDir, lang),
		Evidence:  "no UI or server framework found in package.json",
	}
	for _, name := range []string{"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs"} {
		if fileExists(filepath.Join(pkgDir, name)) {
			if v, ok := pkg.dep("vite"); ok {
				det.ViteMajor = semverMajor(v)
			}
			if det.ViteMajor == 0 {
				det.ViteMajor = -1 // config present, version unreadable
			}
			break
		}
	}
	for _, ui := range jsUILibraries {
		if _, ok := pkg.dep(ui); ok {
			det.BrowserLike = true
			break
		}
	}
	// A Vite app is defined by its index.html entry point; a package that has one renders to a DOM
	// even when its UI library is not on the list above.
	if !det.BrowserLike && fileExists(filepath.Join(pkgDir, "index.html")) {
		det.BrowserLike = true
	}

	if v, ok := pkg.dep("@angular/core"); ok {
		det.Framework = JSFrameworkAngular
		det.FrameworkVersion, det.FrameworkMajor = v, semverMajor(v)
		det.Evidence = "@angular/core " + v
		return det, nil
	}
	if v, ok := pkg.dep("@nestjs/core"); ok {
		det.Framework = JSFrameworkNest
		det.FrameworkVersion, det.FrameworkMajor = v, semverMajor(v)
		det.Evidence = "@nestjs/core " + v
		return det, nil
	}
	if v, ok := pkg.dep("react"); ok {
		det.Framework = JSFrameworkReact
		det.FrameworkVersion, det.FrameworkMajor = v, semverMajor(v)
		det.Evidence = "react " + v
		if det.ViteMajor != 0 {
			det.Evidence += " with Vite"
		}
		return det, nil
	}
	if v, ok := pkg.dep("vue"); ok {
		det.Framework = JSFrameworkVue
		det.FrameworkVersion, det.FrameworkMajor = v, semverMajor(v)
		det.Evidence = "vue " + v
		return det, nil
	}
	if v, ok := pkg.dep("svelte"); ok {
		det.Framework = JSFrameworkSvelte
		det.FrameworkVersion, det.FrameworkMajor = v, semverMajor(v)
		det.Evidence = "svelte " + v
		return det, nil
	}
	if det.IsESM {
		det.Framework = JSFrameworkNodeESM
		det.Evidence = `package.json declares "type": "module"`
	}
	return det, nil
}

// buildJSTestProfile turns a detection into the required stack.
//
// nodeVersion is the Node runtime the install and the generated tests will actually run on — the
// bootstrap container's when there is one, the host's otherwise. It is not a repo fact: only jsdom
// depends on it (see jsdomVersionForNode), and callers that never install anything pass "".
func buildJSTestProfile(det jsFrameworkDetection, nodeVersion string) jsTestProfile {
	// Resolved once, used by every DOM stack below; jsdomOK is false only when the runtime is older
	// than every current jsdom line, which becomes a declined profile after the switch.
	jsdomVer, jsdomOK := jsdomVersionForNode(nodeVersion)

	p := jsTestProfile{
		Framework:        det.Framework,
		FrameworkVersion: det.FrameworkVersion,
		IsTS:             det.IsTS,
		IsESM:            det.IsESM,
		ViteMajor:        det.ViteMajor,
		Evidence:         det.Evidence,
		TestScript:       jsAsqsTestScript,
	}

	// Vite is the strongest signal a JS package gives about how it is built, so it decides the runner
	// for everything except the two frameworks that bring their own answer:
	//
	//   - Angular ships its own builder and jest-preset-angular; a vite.config in an Angular repo does
	//     not change that (Vitest there needs @analogjs/vitest-angular, which bootstrap does not wire).
	//   - NestJS is a CommonJS server framework whose own convention, and whose docs, are Jest.
	//
	// Everything else that builds with Vite gets Vitest, because Vitest reads the repo's own Vite
	// config: JSX/TSX transform, path aliases, CSS handling and import.meta.env all behave exactly as
	// they do in the app build. Under Jest each of those needs a parallel setting that drifts from
	// vite.config on every change.
	viteDecides := det.ViteMajor != 0 && det.Framework != JSFrameworkAngular && det.Framework != JSFrameworkNest

	switch det.Framework {
	case JSFrameworkAngular:
		preset, jestMajor := jestPresetAngularForMajor(det.FrameworkMajor)
		if preset == "" {
			p.Declined = true
			p.DeclinedReason = fmt.Sprintf("Angular %s is older than any current jest-preset-angular peer range (>= 15). "+
				"Configure Karma/Jasmine (`ng test`) or a preset manually.", det.FrameworkVersion)
			p.Stack = "angular-declined"
			return p
		}
		jestVer, typesVer := VersionJest, VersionTypesJest
		if jestMajor == 30 {
			jestVer, typesVer = VersionJest30, VersionTypesJest30
		}
		// jest-preset-angular supplies the transformer and the jsdom environment; adding ts-jest or
		// jest-environment-jsdom beside it invites two transformer versions on one classpath.
		p.Runner = JSRunnerJest
		p.Deps = []jsDep{
			{"jest", jestVer},
			{"jest-preset-angular", preset},
			{"@types/jest", typesVer},
		}
		p.TestEnvironment = "jsdom"
		p.ConfigFile = "jest.config.cjs"
		p.SetupFile = "setup-jest.ts"
		p.Stack = "jest-preset-angular"
		p.FrameworkSmoke = jsSmokeAngular
		p.FrameworkSmokeRequired = false

	case JSFrameworkReact:
		rtl := VersionTestingLibraryReact
		if det.FrameworkMajor > 0 && det.FrameworkMajor < 18 {
			rtl = VersionTestingLibraryReactLegacy
		}
		if viteDecides {
			p.Runner = JSRunnerVitest
			p.Deps = []jsDep{
				{"vitest", vitestVersionForVite(det.ViteMajor)},
				{"jsdom", jsdomVer},
				{"@testing-library/react", rtl},
				{"@testing-library/jest-dom", VersionTestingLibraryJestDom},
				// Required by the generation prompt's React hint; see VersionTestingLibraryUserEvent.
				{"@testing-library/user-event", VersionTestingLibraryUserEvent},
			}
			p.ConfigFile = "vitest.config.ts"
			p.SetupFile = "vitest.setup.ts"
			p.Stack = "vitest-react"
		} else {
			p.Runner = JSRunnerJest
			p.Deps = []jsDep{
				{"jest", VersionJest},
				{"jest-environment-jsdom", VersionJestEnvironmentJsdom},
				{"@types/jest", VersionTypesJest},
				{"@testing-library/react", rtl},
				{"@testing-library/jest-dom", VersionTestingLibraryJestDom},
				// Required by the generation prompt's React hint; see VersionTestingLibraryUserEvent.
				{"@testing-library/user-event", VersionTestingLibraryUserEvent},
			}
			if det.IsTS {
				p.Deps = append(p.Deps, jsDep{"ts-jest", VersionTSJest})
			}
			p.ConfigFile = "jest.config.cjs"
			p.SetupFile = "jest.setup.ts"
			p.Stack = "jest-react"
		}
		p.TestEnvironment = "jsdom"
		p.FrameworkSmoke = jsSmokeReact
		p.FrameworkSmokeRequired = false

	case JSFrameworkVue, JSFrameworkSvelte:
		// Both are component frameworks whose files (.vue / .svelte) only compile through their Vite
		// plugin. Vitest inherits that plugin from the merged vite.config, which is precisely why the
		// runner choice matters here: under Jest each would need a separate transformer.
		p.Runner = JSRunnerVitest
		p.Deps = []jsDep{
			{"vitest", vitestVersionForVite(det.ViteMajor)},
			{"jsdom", jsdomVer},
			{"@testing-library/jest-dom", VersionTestingLibraryJestDom},
		}
		if det.Framework == JSFrameworkVue {
			tu := VersionVueTestUtils
			if det.FrameworkMajor > 0 && det.FrameworkMajor < 3 {
				tu = VersionVueTestUtilsLegacy
			}
			p.Deps = append(p.Deps, jsDep{"@vue/test-utils", tu})
			p.Stack = "vitest-vue"
			p.FrameworkSmoke = jsSmokeVue
		} else {
			p.Deps = append(p.Deps, jsDep{"@testing-library/svelte", VersionTestingLibrarySvelte})
			p.Stack = "vitest-svelte"
			p.FrameworkSmoke = jsSmokeSvelte
			p.ResolveConditions = []string{"browser"}
		}
		p.TestEnvironment = "jsdom"
		p.ConfigFile = jsVitestConfigName(det.IsTS)
		p.SetupFile = "vitest.setup.ts"
		p.FrameworkSmokeRequired = false

	case JSFrameworkNest:
		testing := nestTestingForMajor(det.FrameworkMajor)
		if testing == "" {
			p.Declined = true
			p.DeclinedReason = fmt.Sprintf("Could not resolve a @nestjs/testing release for @nestjs/core %s.", det.FrameworkVersion)
			p.Stack = "nestjs-declined"
			return p
		}
		p.Runner = JSRunnerJest
		p.Deps = []jsDep{
			{"jest", VersionJest},
			{"ts-jest", VersionTSJest},
			{"@types/jest", VersionTypesJest},
			{"@nestjs/testing", testing},
		}
		p.TestEnvironment = "node"
		p.ConfigFile = "jest.config.cjs"
		p.Stack = "jest-nestjs"
		p.FrameworkSmoke = jsSmokeNest
		p.FrameworkSmokeRequired = false

	default:
		switch {
		case viteDecides:
			// A Vite package with no framework bootstrap special-cases: Vue, Svelte, Solid, Lit, a
			// vanilla TS app, or a Vite library. Vitest either way; the environment follows whether
			// the package renders to a DOM.
			p.Runner = JSRunnerVitest
			p.Deps = []jsDep{{"vitest", vitestVersionForVite(det.ViteMajor)}}
			p.TestEnvironment = "node"
			p.Stack = "vitest-vite"
			if det.BrowserLike {
				p.Deps = append(p.Deps, jsDep{"jsdom", jsdomVer})
				p.TestEnvironment = "jsdom"
				p.Stack = "vitest-vite-jsdom"
			}
			p.ConfigFile = jsVitestConfigName(det.IsTS)

		case det.IsESM:
			// Jest runs ESM only behind --experimental-vm-modules; the previous bootstrap sidestepped
			// that by writing a CommonJS config and a .cjs smoke test, which passes verification and
			// then cannot run the ESM test files generation produces. Vitest is ESM-native.
			p.Runner = JSRunnerVitest
			p.Deps = []jsDep{{"vitest", vitestVersionForVite(det.ViteMajor)}}
			p.TestEnvironment = "node"
			p.ConfigFile = jsVitestConfigName(det.IsTS)
			p.Stack = "vitest-node-esm"

		default:
			p.Runner = JSRunnerJest
			p.Deps = []jsDep{{"jest", VersionJest}}
			if det.IsTS {
				p.Deps = append(p.Deps,
					jsDep{"ts-jest", VersionTSJest},
					jsDep{"@types/jest", VersionTypesJest},
					jsDep{"@types/node", VersionTypesNode},
				)
			}
			p.TestEnvironment = "node"
			p.ConfigFile = "jest.config.cjs"
			p.Stack = "jest-node"
		}
	}

	p = ensureNodeTypesForNodeEnvironment(p)

	// A stack that needs jsdom on a runtime no jsdom line supports cannot host a generated test, and
	// the failure is invisible until the smoke gate: npm installs the package regardless, then the
	// Vitest worker dies inside jsdom's own require(). Decline instead, so the package is skipped
	// with a reason rather than the whole run aborted with someone else's stack trace.
	if !jsdomOK && jsProfileNeedsJsdom(p) {
		return jsTestProfile{
			Framework:        det.Framework,
			FrameworkVersion: det.FrameworkVersion,
			IsTS:             det.IsTS,
			IsESM:            det.IsESM,
			ViteMajor:        det.ViteMajor,
			Evidence:         det.Evidence,
			TestScript:       jsAsqsTestScript,
			Stack:            jsStackJsdomDeclined,
			Declined:         true,
			DeclinedReason: fmt.Sprintf("Node %s is older than every current jsdom line (>= 18 required), "+
				"so a DOM test stack cannot run here. Raise general.sandbox.images.node, or the "+
				"host's Node when bootstrap runs outside Docker, before bootstrapping this package.",
				strings.TrimSpace(nodeVersion)),
		}
	}
	return p
}

// ensureNodeTypesForNodeEnvironment adds Node's own type declarations to a Node-environment
// TypeScript stack.
//
// Its tests legitimately reach for process, Buffer, path and __dirname, and none of them are
// declared by the runner's globals. The jest-node profile has always installed this; the Vitest ones
// never did.
//
// Deliberately NOT extended to the jsdom profiles. Those are browser applications, their tsconfig
// usually has no compilerOptions.types, and @types/node is therefore ambient across src/** — which
// would make `process.env` type-check inside browser code that will not have a `process` at run
// time, removing an error the repository was correctly getting. A generated jsdom test that reaches
// for a Node global is the fix loop's to repair — it is a one-token edit in a writable artifact, and
// the idiomatic `globalThis` needs no declaration at all — so generation is steered away from
// `global` in the test-stack contract instead.
//
// Also not left to chance: @types/node arrives as a TRANSITIVE dependency of vitest in some
// resolutions and not others (it did not in the run above), so a stack that needs it must declare it.
func ensureNodeTypesForNodeEnvironment(p jsTestProfile) jsTestProfile {
	if !p.IsTS || p.TestEnvironment != "node" || profileHasDep(p, typesNodePackage) {
		return p
	}
	p.Deps = append(p.Deps, jsDep{typesNodePackage, VersionTypesNode})
	return p
}

// jsProfileNeedsJsdom reports whether the profile installs jsdom itself. Jest's DOM stacks do not:
// jest-environment-jsdom vendors its own, so their testEnvironment is 'jsdom' without a jsdom dep.
func jsProfileNeedsJsdom(p jsTestProfile) bool {
	for _, d := range p.Deps {
		if d.Name == "jsdom" {
			return true
		}
	}
	return false
}

// jsVitestConfigName keeps the config file's language matching the package's.
func jsVitestConfigName(isTS bool) string {
	if isTS {
		return "vitest.config.ts"
	}
	return "vitest.config.js"
}

// vitestVersionForVite picks a Vitest line the repo's Vite major can satisfy. Vitest 4 declares Vite
// ^6 || ^7 || ^8 as a peer; installing it next to Vite 5 fails resolution.
func vitestVersionForVite(viteMajor int) string {
	switch {
	case viteMajor >= 6:
		return VersionVitest4
	case viteMajor == 5:
		return VersionVitest3
	case viteMajor >= 1:
		return VersionVitest1
	default:
		// No Vite in the repo: Vitest 3 carries its own, so nothing else has to be installed.
		return VersionVitest3
	}
}

// missingDeps returns the profile deps absent from package.json.
func (p jsTestProfile) missingDeps(pkg jsPackageJSON) []jsDep {
	var out []jsDep
	for _, d := range p.Deps {
		if _, ok := pkg.dep(d.Name); ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

func describeJSDeps(deps []jsDep) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.String())
	}
	return out
}

// summarizeJSProfile renders a one-line description for stderr and audit messages.
func summarizeJSProfile(p jsTestProfile) string {
	v := p.FrameworkVersion
	if v == "" {
		v = "no version"
	}
	lang := "JavaScript"
	if p.IsTS {
		lang = "TypeScript"
	}
	return fmt.Sprintf("%s (%s, %s) → %s on %s", p.Framework, v, lang, p.Stack, p.TestEnvironment)
}
