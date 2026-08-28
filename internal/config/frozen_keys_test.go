package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// frozenKeys are the YAML keys CP37 turned into constants at their consumers. Each entry is the
// leaf key name as it appeared in v1 YAML, mapped to the Go field name it used to have.
//
// The value is what makes the second direction of this check possible: a name-based lint cannot tell
// a config field from an identically named field somewhere else, but it CAN tell that a struct field
// with this exact name is back inside config.Config — which is the shape an accidental un-freeze
// takes. Both directions matter, and both have bitten upstream.
var frozenKeys = map[string]string{
	// indexer.chunk.* — the whole block. It was not merely un-tuned in core, it was INERT: the
	// pipeline never set indexer.RunOptions.ChunkConfig, so run.go always fell back to
	// DefaultChunkConfig() and every one of these keys did nothing when set.
	"min_tokens":              "MinTokens",
	"max_tokens":              "MaxTokens",
	"chars_per_token":         "CharsPerToken",
	"enrich_chunk_content":    "EnrichChunkContent",
	"max_chunk_header_runes":  "MaxChunkHeaderRunes",
	"merge_small_symbols":     "MergeSmallSymbols",
	"enable_secondary_chunks": "EnableSecondaryChunks",

	// indexer.* — scheduling-adjacent and container tuning.
	"jst_jsonl_out":                  "JSTJsonlOut",
	"docker_network":                 "DockerNetwork",
	"docker_cpus":                    "DockerCPUs",
	"overview_max_completion_tokens": "OverviewMaxCompletionTokens",
	"max_chunks_per_dependency":      "MaxChunksPerDependency",
	"max_chunks_total":               "MaxChunksTotal",

	// retrieval.context_compact.* — everything except the on/off switch, which stays.
	"max_non_target_chunk_runes":   "MaxNonTargetChunkRunes",
	"max_boilerplate_scan_runes":   "MaxBoilerplateScanRunes",
	"merge_same_file_dependencies": "MergeSameFileDependencies",
	"dedupe_import_boilerplate":    "DedupeImportBoilerplate",

	// retrieval.* — the GLOBAL budgets. profile_budgets is deliberately NOT frozen: it is the
	// per-profile override that still lets these be tuned where tuning is meaningful, and it carries
	// its own identically named leaf keys, which is why this check compares Go field names on
	// config.Config rather than YAML keys alone.
	"similar_mmr_lambda": "SimilarMMRLambda",

	// runner.policy.project_intel.* — scan shape.
	"max_total_runes":       "MaxTotalRunes",
	"max_doc_files":         "MaxDocFiles",
	"max_skill_files":       "MaxSkillFiles",
	"min_relevance_score":   "MinRelevanceScore",
	"summarize_above_runes": "SummarizeAboveRunes",
	"cache_enabled":         "CacheEnabled",
	"force_refresh":         "ForceRefresh",
	"fingerprint_mode":      "FingerprintMode",
}

// The three global retrieval budgets and cache_path are NOT checked in the template direction, and
// under v2 they cannot be. `max_similar_tests`, `max_dependency_chunks` and `max_fixtures` are live
// leaf keys of retrieval.profile_budgets — the per-profile override that survived the freeze — so a
// line-oriented search for those names matches valid configuration. `cache_path` has no v2 spelling
// at all.
//
// Nothing is lost by dropping them: CP38 made the loader STRICT, so a genuinely frozen key in a
// template now fails the load and names its own path. The template check below is no longer the only
// thing standing between a stale key and silence — it is a clearer message, and a way to check files
// the loader is never pointed at.

