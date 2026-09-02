package evaluator

import "testing"

// tscOut is the shape of the compile failure from the run of 2026-09-01: 112 diagnostics, none of
// which the fix loop's per-file machinery could see.
const tscOut = `
> react-test@0.0.0 build
> tsc --noEmit -p tsconfig.app.json && vite build

src/app/AppLayout.test.tsx(34,22): error TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'.
src/app/AppLayout.test.tsx(35,24): error TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'.
src/app/router.test.tsx(121,3): error TS1232: An import declaration can only be used at the top level of a namespace or module.
src/features/orders/orderFormat.test.ts(27,22): error TS2304: Cannot find name 'formatOrderRef'.
`

// FileDiagnostics feeds stalledFiles, which is what raises evaluator.fix_file_no_progress. On tsc
// output it returned nil, so the loop compared two empty maps every round and the breaker was dead.
func TestFileDiagnostics_attributesTSCDiagnosticsPerFile(t *testing.T) {
	got := FileDiagnostics(tscOut)
	if len(got) != 3 {
		t.Fatalf("FileDiagnostics returned %d file(s): %v", len(got), keysOfDiagnostics(got))
	}
	for _, want := range []string{
		"src/app/AppLayout.test.tsx",
		"src/app/router.test.tsx",
		"src/features/orders/orderFormat.test.ts",
	} {
		if got[want] == "" {
			t.Errorf("no diagnostics attributed to %s (got %v)", want, keysOfDiagnostics(got))
		}
	}
}

// The fingerprints are what "did this file's diagnostics change?" compares, so two different
// outputs for the same file must not fingerprint alike.
func TestFileDiagnostics_fingerprintChangesWithTheDiagnostic(t *testing.T) {
	a := FileDiagnostics("src/a.test.tsx(3,1): error TS2304: Cannot find name 'x'.\n")
	b := FileDiagnostics("src/a.test.tsx(3,1): error TS2304: Cannot find name 'y'.\n")
	if a["src/a.test.tsx"] == "" || b["src/a.test.tsx"] == "" {
		t.Fatal("expected a fingerprint for src/a.test.tsx in both")
	}
	if a["src/a.test.tsx"] == b["src/a.test.tsx"] {
		t.Error("different diagnostics fingerprinted identically")
	}
}

// stalledFiles is the breaker itself: a file written last round whose diagnostics came back
// identical. It could never fire on TypeScript because both sides were always nil.
func TestStalledFiles_firesOnUnchangedTSCDiagnostics(t *testing.T) {
	before := FileDiagnostics(tscOut)
	after := FileDiagnostics(tscOut)
	stalled := stalledFiles(before, after, []string{"src/app/router.test.tsx"})
	if len(stalled) != 1 || stalled[0] != "src/app/router.test.tsx" {
		t.Fatalf("stalledFiles = %v, want [src/app/router.test.tsx]", stalled)
	}
}

// ParsePrimaryFailureSite gates the primary-site guard and evaluator.fix_primary_site_untouched.
func TestParsePrimaryFailureSite_readsTSCLocation(t *testing.T) {
	site := ParsePrimaryFailureSite(tscOut)
	if !site.OK {
		t.Fatal("primary failure site not recognised in tsc output")
	}
	if site.Path != "src/app/AppLayout.test.tsx" || site.Line != 34 {
		t.Errorf("site = %s:%d, want src/app/AppLayout.test.tsx:34", site.Path, site.Line)
	}
}

// MSBuild uses the same parenthesised shape and was equally invisible.
func TestParsePrimaryFailureSite_readsMSBuildLocation(t *testing.T) {
	site := ParsePrimaryFailureSite("Controllers/OwnerController.cs(33,12): error CS1002: ; expected\n")
	if !site.OK || site.Path != "Controllers/OwnerController.cs" || site.Line != 33 {
		t.Errorf("site = %+v, want Controllers/OwnerController.cs:33 OK", site)
	}
}

// The javac shapes this pattern was written for must be untouched.
func TestParsePrimaryFailureSite_javacShapesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, out, path string
		line            int
	}{
		{"maven bracket", "OwnerTests.java:[149,17] cannot find symbol\n", "OwnerTests.java", 149},
		{"plain colon", "Foo.java:12: error: cannot find symbol\n", "Foo.java", 12},
		{"go", "internal/foo.go:12:3: undefined: bar\n", "internal/foo.go", 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := ParsePrimaryFailureSite(tc.out)
			if !site.OK || site.Path != tc.path || site.Line != tc.line {
				t.Errorf("site = %+v, want %s:%d", site, tc.path, tc.line)
			}
		})
	}
}

// A bare `foo.ts(3)` with no column must not open a diagnostic bucket: prose and code snippets
// reach this function inside error logs, and a bogus primary site sends the whole round at the
// wrong file.
func TestFileDiagnostics_ignoresParenWithoutColumn(t *testing.T) {
	if got := FileDiagnostics("the helper in foo.ts(3) is fine\n"); len(got) != 0 {
		t.Errorf("FileDiagnostics = %v, want none", keysOfDiagnostics(got))
	}
}

func keysOfDiagnostics(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
