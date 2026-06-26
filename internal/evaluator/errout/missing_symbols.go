package errout

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errloc"
)

// Java/Kotlin compiler diagnostics that name a *type* the build could not resolve. Unlike
// AllCitedRepoPaths (which resolves a fully-qualified name to its on-disk file when the package is
// correct), these target the case where the cited package is wrong or absent — the hallmark LLM
// failure: the generated test imports org.example.model.PetType while the class actually lives in
// org.example.owner, so no FQCN resolves and the type's real source is never shown to the fixer.
var (
	// `symbol:   class PetType` (bare) or `symbol: class a.b.PetType` (qualified).
	javaSymbolClassRe = regexp.MustCompile(`(?m)symbol:\s+class\s+([\w.]+)`)
	// `location: class a.b.PetType` — the type that owns a missing variable/method (e.g. PetType.DOG).
	javaLocationClassRe = regexp.MustCompile(`(?m)location:\s+(?:class|interface)\s+([\w.]+)`)
	// `constructor Pet in class a.b.Pet cannot be applied to given types` — wrong constructor usage.
	javaConstructorRe = regexp.MustCompile(`constructor\s+([A-Za-z_]\w*)\s+in\s+(?:class|interface)\s+([\w.]+)`)
)

// ResolveMissingTypeFiles parses compiler diagnostics for type names the build could not resolve and
// maps them to real source files in the repo — by package when the fully-qualified name is correct,
// and by basename search when it is wrong or missing. Returns repo-relative, forward-slashed,
// de-duplicated paths, capped at limit (main sources preferred, test sources never returned). Java and
// Kotlin only; other languages return nil because their compilers cite the offending file path
// directly, which AllCitedRepoPaths already loads.
func ResolveMissingTypeFiles(raw, repoRoot, lang string, limit int) []string {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(repoRoot) == "" || limit <= 0 {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java", "kotlin", "kt":
	default:
		return nil
	}
	cleanRoot := filepath.Clean(repoRoot)
	bare, fqcn := javaMissingTypeCandidates(raw)

	seen := make(map[string]bool)
	var out []string
	add := func(rel string) {
		rel = errloc.NormalizePath(rel)
		if rel == "" || seen[rel] {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}

	// 1) Fully-qualified hints first (exact package, just a stat).
	for _, f := range fqcn {
		if len(out) >= limit {
			return out[:limit]
		}
		if rel := resolveJavaFQCN(cleanRoot, f); rel != "" {
			add(rel)
		}
	}
	// 2) Bare names whose stem was not already resolved via an FQCN hit → basename search.
	resolvedStems := make(map[string]bool, len(out))
	for _, p := range out {
		resolvedStems[strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))] = true
	}
	wantStems := make(map[string]bool)
	for _, b := range bare {
		if b == "" || resolvedStems[b] {
			continue
		}
		wantStems[b] = true
	}
	if len(wantStems) > 0 && len(out) < limit {
		for _, rel := range findRepoSourceFilesByStem(cleanRoot, lang, wantStems, limit-len(out)) {
			add(rel)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// javaMissingTypeCandidates returns the de-duplicated, sorted sets of bare type names and
// fully-qualified class names mentioned by missing-symbol / wrong-constructor diagnostics in raw.
func javaMissingTypeCandidates(raw string) (bare []string, fqcn []string) {
	bareSet := make(map[string]bool)
	fqcnSet := make(map[string]bool)
	addToken := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return
		}
		if strings.Contains(tok, ".") {
			if !isJavaBuiltinFQCN(tok) {
				fqcnSet[tok] = true
			}
			tok = tok[strings.LastIndex(tok, ".")+1:]
		}
		if tok != "" && tok[0] >= 'A' && tok[0] <= 'Z' {
			bareSet[tok] = true
		}
	}
	for _, m := range javaSymbolClassRe.FindAllStringSubmatch(raw, -1) {
		addToken(m[1])
	}
	for _, m := range javaLocationClassRe.FindAllStringSubmatch(raw, -1) {
		addToken(m[1])
	}
	for _, m := range javaConstructorRe.FindAllStringSubmatch(raw, -1) {
		addToken(m[1]) // bare constructor name == enclosing type name
		addToken(m[2]) // fully-qualified enclosing class
	}
	for k := range bareSet {
		bare = append(bare, k)
	}
	for k := range fqcnSet {
		fqcn = append(fqcn, k)
	}
	sort.Strings(bare)
	sort.Strings(fqcn)
	return bare, fqcn
}

// findRepoSourceFilesByStem walks repoRoot for non-test source files whose basename stem is in stems.
// Main sources (under a src/main/ segment) are preferred and returned first; other non-test sources are
// a fallback. The walk skips build/VCS junk directories and is bounded so a pathological monorepo can't
// make a single fix attempt walk forever.
func findRepoSourceFilesByStem(repoRoot, lang string, stems map[string]bool, limit int) []string {
	if len(stems) == 0 || limit <= 0 {
		return nil
	}
	exts := map[string]bool{".java": true}
	if l := strings.ToLower(strings.TrimSpace(lang)); l == "kotlin" || l == "kt" {
		exts[".kt"] = true
	}
	var main, fallback []string
	visited := 0
	const maxVisit = 60000
	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "target", "build", "node_modules", "out", "bin", ".gradle", "dist", ".idea", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxVisit {
			return filepath.SkipAll
		}
		if !exts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		stem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if !stems[stem] {
			return nil
		}
		rel, e := filepath.Rel(repoRoot, path)
		if e != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "src/test/") || strings.Contains(rel, "/src/test/") {
			return nil // never pull another test in as a "missing type"
		}
		if strings.HasPrefix(rel, "src/main/") || strings.Contains(rel, "/src/main/") {
			main = append(main, rel)
		} else {
			fallback = append(fallback, rel)
		}
		if len(main) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(main)
	sort.Strings(fallback)
	out := append(main, fallback...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
