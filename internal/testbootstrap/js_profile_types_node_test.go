package testbootstrap

import "testing"

func profileDepNames(p jsTestProfile) []string {
	out := make([]string, 0, len(p.Deps))
	for _, d := range p.Deps {
		out = append(out, d.Name)
	}
	return out
}

// A Node-environment TypeScript stack must declare @types/node itself. It is a TRANSITIVE dependency
// of vitest in some resolutions and absent in others — the run of 2026-09-01 had none on disk — so a
// stack that needs it cannot rely on the graph happening to supply it.
func TestEnsureNodeTypesForNodeEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   jsTestProfile
		want bool
	}{
		{"node env + TypeScript gets it", jsTestProfile{IsTS: true, TestEnvironment: "node", Deps: []jsDep{{"vitest", "x"}}}, true},
		{"JavaScript project does not", jsTestProfile{IsTS: false, TestEnvironment: "node", Deps: []jsDep{{"vitest", "x"}}}, false},
		{"jsdom stack does not", jsTestProfile{IsTS: true, TestEnvironment: "jsdom", Deps: []jsDep{{"vitest", "x"}, {"jsdom", "x"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureNodeTypesForNodeEnvironment(tc.in)
			if profileHasDep(got, typesNodePackage) != tc.want {
				t.Errorf("deps = %v, want %s present=%v", profileDepNames(got), typesNodePackage, tc.want)
			}
		})
	}
}

// Idempotent: the jest-node profile already listed @types/node before this rule existed, and a
// second entry would put a duplicate into package.json.
func TestEnsureNodeTypesForNodeEnvironment_idempotent(t *testing.T) {
	in := jsTestProfile{IsTS: true, TestEnvironment: "node",
		Deps: []jsDep{{"jest", "x"}, {typesNodePackage, VersionTypesNode}}}
	got := ensureNodeTypesForNodeEnvironment(ensureNodeTypesForNodeEnvironment(in))
	n := 0
	for _, d := range got.Deps {
		if d.Name == typesNodePackage {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%s appears %d time(s): %v", typesNodePackage, n, profileDepNames(got))
	}
}

// The jsdom stacks must NOT install @types/node: their tsconfig usually has no compilerOptions.types,
// so it would be ambient over src/** and `process.env` would start type-checking inside browser code
// that has no `process` at run time — ASQS would be removing an error the repository was correctly
// getting. Verified with tsc 5.7.3: with @types/node installed and no types array, `process.env` in
// a src/ file compiles clean.
//
// A generated test that reaches for a Node global on such a stack is the FIXER's to repair (it is a
// one-token edit in a writable artifact, and `globalThis` needs no declaration at all); generation
// is steered away from `global` by the test-stack contract.
func TestJSProfile_domStacksDoNotInstallTypesNode(t *testing.T) {
	for _, p := range []jsTestProfile{
		{Stack: "vitest-react", TestEnvironment: "jsdom", IsTS: true,
			Deps: []jsDep{{"vitest", "x"}, {"jsdom", "x"}, {"@testing-library/react", "x"}, {jestDomPackage, "x"}}},
		{Stack: "vitest-vue", TestEnvironment: "jsdom", IsTS: true,
			Deps: []jsDep{{"vitest", "x"}, {"jsdom", "x"}, {jestDomPackage, "x"}, {"@vue/test-utils", "x"}}},
	} {
		if profileHasDep(p, typesNodePackage) {
			t.Errorf("%s must not install %s; got %v", p.Stack, typesNodePackage, profileDepNames(p))
		}
	}
}
