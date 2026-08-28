package websearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The cache belongs to the repository under test and is stored inside it, exactly like
// .asqs/project-intel-cache.json. It is warm within a run and across scheduler reruns of that run;
// a new run clones fresh, so it starts cold — a deliberate trade of cross-run reuse for
// repo-ownership (see DefaultCachePathRel).
func TestResolveCachePath_defaultIsUnderTheRepoUnderTest(t *testing.T) {
	repo := t.TempDir()
	got := ResolveCachePath(repo, "")
	want := filepath.Join(repo, filepath.FromSlash(DefaultCachePathRel))
	if got != want {
		t.Fatalf("default = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, repo) {
		t.Fatalf("cache %q must live inside the repo under test %q", got, repo)
	}
	// Same folder and file-naming discipline as the project-intel cache it sits beside.
	if filepath.Base(filepath.Dir(got)) != ".asqs" || filepath.Ext(got) != ".json" {
		t.Fatalf("default %q must be a .json document directly in .asqs/", got)
	}
}

// A repo-relative configured value is joined with the repo, like projectintel's cacheRel.
func TestResolveCachePath_relativeConfigIsRepoRelative(t *testing.T) {
	repo := t.TempDir()
	got := ResolveCachePath(repo, ".asqs/custom-ws.json")
	if want := filepath.Join(repo, ".asqs", "custom-ws.json"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An ABSOLUTE configured path is honoured as-is: that is the supported way to place the cache
// outside the per-run clone when an operator wants cross-run reuse.
func TestResolveCachePath_absoluteConfigEscapesTheClone(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "shared-websearch-cache.json")
	got := ResolveCachePath(repo, outside)
	if got != filepath.Clean(outside) {
		t.Fatalf("absolute config = %q, want %q", got, outside)
	}
	if strings.HasPrefix(got, repo) {
		t.Fatal("an absolute path must not be re-anchored into the repo")
	}
	if got := ResolveCachePath(repo, "  "+outside+"  "); got != filepath.Clean(outside) {
		t.Errorf("whitespace-padded config not trimmed: %q", got)
	}
}

// No repo path (a caller without a workspace) still yields a usable relative path rather than
// failing or silently disabling the cache.
func TestResolveCachePath_withoutRepoStaysRelative(t *testing.T) {
	if got := ResolveCachePath("", ""); got != filepath.FromSlash(DefaultCachePathRel) {
		t.Fatalf("got %q, want %q", got, DefaultCachePathRel)
	}
}

// Within a run (and across scheduler reruns, which keep the same workspace) a second client
// resolving the same repo must read what the first wrote.
func TestDiskCache_entriesSurviveAcrossClients(t *testing.T) {
	repo := t.TempDir()
	type payload struct{ V string }

	first := newDiskCache(ResolveCachePath(repo, ""), 0)
	first.put(searchCacheKey("brave", "spring boot RuntimeHintsRegistrar test example", 5), payload{V: "results"})

	second := newDiskCache(ResolveCachePath(repo, ""), 0)
	var out payload
	if !second.get(searchCacheKey("brave", "spring boot RuntimeHintsRegistrar test example", 5), false, &out) {
		t.Fatal("a second client resolving the same config must hit the entry the first wrote")
	}
	if out.V != "results" {
		t.Fatalf("payload = %+v", out)
	}
}

// One file, in the same versioned-envelope shape the project-intel cache uses, holding every
// entry — that is what makes it inspectable and deletable the way operators already expect.
func TestDiskCache_singleVersionedJSONFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "websearch-cache.json")
	c := newDiskCache(path, 0)
	c.put(searchCacheKey("brave", "one", 5), []string{"a"})
	c.put(searchCacheKey("brave", "two", 5), []string{"b"})
	c.put(fetchCacheKey("https://docs.spring.io/x"), map[string]string{"c": "d"})

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "websearch-cache.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("want exactly one cache file, got %v", names)
	}

	var doc cacheFile
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("cache file is not valid JSON: %v", err)
	}
	if doc.FormatVersion != CacheFormatVersion {
		t.Errorf("format_version = %d, want %d", doc.FormatVersion, CacheFormatVersion)
	}
	if doc.CreatedAt == "" {
		t.Error("created_at must be stamped, as project-intel does")
	}
	if len(doc.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(doc.Entries))
	}
}

