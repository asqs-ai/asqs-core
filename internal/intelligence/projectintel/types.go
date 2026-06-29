// Package projectintel discovers repo markdown docs, Cursor-style SKILL.md files, OpenAPI specs,
// and SQL schemas; ranks them for generation context; optionally summarises with an LLM; and
// supports a local cross-run cache under .asqs/. Available by default (enabled when the
// ProjectIntelConfig field is absent from the config file).
package projectintel

import (
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// DocKind classifies a discovered path.
type DocKind string

const (
	DocKindDoc    DocKind = "doc"
	DocKindSkill  DocKind = "skill"
	DocKindAPI    DocKind = "api"    // OpenAPI/Swagger YAML files
	DocKindSchema DocKind = "schema" // SQL schema files
)

// Candidate is a matched file with stat metadata for fingerprinting (no body).
type Candidate struct {
	RelPath string
	Kind    DocKind
	Size    int64
	ModTime time.Time
}

// RankedCandidate carries relevance score and optional body for building the snapshot.
// DocEmbedding holds the embedding of this candidate's summary (non-nil when UseEmbeddingsRank
// is true and embedding succeeded).
type RankedCandidate struct {
	Candidate
	Score        float64
	Content      string    // full or truncated read text used for ranking/summary
	DocEmbedding []float32 // embedding of the summary; nil when not computed
}

// Snapshot is the material injected into generation prompts and persisted in the cache file.
type Snapshot struct {
	Markdown    string
	Diagnostics []string
}

// Options are resolved from config + defaults before Run.
type Options struct {
	Enabled             bool
	MaxTotalRunes       int
	MaxDocFiles         int
	MaxSkillFiles       int
	MinRelevanceScore   float64
	SummarizeAboveRunes int
	UseEmbeddingsRank   bool
	ExtraDocGlobs       []string
	ExtraSkillGlobs     []string
	CacheEnabled        bool
	CachePath           string // repo-relative, e.g. .asqs/project-intel-cache.json
	ForceRefresh        bool
	FingerprintMode     string // "stat" or "content"
}

// Input is one project-intel execution.
type Input struct {
	RepoAbs       string
	MonoWorkspace string // normalized repo-relative prefix, empty = whole repo
	Lang          string
	TestFramework string
	E2EFramework  string
	CurrentFiles  []indexer.FileVersion
	Skip          bool   // policy or caller: skip all work
	ConfigFingerprint string // hash of relevant config slice (caller supplies)
	LLM           model.ChatCompleter // optional; nil => extractive only
	// Embedder produces vectors for doc summaries when UseEmbeddingsRank is true.
	Embedder model.Embedder
	Opts     Options
}

// Result carries the snapshot plus audit-friendly counters.
type Result struct {
	Snapshot Snapshot

	// Candidates holds all ranked candidates with their embeddings for per-gap re-ranking.
	// Populated on non-cache runs; use SelectForGap to re-rank for a specific target symbol.
	Candidates []RankedCandidate

	CacheHit  bool
	CachePath string

	ScannedRelPaths        []string
	ScannedRelPathsOmitted int

	FilesScanned      int
	DocsSelected      int
	SkillsSelected    int
	LLMSummarizeCalls int
	Truncations       int
	ApproxRunes       int
	Mode              string // off|lexical|embedding|cache_hit
	FilesFingerprint  string
	RelevanceFingerprint string
	DurationMs        int64
}