// A frozen key must not come back as a struct field on Config.
//
// This is the direction that catches a merge or a port re-introducing a knob that was deliberately
// removed. It fires on the Go field NAME, so it is exact for every key whose name is unique to its
// old home — which is all of frozenKeys, by construction; the two ambiguous groups are excluded
// above and covered by the template direction instead.
func TestFrozenKeysAreNotStructFieldsAgain(t *testing.T) {
	live := map[string]string{}
	walkStructFields(reflect.TypeOf(Config{}), map[reflect.Type]bool{}, func(owner, field string) {
		live[field] = owner
	})
	var back []string
	for yamlKey, goField := range frozenKeys {
		if owner, ok := live[goField]; ok {
			back = append(back, yamlKey+" ("+owner+"."+goField+")")
		}
	}
	if len(back) > 0 {
		sort.Strings(back)
		t.Errorf("these keys were frozen into constants in CP37 but are struct fields again:\n  %s\n\n"+
			"Re-introducing one means an operator can set a value the consumer's constant then "+
			"overrides, or worse, that the constant moved. Wire it properly or leave it frozen.",
			strings.Join(back, "\n  "))
	}
}

// A frozen key must not appear in a shipped YAML template.
//
// CP37 wrote this when unknown keys parsed leniently and a leftover frozen key was silently ignored,
// leaving a file that told an operator setting it did something. CP38's strict loader closed that
// hole — such a key now fails the load outright — so this check survives for the better message and
// for YAML the loader is never pointed at.
func TestFrozenKeysAbsentFromShippedYAML(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	var all []string
	for k := range frozenKeys {
		all = append(all, k)
	}

	files := []string{filepath.Join(root, "config.example.yaml")}
	testdata, err := filepath.Glob(filepath.Join(root, "internal", "config", "testdata", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, testdata...)

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		for _, key := range all {
			// Match the key in YAML position — start of a line after optional indentation, followed
			// by a colon — so prose in a comment does not trip the check.
			re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*:`)
			if re.Match(b) {
				t.Errorf("%s sets %q, which CP37 froze into a constant; the loader ignores it silently, "+
					"so the line reads as a working knob and is not one — delete it", rel, key)
			}
		}
	}
}

// The frozen values themselves. A freeze is only honest if the constant equals what the key used to
// resolve to; changing one of these is a behaviour change wearing a refactor's clothes.
func TestFrozenValuesAreUnchanged(t *testing.T) {
	var pi ProjectIntelConfig
	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"project_intel.max_total_runes", pi.EffectiveMaxTotalRunes(), 12000},
		{"project_intel.max_doc_files", pi.EffectiveMaxDocFiles(), 12},
		{"project_intel.max_skill_files", pi.EffectiveMaxSkillFiles(), 8},
		{"project_intel.min_relevance_score", pi.EffectiveMinRelevanceScore(), 0.08},
		{"project_intel.summarize_above_runes", pi.EffectiveSummarizeAboveRunes(), 6000},
		{"project_intel.cache_enabled", pi.EffectiveCacheEnabled(), true},
		{"project_intel.cache_path", pi.EffectiveCachePath(), ".asqs/project-intel-cache.json"},
		{"project_intel.fingerprint_mode", pi.EffectiveFingerprintMode(), "stat"},
		{"websearch.cache_path", WebSearchConfig{}.EffectiveCachePath(), ".asqs/websearch-cache.json"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s is now %v, was %v — freezing must preserve the value byte-for-byte", c.name, c.got, c.want)
		}
	}
}

// ConfigFingerprintHash keys the project-intel cache. Five of its inputs are frozen constants now,
// and they are still folded in for exactly that reason: dropping them would change the hash and
// cold-start every .asqs/project-intel-cache.json in the field, invisibly, as a side effect of a
// refactor. The hash below is the pre-freeze value for a default config.
func TestProjectIntelFingerprintSurvivedTheFreeze(t *testing.T) {
	got := ProjectIntelConfig{}.ConfigFingerprintHash()
	// Computed independently from the PRE-freeze formula and its pre-freeze default values, so this
	// is evidence the freeze preserved the hash rather than a re-recording of whatever it became.
	const wantPreFreeze = "abc01acf12f24b9619c11d62739cb5f077ac5b453718572d4c7fb759fef90714"
	if got != wantPreFreeze {
		t.Errorf("project-intel fingerprint changed to %s; every cached scan is invalidated", got)
	}
	if len(got) != 64 {
		t.Errorf("fingerprint is not a sha256 hex string: %q", got)
	}
}
