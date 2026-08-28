package extendmerge

import (
	"path/filepath"
	"regexp"
	"strings"
)

// extendPayloadKind classifies what an extend-existing generation actually produced.
//
// The extend write path splices its payload verbatim inside an existing type's body
// (insertInsideClassBody). That is only correct for a members-only payload. When the model
// returns a whole compilation unit instead — `package p; import …; class FooTests { … }` — the
// splice puts package/import lines inside a class body, which the compiler reports as
// `illegal start of type` (javac) or `} expected` + `A using clause must precede all other
// elements` + `Type or namespace definition, or end-of-file expected` (roslyn).
//
// Two independent paths can hand us a full compilation unit, so the classification has to live
// at the merge point rather than in the prompt:
//   - a model that ignores the methods-only instruction, and
//   - WriteCoordinator.WriteGapTestItems, which flips ExtendExisting on for a *second* gap
//     targeting a path an earlier gap created — that item was generated as a fresh full file and
//     never saw an extend prompt at all.
type extendPayloadKind int

const (
	// payloadMembersOnly is ready to splice as-is.
	payloadMembersOnly extendPayloadKind = iota
	// payloadCompilationUnit is a full file that must be unwrapped to its primary type body first.
	payloadCompilationUnit
	// payloadUnusable carries nothing recoverable (empty, or leaked markdown fences).
	payloadUnusable
)

func (k extendPayloadKind) String() string {
	switch k {
	case payloadMembersOnly:
		return "members_only"
	case payloadCompilationUnit:
		return "compilation_unit"
	default:
		return "unusable"
	}
}

var (
	// javaTopLevelImportRE matches an `import x.y.Z;` / `import static …` at column 0. Inside a
	// class body an import is illegal, so its presence marks a compilation unit.
	javaTopLevelImportRE = regexp.MustCompile(`(?m)^import\s+(?:static\s+)?[\w.$]+(?:\.\*)?\s*;`)
	// javaTypeDeclRE captures the name of a Java type declaration. Annotations normally sit on
	// their own preceding lines, so the declaration line itself is what we match.
	javaTypeDeclRE = regexp.MustCompile(`(?m)^([ \t]*)(?:(?:public|private|protected|static|final|abstract|sealed|non-sealed|strictfp)\s+)*(?:class|interface|enum|record)\s+(\w+)`)
	// csharpTopLevelUsingRE matches any C# using DIRECTIVE at column 0, in every form the language
	// allows: plain, `global using`, `using static`, and `using Alias = Target`.
	//
	// It delegates to csharpUsingLineRE (import_union.go) rather than keeping a second, simpler
	// pattern. The two had drifted: this one required `using` to be followed directly by a dotted
	// path, so `using static Xunit.Assert;` matched neither it nor the hoist regex — the directive
	// stayed in the payload, classified as members-only, and was spliced INSIDE the class body,
	// where C# rejects it ("A using clause must precede all other elements defined in the
	// namespace"). One regex, one answer.
	csharpTopLevelUsingRE = csharpUsingLineRE
	// csharpNamespaceRE matches both block (`namespace N {`) and file-scoped (`namespace N;`) forms.
	csharpNamespaceRE = regexp.MustCompile(`(?m)^\s*namespace\s+[\w.]+\s*[;{]`)
	// javaMethodDeclRE / csharpMethodDeclRE capture the method name of a declaration that opens a
	// body on the same line. Used only for duplicate suppression, which is skipped when the span
	// cannot be resolved cleanly.
	javaMethodDeclRE   = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|abstract|synchronized|default|native|strictfp)\s+)*[\w<>\[\], .?]+\s+(\w+)\s*\([^()]*\)(?:\s*throws\s+[\w.,\s]+)?\s*\{`)
	csharpMethodDeclRE = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|async|override|virtual|sealed|partial|extern|unsafe|new)\s+)*[\w<>\[\], .?]+\s+(\w+)\s*\([^()]*\)\s*\{`)
	// javaFieldDeclRE / csharpFieldDeclRE capture the NAME of a field declaration.
	//
	// dropDuplicateMembers used to suppress duplicate METHODS only, so a payload that redeclared
	// the class's fields spliced them straight in and javac rejected the file with
	// "variable TEST_OWNER_ID is already defined in class VisitControllerTests". Three files in one
	// run. The gap was always there; it only became the binding failure once the import union
	// stopped the merges failing earlier on missing symbols.
	//
	// At least one modifier is required so an ordinary statement (`owners.save(x);`) cannot match,
	// and a `(` before the terminator rules out method calls and declarations.
	javaFieldDeclRE   = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|transient|volatile)\s+)+[\w<>\[\],.?\s]+?\s+(\w+)\s*(?:=[^;(){}]*)?;`)
	csharpFieldDeclRE = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|readonly|const|volatile)\s+)+[\w<>\[\],.?\s]+?\s+(\w+)\s*(?:=[^;(){}]*)?;`)
	// leadingAnnotationRE / leadingAttributeRE match the decorator lines that belong to the
	// declaration immediately below them.
	leadingAnnotationRE = regexp.MustCompile(`^[ \t]*@\w`)
	leadingAttributeRE  = regexp.MustCompile(`^[ \t]*\[[^\]]*\]\s*$`)
)

