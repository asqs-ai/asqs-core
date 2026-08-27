package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// Dependency doc ingestion (Spec B55): the same "what does this framework annotation do" need B54
// serves, answered from a LOCAL index with three properties web search cannot have — deterministic,
// exact for the RESOLVED version, and air-gapped.
//
// Everything here reads what the project's own build already put on disk: Maven `-sources.jar`
// files in the local repository, the XML documentation files NuGet ships beside assemblies, and
// the `.d.ts` type declarations in node_modules. NO subprocess and NO network — apisurface's
// `mvn dependency:build-classpath` approach is right for the fixer's live classpath and wrong
// here, where indexing runs in containers that may carry neither Maven nor network access.
//
// Direct dependencies only, from the manifests. Transitive closure multiplies chunk count by an
// order of magnitude for docs mostly nobody asked about, and the caps below are the review focus:
// tens of thousands of low-value chunks in the same vector space is the fastest way to undo
// B04–B10.

// DependencyDocOptions bounds the ingestion. Zero value = disabled.
type DependencyDocOptions struct {
	Enabled bool
	// MaxChunksPerDependency caps one dependency's contribution. 0 = 80 (raised from 40:
	// Playwright's public API alone exceeds 40 types and was clipped on a live run).
	MaxChunksPerDependency int
	// MaxChunksTotal caps the whole ingestion. 0 = 400.
	MaxChunksTotal int
	// MavenRepoDir overrides ~/.m2/repository (tests). Empty = default.
	MavenRepoDir string
	// NuGetPackagesDir overrides the NuGet global packages folder (tests). Empty = NUGET_PACKAGES
	// or ~/.nuget/packages.
	NuGetPackagesDir string
}

func (o DependencyDocOptions) perDep() int {
	if o.MaxChunksPerDependency > 0 {
		return o.MaxChunksPerDependency
	}
	return 80
}

func (o DependencyDocOptions) total() int {
	if o.MaxChunksTotal > 0 {
		return o.MaxChunksTotal
	}
	return 400
}

// ChunkTypeDependencyDoc marks ingested dependency documentation. Retrieval profiles enumerate
// chunk types as an allowlist, so this type is invisible to similar-test retrieval by
// construction; search_code excludes it unless asked (ExcludeChunkType), and get_symbol falls
// back to it on a repository-symbol miss. Canonical constant lives in embeddings.
const ChunkTypeDependencyDoc = embeddings.ChunkTypeDependencyDoc

// depDoc is one ingestable documentation chunk before embedding.
type depDoc struct {
	Coordinate string // "org.springframework:spring-test:6.2.1" / "xunit@2.9.0" / "react@18.3.1"
	Source     string // "maven" | "nuget" | "npm"
	FQName     string // best-effort symbol identity: "org.springframework.test.context.TestContext"
	Content    string
}

// DependencyDocStats reports what one ingestion did, for the audit event.
type DependencyDocStats struct {
	DepsScanned  int
	DepsIngested int
	Chunks       int
	NoArtifact   int
	Capped       bool
	// IngestedCoordinates / NoArtifactCoordinates name the dependencies behind the counts: a log
	// reader diagnosing "N without local artifact" needs WHICH ones without re-deriving the
	// manifest against a clone that no longer exists. Direct dependencies only, so the lists stay
	// small by construction.
	IngestedCoordinates   []string
	NoArtifactCoordinates []string
}

