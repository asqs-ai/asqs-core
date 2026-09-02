package errloc

import (
	"strings"
	"testing"
)

// tscCompileOutput is taken verbatim from the failing compile step of the run of 2026-09-01
// (repo maximal-dev/asqs-react-test), which produced 112 of these and from which ParseLocations
// previously extracted zero locations.
const tscCompileOutput = `
> react-test@0.0.0 build
> tsc --noEmit -p tsconfig.app.json && vite build

src/app/AppLayout.test.tsx(34,22): error TS2339: Property 'toBeInTheDocument' does not exist on type 'Assertion<HTMLElement>'.
src/app/router.test.tsx(121,3): error TS1232: An import declaration can only be used at the top level of a namespace or module.
src/features/orders/orderFormat.test.ts(27,22): error TS2304: Cannot find name 'formatOrderRef'.
src/pages/settings/SettingsProfilePage.test.tsx(21,28): error TS2322: Type '{ user: any; }' is not assignable to type 'IntrinsicAttributes'.
`

func TestParseLocations_tscParenthesisedPositions(t *testing.T) {
	locs := ParseLocations(tscCompileOutput)
	want := map[string]int{
		"src/app/AppLayout.test.tsx":                      34,
		"src/app/router.test.tsx":                         121,
		"src/features/orders/orderFormat.test.ts":         27,
		"src/pages/settings/SettingsProfilePage.test.tsx": 21,
	}
	got := map[string]int{}
	for _, l := range locs {
		got[l.File] = l.Line
	}
	for f, line := range want {
		if got[f] != line {
			t.Errorf("ParseLocations missed %s:%d (got line %d)", f, line, got[f])
		}
	}
}

// MSBuild/roslyn is the shape this pattern was originally written for and must keep working.
func TestParseLocations_msbuildStillParsed(t *testing.T) {
	locs := ParseLocations(`Controllers\OwnerController.cs(33,12): error CS1002: ; expected`)
	if len(locs) != 1 || locs[0].Line != 33 {
		t.Fatalf("locs = %+v, want one location at line 33", locs)
	}
}

// Hyphenated paths are ordinary in JS/TS repos; the old `.cs`-shaped class silently truncated them.
func TestParseLocations_hyphenatedPathSurvives(t *testing.T) {
	locs := ParseLocations(`packages/my-app/src/order-form.test.tsx(9,4): error TS2304: Cannot find name 'x'.`)
	if len(locs) != 1 {
		t.Fatalf("locs = %+v, want exactly one", locs)
	}
	if locs[0].File != "packages/my-app/src/order-form.test.tsx" || locs[0].Line != 9 {
		t.Errorf("got %+v, want the full hyphenated path at line 9", locs[0])
	}
}

// The colon shapes must be untouched by the widening.
func TestParseLocations_colonShapesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, log, file string
		line            int
	}{
		{"javac plain", `Foo.java:12: error: cannot find symbol`, "Foo.java", 12},
		{"go", `internal/foo.go:12:3: undefined: bar`, "internal/foo.go", 12},
		{"jvm stack frame", `at com.example.Foo.method(Foo.java:40)`, "Foo.java", 40},
		{"vitest stack", `❯ src/lib/validation.test.ts:8:15`, "src/lib/validation.test.ts", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locs := ParseLocations(tc.log)
			found := false
			for _, l := range locs {
				if l.File == tc.file && l.Line == tc.line {
					found = true
				}
			}
			if !found {
				t.Errorf("ParseLocations(%q) = %+v, want %s:%d", tc.log, locs, tc.file, tc.line)
			}
		})
	}
}

// A JVM stack frame must not also match the parenthesised pattern and yield a bogus second hit.
func TestParseLocations_jvmFrameNotDoubleCounted(t *testing.T) {
	locs := ParseLocations(`at com.example.Foo.method(Foo.java:40)`)
	if len(locs) != 1 {
		t.Errorf("locs = %+v, want exactly one location", locs)
	}
}

// The Maven compiler-plugin's `path:[line,col]` form is deliberately NOT handled here: errout's
// AllCitedRepoPaths runs its own mavenBracketFilePattern pass for it. Asserted so the division of
// labour is explicit rather than an accident someone "fixes" by widening this package.
func TestParseLocations_mavenBracketIsErroutsJob(t *testing.T) {
	if locs := ParseLocations(`/repo/src/test/java/Foo.java:[12,5] error: cannot find symbol`); len(locs) != 0 {
		t.Errorf("ParseLocations now handles the bracket form (%+v); errout has a second pass for it", locs)
	}
}

// Absolute paths must stay matchable. This pattern is anchored by a preceding delimiter, so a path
// class that cannot begin with `/` or `\\` makes them unreachable: the match would have to start
// after the separator, whose preceding character is then not a delimiter. Both forms are ordinary —
// Docker sandbox paths (/workspace/...) and Windows MSBuild (C:\\proj\\...). asqs-core has no
// errloc_test.go, so without these cases the regression is invisible here.
func TestParseLocations_absolutePathsInParenForm(t *testing.T) {
	for _, tc := range []struct {
		name, log, suffix string
		line              int
	}{
		{"docker workspace", `/workspace/src/Api/Program.cs(10,1): error CS1002: ; expected`, "src/Api/Program.cs", 10},
		{"windows drive", `C:\proj\Services\UserService.cs(33,12): error CS1002: ; expected`, `UserService.cs`, 33},
		{"posix tsc", `/workspace/src/app/AppLayout.test.tsx(34,22): error TS2339: nope`, "src/app/AppLayout.test.tsx", 34},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locs := ParseLocations(tc.log)
			if len(locs) == 0 {
				t.Fatalf("ParseLocations(%q) found nothing", tc.log)
			}
			ok := false
			for _, l := range locs {
				if strings.HasSuffix(strings.ReplaceAll(l.File, `\`, "/"), tc.suffix) && l.Line == tc.line {
					ok = true
				}
			}
			if !ok {
				t.Errorf("got %#v, want a location ending %s at line %d", locs, tc.suffix, tc.line)
			}
		})
	}
}
