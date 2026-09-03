package errloc

import "testing"

// vitest colours its frames with the position dimmed SEPARATELY from the path:
// `src/app/router.test.tsx:` + ESC[2m + `59:24` + ESC[22m. With the escape between the colon and
// the digits, reFileLineColon matched nothing in run api-72dad6bb281cacee338f43c48432a780 and the
// fixer's scope was narrowed to the one file an uncoloured React warning happened to name.
func TestParseLocations_vitestColouredFrames(t *testing.T) {
	log := "\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m src/app/router.test.tsx\x1b[2m > \x1b[22mrouter\n" +
		"Error: Cannot find module './router'\n" +
		"\x1b[36m \x1b[2m❯\x1b[22m src/app/router.test.tsx:\x1b[2m59:24\x1b[22m\x1b[39m\n" +
		"\x1b[36m \x1b[2m❯\x1b[22m src/lib/validation.test.ts:\x1b[2m27:38\x1b[22m\x1b[39m\n"
	locs := ParseLocations(log)
	want := map[string]int{"src/app/router.test.tsx": 59, "src/lib/validation.test.ts": 27}
	for _, l := range locs {
		if line, ok := want[l.File]; ok && l.Line == line {
			delete(want, l.File)
		}
	}
	if len(want) != 0 {
		t.Fatalf("coloured vitest frames not parsed; missing %v, got %v", want, locs)
	}
}