// classifyExtendPayload reports what an extend payload for path contains.
//
// JS/TS is deliberately always payloadMembersOnly: insertInsideClassBody appends module-shaped
// payloads at EOF rather than splicing into a type body, which is already correct for
// describe/test modules. Merging duplicate ESM imports is a separate problem and is not solved
// here.
func classifyExtendPayload(path, payload string) extendPayloadKind {
	s := strings.TrimSpace(payload)
	if s == "" {
		return payloadUnusable
	}
	if strings.Contains(s, "```") {
		// A leaked markdown fence means the model wrapped its answer in prose. Splicing that
		// into a source file guarantees a compile error and buries the real content.
		return payloadUnusable
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		if hasJavaPackageDeclaration(s) || javaTopLevelImportRE.MatchString(s) {
			return payloadCompilationUnit
		}
		if javaTypeDeclAtColumnZero(s) {
			if _, _, ok := javaPrimaryTypeBodyRange(s); ok {
				return payloadCompilationUnit
			}
		}
		return payloadMembersOnly
	case ".cs":
		if csharpNamespaceRE.MatchString(s) || csharpTopLevelUsingRE.MatchString(s) {
			return payloadCompilationUnit
		}
		if csharpTypeDeclAtColumnZero(s) {
			if _, _, ok := csharpPrimaryTypeBodyRange(s); ok {
				return payloadCompilationUnit
			}
		}
		return payloadMembersOnly
	default:
		return payloadMembersOnly
	}
}

// javaTypeDeclAtColumnZero reports whether a Java type is declared with no leading indentation.
// A nested helper type inside a members-only payload is indented, so column 0 is what separates
// "this payload IS a type" from "this payload CONTAINS a type".
func javaTypeDeclAtColumnZero(s string) bool {
	for _, m := range javaTypeDeclRE.FindAllStringSubmatch(s, -1) {
		if m[1] == "" {
			return true
		}
	}
	return false
}

func csharpTypeDeclAtColumnZero(s string) bool {
	for _, loc := range csharpClassDeclRE.FindAllStringIndex(s, -1) {
		lineStart := strings.LastIndex(s[:loc[0]], "\n") + 1
		if strings.TrimSpace(s[lineStart:loc[0]]) == "" && lineStart == loc[0] {
			return true
		}
	}
	return false
}

// unwrapCompilationUnit extracts the primary type's body from a full compilation unit so it can
// be spliced into an existing type. Returns ok=false when no type body can be located, in which
// case the caller must skip the write rather than corrupt the file on disk.
//
// A payload declaring two top-level types is unwrapped to the preferred one only (the type whose
// name contains "test"); splicing two top-level types into a class body is precisely the
// `illegal start of type` failure this function exists to prevent.
func unwrapCompilationUnit(path, payload string) (string, bool) {
	s := strings.ReplaceAll(strings.TrimSpace(payload), "\r\n", "\n")
	var open, closeIdx int
	var ok bool
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		open, closeIdx, ok = javaPrimaryTypeBodyRange(s)
	case ".cs":
		open, closeIdx, ok = csharpPrimaryTypeBodyRange(s)
	default:
		return "", false
	}
	if !ok || open < 0 || closeIdx <= open || closeIdx > len(s) {
		return "", false
	}
	body := strings.TrimSpace(s[open+1 : closeIdx])
	if body == "" {
		return "", false
	}
	return dedent(body), true
}

// dedent removes the common leading indentation from every non-blank line.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	min := -1
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		n := len(ln) - len(strings.TrimLeft(ln, " \t"))
		if min < 0 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return s
	}
	for i, ln := range lines {
		if len(ln) >= min && strings.TrimSpace(ln) != "" {
			lines[i] = ln[min:]
		}
	}
	return strings.Join(lines, "\n")
}

