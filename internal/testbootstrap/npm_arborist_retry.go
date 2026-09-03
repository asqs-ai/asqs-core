package testbootstrap

import (
	"bytes"
	"regexp"
)

// npmFallbackSpec is the npm the bootstrap retries with when the image's npm crashes inside its
// own dependency resolver. Pinned to a major rather than `latest` so the retry is reproducible;
// npm 12 requires Node ^20.17 || >=22.9, which every image the bootstrap uses satisfies.
//
// Verified 2026-09-03: node:22-bookworm ships npm 10.9.8, which died with
// "Cannot read properties of null (reading 'edgesOut')" in @npmcli/arborist's #loadPeerSet the
// moment vitest 5.0.0 was published (12:24Z) — adding vitest@4.1.11, and nothing else, to an
// untouched Vite React repository was enough. The same manifest installed at 06:42 the same day.
// npm 12 resolved it, produced a lockfileVersion 3 lock, and npm 10.9.8's `npm ci` accepted that
// lock in the evaluation step. `--legacy-peer-deps` also survives the crash but is NOT a fix: it
// stops auto-installing peers, which drops @testing-library/dom and breaks every React test.
const npmFallbackSpec = "npm@12"

// npmRetryMarker is written into the install output when the fallback ran, so callers can audit
// the retry without a second return value.
const npmRetryMarker = "[asqs] npm crashed inside its dependency resolver; retrying the install with " + npmFallbackSpec + " via npx\n"

// npmArboristCrashRE matches npm dying on a TypeError inside itself rather than reporting a
// dependency problem: `npm error Cannot read properties of null (reading 'edgesOut')` and its
// `npm ERR!` (npm <10) and `TypeError:` spellings. Registry errors (E404, ERESOLVE, ETARGET)
// deliberately do not match — those are the repository's problem, and a newer npm would report
// the same thing.
var npmArboristCrashRE = regexp.MustCompile(`(?m)^npm (?:error|ERR!)\s+(?:TypeError: )?Cannot read properties of (?:null|undefined)`)

// npmArboristCrash reports whether an npm install failed on an internal npm crash.
func npmArboristCrash(output []byte) bool {
	return npmArboristCrashRE.Match(output)
}

// npmFallbackInstallArgv rewrites an `npm <args…>` argv to run the same command through the
// pinned fallback npm: `npx --yes npm@12 <args…>`. npx is shipped with every npm, so nothing has
// to be installed globally in the image, and the repository's own npm stays untouched.
func npmFallbackInstallArgv(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := []string{"npx", "--yes", npmFallbackSpec}
	return append(out, argv[1:]...)
}

// installOutputRetried reports whether an install output carries the retry marker.
func installOutputRetried(output []byte) bool {
	return bytes.Contains(output, []byte(npmRetryMarker))
}
