// Package tools exposes the code index to the model as read-only tools.
//
// Generation is otherwise one-shot: retrieval assembles a context and the model gets a single turn.
// When retrieval misses — and measured against a labelled suite it misses roughly half the relevant
// chunks — the model has no way to ask for what it needs and invents a plausible signature instead.
// These tools are that missing channel.
//
// Everything here is READ-ONLY. Writes stay with PerGapWrite and its path locking; that boundary
// does not move, and no tool in this package shells out.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/asqs/asqs-core/internal/websearch"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// Names of the tools, so callers and tests refer to one spelling.
const (
	ToolGetSymbol     = "get_symbol"
	ToolExpandSymbol  = "expand_symbol"
	ToolSearchCode    = "search_code"
	ToolFindTestsFor  = "find_tests_for"
	ToolReadFileRange = "read_file_range"
)

// MetaReader is the metadata surface the tools need.
type MetaReader interface {
	ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*metadata.Symbol, error)
	GetSymbolByID(ctx context.Context, repoID, id string) (*metadata.Symbol, error)
	GetEdgesTo(ctx context.Context, repoID, calleeSymbolID string) ([]*metadata.Edge, error)
	ExpandGraph(ctx context.Context, repoID, startID string, opt metadata.ExpandGraphOptions) ([]metadata.ExpandRow, error)
}

// ChunkReader is the embeddings surface the tools need.
type ChunkReader interface {
	List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error)
	Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error)
	SearchLexical(ctx context.Context, query string, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error)
}

