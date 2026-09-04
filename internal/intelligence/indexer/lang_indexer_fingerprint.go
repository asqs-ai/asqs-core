package indexer

import "strings"

// langIndexerFingerprintSep separates a file's content hash from the fingerprint of the language
// indexer that parses it, inside FileVersion.SHA.
const langIndexerFingerprintSep = "+"

// langIndexerStampedLangs are the languages the JS/TS indexer parses: its extraction rules apply
// to these files and to no others, so only their versions carry its fingerprint.
var langIndexerStampedLangs = map[string]bool{"typescript": true, "javascript": true, "html": true}

// StampLangIndexerFingerprint appends the language indexer's build fingerprint to the SHA of
// every file version that indexer parses, and reports how many it stamped. Idempotent: a version
// already carrying a fingerprint is re-stamped with the given one, so callers may stamp scanned
// files unconditionally.
//
// DetectChanges compares stored and current SHAs as opaque strings. Folding the indexer's identity
// into the string is what makes a rebuilt indexer re-index unchanged files: run
// api-5d92b3b2d3a0a387a786892824038386 reused the previous day's index in full and the enricher
// added the day before never ran (see jstindexer.Fingerprint). An empty fingerprint stamps
// nothing, so a missing dist degrades to the old content-only behaviour rather than to a SHA that
// ends in a separator.
func StampLangIndexerFingerprint(files []FileVersion, fingerprint string) int {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return 0
	}
	stamped := 0
	for i := range files {
		if !langIndexerStampedLangs[strings.ToLower(files[i].Lang)] {
			continue
		}
		base := files[i].SHA
		if j := strings.Index(base, langIndexerFingerprintSep); j >= 0 {
			base = base[:j]
		}
		files[i].SHA = base + langIndexerFingerprintSep + fingerprint
		stamped++
	}
	return stamped
}
