package testbootstrap

import "testing"

// The contract's import roots are derived from the profile deps, so installing user-event must
// also advertise it to the generator; otherwise the prompt's React hint and the contract's closed
// allow-list contradict each other in the same system message.
func TestJSContract_reactListsUserEvent(t *testing.T) {
	p := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, IsTS: true}, testNodeVersion)
	c := jsContract(p, "typescript")
	found := false
	for _, imp := range c.AvailableImports {
		if imp == "@testing-library/user-event" {
			found = true
		}
	}
	if !found {
		t.Fatalf("available_imports must list @testing-library/user-event; got %v", c.AvailableImports)
	}
}

// F8. The contract carries the package's module system so the generator can state the
// import-vs-require rule instead of repairing `require()` calls one test at a time.
func TestJSContract_moduleTypeFollowsPackageType(t *testing.T) {
	esm := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, IsESM: true}, testNodeVersion)
	if got := jsContract(esm, "typescript").ModuleType; got != "esm" {
		t.Errorf("ESM package → module_type %q, want esm", got)
	}
	cjs := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18}, testNodeVersion)
	if got := jsContract(cjs, "typescript").ModuleType; got != "commonjs" {
		t.Errorf("CommonJS package → module_type %q, want commonjs", got)
	}
}