// ToolInvoker is the surface the loop needs: what tools exist, and how to call one. *Registry
// satisfies it; tests substitute their own.
type ToolInvoker interface {
	Definitions() []model.ToolDefinition
	Invoke(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// Registry binds the tools to one repository. Every tool is repo-scoped through RepoID; a tool that
// could read another repository's index would be a data-leak surface, not merely a bug.
type Registry struct {
	Meta     MetaReader
	Chunks   ChunkReader
	Embedder model.Embedder // optional; without it search_code falls back to the lexical channel
	RepoID   string
	Lang     string
	// RepoRoot bounds read_file_range. Empty means the tool is unavailable rather than unbounded.
	RepoRoot string
	// MaxChars caps every tool's output. 0 uses DefaultMaxChars.
	MaxChars int
	// ThirdPartySurface, when set, answers a get_symbol miss with member signatures resolved from
	// the project's build classpath. The symbol index covers only this repository's sources, and
	// the questions a fix round actually asks are about DEPENDENCIES — upstream measured a run that
	// missed on three third-party types and then guessed one nonexistent method after another. The
	// injected implementation may shell out (javap); this package still never execs.
	ThirdPartySurface func(ctx context.Context, fqName string) (string, bool)
	// Web enables the external documentation tools. Nil — the default — registers neither: the
	// first tool that sends data OUT stays absent unless an operator wired it on.
	Web *websearch.Client
	// webURLs is the per-run ledger of URLs searches returned; web_fetch retrieves only members.
	webURLs *websearch.URLLedger
	webMu   sync.Mutex
	// lastTruncated records whether the most recent Invoke cut its result at MaxChars.
	lastTruncated bool
}

// DefaultMaxChars bounds a single tool result.
//
// A tool that returns a 3000-line class body defeats the purpose: it spends more context than the
// pre-loaded chunk it was meant to replace, and the model then has less room for the answer. Every
// tool truncates to this and says so in the output, because silent truncation would have the model
// reason confidently about a body whose second half it never saw.
const DefaultMaxChars = 6000

func (r *Registry) maxChars() int {
	if r.MaxChars > 0 {
		return r.MaxChars
	}
	return DefaultMaxChars
}

// truncate caps s and appends an explicit marker when it had to cut.
func truncate(s string, max int) string {
	out, _ := truncateN(s, max)
	return out
}

// truncateN is truncate, also reporting whether it cut.
//
// The loop records a Truncated flag on every attempt, and it previously reflected only the loop's
// own shared-budget truncation. A result cut by THIS cap reported truncated=false — observed on a
// real run, where a 6047-character result (the 6000 cap plus the marker) was logged as untruncated.
// A drilldown reading that flag would show a complete result where half the body was missing.
func truncateN(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	return s[:max] + fmt.Sprintf("\n… [truncated: %d of %d characters shown]", max, len(s)), true
}

type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// Definitions returns the tool schemas to advertise to the model.
//
// Tools whose dependencies are missing are omitted rather than advertised and failed at call time:
// a model that is told it can read files and then gets an error every time wastes turns and loses
// confidence in the whole tool set.
func (r *Registry) Definitions() []model.ToolDefinition {
	out := r.webDefinitions()
	if r.Meta != nil {
		// The description names what a miss falls back to, because the model demonstrably does not
		// pivot on its own: upstream's run that motivated the fallbacks took a bare "not indexed"
		// miss and spent its remaining turns on repo-only search_code, never touching web_search.
		getSymbolDesc := "Return the source of a symbol by fully-qualified name, with its signature and location. Use this instead of guessing a signature. The index covers this repository's own sources"
		switch {
		case r.ThirdPartySurface != nil:
			getSymbolDesc += "; third-party types are answered from the build classpath when resolvable."
		case r.Web != nil:
			getSymbolDesc += "; for third-party or framework APIs use web_search."
		default:
			getSymbolDesc += " only."
		}
		out = append(out,
			model.ToolDefinition{
				Name:        ToolGetSymbol,
				Description: getSymbolDesc,
				Schema: rawJSON(`{"type":"object","properties":{
					"fq_name":{"type":"string","description":"fully-qualified name, e.g. com.acme.PricingEngine#quote"}},
					"required":["fq_name"]}`),
			},
			model.ToolDefinition{
				Name:        ToolExpandSymbol,
				Description: "List symbols related to this one: callers, callees, or both. Answers \"who calls this?\" and \"what implements this?\".",
				Schema: rawJSON(`{"type":"object","properties":{
					"fq_name":{"type":"string"},
					"direction":{"type":"string","enum":["callers","callees","both"],"description":"default callees"},
					"depth":{"type":"integer","description":"1-5, default 2"},
					"edge_types":{"type":"array","items":{"type":"string"},"description":"optional filter, e.g. [\"CALLS\"]"}},
					"required":["fq_name"]}`),
			},
			model.ToolDefinition{
				Name:        ToolFindTestsFor,
				Description: "Find existing tests that cover a symbol, so an existing test file can be extended rather than duplicated.",
				Schema:      rawJSON(`{"type":"object","properties":{"fq_name":{"type":"string"}},"required":["fq_name"]}`),
			},
		)
	}
	if r.Chunks != nil {
		out = append(out, model.ToolDefinition{
			Name:        ToolSearchCode,
			Description: "Search the indexed code for a pattern or concept, e.g. \"a test that mocks a repository\". Returns matching chunks.",
			Schema: rawJSON(`{"type":"object","properties":{
				"query":{"type":"string"},
				"chunk_type":{"type":"string","description":"optional: test, definition, route, …"},
				"k":{"type":"integer","description":"1-8, default 5"}},
				"required":["query"]}`),
		})
	}
	if strings.TrimSpace(r.RepoRoot) != "" {
		out = append(out, model.ToolDefinition{
			Name:        ToolReadFileRange,
			Description: "Read a line range from a file in the repository.",
			Schema: rawJSON(`{"type":"object","properties":{
				"path":{"type":"string","description":"repo-relative path"},
				"start":{"type":"integer","description":"1-based first line"},
				"end":{"type":"integer","description":"1-based last line, inclusive"}},
				"required":["path","start","end"]}`),
		})
	}
	return out
}

// Invoke dispatches a tool call and returns the text to send back as the tool result.
//
// Errors are returned to the CALLER, not rendered into the result string: the loop decides whether
// a failed tool call is worth reporting to the model, retrying, or aborting on. Rendering "error:
// ..." as a successful result would let a typo look like a finding.
func (r *Registry) Invoke(ctx context.Context, name string, args json.RawMessage) (string, error) {
	// Dispatch re-checks the dependency each tool needs, mirroring Definitions.
	//
	// Advertising and dispatch are NOT the same gate. The tool name comes from the model, and a
	// model can name a tool that was never advertised: the fixer's system prompt lists the whole
	// suite in prose, so a registry built without a chunk store still gets asked for search_code.
	// Dispatching that reached `r.Chunks.SearchLexical` on a nil store and crashed the process —
	// observed live. An unavailable tool must answer with an error the loop can hand back to the
	// model, exactly like an unknown one.
	switch strings.TrimSpace(name) {
	case ToolGetSymbol:
		if r.Meta == nil {
			return "", errToolUnavailable(ToolGetSymbol, "the symbol index")
		}
		return r.getSymbol(ctx, args)
	case ToolExpandSymbol:
		if r.Meta == nil {
			return "", errToolUnavailable(ToolExpandSymbol, "the symbol index")
		}
		return r.expandSymbol(ctx, args)
	case ToolSearchCode:
		if r.Chunks == nil {
			return "", errToolUnavailable(ToolSearchCode, "the chunk index")
		}
		return r.searchCode(ctx, args)
	case ToolFindTestsFor:
		if r.Meta == nil {
			return "", errToolUnavailable(ToolFindTestsFor, "the symbol index")
		}
		return r.findTestsFor(ctx, args)
	case ToolReadFileRange:
		return r.readFileRange(args)
	case ToolWebSearch:
		if r.Web == nil {
			return "", errToolUnavailable(ToolWebSearch, "web access")
		}
		return r.webSearch(ctx, args)
	case ToolWebFetch:
		if r.Web == nil {
			return "", errToolUnavailable(ToolWebFetch, "web access")
		}
		return r.webFetch(ctx, args)
	default:
		return "", fmt.Errorf("tools: unknown tool %q", name)
	}
}

// errToolUnavailable phrases a missing dependency as something the model can act on: it says the
// tool is unavailable this run, so the model stops asking, rather than reading as a transient
// failure worth retrying.
func errToolUnavailable(tool, needs string) error {
	return fmt.Errorf("tools: %s is not available in this run (%s is not configured); do not call it again", tool, needs)
}

func decodeArgs(args json.RawMessage, dst any) error {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(args, dst); err != nil {
		return fmt.Errorf("tools: arguments are not valid JSON for this tool: %w", err)
	}
	return nil
}

// toolTruncated records whether the most recent Invoke had to cut its result to fit MaxChars.
//
// Carried on the registry rather than returned, so the Invoke signature stays a plain
// (string, error) for every caller including the Copilot SDK adapter. Only the loop reads it, and
// only immediately after the call it made.
func (r *Registry) capped(s string) string {
	out, cut := truncateN(s, r.maxChars())
	r.lastTruncated = cut
	return out
}

// LastResultTruncated reports whether the most recent Invoke cut its result at MaxChars.
func (r *Registry) LastResultTruncated() bool { return r.lastTruncated }
