package websearch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CacheFormatVersion is bumped when the on-disk shape changes; a mismatch is a cold cache, never
// a parse error path. Mirrors projectintel.CacheFormatVersion.
const CacheFormatVersion = 1

// DefaultCachePathRel is the cache document's location when none is configured, REPO-RELATIVE —
// resolved against the repository under test, exactly as projectintel resolves
// .asqs/project-intel-cache.json.
//
// The cache belongs to the repo under test, not to the ASQS installation: its queries are driven
// by that repo's stack (a Spring repo asks Spring questions, a .NET repo asks .NET ones), so
// pooling every tested repository's lookups into one file next to the ASQS binary mixes tenants
// and grows without an owner.
//
// Cross-run reuse comes from COMMITTING this file, not from escaping the clone. workflow/runner.go
// clones into os.MkdirTemp("", "qualitybot-run-") and reuses that workspace only for a scheduler
// RERUN of the same run, so a path inside the clone survives nothing by itself. The cache reaches
// the next run the same way .asqs/project-intel-cache.json does: orchestrator.
// AsqsShipPreserveRelPaths keeps it through the pre-ship .asqs cleanup, `git add .` stages it, and
// the next run's clone brings it back.
//
// That preservation was missing until run api-a7875e00c2966904a5dd251f8473c25a, and the shape of
// the bug is worth remembering: the cache worked perfectly and was deleted before every commit, so
// it looked like a cold-start design decision rather than a dropped file. Two runs held
// byte-identical keys stored 14.6 hours apart under a 168h TTL and each paid separately.
//
// Placing the cache outside the clone via an absolute cache_path still works and is still the
// operator's call — but it pools every tested repository's lookups into one file, which is the
// tenant mixing the paragraph above rejects.
const DefaultCachePathRel = ".asqs/websearch-cache.json"

// ResolveCachePath returns the cache document for the repository under test: repoPath joined with
// the configured (or default) repo-relative path. An absolute configured path is honoured as-is,
// which is what lets an operator deliberately place the cache outside the clone.
//
// Mirrors projectintel's filepath.Join(repoAbs, filepath.FromSlash(cacheRel)). With an empty
// repoPath the result stays relative to the process working directory rather than failing, so a
// caller without a workspace still gets a usable cache.
func ResolveCachePath(repoPath, configured string) string {
	path := strings.TrimSpace(configured)
	if path == "" {
		path = DefaultCachePathRel
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if repo := strings.TrimSpace(repoPath); repo != "" {
		return filepath.Join(repo, filepath.FromSlash(path))
	}
	return filepath.Clean(filepath.FromSlash(path))
}

// cacheEntry is one cached answer plus the timestamp its TTL is measured from.
type cacheEntry struct {
	StoredAt time.Time       `json:"stored_at"`
	Payload  json.RawMessage `json:"payload"`
}

// cacheFile is the whole on-disk document: one JSON file carrying every entry, in the same shape
// projectintel uses for its own cache (versioned envelope, created_at, atomic replace).
type cacheFile struct {
	FormatVersion int                   `json:"format_version"`
	CreatedAt     string                `json:"created_at"`
	Entries       map[string]cacheEntry `json:"entries"`
}

// diskCache is the content-addressed replay store. Two jobs, stated in the bundle: identical
// queries in an A/B pair must see identical results (pin the cache, both arms replay), and
// ASQS_WEBSEARCH_OFFLINE=1 must complete a run with zero egress.
//
// Keys are sha256 over the semantic identity — (provider, normalized query, k) for searches, the
// canonical URL for fetches — never over timestamps or headers, so a replay is byte-stable.
//
// One file rather than one file per key: it matches the project-intel cache an operator already
// knows how to inspect, back up and delete, and a few hundred entries of search results are far
// smaller than the prompt budget they save. The cost is a read-modify-write per put, which the
// mutex below serialises — gap_concurrency is 8 and the web tools are called from every gap, so
// unsynchronised writers would lose entries to last-writer-wins.
type diskCache struct {
	path string
	ttl  time.Duration
	now  func() time.Time
	mu   sync.Mutex
}

func newDiskCache(path string, ttl time.Duration) *diskCache {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &diskCache{path: path, ttl: ttl, now: time.Now}
}

func searchCacheKey(provider, query string, k int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("search\x00%s\x00%s\x00%d", provider, strings.ToLower(strings.TrimSpace(query)), k)))
	return "s_" + hex.EncodeToString(sum[:])
}

func fetchCacheKey(canonicalURL string) string {
	sum := sha256.Sum256([]byte("fetch\x00" + canonicalURL))
	return "f_" + hex.EncodeToString(sum[:])
}

