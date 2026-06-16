package retrieval

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// FormatOptions control how retrieval context is rendered for the LLM.
type FormatOptions struct {
	// Section headers (markdown ##). Empty = use defaults.
	SectionPrefix string // e.g. "## " for markdown
	// Max content length per chunk (chars); 0 = no limit.
	MaxChunkChars int
	// Include sections (all true = include everything).
	IncludeTargetMethod  bool
	IncludeTargetClass   bool
	IncludeDependencies  bool
	IncludeDomainModels  bool
	IncludeSymbolTable   bool
	IncludeSimilarTests  bool
	IncludeRelatedChunks bool // api_contract / profile extras (no symbol row)
	IncludeFixtures      bool
	IncludeConfig        bool
	// TestFramework is the detected JS/TS test framework (e.g. "mocha", "jest", "jasmine"). When set, the context instructs the LLM to use this framework only.
	TestFramework string
	// E2EFramework is the detected Playwright/Cypress stack; used when the plan item Layer is e2e.
	E2EFramework string
	// DocGeneration when true: build user message for per-symbol documentation (JSDoc/TSDoc/XML), not test generation.
	// Intro and section titles avoid "generate tests" / describe-it-expect instructions that contradict the doc system prompt.
	DocGeneration bool
	// UseStructuredTestJSON when true, OutputContractBlock requires a single JSON object (path → full test file content) for test/E2E generation. Ignored when DocGeneration is true. Orchestrator sets false when appending to an existing test file.
	UseStructuredTestJSON bool
	// ContextCompact is populated from config (retrieval.context_compact) by the orchestrator. When Enabled (default on when enabled key omitted in YAML), the workflow compacts each item's RetrievalContext once before parallel test/doc generation.
	ContextCompact ContextCompactOptions
}

// DefaultFormatOptions returns options that include all sections with markdown headers.
func DefaultFormatOptions() FormatOptions {
	return FormatOptions{
		SectionPrefix:        "## ",
		IncludeTargetMethod:  true,
		IncludeTargetClass:   true,
		IncludeDependencies:  true,
		IncludeDomainModels:  true,
		IncludeSymbolTable:   true,
		IncludeSimilarTests:  true,
		IncludeRelatedChunks: true,
		IncludeFixtures:      true,
		IncludeConfig:        true,
	}
}

