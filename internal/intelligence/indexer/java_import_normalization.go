package indexer

import "strings"

// NormalizeJavaImportDecl converts JavaParser ImportDeclaration text (e.g. "import com.foo.Bar;" or
// "import static com.foo.Bar.BAZ;") to a qualified name used for symbol lookup. Star imports return empty.
// Bare FQNs (already normalized) pass through.
func NormalizeJavaImportDecl(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ";")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "import static ") {
		s = strings.TrimSpace(s[len("import static "):])
	} else if strings.HasPrefix(lower, "import ") {
		s = strings.TrimSpace(s[len("import "):])
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".*") {
		return ""
	}
	return s
}