// ingestDependencyDocs resolves, extracts, embeds and stores dependency docs for one run.
// Idempotent per dependency: each dependency's chunks live under one virtual file
// (dep://<source>/<coordinate>), deleted before re-insert — the same delete-then-insert shape the
// real files use.
func ingestDependencyDocs(ctx context.Context, emb EmbeddingsWriter, opts RunOptions, dd DependencyDocOptions, chunkCfg ChunkConfig) (DependencyDocStats, error) {
	var stats DependencyDocStats
	if !dd.Enabled || emb == nil || opts.Embedder == nil {
		return stats, nil
	}
	type resolved struct {
		coordinate, source string
		docs               []depDoc
	}
	var all []resolved

	for _, c := range parsePomDirectDeps(opts.RepoPath) {
		stats.DepsScanned++
		docs := javaDocsForCoord(c, dd.mavenRoot())
		if len(docs) == 0 {
			stats.NoArtifact++
			stats.NoArtifactCoordinates = append(stats.NoArtifactCoordinates, c.coordinate())
			continue
		}
		all = append(all, resolved{c.coordinate(), "maven", docs})
	}
	for _, c := range parseCsprojPackageRefs(opts.RepoPath) {
		stats.DepsScanned++
		docs := csharpDocsForPackage(c, dd.nugetRoot())
		if len(docs) == 0 {
			stats.NoArtifact++
			stats.NoArtifactCoordinates = append(stats.NoArtifactCoordinates, c.id+"@"+c.version)
			continue
		}
		all = append(all, resolved{c.id + "@" + c.version, "nuget", docs})
	}
	for _, name := range parsePackageJSONDeps(opts.RepoPath) {
		stats.DepsScanned++
		coord, docs := tsDocsForPackage(opts.RepoPath, name)
		if len(docs) == 0 {
			stats.NoArtifact++
			// No installed package means no resolved version; the manifest name is the best
			// identity there is.
			stats.NoArtifactCoordinates = append(stats.NoArtifactCoordinates, name)
			continue
		}
		all = append(all, resolved{coord, "npm", docs})
	}

	total := 0
	for _, r := range all {
		if total >= dd.total() {
			stats.Capped = true
			break
		}
		docs := r.docs
		if len(docs) > dd.perDep() {
			docs = docs[:dd.perDep()]
			stats.Capped = true
		}
		if remaining := dd.total() - total; len(docs) > remaining {
			docs = docs[:remaining]
			stats.Capped = true
		}
		virtualFile := "dep://" + r.source + "/" + r.coordinate
		// Delete-then-insert keeps reindexing idempotent, exactly as real files do.
		if _, err := emb.DeleteByFile(ctx, opts.RepoID, virtualFile); err != nil {
			return stats, fmt.Errorf("dependency docs: delete %s: %w", virtualFile, err)
		}
		toEmbed := make([]*ChunkToEmbed, 0, len(docs))
		for _, d := range docs {
			meta, _ := json.Marshal(map[string]string{
				"coordinate":        d.Coordinate,
				"dependency_source": d.Source,
				"fq_name":           d.FQName,
				"simple_name":       simpleNameOf(d.FQName),
			})
			toEmbed = append(toEmbed, &ChunkToEmbed{
				Content:      Sanitize(d.Content, DefaultSanitizeOptions()),
				File:         virtualFile,
				Lang:         langOfSource(r.source),
				ChunkType:    ChunkTypeDependencyDoc,
				RepoID:       opts.RepoID,
				MetadataJSON: meta,
			})
		}
		chunks, _, err := embedChunksWithFallback(ctx, opts.Embedder, toEmbed, chunkCfg, nil)
		if err != nil {
			return stats, fmt.Errorf("dependency docs: embed %s: %w", r.coordinate, err)
		}
		if _, err := emb.InsertChunks(ctx, chunks); err != nil {
			return stats, fmt.Errorf("dependency docs: insert %s: %w", r.coordinate, err)
		}
		stats.DepsIngested++
		stats.IngestedCoordinates = append(stats.IngestedCoordinates, r.coordinate)
		stats.Chunks += len(chunks)
		total += len(chunks)
	}
	return stats, nil
}

func langOfSource(source string) string {
	switch source {
	case "maven":
		return "java"
	case "nuget":
		return "csharp"
	default:
		return "typescript"
	}
}

func simpleNameOf(fq string) string {
	fq = strings.TrimSpace(fq)
	if i := strings.LastIndexAny(fq, "./"); i >= 0 && i+1 < len(fq) {
		return fq[i+1:]
	}
	return fq
}

func (o DependencyDocOptions) mavenRoot() string {
	if o.MavenRepoDir != "" {
		return o.MavenRepoDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".m2", "repository")
}

func (o DependencyDocOptions) nugetRoot() string {
	if o.NuGetPackagesDir != "" {
		return o.NuGetPackagesDir
	}
	if env := strings.TrimSpace(os.Getenv("NUGET_PACKAGES")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nuget", "packages")
}
