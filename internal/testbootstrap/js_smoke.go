package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	jsSmokeDir           = "__tests__"
	jsUnitSmokeBase      = "asqs-bootstrap-smoke"
	jsFrameworkSmokeBase = "asqs-framework-smoke"
)

// jsSmokeFile is a smoke test staged on disk.
type jsSmokeFile struct {
	Abs string
	// Rel is the package-relative path, which is what both runners accept as a positional filter.
	Rel   string
	Wrote bool
}

// jsSmokeExt picks the file extension so the runner's own transform is exercised: a .ts smoke on a
// TypeScript package proves ts-jest / the Vite transform actually works, which a .cjs smoke does not.
func jsSmokeExt(p jsTestProfile, jsx bool) string {
	switch {
	case p.IsTS && jsx:
		return ".tsx"
	case p.IsTS:
		return ".ts"
	case jsx:
		return ".jsx"
	case p.Runner == JSRunnerVitest:
		return ".js"
	default:
		// Jest on a "type":"module" package cannot load ESM .js without --experimental-vm-modules;
		// .cjs sidesteps that for the runner-only smoke.
		if p.IsESM {
			return ".cjs"
		}
		return ".js"
	}
}

// unitSmokeImports returns the import/require preamble for the runner.
func unitSmokeImports(p jsTestProfile, ext string) string {
	if p.Runner == JSRunnerVitest {
		return "import { describe, it, expect } from 'vitest';\n"
	}
	if ext == ".cjs" {
		return "const { describe, it, expect } = require('@jest/globals');\n"
	}
	return "" // Jest globals are ambient in .ts/.js test files
}

// renderJSUnitSmoke builds the mandatory smoke test: it must exercise the transform and the runner,
// not merely parse.
func renderJSUnitSmoke(p jsTestProfile) (source, ext string) {
	ext = jsSmokeExt(p, false)
	var b strings.Builder
	fmt.Fprintf(&b, "/* %s — safe to edit or delete.\n", asqsGeneratedHeader)
	b.WriteString(" * Proves the runner starts, the transform handles this file, and assertions execute.\n */\n")
	b.WriteString(unitSmokeImports(p, ext))
	b.WriteString("\ndescribe('asqs bootstrap smoke', () => {\n")
	b.WriteString("  it('runs a test through the configured transform', () => {\n")
	if p.IsTS && ext != ".cjs" {
		b.WriteString("    const value: string = ['as', 'qs'].join('');\n")
		b.WriteString("    expect(value).toBe('asqs');\n")
	} else {
		b.WriteString("    expect(['as', 'qs'].join('')).toBe('asqs');\n")
	}
	b.WriteString("  });\n")
	if p.TestEnvironment == "jsdom" {
		// The single most common reason a generated component test fails is a runner configured with
		// the default 'node' environment, where there is no document. Assert it here so the mandatory
		// gate catches it, rather than leaving it to the first generated test.
		b.WriteString("\n  it('provides a DOM environment', () => {\n")
		b.WriteString("    const el = document.createElement('div');\n")
		b.WriteString("    el.textContent = 'asqs';\n")
		b.WriteString("    document.body.appendChild(el);\n")
		b.WriteString("    expect(document.body.textContent).toContain('asqs');\n")
		b.WriteString("  });\n")
	}
	b.WriteString("});\n")
	return b.String(), ext
}

// jsFrameworkSmokeSpec is a framework smoke test plus, when the framework needs one, a real
// component file for it to import.
//
// The companion exists because a .vue or .svelte file compiles only through that framework's Vite
// plugin. Importing one is the only way the smoke test proves the plugin actually reaches the test
// run through the merged vite.config — which is the entire reason Vitest is chosen for these stacks.
type jsFrameworkSmokeSpec struct {
	TestSource      string
	TestExt         string
	CompanionName   string
	CompanionSource string
}

