package generator

import (
	"context"
	"fmt"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
)

// LLMDocGenerator generates per-symbol documentation as in-file comments (e.g. Javadoc) to be inserted above the symbol in the source file.
// Provider-agnostic: works with any ChatCompleter (OpenAI, Anthropic, etc.); response is normalized with extractCodeBlockContent.
type LLMDocGenerator struct {
	LLM    model.ChatCompleter
	Prompt string // optional system prompt for documentation style
}

const defaultDocPrompt = `You are an expert technical writer. Generate in-file documentation for the given class or method that will be inserted directly above the symbol in the source file.
The user message may include repository context with **example test code** (describe/it/expect, Playwright, etc.) for retrieval only. **Ignore that for your output:** you must not paste, wrap, or “document” tests as the answer. Output **only** API/symbol documentation as comments.
For Java: output only a Javadoc block (/** ... */). Document purpose, parameters (@param), return value (@return), checked exceptions (@throws), and key behavior. One line per tag, no extra code or markdown.
For C#: output only an XML doc block (/// <summary>...</summary>, /// <param>, /// <returns>). No extra code or markdown.
For JavaScript/TypeScript: output only a JSDoc/TSDoc block (/** ... */). Use @param for parameters, @returns for return value, @async for async functions. Follow existing JSDoc/TSDoc conventions in the codebase when visible. Place the block so it will sit one line above the "export const ..." or declaration line. No extra code or markdown.
Output only the comment block, nothing else. The block will be placed immediately above the symbol declaration.`

// GenerateDoc implements DocGenerator. Returns the in-file doc comment (e.g. Javadoc) and an empty path; the orchestrator sets Path to the source file and InsertAboveLine from the symbol.
func (g *LLMDocGenerator) GenerateDoc(ctx context.Context, item *retrieval.TestPlanItem, contextStr string) (content string, path string, err error) {
	if g.LLM == nil {
		return "", "", fmt.Errorf("llm doc generator: ChatCompleter required")
	}
	system := g.Prompt
	if system == "" {
		system = defaultDocPrompt
	}
	system = appendSkillPack(system, "Documentation skill pack:", docsSkillPack())
	messages := []model.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: contextStr},
	}
	result, err := g.LLM.Complete(ctx, messages, model.CompleteOptions{MaxTokens: 4096})
	if err != nil {
		return "", "", err
	}
	content = NormalizeGeneratedDocContent(result.Content)
	// Path is set by the orchestrator to the source file; return empty here.
	return content, "", nil
}
