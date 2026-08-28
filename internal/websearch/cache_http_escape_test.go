package websearch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// escapedScheme is the JSON escape written to disk: "http", a backslash, then "u003A//". Built
// from a rune so the literal cannot be mangled by tooling that rewrites escape sequences.
var escapedScheme = "http" + string(rune(92)) + "u003A//"

// The cache lives inside the repository under test (AsqsShipPreserveRelPaths commits it so the next
// run's clone starts warm), which puts it in front of that project's own linters. Spring
// Petclinic's `nohttp` checkstyle rule greps every file for the literal bytes "http://" and fails
// the build — so ASQS broke the build of the project it was testing, with its own cache, and every
// fixer round that searched the web added another URL and another violation.
func TestCache_writesNoLiteralHTTPScheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".asqs", "websearch-cache.json")
	c := newDiskCache(path, time.Hour)

	c.put("s_key", []Result{
		{Title: "How @ModelAttribute works", URL: "http://ankursinghal86.blogspot.com/2014/07/how.html", Snippet: "see http://repo.spring.io/milestone"},
		{Title: "Spring guides", URL: "https://spring.io/guides", Snippet: "already https"},
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("http://")) {
		t.Fatalf("cache file contains the literal scheme nohttp scans for:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(escapedScheme)) {
		t.Fatalf("expected the escaped scheme on disk:\n%s", raw)
	}
	// https:// must be untouched — it cannot contain "http://" as a substring.
	if !bytes.Contains(raw, []byte("https://spring.io/guides")) {
		t.Errorf("https URL was altered:\n%s", raw)
	}
}

// The whole point of the cache is replaying a provider response. Escaping must be invisible to
// every reader that parses the file as JSON.
func TestCache_escapedFileRoundTripsExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := newDiskCache(path, time.Hour)

	want := []Result{
		{Title: "t1", URL: "http://plain.example.com/a?x=1", Snippet: "mentions http://other.example.org/b"},
		{Title: "t2", URL: "https://secure.example.com/c", Snippet: "no scheme here"},
	}
	c.put("s_key", want)

	var got []Result
	if !c.get("s_key", false, &got) {
		t.Fatal("cache miss after put")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result %d round-tripped as %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A second put re-marshals the loaded document; json.RawMessage is emitted verbatim, so an entry
// stored earlier must not regain its literal scheme.
func TestCache_escapeSurvivesLaterWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := newDiskCache(path, time.Hour)

	c.put("s_first", []Result{{URL: "http://first.example.com"}})
	c.put("s_second", []Result{{URL: "http://second.example.com"}})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("http://")) {
		t.Fatalf("a literal scheme reappeared after a second write:\n%s", raw)
	}
	var first, second []Result
	if !c.get("s_first", false, &first) || !c.get("s_second", false, &second) {
		t.Fatal("an entry was lost across writes")
	}
	if first[0].URL != "http://first.example.com" || second[0].URL != "http://second.example.com" {
		t.Errorf("values changed: %q / %q", first[0].URL, second[0].URL)
	}
}

// Without healing on read the fix is intermittent: put() escapes on write, but a run whose every
// lookup is a cache HIT never writes, so a file from before this change keeps failing the build.
func TestCache_healsAFileWrittenBeforeTheEscapeExisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	stale := `{
  "format_version": ` + itoa(CacheFormatVersion) + `,
  "created_at": "2026-08-22T00:00:00Z",
  "entries": {
    "s_key": {
      "stored_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
      "payload": [{"Title":"t","URL":"http://legacy.example.com/x","Snippet":""}]
    }
  }
}`
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newDiskCache(path, time.Hour)

	var got []Result
	if !c.get("s_key", false, &got) {
		t.Fatal("stale cache should still be readable")
	}
	if got[0].URL != "http://legacy.example.com/x" {
		t.Errorf("value changed while healing: %q", got[0].URL)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("http://")) {
		t.Fatalf("a read did not heal the legacy file:\n%s", raw)
	}
}

func TestEscapeHTTPSchemeForLinters(t *testing.T) {
	esc := func(scheme, rest string) string { return scheme + string(rune(92)) + "u003A//" + rest }
	cases := []struct{ in, want string }{
		{`{"u":"http://a.example"}`, `{"u":"` + esc("http", "a.example") + `"}`},
		{`{"u":"https://a.example"}`, `{"u":"https://a.example"}`},               // no substring collision
		{`{"u":"HTTP://a.example"}`, `{"u":"` + esc("HTTP", "a.example") + `"}`}, // case preserved
		{`{"u":"see http://a and http://b"}`, `{"u":"see ` + esc("http", "a") + ` and ` + esc("http", "b") + `"}`},
		{`{"u":"no scheme"}`, `{"u":"no scheme"}`},
	}
	for _, tc := range cases {
		got := string(escapeHTTPSchemeForLinters([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("escape(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
	// Idempotent: healing on every load must not double-escape.
	once := escapeHTTPSchemeForLinters([]byte(`{"u":"http://a.example"}`))
	if twice := escapeHTTPSchemeForLinters(once); !bytes.Equal(once, twice) {
		t.Errorf("not idempotent: %s -> %s", once, twice)
	}
	// The output must remain valid JSON that decodes to the original value.
	var v struct{ U string }
	if err := json.Unmarshal(once, &v); err != nil {
		t.Fatalf("escaped output is not valid JSON: %v", err)
	}
	if v.U != "http://a.example" {
		t.Errorf("decoded %q, want the original URL", v.U)
	}
}

// Cache keys are a hash of the semantic identity, never of the payload, so escaping cannot affect
// hit rate.
func TestCache_keysAreUnaffectedByEscaping(t *testing.T) {
	a := searchCacheKey("brave", "spring nohttp", 5)
	b := searchCacheKey("brave", "spring nohttp", 5)
	if a != b || !strings.HasPrefix(a, "s_") {
		t.Fatalf("search key is not stable: %q vs %q", a, b)
	}
	if f := fetchCacheKey("http://a.example/x"); !strings.HasPrefix(f, "f_") {
		t.Errorf("fetch key shape changed: %q", f)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