// renderJSFrameworkSmokeSpec builds the framework-representative smoke test, or a zero spec when the
// profile has none.
func renderJSFrameworkSmokeSpec(p jsTestProfile) jsFrameworkSmokeSpec {
	switch p.FrameworkSmoke {
	case jsSmokeVue:
		return jsFrameworkSmokeSpec{
			TestSource: "/* " + asqsGeneratedHeader + " — safe to edit or delete.\n" +
				" * Mounting a single-file component is the assertion: it proves @vitejs/plugin-vue reaches\n" +
				" * the test run through the merged Vite config, and that jsdom and @vue/test-utils work.\n */\n" +
				"import { describe, it, expect } from 'vitest';\n" +
				"import { mount } from '@vue/test-utils';\n\n" +
				"import AsqsSmoke from './AsqsSmoke.vue';\n\n" +
				"describe('asqs framework smoke (vue)', () => {\n" +
				"  it('mounts a single-file component', () => {\n" +
				"    const wrapper = mount(AsqsSmoke);\n" +
				"    expect(wrapper.text()).toContain('asqs bootstrap ok');\n" +
				"  });\n});\n",
			TestExt:       ".ts",
			CompanionName: "AsqsSmoke.vue",
			CompanionSource: "<!-- " + asqsGeneratedHeader + " — safe to edit or delete. -->\n" +
				"<script setup lang=\"ts\">\nconst message: string = 'asqs bootstrap ok';\n</script>\n\n" +
				"<template>\n  <span>{{ message }}</span>\n</template>\n",
		}
	case jsSmokeSvelte:
		return jsFrameworkSmokeSpec{
			TestSource: "/* " + asqsGeneratedHeader + " — safe to edit or delete.\n" +
				" * Rendering a component is the assertion: it proves @sveltejs/vite-plugin-svelte reaches\n" +
				" * the test run through the merged Vite config, and that jsdom and Testing Library work.\n */\n" +
				"import { describe, it, expect } from 'vitest';\n" +
				"import { render, screen } from '@testing-library/svelte';\n\n" +
				"import AsqsSmoke from './AsqsSmoke.svelte';\n\n" +
				"describe('asqs framework smoke (svelte)', () => {\n" +
				"  it('renders a component', () => {\n" +
				"    render(AsqsSmoke);\n" +
				"    expect(screen.getByText('asqs bootstrap ok')).toBeInTheDocument();\n" +
				"  });\n});\n",
			TestExt:       ".ts",
			CompanionName: "AsqsSmoke.svelte",
			CompanionSource: "<!-- " + asqsGeneratedHeader + " — safe to edit or delete. -->\n" +
				"<script lang=\"ts\">\n  const message: string = 'asqs bootstrap ok';\n</script>\n\n" +
				"<span>{message}</span>\n",
		}
	}
	src, ext := renderJSFrameworkSmoke(p)
	return jsFrameworkSmokeSpec{TestSource: src, TestExt: ext}
}

// renderJSFrameworkSmoke builds the framework-representative smoke test, or "" when the profile has
// none. jsx reports whether the file needs a JSX extension.
func renderJSFrameworkSmoke(p jsTestProfile) (source, ext string) {
	switch p.FrameworkSmoke {
	case jsSmokeReact:
		ext = jsSmokeExt(p, true)
		var b strings.Builder
		fmt.Fprintf(&b, "/* %s — safe to edit or delete.\n", asqsGeneratedHeader)
		b.WriteString(" * Rendering a component is the assertion: it proves the JSX transform, the jsdom\n")
		b.WriteString(" * environment and React Testing Library all work together in this package. The click\n")
		b.WriteString(" * proves @testing-library/user-event resolves, since generated tests are told to use it.\n */\n")
		if p.Runner == JSRunnerVitest {
			b.WriteString("import { describe, it, expect } from 'vitest';\n")
		}
		b.WriteString("import { useState } from 'react';\n")
		b.WriteString("import { render, screen } from '@testing-library/react';\n")
		b.WriteString("import userEvent from '@testing-library/user-event';\n\n")
		b.WriteString("function AsqsSmokeComponent() {\n")
		b.WriteString("  const [clicked, setClicked] = useState(false);\n")
		b.WriteString("  return <button onClick={() => setClicked(true)}>{clicked ? 'asqs click ok' : 'asqs bootstrap ok'}</button>;\n")
		b.WriteString("}\n\n")
		b.WriteString("describe('asqs framework smoke (react)', () => {\n")
		b.WriteString("  it('renders a component into jsdom', () => {\n")
		b.WriteString("    render(<AsqsSmokeComponent />);\n")
		b.WriteString("    expect(screen.getByText('asqs bootstrap ok')).toBeInTheDocument();\n")
		b.WriteString("  });\n")
		b.WriteString("  it('drives the component with user-event', async () => {\n")
		b.WriteString("    render(<AsqsSmokeComponent />);\n")
		b.WriteString("    await userEvent.click(screen.getByText('asqs bootstrap ok'));\n")
		b.WriteString("    expect(screen.getByText('asqs click ok')).toBeInTheDocument();\n")
		b.WriteString("  });\n});\n")
		return b.String(), ext

	case jsSmokeAngular:
		var b strings.Builder
		fmt.Fprintf(&b, "/* %s — safe to edit or delete.\n", asqsGeneratedHeader)
		b.WriteString(" * Compiling and creating a component through TestBed is the assertion: it proves the\n")
		b.WriteString(" * Angular transformer, zone.js and the jsdom environment are all wired up.\n */\n")
		b.WriteString("import { Component } from '@angular/core';\nimport { TestBed } from '@angular/core/testing';\n\n")
		b.WriteString("@Component({ standalone: true, selector: 'asqs-smoke', template: '<span>asqs bootstrap ok</span>' })\n")
		b.WriteString("class AsqsSmokeComponent {}\n\n")
		b.WriteString("describe('asqs framework smoke (angular)', () => {\n")
		b.WriteString("  it('compiles and creates a component', async () => {\n")
		b.WriteString("    await TestBed.configureTestingModule({ imports: [AsqsSmokeComponent] }).compileComponents();\n")
		b.WriteString("    const fixture = TestBed.createComponent(AsqsSmokeComponent);\n")
		b.WriteString("    fixture.detectChanges();\n")
		b.WriteString("    expect(fixture.nativeElement.textContent).toContain('asqs bootstrap ok');\n")
		b.WriteString("  });\n});\n")
		return b.String(), ".ts"

	case jsSmokeNest:
		var b strings.Builder
		fmt.Fprintf(&b, "/* %s — safe to edit or delete.\n", asqsGeneratedHeader)
		b.WriteString(" * Compiling a testing module is the assertion: it proves @nestjs/testing resolves and\n")
		b.WriteString(" * that decorator metadata is being emitted, which every generated Nest test depends on.\n */\n")
		b.WriteString("import { Injectable } from '@nestjs/common';\nimport { Test } from '@nestjs/testing';\n\n")
		b.WriteString("@Injectable()\nclass AsqsSmokeService {\n  ping(): string {\n    return 'asqs bootstrap ok';\n  }\n}\n\n")
		b.WriteString("describe('asqs framework smoke (nestjs)', () => {\n")
		b.WriteString("  it('compiles a testing module and resolves a provider', async () => {\n")
		b.WriteString("    const moduleRef = await Test.createTestingModule({ providers: [AsqsSmokeService] }).compile();\n")
		b.WriteString("    expect(moduleRef.get(AsqsSmokeService).ping()).toBe('asqs bootstrap ok');\n")
		b.WriteString("  });\n});\n")
		return b.String(), ".ts"
	}
	return "", ""
}