// javaPrimaryTypeBodyRange returns the open/close brace indices of the primary type's body,
// preferring a type whose name contains "test". Mirrors csharpPrimaryTypeBodyRange.
func javaPrimaryTypeBodyRange(s string) (int, int, bool) {
	matches := javaTypeDeclRE.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return 0, 0, false
	}
	pick := 0
	for i, m := range matches {
		if strings.Contains(strings.ToLower(s[m[4]:m[5]]), "test") {
			pick = i
			break
		}
	}
	headerEnd := matches[pick][1]
	open := firstTypeOpenBrace(s, headerEnd)
	if open < 0 {
		return 0, 0, false
	}
	closeIdx := scanToMatchingCloseBrace(s, open)
	if closeIdx < 0 {
		return 0, 0, false
	}
	return open, closeIdx, true
}

// csharpPrimaryTypeBodyRange returns the open/close brace indices of the primary C# test type's
// body. csharpInsertIndexBeforeTestClassClose is a thin wrapper returning only the close index.
func csharpPrimaryTypeBodyRange(s string) (int, int, bool) {
	searchFrom := 0
	// Block-scoped namespace: start the type search after its opening brace so the namespace's
	// own `{` is not mistaken for a type body. File-scoped (`namespace N;`) needs no adjustment.
	nsBlock := regexp.MustCompile(`(?m)^\s*namespace\s+[\w.]+\s*\{`).FindStringIndex(s)
	if nsBlock != nil {
		rel := s[nsBlock[0]:nsBlock[1]]
		nsOpen := strings.LastIndex(rel, "{")
		if nsOpen < 0 {
			return 0, 0, false
		}
		searchFrom = nsBlock[0] + nsOpen + 1
	}
	var matches [][]int
	cursor := searchFrom
	for {
		loc := csharpClassDeclRE.FindStringSubmatchIndex(s[cursor:])
		if loc == nil {
			break
		}
		matches = append(matches, []int{cursor + loc[0], cursor + loc[1], cursor + loc[2], cursor + loc[3]})
		cursor += loc[1]
	}
	if len(matches) == 0 {
		return 0, 0, false
	}
	pick := 0
	for i, m := range matches {
		if strings.Contains(strings.ToLower(s[m[2]:m[3]]), "test") {
			pick = i
			break
		}
	}
	open := firstTypeOpenBrace(s, matches[pick][1])
	if open < 0 {
		return 0, 0, false
	}
	closeIdx := scanToMatchingCloseBrace(s, open)
	if closeIdx < 0 {
		return 0, 0, false
	}
	return open, closeIdx, true
}

// dropDuplicateMembers removes methods from an extend payload whose names are already declared in
// the file being extended, and returns the surviving payload plus the dropped names.
//
// Without it, a model that re-emits an existing test (against instruction) produces
// `method X() is already defined`, which costs a full fix round. Conservative by construction: a
// method is only dropped when its full brace span resolves cleanly, so an unparseable payload is
// passed through untouched rather than mangled.
func dropDuplicateMembers(path, existing, payload string) (string, []string) {
	var re, fieldRE, typeRE *regexp.Regexp
	var decoratorRE *regexp.Regexp
	var typeNameGroup int
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		re, fieldRE, decoratorRE = javaMethodDeclRE, javaFieldDeclRE, leadingAnnotationRE
		typeRE, typeNameGroup = javaTypeDeclRE, 2
	case ".cs":
		re, fieldRE, decoratorRE = csharpMethodDeclRE, csharpFieldDeclRE, leadingAttributeRE
		typeRE, typeNameGroup = csharpClassDeclRE, 1
	default:
		return payload, nil
	}
	// Direct members only. The sweep used to be flat over the whole file, so a name declared
	// inside an existing @Nested class counted as "already defined" for a payload that declares
	// its own nested class — and a new `@Nested class X { @BeforeEach void setup() }` lost its
	// setup() to the outer class's. Nested types are handled separately below, by name.
	existingNames := directDeclNames(existing, re, 1)
	existingFields := directDeclNames(existing, fieldRE, 1)
	// Nested TYPE names are collected across the whole file: a payload type collides with any
	// type the file already declares, at any depth, because the splice lands them as siblings.
	existingTypes := make(map[string]bool)
	if typeRE != nil {
		for _, m := range typeRE.FindAllStringSubmatch(existing, -1) {
			existingTypes[m[typeNameGroup]] = true
		}
	}
	payload, droppedTypes := dropDuplicateTypes(payload, typeRE, typeNameGroup, decoratorRE, existingTypes)
	// Fields are cut first: they have no brace-delimited body, so removing them cannot disturb the
	// method spans the loop below resolves by brace matching.
	payload, droppedFields := dropDuplicateFields(payload, fieldRE, decoratorRE, existingFields)
	droppedFields = append(droppedTypes, droppedFields...)
	if len(existingNames) == 0 {
		return strings.TrimSpace(payload), droppedFields
	}

	s := strings.ReplaceAll(payload, "\r\n", "\n")
	var dropped []string
	// Walk matches right-to-left so earlier byte offsets stay valid as we cut.
	locs := re.FindAllStringSubmatchIndex(s, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		name := s[loc[2]:loc[3]]
		if !existingNames[name] {
			continue
		}
		// A payload method nested inside a type the payload itself declares is scoped to that
		// type, so it cannot collide with the outer class's member of the same name.
		if braceDepthAt(s, 0, loc[0]) > 0 {
			continue
		}
		open := strings.IndexByte(s[loc[0]:loc[1]], '{')
		if open < 0 {
			continue
		}
		open += loc[0]
		closeIdx := scanToMatchingCloseBrace(s, open)
		if closeIdx < 0 {
			continue
		}
		start := strings.LastIndex(s[:loc[0]], "\n") + 1
		// Absorb the decorator lines (annotations / attributes) attached to this declaration.
		for start > 0 {
			prevStart := strings.LastIndex(s[:start-1], "\n") + 1
			line := s[prevStart : start-1]
			if strings.TrimSpace(line) == "" || decoratorRE.MatchString(line) {
				if strings.TrimSpace(line) == "" {
					break
				}
				start = prevStart
				continue
			}
			break
		}
		end := closeIdx + 1
		if end < len(s) && s[end] == '\n' {
			end++
		}
		s = s[:start] + s[end:]
		dropped = append(dropped, name)
	}
	// Restore source order for a stable audit payload.
	for i, j := 0, len(dropped)-1; i < j; i, j = i+1, j-1 {
		dropped[i], dropped[j] = dropped[j], dropped[i]
	}
	return strings.TrimSpace(s), append(droppedFields, dropped...)
}

