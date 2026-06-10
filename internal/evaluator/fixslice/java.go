package fixslice

import (
	"strings"
)

// sliceJava returns a signature-only rendering of a .java (or .kt / .kts / .scala) source file.
// Strategy: a character scanner tracks brace depth while honouring string/char literals and
// comments, then decides per line what to emit:
//
//   - depth 0 (top of file): emit package / imports / annotations / class headers verbatim.
//   - depth ≥ 1 with the top frame being a class body: emit fields, nested class headers, and
//     method signatures (with elidedBody). Skip the contents of method bodies entirely.
//   - depth ≥ 1 with the top frame being a method body: skip until the method closes.
//
// Any mismatched brace at EOF makes us fall back to returning the input unchanged so the caller
// keeps shipping the full file (safer than a truncated one).
func sliceJava(body string) string {
	s := newScanner(body, javaPolicy{})
	if !s.run() {
		return body
	}
	return s.out.String()
}

type langPolicy interface {
	isClassLikeHeader(trimmed string) bool
	looksLikeMethod(trimmed string) bool
}

type javaPolicy struct{}

func (javaPolicy) isClassLikeHeader(trimmed string) bool { return isClassLikeHeaderLine(trimmed) }
func (javaPolicy) looksLikeMethod(trimmed string) bool   { return looksLikeJavaMethodLine(trimmed) }

type scanner struct {
	src    string
	out    strings.Builder
	pos    int
	depth  int
	policy langPolicy

	// classFrame[i] is true when depth-frame i is a class/interface/enum/record body; false for
	// method bodies, anonymous blocks, lambdas, etc.
	classFrame []bool

	// pendingClassFrames tracks class headers whose `{` appears on a later line (C# / K&R style).
	// Each unit consumes exactly one of the newly-opened braces on a subsequent line.
	pendingClassFrames int
}

func newScanner(src string, policy langPolicy) *scanner {
	return &scanner{src: src, policy: policy}
}

// run drives the scan. Returns true on success, false if we hit an unmatched brace.
func (s *scanner) run() bool {
	for s.pos < len(s.src) {
		lineStart := s.pos
		lineEnd, braceOpened, braceClosed := s.scanLine(s.pos)
		line := s.src[lineStart:lineEnd]
		s.pos = lineEnd

		inMethodBefore := s.depth > 0 && !s.topIsClass()
		if inMethodBefore {
			s.adjustDepth(braceOpened, braceClosed)
			continue
		}

		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			s.out.WriteString(line)
		case s.policy.isClassLikeHeader(trimmed):
			s.out.WriteString(line)
			// Every brace this line opens (or will open on a later line) is a class-body frame.
			s.pendingClassFrames++
			s.adjustDepth(braceOpened, braceClosed)
			continue
		case s.depth == 0:
			s.out.WriteString(line)
		default:
			if s.policy.looksLikeMethod(trimmed) {
				s.emitMethod(line)
				continue
			}
			s.out.WriteString(line)
		}
		s.adjustDepth(braceOpened, braceClosed)
	}
	return s.depth == 0
}

