package jstindexer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint identifies the JS/TS indexer BUILD that will parse this run's files: a SHA-256 over
// the contents of every dist/*.js file under indexerPath, in path order, hex-encoded and
// shortened. Empty when indexerPath has no dist directory.
//
// Why the indexer's identity has to reach change detection: the index is incremental and keyed on
// file content, so a repository whose files did not change is not re-indexed — which is right
// until the indexer itself changes what it extracts. Run api-5d92b3b2d3a0a387a786892824038386
// (2026-09-04) ran a rebuilt indexer that emits UI_TEST_HOOK symbols from JSX and reported
// "0 files added, 0 changed, 0 removed": the previous day's index was reused byte for byte, the
// new enricher never ran, and the selector inventory it existed to fill stayed empty. Stamping
// this value into each JS/TS file version (indexer.StampLangIndexerFingerprint) makes a rebuilt
// indexer look like a changed file to DetectChanges, with no schema change and no new state.
//
// dist/ is what actually executes (see the js-ts-indexer runner), so the hash is over dist, not
// src: a source edit that was never built must not trigger a re-index that would extract nothing
// new.
func Fingerprint(indexerPath string) string {
	dist := distDirFor(indexerPath)
	if dist == "" {
		return ""
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dist, n))
		if err != nil {
			return ""
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// distDirFor resolves the dist directory from any of the spellings the indexer path is configured
// in: the entry file (`tools/js-ts-indexer/dist/index.js`, which is what indexer.jsts.indexer_path
// documents and what RunIndexer executes), the dist directory itself, or the package root that
// contains it. Empty when none of them exists. An empty input is empty output: the working
// directory must never be fingerprinted by accident.
//
// The first cut joined "dist" onto whatever it was given, which for the documented entry-file
// spelling looked for dist/index.js/dist and silently stamped nothing — run
// api-3cf0a2f72bb2f470d6edf4e3cd0f2c41 carried no index.lang_indexer_fingerprint event at all.
func distDirFor(indexerPath string) string {
	p := strings.TrimSpace(indexerPath)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if !st.IsDir() {
		p = filepath.Dir(p)
	}
	if filepath.Base(p) == "dist" {
		return p
	}
	nested := filepath.Join(p, "dist")
	if st, err := os.Stat(nested); err == nil && st.IsDir() {
		return nested
	}
	return ""
}
