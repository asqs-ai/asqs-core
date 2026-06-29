package projectintel

import (
	"context"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

const summarizeMaxOutRunes = 3500

// readLeadText returns up to maxRunes UTF-8 runes from the start of a file.
func readLeadText(absPath string, maxRunes int) (string, error) {
	b, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	s := string(b)
	r := []rune(s)
	if len(r) > maxRunes {
		s = string(r[:maxRunes])
	}
	return s, nil
}

// SummarizeWithLLM asks the model for a short summary; falls back to extractive on error/empty.
func SummarizeWithLLM(ctx context.Context, llm model.ChatCompleter, title, body string) (string, bool, error) {
	if llm == nil {
		return ExtractiveSummary(body, summarizeMaxOutRunes), false, nil
	}
	prompt := "Summarize the following repository document for an AI that will generate unit tests and in-file API documentation. " +
		"Preserve stack-specific commands and naming (test runners, frameworks, paths). Plain text, bullets allowed, no code fences. " +
		"Max ~120 lines. Title: " + title + "\n\n---\n\n" + truncateRunes(body, 24000)
	msgs := []model.Message{
		{Role: "system", Content: "You compress technical docs. Reply with concise prose only."},
		{Role: "user", Content: prompt},
	}
	res, err := llm.Complete(ctx, msgs, model.CompleteOptions{MaxTokens: 2048})
	if err != nil || res == nil || strings.TrimSpace(res.Content) == "" {
		return ExtractiveSummary(body, summarizeMaxOutRunes), false, err
	}
	out := strings.TrimSpace(res.Content)
	return truncateRunes(out, summarizeMaxOutRunes), true, nil
}

// ExtractiveSummary keeps headings and first lines under maxOut runes.
func ExtractiveSummary(text string, maxOut int) string {
	lines := strings.Split(text, "\n")
	var b strings.Builder
	runes := 0
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "*") {
			if runes+utf8.RuneCountInString(ln)+1 > maxOut {
				break
			}
			b.WriteString(ln)
			b.WriteByte('\n')
			runes += utf8.RuneCountInString(ln) + 1
			continue
		}
		if trim == "" {
			continue
		}
		if runes+utf8.RuneCountInString(ln)+1 > maxOut {
			break
		}
		b.WriteString(ln)
		b.WriteByte('\n')
		runes += utf8.RuneCountInString(ln) + 1
		if runes > maxOut/3 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	return truncateRunes(out, maxOut)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n... [truncated]"
}