// A format bump, a truncated file, or a missing file is a COLD cache — never an error path that
// could cost a run.
func TestDiskCache_unreadableOrVersionMismatchIsColdNotFatal(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{"garbage", "not json at all"},
		{"wrong version", `{"format_version":999,"entries":{}}`},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			c := newDiskCache(path, 0)
			var out []string
			if c.get(searchCacheKey("brave", "q", 5), false, &out) {
				t.Fatal("a damaged cache must read as empty")
			}
			// And it must still be writable afterwards.
			c.put(searchCacheKey("brave", "q", 5), []string{"fresh"})
			if !c.get(searchCacheKey("brave", "q", 5), false, &out) {
				t.Fatal("a damaged cache must be replaced, not poisoned")
			}
		})
	}
	// A path that was never written is simply a miss.
	if c := newDiskCache(filepath.Join(dir, "nope.json"), 0); c == nil {
		t.Fatal("a not-yet-existing path is still a usable cache")
	}
}

// Expired entries are dropped on write, or the file grows forever.
func TestDiskCache_expiredEntriesArePrunedOnPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "websearch-cache.json")
	c := newDiskCache(path, time.Hour)
	base := time.Now()
	c.now = func() time.Time { return base }
	c.put(searchCacheKey("brave", "old", 5), []string{"stale"})

	c.now = func() time.Time { return base.Add(2 * time.Hour) }
	c.put(searchCacheKey("brave", "new", 5), []string{"fresh"})

	var doc cacheFile
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Entries[searchCacheKey("brave", "old", 5)]; ok {
		t.Error("expired entry survived a later put; the file would grow without bound")
	}
	if _, ok := doc.Entries[searchCacheKey("brave", "new", 5)]; !ok {
		t.Error("fresh entry missing")
	}
}

// Offline replay ignores the TTL: a stale answer beats no answer when egress is forbidden.
func TestDiskCache_offlineIgnoresTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "websearch-cache.json")
	c := newDiskCache(path, time.Hour)
	base := time.Now()
	c.now = func() time.Time { return base }
	c.put(searchCacheKey("brave", "q", 5), []string{"cached"})

	c.now = func() time.Time { return base.Add(72 * time.Hour) }
	var out []string
	if c.get(searchCacheKey("brave", "q", 5), false, &out) {
		t.Fatal("an expired entry must miss when online")
	}
	if !c.get(searchCacheKey("brave", "q", 5), true, &out) {
		t.Fatal("offline replay must ignore the TTL")
	}
}

// Concurrent puts must not lose entries: gap_concurrency is 8 and the web tools run from every
// gap, so a read-modify-write on one shared file needs the mutex to hold.
func TestDiskCache_concurrentPutsKeepEveryEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "websearch-cache.json")
	c := newDiskCache(path, 0)
	const n = 24
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			c.put(searchCacheKey("brave", string(rune('a'+i))+"query", 5), []string{"v"})
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	var doc cacheFile
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Entries) != n {
		t.Fatalf("entries = %d, want %d (concurrent writers lost entries)", len(doc.Entries), n)
	}
}

// Key derivation is the cache's identity; changing it silently invalidates every stored entry.
func TestCacheKeys_areStable(t *testing.T) {
	if got, want := searchCacheKey("brave", "spring boot RuntimeHintsRegistrar test example", 5),
		"s_986e1494d9f1b562cbcb5d5ac30c41da806ca64c5214aaebd0a355d966837e74"; got != want {
		t.Errorf("search key changed:\n got %s\nwant %s", got, want)
	}
	// Normalisation: case and surrounding whitespace must not fork the key.
	if searchCacheKey("brave", "  Spring Boot RuntimeHintsRegistrar Test Example ", 5) !=
		searchCacheKey("brave", "spring boot runtimehintsregistrar test example", 5) {
		t.Error("query normalisation broke")
	}
}
