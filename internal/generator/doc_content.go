package generator

import (
	"regexp"
	"strings"
)

// llmDocWrapperTags are XML-ish envelope tags models sometimes emit around an otherwise
// valid comment block (e.g. "<result>…</result>"). They are never valid in source files.
var llmDocWrapperTags = []string{
	"result", "output", "response", "answer", "content", "artifact",
}

var docWrapperCloseAfterBlockRE = regexp.MustCompile(`(?s)(.*\*/)\s*</(?:result|output|response|answer|content|artifact)\s*>\s*$`)

// NormalizeGeneratedDocContent prepares LLM doc output for insertion above a symbol.
// It reuses the markdown fence extractor, then strips common response wrapper tags the
// model may append after a Javadoc/JSDoc/XML doc block.
func NormalizeGeneratedDocContent(s string) string {
	s = strings.TrimSpace(extractCodeBlockContent(s))
	s = stripLLMDocWrapperTags(s)
	return strings.TrimSpace(s)
}

func stripLLMDocWrapperTags(s string) string {
	s = strings.TrimSpace(s)
	for {
		prev := s
		s = stripTrailingDocWrapperLines(s)
		s = stripLeadingDocWrapperLines(s)
		if m := docWrapperCloseAfterBlockRE.FindStringSubmatch(s); m != nil {
			s = strings.TrimSpace(m[1])
		}
		if s == prev {
			return s
		}
	}
}

func stripTrailingDocWrapperLines(s string) string {
	for {
		lines := strings.Split(s, "\n")
		if len(lines) == 0 {
			return ""
		}
		last := strings.TrimSpace(lines[len(lines)-1])
		if !isStandaloneLLMWrapperTagLine(last) {
			return strings.TrimRight(s, "\n\r")
		}
		s = strings.TrimSpace(strings.Join(lines[:len(lines)-1], "\n"))
	}
}

func stripLeadingDocWrapperLines(s string) string {
	for {
		lines := strings.Split(s, "\n")
		if len(lines) == 0 {
			return ""
		}
		first := strings.TrimSpace(lines[0])
		if !isStandaloneLLMWrapperTagLine(first) {
			return strings.TrimLeft(s, "\n\r")
		}
		s = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
}

func isStandaloneLLMWrapperTagLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "<") || !strings.HasSuffix(line, ">") {
		return false
	}
	lower := strings.ToLower(line)
	for _, name := range llmDocWrapperTags {
		switch {
		case lower == "<"+name+">", lower == "</"+name+">":
			return true
		case strings.HasPrefix(lower, "<"+name+" ") && strings.HasSuffix(lower, ">"):
			return true
		case strings.HasPrefix(lower, "</"+name+" ") && strings.HasSuffix(lower, ">"):
			return true
		}
	}
	return false
}