// dropDuplicateFields removes payload field declarations whose name is already declared in the
// target type, absorbing any decorator lines (@Autowired, @MockitoBean, [Fact]) attached above.
func dropDuplicateFields(payload string, fieldRE, decoratorRE *regexp.Regexp, existing map[string]bool) (string, []string) {
	if len(existing) == 0 {
		return payload, nil
	}
	s := strings.ReplaceAll(payload, "\r\n", "\n")
	var dropped []string
	locs := fieldRE.FindAllStringSubmatchIndex(s, -1)
	// Right-to-left so earlier offsets stay valid as we cut.
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		name := s[loc[2]:loc[3]]
		if !existing[name] {
			continue
		}
		start := strings.LastIndex(s[:loc[0]], "\n") + 1
		for start > 0 {
			prevStart := strings.LastIndex(s[:start-1], "\n") + 1
			line := s[prevStart : start-1]
			if strings.TrimSpace(line) != "" && decoratorRE.MatchString(line) {
				start = prevStart
				continue
			}
			break
		}
		end := loc[1]
		if end < len(s) && s[end] == '\n' {
			end++
		}
		s = s[:start] + s[end:]
		dropped = append(dropped, name)
	}
	for i, j := 0, len(dropped)-1; i < j; i, j = i+1, j-1 {
		dropped[i], dropped[j] = dropped[j], dropped[i]
	}
	return s, dropped
}

// directDeclNames returns the names matched by re that sit at exactly the given brace depth,
// measured from the start of s.
//
// depth 1 is "a direct member of the file's primary type" for a whole compilation unit; depth 0 is
// the same thing for a members-only payload, which has no type header of its own.
func directDeclNames(s string, re *regexp.Regexp, depth int) map[string]bool {
	out := make(map[string]bool)
	if re == nil || strings.TrimSpace(s) == "" {
		return out
	}
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		if braceDepthAt(s, 0, loc[0]) != depth {
			continue
		}
		out[s[loc[2]:loc[3]]] = true
	}
	return out
}