// BuildLLMContextForGap builds a full LLM context string for one test-plan item: a short intro (gap reason, target symbol)
// followed by the formatted retrieval context. Use this when generating tests or docs for a single gap.
func BuildLLMContextForGap(item *TestPlanItem, opts FormatOptions) string {
	if item == nil || item.Context == nil {
		return ""
	}
	var intro strings.Builder
	if item.Gap != nil && item.Gap.Symbol != nil {
		symKind := strings.TrimSpace(item.Gap.Symbol.Kind)
		isE2E := IsE2EPlanItem(item)
		if isE2E {
			if opts.DocGeneration {
				intro.WriteString("Write **in-file documentation only** (e.g. **Javadoc** `/** … */` on Java controllers/routes, **JSDoc/TSDoc** on JS/TS, **XML** `///` on C#) for the following E2E-related target. ")
				intro.WriteString("Do **not** output Playwright, Cypress, Jest, Vitest, JUnit test bodies, or other runnable test suites—no `describe`, `it`, `test(`, `expect`, or spec files.\n\n")
				intro.WriteString(fmt.Sprintf("Gap reason: %s. ", item.Gap.Reason))
				intro.WriteString(fmt.Sprintf("Target: %s (%s) in %s.", item.Gap.Symbol.FQName, item.Gap.Symbol.Kind, item.Gap.Symbol.File))
				if symKind == "API_ROUTE" {
					intro.WriteString(" Summarize the HTTP surface (method, path shape, purpose) in prose inside the comment block only. ")
				}
				if symKind == "PAGE_ROUTE" {
					intro.WriteString(" Summarize the route’s role and parameters in prose inside the comment block only. ")
				}
			} else {
				symLang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
				fw := strings.TrimSpace(opts.E2EFramework)
				if fw == "" {
					fw = DefaultE2EFrameworkForLang(item.Gap.Symbol.Lang)
				}
				intro.WriteString("Generate or extend **end-to-end** tests for the following target. ")
				intro.WriteString(fmt.Sprintf("Gap reason: %s. ", item.Gap.Reason))
				intro.WriteString(fmt.Sprintf("Target: %s (%s) in %s.", item.Gap.Symbol.FQName, item.Gap.Symbol.Kind, item.Gap.Symbol.File))
				if symKind == "API_ROUTE" {
					intro.WriteString(" This is an **HTTP entrypoint** that still needs an integration or E2E flow (e.g. Spring MockMvc / WebTestClient / RestAssured, Nest/supertest, or UI automation that drives this API). ")
				}
				if symKind == "PAGE_ROUTE" {
					intro.WriteString(" This is a **client-side route** (e.g. React Router / Angular Router). Add or extend a browser E2E spec that visits this path. ")
				}
				intro.WriteString(fmt.Sprintf("\n\n**Detected E2E stack: %s.** Use this stack only (JS/TS: @playwright/test or Cypress; Java: Playwright Java / Selenium / Selenide; .NET: Microsoft.Playwright / Selenium). ", fw))
				if symLang == "javascript" || symLang == "typescript" || symLang == "js" || symLang == "ts" {
					intro.WriteString("**Do not** write Jest/Vitest **unit** tests here (`jest.mock`, `vi.mock`, `test`/`expect` from `@jest/globals`) when the stack is Cypress or Playwright—use that runner’s APIs only. ")
					if strings.EqualFold(fw, "cypress") {
						intro.WriteString("**Paths:** Cypress expects specs under **`cypress/e2e/`** with **`*.cy.ts`** / **`*.cy.tsx`** (not `e2e/*.spec.ts`). ")
					} else {
						intro.WriteString("**Paths:** Playwright expects specs under **`e2e/`** (or `testDir` in config) with **`*.spec.ts`** / **`*.spec.tsx`**—not `cypress/e2e/*.cy.ts`. ")
					}
				}
				if hint := strings.TrimSpace(E2EPromptCanonicalHints(item.Gap.Symbol.Lang, fw)); hint != "" {
					intro.WriteString("\n\n")
					intro.WriteString(hint)
				}
				intro.WriteString("Do not emit unit-test-only patterns unless they belong inside an E2E flow.")
				intro.WriteString(" Prefer **enabled** E2E: do **not** default to @Disabled / @Ignore / test.skip / blanket Assumptions solely because \"Docker/CI/Testcontainers unavailable\"—the runner is set up for E2E (often Docker with browsers). **Last resort only:** if a case truly cannot run, skip a **single** test with a **specific** reason (not a generic CI excuse); prefer method-level skip over disabling a whole class.")
			}
		} else if opts.DocGeneration {
			intro.WriteString("Generate **in-file documentation only** for the following symbol. **Java:** Javadoc `/** … */` with `@param`, `@return`, `@throws` as appropriate. **C#:** XML `///` comments. **JavaScript/TypeScript:** JSDoc/TSDoc `/** … */`. ")
			intro.WriteString("Your answer must be **only** a comment block to insert above the declaration—**no** new class/method bodies, **no** `describe` / `it` / `test(` / `expect` (those apply to JS tests only), **no** test modules.\n\n")
			intro.WriteString(fmt.Sprintf("Gap reason: %s. ", item.Gap.Reason))
			intro.WriteString(fmt.Sprintf("Target: %s (%s) in %s.", item.Gap.Symbol.FQName, item.Gap.Symbol.Kind, item.Gap.Symbol.File))
			intro.WriteString(" Summarize purpose, parameters, return value, checked exceptions, and side effects using the project’s existing doc style when visible in the context below.")
		} else {
			intro.WriteString("Generate unit tests (or documentation) for the following symbol. ")
			intro.WriteString(fmt.Sprintf("Gap reason: %s. ", item.Gap.Reason))
			intro.WriteString(fmt.Sprintf("Target: %s (%s) in %s.", item.Gap.Symbol.FQName, item.Gap.Symbol.Kind, item.Gap.Symbol.File))
			intro.WriteString(" **Quality:** Tests must **execute** the symbol (or call its public API) and assert **outcomes** and **mock interactions**—not merely scan implementation source as text. " +
				"**Examples of bad patterns:** JavaScript/TypeScript — `fs.readFile`/`readFileSync` on the target file + `toContain`/`match` on that string; Java — `Files.readString`/`readAllLines` on the `.java` under test + substring `assertThat`/`assertTrue(str.contains(...))` as the main check. " +
				"Do **not** emit tautological assertions (e.g. `expect(true).toBe(true)`, `assertTrue(true)`, or `assertEquals` with the same literal on both sides as the only assertion). ")
			if opts.TestFramework != "" {
				intro.WriteString(fmt.Sprintf("\n\n**Detected test framework: %s.** Use only this framework for the generated tests (e.g. Mocha: describe/it; Jest: describe/it/expect; Jasmine: describe/it/expect). Do not use a different test framework.", opts.TestFramework))
			}
			lang := strings.ToLower(strings.TrimSpace(item.Gap.Symbol.Lang))
			switch lang {
			case "javascript", "typescript", "js", "ts":
				intro.WriteString(fmt.Sprintf("\n\n**Output placement (critical):** `%s` is **application/source code**. Emit **only** a **standalone test module** for a **different path** (e.g. sibling `*.test.ts` / `*.spec.ts` / `__tests__/*`). Do **not** output the implementation file with `describe` / `it` / `test(` appended; do **not** paste the whole source file into the answer.", item.Gap.Symbol.File))
				intro.WriteString(" **Behavior:** Import/mock modules so you can call the real function with stubbed `strapi`/`prisma`/HTTP and assert return values and call arguments—never substitute string-scans of the source file for real tests. " +
					"For **.tsx** / React components, prefer Testing Library: render, query the DOM, assert visible text/roles or mock calls—do not “test” route or file paths by comparing a string to itself.")
			case "csharp", "cs":
				intro.WriteString(fmt.Sprintf("\n\n**Output placement (critical):** `%s` is **production** C# source. Emit a **separate** test class file (e.g. sibling `*Tests.cs` / `*Test.cs` or the path given in the output contract)—do **not** append test methods to the production file.", item.Gap.Symbol.File))
				intro.WriteString(" **Target .NET:** Honor the **Target .NET (build & API surface)** section prepended above (target framework monikers and `LangVersion`). Use only BCL/APIs and C# syntax valid for that target.")
				intro.WriteString(" **Behavior:** Instantiate or call the **public API** under test; use **xUnit** / **NUnit** / **MSTest** and **Moq** (or repo patterns from **Similar tests**) with real `Assert` on outcomes and mock verification—not `File.ReadAllText` on the `.cs` file plus substring checks.")
				intro.WriteString(" **Forbidden — pointless tests:** Do not write tests that only use **reflection** (`Type.GetMethod`, `GetParameters`, `typeof(X)` + `Assert.NotNull` without executing logic) to assert that a method or type “exists”. The compiler already guarantees that. Prefer tests that **invoke** the implementation (or a small testable seam) with controlled inputs and assert **return values**, **exceptions**, or **mock.Verify**.")
			case "java":
				src := filepath.ToSlash(strings.TrimSpace(item.Gap.Symbol.File))
				intro.WriteString("\n\n**Java — default stack:** Prefer **JUnit 5** (`@Test`, `org.junit.jupiter.api.Assertions`) and **Mockito** when **Similar tests** or the repo use them; otherwise match existing JUnit 4 / TestNG style in context.")
				intro.WriteString(fmt.Sprintf("\n\n**Output placement (critical):** `%s` is **production** Java (`src/main/java` or the module’s main sources). Emit a **separate** test class under **`src/test/java`** (or Gradle’s `src/test/java` / `src/testFixtures/java` convention) with the **same package** as the type under test unless the repo uses a different layout shown in **Similar tests**.", src))
				intro.WriteString(" **Never** append `@Test` methods to the production `.java` file. **Behavior:** construct or `@InjectMocks` the class under test, stub collaborators with Mockito, call **public** API, then `Assertions.assert*` / `verify(...)`—not source-file string checks.")
			}
		}
		if !opts.DocGeneration {
			if hint := branchGapInstructionBlock(item.Context); hint != "" {
				intro.WriteString("\n\n")
				intro.WriteString(hint)
			}
		}
		intro.WriteString("\n\n")
	}
	body := intro.String() + BuildLLMContext(item.Context, opts)
	contract := OutputContractBlock(item, opts)
	if strings.TrimSpace(contract) == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return contract
	}
	return body + "\n\n" + contract
}