// scanLine advances from start to the end of the logical line (newline inclusive) and returns the
// end offset plus open/close brace counts on that line. String literals, character literals, and
// both comment styles are respected.
func (s *scanner) scanLine(start int) (end, opened, closed int) {
	i := start
	for i < len(s.src) {
		c := s.src[i]
		switch {
		case c == '\n':
			i++
			return i, opened, closed
		case c == '/' && i+1 < len(s.src) && s.src[i+1] == '/':
			for i < len(s.src) && s.src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s.src) && s.src[i+1] == '*':
			i += 2
			for i+1 < len(s.src) && !(s.src[i] == '*' && s.src[i+1] == '/') {
				i++
			}
			if i+1 < len(s.src) {
				i += 2
			}
		case c == '"':
			i++
			for i < len(s.src) && s.src[i] != '"' {
				if s.src[i] == '\\' && i+1 < len(s.src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(s.src) {
				i++
			}
		case c == '\'':
			i++
			for i < len(s.src) && s.src[i] != '\'' {
				if s.src[i] == '\\' && i+1 < len(s.src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(s.src) {
				i++
			}
		case c == '{':
			opened++
			i++
		case c == '}':
			closed++
			i++
		default:
			i++
		}
	}
	return i, opened, closed
}

func (s *scanner) adjustDepth(opened, closed int) {
	// First, attribute as many newly-opened braces as possible to pending class headers waiting
	// for their `{` on a later line. The remainder open as method/block frames.
	classOpens := 0
	if s.pendingClassFrames > 0 && opened > 0 {
		take := s.pendingClassFrames
		if take > opened {
			take = opened
		}
		classOpens = take
		s.pendingClassFrames -= take
	}
	nonClassOpens := opened - classOpens
	for k := 0; k < classOpens; k++ {
		s.classFrame = append(s.classFrame, true)
	}
	for k := 0; k < nonClassOpens; k++ {
		s.classFrame = append(s.classFrame, false)
	}
	// Apply the closes.
	for k := 0; k < closed; k++ {
		if len(s.classFrame) > 0 {
			s.classFrame = s.classFrame[:len(s.classFrame)-1]
		}
	}
	s.depth += opened - closed
}

func (s *scanner) topIsClass() bool {
	if len(s.classFrame) == 0 {
		return false
	}
	return s.classFrame[len(s.classFrame)-1]
}

// emitMethod writes the method signature with elidedBody and advances s.pos past the matching
// closing brace of the method body so no body content is ever emitted. Abstract/interface methods
// (ending with `;`) are kept verbatim.
func (s *scanner) emitMethod(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasSuffix(trimmed, ";") && !strings.Contains(line, "{") {
		s.out.WriteString(line)
		return
	}
	// `{` somewhere on this line or on a later one.
	if !strings.Contains(line, "{") {
		s.out.WriteString(line)
		for s.pos < len(s.src) {
			nextEnd, nOpen, nClose := s.scanLine(s.pos)
			nextLine := s.src[s.pos:nextEnd]
			s.pos = nextEnd
			if !strings.Contains(nextLine, "{") {
				s.out.WriteString(nextLine)
				continue
			}
			idx := strings.Index(nextLine, "{")
			head := nextLine[:idx]
			s.writeElided(head)
			rest := nextLine[idx:]
			ro, rc := countBraces(rest)
			s.skipMethodBody(ro-rc, nOpen-nClose-(ro-rc))
			return
		}
		return
	}
	idx := strings.Index(line, "{")
	head := line[:idx]
	rest := line[idx:]
	s.writeElided(head)
	openInRest, closeInRest := countBraces(rest)
	s.skipMethodBody(openInRest-closeInRest, 0)
}

func (s *scanner) writeElided(head string) {
	s.out.WriteString(strings.TrimRight(head, " \t"))
	s.out.WriteString(" ")
	s.out.WriteString(elidedBody)
	s.out.WriteString("\n")
}

// skipMethodBody consumes input until the currently-open method frame closes. methodDepth is the
// net open-braces accumulated *inside* the method frame up to the current position; the method
// closes when it returns to 0.
func (s *scanner) skipMethodBody(methodDepth, extra int) {
	_ = extra
	if methodDepth <= 0 {
		return
	}
	for s.pos < len(s.src) && methodDepth > 0 {
		nextEnd, nOpen, nClose := s.scanLine(s.pos)
		s.pos = nextEnd
		methodDepth += nOpen - nClose
	}
}

// isClassLikeHeaderLine matches Java/Kotlin/Scala class/interface/enum/record/annotation headers.
func isClassLikeHeaderLine(trimmed string) bool {
	head := trimmed
	if i := strings.Index(head, "{"); i >= 0 {
		head = head[:i]
	}
	head = " " + strings.TrimSpace(head) + " "
	for _, k := range []string{" class ", " interface ", " enum ", " record ", " @interface "} {
		if strings.Contains(head, k) {
			return true
		}
	}
	return false
}

// looksLikeJavaMethodLine returns true when trimmed looks like a method/constructor header opener.
// Heuristic: contains `(`, has a modifier/type prefix, and ends with either `{`, `;`, `throws …`,
// or (for multi-line signatures) `,`/`(`.
func looksLikeJavaMethodLine(trimmed string) bool {
	open := strings.Index(trimmed, "(")
	if open < 0 {
		return false
	}
	close := strings.LastIndex(trimmed, ")")
	prefix := strings.TrimSpace(trimmed[:open])
	if prefix == "" {
		return false
	}
	if close < open {
		// multi-line signature continuation — treat as method opener.
		if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "(") {
			return true
		}
		return false
	}
	tail := strings.TrimSpace(trimmed[close+1:])
	endsLikeDecl := tail == "" || strings.HasPrefix(tail, "throws") || strings.HasSuffix(tail, "{") || strings.HasSuffix(tail, ";") || strings.Contains(tail, "{")
	if !endsLikeDecl {
		return false
	}
	hasSpace := strings.ContainsAny(prefix, " \t")
	if !hasSpace {
		// possible constructor: `ClassName(...)`.
		if prefix[0] >= 'A' && prefix[0] <= 'Z' {
			return true
		}
		return false
	}
	return true
}

// countBraces returns open/close counts on segment, respecting strings/chars/comments.
func countBraces(segment string) (open, close int) {
	i := 0
	for i < len(segment) {
		c := segment[i]
		switch {
		case c == '/' && i+1 < len(segment) && segment[i+1] == '/':
			for i < len(segment) && segment[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(segment) && segment[i+1] == '*':
			i += 2
			for i+1 < len(segment) && !(segment[i] == '*' && segment[i+1] == '/') {
				i++
			}
			if i+1 < len(segment) {
				i += 2
			}
		case c == '"':
			i++
			for i < len(segment) && segment[i] != '"' {
				if segment[i] == '\\' && i+1 < len(segment) {
					i += 2
					continue
				}
				i++
			}
			if i < len(segment) {
				i++
			}
		case c == '\'':
			i++
			for i < len(segment) && segment[i] != '\'' {
				if segment[i] == '\\' && i+1 < len(segment) {
					i += 2
					continue
				}
				i++
			}
			if i < len(segment) {
				i++
			}
		case c == '{':
			open++
			i++
		case c == '}':
			close++
			i++
		default:
			i++
		}
	}
	return
}
