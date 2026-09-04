package uitesthooks

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// maxSlugRunes bounds the text part of a hook name. Long button labels ("Continue to the next
// step of the checkout") would otherwise produce ids nobody types.
const maxSlugRunes = 24

var nonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)

// Kebab lowercases s and replaces every run of non-alphanumerics with a single hyphen. CamelCase
// boundaries become hyphens too ("HomePage" → "home-page"), which is what makes a file stem read
// as a prefix.
func Kebab(s string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range s {
		if unicode.IsUpper(r) && prev != 0 && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	out := nonAlnumRE.ReplaceAllString(b.String(), "-")
	return strings.Trim(out, "-")
}

// Slug shortens free text (a button label, a heading) into the text part of a hook name, or ""
// when nothing usable remains.
func Slug(text string) string {
	k := Kebab(strings.TrimSpace(text))
	if k == "" {
		return ""
	}
	r := []rune(k)
	if len(r) > maxSlugRunes {
		k = strings.TrimRight(string(r[:maxSlugRunes]), "-")
	}
	return k
}

// FilePrefix derives the name prefix from a source path: the file stem, minus a `.component`
// suffix (Angular) and an `index` stem replaced by its directory, in kebab case.
// "src/pages/HomePage.tsx" → "home-page"; "catalog.component.html" → "catalog";
// "src/features/orders/index.tsx" → "orders".
func FilePrefix(rel string) string {
	base := filepath.Base(filepath.ToSlash(rel))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSuffix(stem, ".component")
	if stem == "index" || stem == "" {
		stem = filepath.Base(filepath.Dir(filepath.ToSlash(rel)))
	}
	return Kebab(stem)
}

// HookName composes `<prefix>-<role>[-<slug>]`.
func HookName(prefix, role, slug string) string {
	parts := []string{prefix, role}
	if slug != "" {
		parts = append(parts, slug)
	}
	return strings.Trim(strings.Join(parts, "-"), "-")
}

// nameSet hands out names unique within one file by suffixing -2, -3, … on repeats.
type nameSet map[string]int

func (n nameSet) take(name string) string {
	n[name]++
	if n[name] == 1 {
		return name
	}
	return name + "-" + itoa(n[name])
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