// BuildLLMContext turns RetrievalContext into a single, clear text document describing the symbol table,
// dependency graph, and code chunks so the LLM can generate tests or documentation for the target gap.
// When opts.DocGeneration is true, section titles and any future copy steer toward comments-only output.
func BuildLLMContext(rc *RetrievalContext, opts FormatOptions) string {
	if rc == nil {
		return ""
	}
	if opts.SectionPrefix == "" {
		opts.SectionPrefix = "## "
	}

	var b strings.Builder

	// Execution-feedback grounding (optional): stderr / test output with file:line — see failure_localize.go and DOCUMENTATION.md.
	if hint := strings.TrimSpace(rc.FailureHint); hint != "" {
		b.WriteString(opts.SectionPrefix)
		b.WriteString("Recent execution failure (stderr / test output)\n\n")
		b.WriteString("This excerpt cites **file:line** locations. Large **dependency**, **similar**, **fixture**, and **config** chunks below may use **[ERROR-LOCALIZED CONTEXT]** windows around those lines; **target method** and **enclosing class** sections stay full. Use this as soft grounding for generation.\n\n")
		r := []rune(hint)
		if len(r) > failureHintExcerptRunes {
			hint = string(r[:failureHintExcerptRunes]) + "\n... [truncated]"
		}
		b.WriteString("```\n")
		b.WriteString(hint)
		b.WriteString("\n```\n\n")
	}

	// --- Target method (under test) ---
	if opts.IncludeTargetMethod && rc.TargetMethod != nil {
		b.WriteString(opts.SectionPrefix)
		if opts.DocGeneration {
			b.WriteString("Target symbol (document this — comments only, not tests)\n\n")
		} else {
			b.WriteString("Target method (generate tests for this)\n\n")
		}
		b.WriteString(symbolTableRowWithSignature(rc.TargetMethod.Symbol))
		b.WriteString("\n\n")
		b.WriteString(chunkBlock(rc.TargetMethod.Chunk, opts.MaxChunkChars))
		b.WriteString("\n\n")
	}

	// --- Enclosing class / component ---
	if opts.IncludeTargetClass && rc.TargetClass != nil && rc.TargetClass.Symbol != nil {
		b.WriteString(opts.SectionPrefix)
		b.WriteString("Enclosing class or component\n\n")
		b.WriteString(symbolTableRow(rc.TargetClass.Symbol))
		b.WriteString("\n\n")
		if rc.TargetClass.Chunk != nil {
			b.WriteString(chunkBlock(rc.TargetClass.Chunk, opts.MaxChunkChars))
			b.WriteString("\n\n")
		}
	}

	// --- Dependency graph: callees with edge types + code ---
	if opts.IncludeDependencies && len(rc.Dependencies) > 0 {
		b.WriteString(opts.SectionPrefix)
		b.WriteString("Dependency graph (symbols used by the target method)\n\n")
		for _, dep := range rc.Dependencies {
			if dep.Symbol == nil {
				continue
			}
			edgeLabel := dep.EdgeType
			if edgeLabel == "" {
				edgeLabel = "uses"
			}
			b.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", dep.Symbol.FQName, edgeLabel, symbolLoc(dep.Symbol)))
			if dep.Depth > 0 || strings.TrimSpace(dep.GraphPath) != "" {
				b.WriteString(fmt.Sprintf("  - provenance: depth=%d", dep.Depth))
				if strings.TrimSpace(dep.GraphPath) != "" {
					b.WriteString(fmt.Sprintf(", path=%s", dep.GraphPath))
				}
				b.WriteString("\n")
			}
			b.WriteString(symbolTableRowWithSignature(dep.Symbol))
			if dep.Chunk != nil {
				b.WriteString("\n")
				b.WriteString(chunkBlock(dep.Chunk, opts.MaxChunkChars))
			}
			b.WriteString("\n\n")
		}
	}

	// --- Domain models + collaborators (types the target uses) ---
	// Split into collaborators (interfaces / injected services to MOCK) and value/domain types (to
	// CONSTRUCT and ASSERT on). Without these the LLM invents type shapes → "cannot find symbol"
	// compile errors. Rows carry the signature so the model sees the exact API to stub/build.
	if opts.IncludeDomainModels && len(rc.DomainModels) > 0 {
		var collaborators, valueTypes []*SymbolChunk
		for _, dm := range rc.DomainModels {
			if dm == nil || dm.Symbol == nil {
				continue
			}
			if isLikelyCollaborator(dm.Symbol) {
				collaborators = append(collaborators, dm)
			} else {
				valueTypes = append(valueTypes, dm)
			}
		}
		writeDomainGroup := func(title, hint string, group []*SymbolChunk) {
			if len(group) == 0 {
				return
			}
			b.WriteString(opts.SectionPrefix)
			b.WriteString(title)
			b.WriteString("\n\n")
			if hint != "" && !opts.DocGeneration {
				b.WriteString(hint)
				b.WriteString("\n\n")
			}
			for _, dm := range group {
				b.WriteString("- ")
				b.WriteString(symbolTableRowWithSignature(dm.Symbol))
				if dm.Chunk != nil {
					b.WriteString("\n")
					b.WriteString(chunkBlock(dm.Chunk, opts.MaxChunkChars))
				}
				b.WriteString("\n\n")
			}
		}
		writeDomainGroup("Collaborators (mock these)",
			"These are the target's dependencies — stub/mock them (Mockito, Moq, jest.mock/vi.mock) and verify interactions; do not hit real implementations.",
			collaborators)
		writeDomainGroup("Domain types (construct real instances, assert on these)",
			"Build real instances of these value/DTO/model types for inputs and assertions; use their actual fields/constructors shown below.",
			valueTypes)
	}

	// --- Symbol table summary (compact view of all symbols above) ---
	if opts.IncludeSymbolTable {
		b.WriteString(opts.SectionPrefix)
		b.WriteString("Symbol table (reference)\n\n")
		b.WriteString("| FQ name | Kind | File | Lines |\n")
		b.WriteString("|--------|------|------|-------|\n")
		seen := make(map[string]bool)
		emitSym := func(s *metadata.Symbol) {
			if s == nil || seen[s.ID] {
				return
			}
			seen[s.ID] = true
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %d–%d |\n", s.FQName, s.Kind, s.File, s.StartLine, s.EndLine))
		}
		if rc.TargetMethod != nil {
			emitSym(rc.TargetMethod.Symbol)
		}
		if rc.TargetClass != nil {
			emitSym(rc.TargetClass.Symbol)
		}
		for _, dep := range rc.Dependencies {
			emitSym(dep.Symbol)
		}
		for _, dm := range rc.DomainModels {
			emitSym(dm.Symbol)
		}
		b.WriteString("\n")
	}

	// --- Similar tests (existing tests for style / patterns) ---
	if opts.IncludeSimilarTests && len(rc.SimilarTests) > 0 {
		b.WriteString(opts.SectionPrefix)
		if opts.DocGeneration {
			b.WriteString("Reference: nearby test or spec code (read only for API usage — do **not** copy, summarize as tests, or output `describe`/`it`/`expect`)\n\n")
		} else {
			b.WriteString("Similar reference chunks (tests / routes / contracts per retrieval profile)\n\n")
		}
		for _, c := range rc.SimilarTests {
			b.WriteString(chunkBlock(c, opts.MaxChunkChars))
			b.WriteString("\n\n")
		}
	}

	// --- Related chunks (e.g. api_contract without symbol) ---
	if opts.IncludeRelatedChunks && len(rc.RelatedChunks) > 0 {
		b.WriteString(opts.SectionPrefix)
		b.WriteString("Related API / client snippets\n\n")
		for _, c := range rc.RelatedChunks {
			b.WriteString(chunkBlock(c, opts.MaxChunkChars))
			b.WriteString("\n\n")
		}
	}

	// --- Fixtures / test helpers ---
	if opts.IncludeFixtures && len(rc.Fixtures) > 0 {
		b.WriteString(opts.SectionPrefix)
		if opts.DocGeneration {
			b.WriteString("Fixtures and helpers (optional context for documenting dependencies)\n\n")
		} else {
			b.WriteString("Fixtures and test helpers\n\n")
		}
		for _, c := range rc.Fixtures {
			if src, reason := chunkRetrievalProvenance(c); src != "" || reason != "" {
				b.WriteString(fmt.Sprintf("- provenance: %s | %s\n", src, reason))
			}
			b.WriteString(chunkBlock(c, opts.MaxChunkChars))
			b.WriteString("\n\n")
		}
	}

	// --- Config (DI, test runner) ---
	if opts.IncludeConfig && len(rc.Config) > 0 {
		b.WriteString(opts.SectionPrefix)
		if opts.DocGeneration {
			b.WriteString("Config snippets (reference for behavior or environment only)\n\n")
		} else {
			b.WriteString("Config (DI / test runner)\n\n")
		}
		for _, c := range rc.Config {
			if src, reason := chunkRetrievalProvenance(c); src != "" || reason != "" {
				b.WriteString(fmt.Sprintf("- provenance: %s | %s\n", src, reason))
			}
			b.WriteString(chunkBlock(c, opts.MaxChunkChars))
			b.WriteString("\n\n")
		}
	}

	return strings.TrimSpace(b.String())
}