func writeJSSmoke(pkgDir, base, ext, source string) (jsSmokeFile, error) {
	return writeJSSmokeIn(pkgDir, jsSmokeDir, base, ext, source)
}

// writeJSSmokeIn is writeJSSmoke with the directory chosen by the caller.
//
// The type-check gate needs its probe INSIDE the project's tsconfig program — generated tests land
// beside sources, and a tsconfig whose include is ["src"] type-checks nothing in __tests__/. A probe
// staged in the default smoke directory would pass vacuously, which is exactly how the run of
// 2026-09-01 recorded verified=true for a stack that could not compile a single generated test.
//
// A non-".test" extension (a Vue/Svelte companion component) is written as-is, so it is a module the
// probe can import rather than a second test file the runner would try to execute.
func writeJSSmokeIn(pkgDir, dir, base, ext, source string) (jsSmokeFile, error) {
	name := base + ".test" + ext
	if ext == ".vue" || ext == ".svelte" {
		name = base + ext
	}
	rel := filepath.ToSlash(filepath.Join(dir, name))
	abs := filepath.Join(pkgDir, filepath.FromSlash(rel))
	f := jsSmokeFile{Abs: abs, Rel: rel}
	if fileExists(abs) {
		return f, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return f, fmt.Errorf("mkdir smoke test dir: %w", err)
	}
	if err := atomicWrite(abs, []byte(source)); err != nil {
		return f, fmt.Errorf("write smoke test: %w", err)
	}
	f.Wrote = true
	return f, nil
}

// writeJSUnitSmokeTest stages the mandatory smoke test.
func writeJSUnitSmokeTest(pkgDir string, p jsTestProfile) (jsSmokeFile, error) {
	source, ext := renderJSUnitSmoke(p)
	return writeJSSmoke(pkgDir, jsUnitSmokeBase, ext, source)
}

// writeJSFrameworkSmokeTest stages the framework-representative smoke test and any companion
// component it imports. The returned test file is the one to run; extra holds everything else this
// call created, so a failed smoke can be removed whole.
func writeJSFrameworkSmokeTest(pkgDir string, p jsTestProfile) (test jsSmokeFile, extra []jsSmokeFile, staged bool, err error) {
	spec := renderJSFrameworkSmokeSpec(p)
	if spec.TestSource == "" {
		return jsSmokeFile{}, nil, false, nil
	}
	if spec.CompanionName != "" {
		companionRel := filepath.ToSlash(filepath.Join(jsSmokeDir, spec.CompanionName))
		companionAbs := filepath.Join(pkgDir, filepath.FromSlash(companionRel))
		c := jsSmokeFile{Abs: companionAbs, Rel: companionRel}
		if !fileExists(companionAbs) {
			if mkErr := os.MkdirAll(filepath.Dir(companionAbs), 0o755); mkErr != nil {
				return jsSmokeFile{}, nil, false, fmt.Errorf("mkdir smoke test dir: %w", mkErr)
			}
			if wErr := atomicWrite(companionAbs, []byte(spec.CompanionSource)); wErr != nil {
				return jsSmokeFile{}, nil, false, fmt.Errorf("write smoke component: %w", wErr)
			}
			c.Wrote = true
		}
		extra = append(extra, c)
	}
	test, err = writeJSSmoke(pkgDir, jsFrameworkSmokeBase, spec.TestExt, spec.TestSource)
	return test, extra, true, err
}

// removeJSSmokeFile deletes a smoke test this run created, so a failed framework smoke never becomes
// a permanently broken file the evaluator inherits.
func removeJSSmokeFile(f jsSmokeFile) {
	if !f.Wrote || f.Abs == "" {
		return
	}
	_ = os.Remove(f.Abs)
}
