package projectintel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

var tokenSplit = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// BuildRunQuery builds a lexical query from run metadata.
func BuildRunQuery(lang, testFW, e2eFW string) string {
	var b strings.Builder
	for _, s := range []string{lang, testFW, e2eFW, "test", "tests", "spec", "jest", "vitest", "mocha", "junit", "mockito", "playwright", "cypress", "documentation", "jsdoc", "tsdoc", "javadoc", "xmldoc"} {
		s = strings.TrimSpace(s)
		if s != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(strings.ToLower(s))
		}
	}
	return b.String()
}

// CoarseLayoutFingerprint hashes first path segment set from CurrentFiles (sample up to 200).
func CoarseLayoutFingerprint(files []indexer.FileVersion) string {
	seg := make(map[string]struct{})
	n := 0
	for _, fv := range files {
		if n >= 200 {
			break
		}
		p := filepath.ToSlash(strings.TrimSpace(fv.Path))
		if p == "" || strings.Contains(p, "..") {
			continue
		}
		parts := strings.Split(p, "/")
		if len(parts) > 0 && parts[0] != "" {
			seg[parts[0]] = struct{}{}
		}
		n++
	}
	var xs []string
	for s := range seg {
		xs = append(xs, s)
	}
	sort.Strings(xs)
	h := sha256.New()
	for _, s := range xs {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func tokenSet(s string) map[string]struct{} {
	toks := tokenSplit.Split(strings.ToLower(s), -1)
	out := make(map[string]struct{})
	for _, t := range toks {
		t = strings.TrimFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
		if len(t) < 2 {
			continue
		}
		out[t] = struct{}{}
	}
	return out
}

// LexicalJaccard returns a similarity score in [0,1] using Jaccard on token sets.
func LexicalJaccard(query, text string) float64 {
	a := tokenSet(query)
	b := tokenSet(text)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// cosineSimilarity returns the cosine of the angle between a and b, or 0 on mismatched/zero vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		af, bf := float64(a[i]), float64(b[i])
		dot += af * bf
		na += af * af
		nb += bf * bf
	}
	if na == 0 || nb == 0 {
		return 0
	}
	cos := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if cos > 1 {
		return 1
	}
	if cos < -1 {
		return -1
	}
	return cos
}

// RankByEmbedding re-ranks candidates by cosine similarity to the target embedding.
// Candidates without DocEmbedding fall to the bottom and retain their lexical order.
func RankByEmbedding(candidates []RankedCandidate, target []float32) []RankedCandidate {
	if len(candidates) == 0 || len(target) == 0 {
		return candidates
	}
	type scored struct {
		idx  int
		sim  float64
		path string
	}
	rows := make([]scored, len(candidates))
	for i, c := range candidates {
		sim := -2.0 // sentinel: no embedding
		if len(c.DocEmbedding) > 0 {
			sim = cosineSimilarity(c.DocEmbedding, target)
		}
		rows[i] = scored{idx: i, sim: sim, path: c.RelPath}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].sim != rows[j].sim {
			return rows[i].sim > rows[j].sim
		}
		return rows[i].path < rows[j].path
	})
	out := make([]RankedCandidate, len(candidates))
	for i, r := range rows {
		out[i] = candidates[r.idx]
	}
	return out
}

// SelectForGap returns gap-specific markdown by re-ranking candidates against the target
// embedding and taking the top maxDoc docs and maxSkill skills.
// Falls back to the pre-built markdown when candidates is empty or target is nil/empty.
func SelectForGap(candidates []RankedCandidate, targetEmbedding []float32, maxDoc, maxSkill, maxTotalRunes int, fallbackMarkdown string) string {
	if len(candidates) == 0 || len(targetEmbedding) == 0 {
		return fallbackMarkdown
	}
	ranked := RankByEmbedding(candidates, targetEmbedding)

	var docs, skills []RankedCandidate
	for _, c := range ranked {
		if c.Kind == DocKindSkill {
			if len(skills) < maxSkill {
				skills = append(skills, c)
			}
		} else {
			if len(docs) < maxDoc {
				docs = append(docs, c)
			}
		}
		if len(docs) >= maxDoc && len(skills) >= maxSkill {
			break
		}
	}

	var b strings.Builder
	b.WriteString("## Repository documentation and agent skills (ASQS project intel)\n\n")
	b.WriteString("The following excerpts summarize **existing** repo docs and Cursor-style skills. Where these repo-specific conventions conflict with generic skill pack guidance in the system prompt, **these repo conventions take precedence**. Do not contradict indexed source code.\n\n")

	appendSection := func(title string, rows []RankedCandidate) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("### ")
		b.WriteString(title)
		b.WriteString("\n\n")
		for _, c := range rows {
			b.WriteString("- **")
			b.WriteString(c.RelPath)
			b.WriteString(fmt.Sprintf("** (score %.3f)\n\n", c.Score))
			b.WriteString(c.Content)
			b.WriteString("\n\n")
		}
	}

	appendSection("Documentation", docs)
	appendSection("Agent skills", skills)

	out := strings.TrimSpace(b.String())
	if maxTotalRunes > 0 && utf8.RuneCountInString(out) > maxTotalRunes {
		out = truncateRunes(out, maxTotalRunes)
	}
	return out
}

// SkillRelevanceBoost adds weight for test/doc oriented skills.
func SkillRelevanceBoost(path, body string) float64 {
	p := strings.ToLower(path + " " + body)
	var score float64
	keywords := []string{"test", "tests", "jest", "vitest", "playwright", "cypress", "junit", "mockito", "xunit", "nunit", "mstest", "describe", "it(", "documentation", "jsdoc", "tsdoc", "javadoc", "///", "skill"}
	for _, k := range keywords {
		if strings.Contains(p, k) {
			score += 0.03
		}
	}
	if strings.Contains(p, "description:") {
		score += 0.02
	}
	if score > 0.25 {
		score = 0.25
	}
	return score
}