// collaboratorNameSuffixes are simple-name endings that strongly imply a behavioral dependency the
// LLM should MOCK rather than construct. Used only for presentation grouping (a misgrouped type is
// still present in context), so a heuristic suffix list is acceptable across Java/C#/TS.
var collaboratorNameSuffixes = []string{
	"Service", "Repository", "Client", "Dao", "Gateway", "Provider", "Mapper", "Manager",
	"Factory", "Handler", "Producer", "Consumer", "Publisher", "Listener", "Store", "Engine",
	"Validator", "Resolver", "Adapter", "Facade", "Strategy", "Broker", "Dispatcher", "Scheduler",
}

// isLikelyCollaborator reports whether a resolved type reads as a dependency to mock (interface kind
// or a service/repository-style name) vs a value/DTO type to construct and assert on.
func isLikelyCollaborator(s *metadata.Symbol) bool {
	if s == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(s.Kind), "interface") {
		return true
	}
	name := s.FQName
	if i := strings.LastIndexAny(name, ".#"); i >= 0 {
		name = name[i+1:]
	}
	for _, suf := range collaboratorNameSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func symbolLoc(s *metadata.Symbol) string {
	if s == nil {
		return ""
	}
	if s.StartColumn != nil && s.EndColumn != nil {
		return fmt.Sprintf("%s:%d:%d–%d:%d", s.File, s.StartLine, *s.StartColumn, s.EndLine, *s.EndColumn)
	}
	return fmt.Sprintf("%s:%d–%d", s.File, s.StartLine, s.EndLine)
}