// dropDuplicateTypes removes nested type declarations from an extend payload whose name is already
// declared in the file being extended, or repeated within the payload itself.
//
// dropDuplicateMembers covered methods and fields only, and the two regexes it used cannot match a
// type: javaMethodDeclRE requires a parameter list, javaFieldDeclRE requires a terminating `;`.
// Meanwhile extendExistingSuffix explicitly invites the shape — "Output only the new test
// method(s) or inner class" — and insertInsideClassBody is a text splice with no notion of what is
// already declared. Run api-4f92fec6985aee5e4ce48de0041747d2 stalled five rounds on the result:
//
//	PetControllerTests.java:[218,9] class …PetControllerTests.ProcessUpdateFormHasErrors
//	  is already defined in class …PetControllerTests
//
// Conservative in the same way as the method pass: a declaration whose brace span does not resolve
// cleanly is left alone rather than mangled.
func dropDuplicateTypes(payload string, typeRE *regexp.Regexp, nameGroup int, decoratorRE *regexp.Regexp, existing map[string]bool) (string, []string) {
	if typeRE == nil {
		return payload, nil
	}
	s := strings.ReplaceAll(payload, "\r\n", "\n")
	seen := make(map[string]bool, len(existing))
	for n := range existing {
		seen[n] = true
	}
	var dropped []string
	locs := typeRE.FindAllStringSubmatchIndex(s, -1)
	// Right-to-left so earlier offsets stay valid as we cut. Names are collected left-to-right
	// first, so an intra-payload repeat drops the LATER copy and keeps the first.
	keep := make([]bool, len(locs))
	for i, loc := range locs {
		name := s[loc[2*nameGroup]:loc[2*nameGroup+1]]
		if seen[name] {
			continue
		}
		seen[name] = true
		keep[i] = true
	}
	for i := len(locs) - 1; i >= 0; i-- {
		if keep[i] {
			continue
		}
		loc := locs[i]
		name := s[loc[2*nameGroup]:loc[2*nameGroup+1]]
		open := strings.IndexByte(s[loc[0]:], '{')
		if open < 0 {
			continue
		}
		open += loc[0]
		closeIdx := scanToMatchingCloseBrace(s, open)
		if closeIdx < 0 {
			continue
		}
		start := strings.LastIndex(s[:loc[0]], "\n") + 1
		// Absorb the decorator lines (@Nested, @DisplayName, [Collection]) above the declaration.
		for start > 0 {
			prevStart := strings.LastIndex(s[:start-1], "\n") + 1
			line := s[prevStart : start-1]
			if strings.TrimSpace(line) == "" || decoratorRE.MatchString(line) {
				if strings.TrimSpace(line) == "" {
					break
				}
				start = prevStart
				continue
			}
			break
		}
		end := closeIdx + 1
		if end < len(s) && s[end] == '\n' {
			end++
		}
		s = s[:start] + s[end:]
		dropped = append(dropped, name)
	}
	for i, j := 0, len(dropped)-1; i < j; i, j = i+1, j-1 {
		dropped[i], dropped[j] = dropped[j], dropped[i]
	}
	return strings.TrimSpace(s), dropped
}

// mergedPayloadInsideTypeBody verifies an extend merge landed the payload inside the primary
// type's body rather than after its closing brace.
//
// This is the structural backstop the syntactic gate cannot provide: a payload appended at EOF
// leaves the file brace-balanced and still declaring a top-level type, so SyntacticShellReason
// passes it. Only position catches it. The failure it guards against is real — a mis-detected
// language shape sent every merge down insertInsideClassBody's append-at-EOF branch and produced
// `class, interface, enum, or record expected` at every appended line.
//
// The check is baseline-relative: whatever follows the primary type's closing brace must be
// unchanged from the pre-merge file. Searching for a marker from the payload does NOT work — a
// payload starting with `@Test` matches the first pre-existing `@Test` inside the class and the
// guard passes while the real insertion sits outside.
//
// Returns true for languages with no type-body concept (JS/TS), where appending at EOF is correct.
func mergedPayloadInsideTypeBody(path, existing, merged string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".cs":
	default:
		return true
	}
	mergedTail, ok := tailAfterPrimaryType(path, merged)
	if !ok {
		// No type body locatable in the merged output — a corruption signal in its own right.
		return false
	}
	existingTail, ok := tailAfterPrimaryType(path, existing)
	if !ok {
		// No baseline to compare against (unparseable original). Fall back to requiring the tail
		// to carry no code, which is what a correct merge always produces.
		return mergedTail == ""
	}
	return mergedTail == existingTail
}

// tailAfterPrimaryType returns the whitespace-normalised remainder of s after the primary type's
// closing brace.
func tailAfterPrimaryType(path, s string) (string, bool) {
	var closeIdx int
	var ok bool
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		_, closeIdx, ok = javaPrimaryTypeBodyRange(s)
	case ".cs":
		_, closeIdx, ok = csharpPrimaryTypeBodyRange(s)
	default:
		return "", false
	}
	if !ok || closeIdx+1 > len(s) {
		return "", false
	}
	return strings.Join(strings.Fields(s[closeIdx+1:]), " "), true
}