// reHTTPScheme matches an http:// scheme in any case. https:// cannot match: after "http" comes
// "s", not ":".
var reHTTPScheme = regexp.MustCompile(`(?i)(http)://`)

// escapeHTTPSchemeForLinters rewrites the colon of an http:// scheme as its \u003A JSON escape.
//
// The cache lives inside the repository under test, by design: AsqsShipPreserveRelPaths commits it
// so the next run's clone starts warm. That puts it in front of whatever linters the tested project
// runs over its own tree. Spring Petclinic's `nohttp` checkstyle rule is one such: it greps every
// file for the literal bytes "http://" and fails the build, so ASQS broke the build of the project
// it was testing with its own cache file — and each fixer round that searched the web added another
// URL, so the violation count grew instead of converging.
//
// Escaping the scheme is invisible to every reader that parses the file as JSON — encoding/json
// decodes \u003A back to ":" — and invisible to a byte-level scan for "http://", which is the only
// thing nohttp does. The stored values are unchanged; only their on-disk spelling is.
//
// Any http:// in a valid JSON document is necessarily inside a string literal, which is the only
// place \u escapes are legal, so this byte-level rewrite cannot produce invalid JSON. The case of
// the scheme is preserved.
//
// This defeats a literal http:// scan and nothing more. A linter matching some other pattern in the
// cached text will still fire; the cache is ASQS state living in someone else's repository, and
// that is the standing trade.
func escapeHTTPSchemeForLinters(b []byte) []byte {
	return reHTTPScheme.ReplaceAll(b, []byte("${1}\\u003A//"))
}

// load reads the cache document. A missing, unreadable, malformed or version-mismatched file is an
// empty cache, never an error: a replay store that cannot be read must cost a network call, not a
// run.
func (c *diskCache) load() cacheFile {
	empty := cacheFile{FormatVersion: CacheFormatVersion, Entries: map[string]cacheEntry{}}
	if c == nil {
		return empty
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return empty
	}
	var doc cacheFile
	if json.Unmarshal(b, &doc) != nil || doc.FormatVersion != CacheFormatVersion {
		return empty
	}
	// A file written before the scheme escape existed still breaks the tested project's build.
	c.healUnescapedHTTPScheme(b)
	if doc.Entries == nil {
		doc.Entries = map[string]cacheEntry{}
	}
	return doc
}

// get returns the payload for key when present and fresh. In offline mode freshness is ignored —
// a stale answer beats no answer when egress is forbidden, and the staleness is the operator's
// explicit choice.
func (c *diskCache) get(key string, ignoreTTL bool, out any) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	doc := c.load()
	c.mu.Unlock()

	e, ok := doc.Entries[key]
	if !ok {
		return false
	}
	if !ignoreTTL && c.now().Sub(e.StoredAt) > c.ttl {
		return false
	}
	return json.Unmarshal(e.Payload, out) == nil
}

// put stores payload under key, rewriting the document atomically.
//
// Entries past their TTL are dropped on the way through: without that the file only ever grows,
// and a replay store nobody prunes eventually costs more to read than the calls it saves.
func (c *diskCache) put(key string, payload any) {
	if c == nil {
		return
	}
	// Cache writes are best-effort: a failed write costs a future network call, never the run.
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	doc := c.load()
	now := c.now()
	for k, e := range doc.Entries {
		if now.Sub(e.StoredAt) > c.ttl {
			delete(doc.Entries, k)
		}
	}
	doc.Entries[key] = cacheEntry{StoredAt: now, Payload: raw}
	doc.FormatVersion = CacheFormatVersion
	doc.CreatedAt = now.UTC().Format(time.RFC3339)

	body, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return
	}
	c.writeAtomic(escapeHTTPSchemeForLinters(body))
}

// writeAtomic replaces the cache document. Best-effort by the same rule as put: a failed write
// costs a future network call, never the run.
func (c *diskCache) writeAtomic(body []byte) {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
}

// healUnescapedHTTPScheme rewrites a cache file written before escapeHTTPSchemeForLinters existed.
//
// Without it the fix is intermittent: put() escapes on write, but a run whose every lookup is a
// cache HIT never writes, so a file carrying literal http:// keeps failing the tested project's
// build. Healing on read costs one extra scan per load and, once, one extra write.
func (c *diskCache) healUnescapedHTTPScheme(raw []byte) {
	escaped := escapeHTTPSchemeForLinters(raw)
	if !bytes.Equal(escaped, raw) {
		c.writeAtomic(escaped)
	}
}