func symbolTableRow(s *metadata.Symbol) string {
	if s == nil {
		return ""
	}
	if s.StartColumn != nil && s.EndColumn != nil {
		return fmt.Sprintf("Symbol: %s | Kind: %s | File: %s | Lines: %d–%d | Cols: %d–%d\n",
			s.FQName, s.Kind, s.File, s.StartLine, s.EndLine, *s.StartColumn, *s.EndColumn)
	}
	return fmt.Sprintf("Symbol: %s | Kind: %s | File: %s | Lines: %d–%d\n", s.FQName, s.Kind, s.File, s.StartLine, s.EndLine)
}

// symbolTableRowWithSignature formats the symbol and appends Signature/Visibility from signature_json when present so the LLM sees the exact API.
func symbolTableRowWithSignature(s *metadata.Symbol) string {
	if s == nil {
		return ""
	}
	out := symbolTableRow(s)
	if len(s.SignatureJSON) == 0 {
		return out
	}
	var parsed struct {
		Signature  string `json:"signature"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(s.SignatureJSON, &parsed); err != nil {
		return out
	}
	if parsed.Signature != "" {
		out += fmt.Sprintf("Signature: %s\n", strings.TrimSpace(parsed.Signature))
	}
	if parsed.Visibility != "" {
		out += fmt.Sprintf("Visibility: %s\n", strings.TrimSpace(parsed.Visibility))
	}
	return out
}

func chunkBlock(c *embeddings.Chunk, maxChars int) string {
	if c == nil {
		return ""
	}
	content := c.Content
	if maxChars > 0 && len(content) > maxChars {
		content = content[:maxChars] + "\n... (truncated)"
	}
	if content == "" {
		return ""
	}
	return fmt.Sprintf("```\n%s\n```", content)
}

func chunkRetrievalProvenance(c *embeddings.Chunk) (sourceKind, reason string) {
	if c == nil || len(c.MetadataJSON) == 0 {
		return "", ""
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(c.MetadataJSON, &meta); err != nil {
		return "", ""
	}
	if v, ok := meta["retrieval_source_kind"].(string); ok {
		sourceKind = strings.TrimSpace(v)
	}
	if v, ok := meta["retrieval_reason"].(string); ok {
		reason = strings.TrimSpace(v)
	}
	return sourceKind, reason
}

func branchGapInstructionBlock(rc *RetrievalContext) string {
	if rc == nil || rc.ExistingTestCoverage == nil || !rc.ExistingTestCoverage.HasExistingTests {
		return ""
	}
	h := rc.ExistingTestCoverage
	var b strings.Builder
	b.WriteString("**Existing tests detected:** Avoid regenerating already-covered behavior. ")
	if len(h.CoveredIntents) > 0 {
		b.WriteString("Covered intents: ")
		b.WriteString(strings.Join(h.CoveredIntents, ", "))
		b.WriteString(". ")
	}
	if len(h.MissingIntents) > 0 {
		b.WriteString("Focus on missing branch intents: ")
		b.WriteString(strings.Join(h.MissingIntents, ", "))
		b.WriteString(". ")
	} else {
		b.WriteString("No explicit missing branch intents were inferred; prioritize edge/error/boundary scenarios not already in similar tests. ")
	}
	b.WriteString("Prefer extending existing test files when practical.")
	return b.String()
}
