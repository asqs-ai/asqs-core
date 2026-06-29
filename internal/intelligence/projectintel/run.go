package projectintel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const rankLeadRunes = 8000

const maxScannedRelPathsInResult = 100

func sortedScannedRelPaths(cands []Candidate, capN int) (paths []string, omitted int) {
	if len(cands) == 0 || capN <= 0 {
		return nil, 0
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		p := filepath.ToSlash(strings.TrimSpace(c.RelPath))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) > capN {
		return out[:capN], len(out) - capN
	}
	return out, 0
}

// Run executes scan → rank → summarize → markdown, with optional disk cache.
func Run(ctx context.Context, in Input) (*Result, error) {
	start := time.Now()
	res := &Result{Mode: "lexical"}
	if in.Skip || !in.Opts.Enabled {
		res.Mode = "off"
		res.Snapshot = Snapshot{Diagnostics: []string{"project_intel skipped (policy or config)"}}
		res.DurationMs = time.Since(start).Milliseconds()
		return res, nil
	}
	repo := filepath.Clean(in.RepoAbs)
	query := BuildRunQuery(in.Lang, in.TestFramework, in.E2EFramework)
	layoutFP := CoarseLayoutFingerprint(in.CurrentFiles)
	relFP := relevanceFingerprint(in.Lang, in.TestFramework, in.E2EFramework, in.MonoWorkspace, layoutFP)
	cfgFP := strings.TrimSpace(in.ConfigFingerprint)

	cands, err := Discover(repo, in.MonoWorkspace, in.Opts.ExtraDocGlobs, in.Opts.ExtraSkillGlobs)
	if err != nil {
		return nil, err
	}
	res.FilesScanned = len(cands)
	res.ScannedRelPaths, res.ScannedRelPathsOmitted = sortedScannedRelPaths(cands, maxScannedRelPathsInResult)
	filesFP := buildFilesFingerprint(repo, in.Opts.FingerprintMode, cands)
	res.FilesFingerprint = filesFP
	res.RelevanceFingerprint = relFP

	cachePath := strings.TrimSpace(in.Opts.CachePath)
	if cachePath == "" {
		cachePath = ".asqs/project-intel-cache.json"
	}
	res.CachePath = cachePath
	if in.Opts.CacheEnabled && !in.Opts.ForceRefresh {
		if snap, cacheCands, hit := tryLoadCache(repo, cachePath, filesFP, relFP, cfgFP); hit {
			res.CacheHit = true
			res.Mode = "cache_hit"
			res.Snapshot = *snap
			res.Candidates = cacheCands
			res.ApproxRunes = utf8.RuneCountInString(snap.Markdown)
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
	}

	type scored struct {
		cand  Candidate
		score float64
		lead  string
	}
	var docs, skills []scored
	for _, c := range cands {
		abs := filepath.Join(repo, filepath.FromSlash(c.RelPath))
		lead, err := readLeadText(abs, rankLeadRunes)
		if err != nil {
			continue
		}
		sc := LexicalJaccard(query, lead)
		if c.Kind == DocKindSkill {
			sc += SkillRelevanceBoost(c.RelPath, lead)
		}
		row := scored{cand: c, score: sc, lead: lead}
		if c.Kind == DocKindSkill {
			skills = append(skills, row)
		} else {
			docs = append(docs, row)
		}
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].score == docs[j].score {
			return docs[i].cand.RelPath < docs[j].cand.RelPath
		}
		return docs[i].score > docs[j].score
	})
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].score == skills[j].score {
			return skills[i].cand.RelPath < skills[j].cand.RelPath
		}
		return skills[i].score > skills[j].score
	})

	minScore := in.Opts.MinRelevanceScore
	maxDoc := in.Opts.MaxDocFiles
	maxSkill := in.Opts.MaxSkillFiles
	if maxDoc <= 0 {
		maxDoc = 12
	}
	if maxSkill <= 0 {
		maxSkill = 8
	}

	var pickedDocs, pickedSkills []scored
	for _, d := range docs {
		if len(pickedDocs) >= maxDoc {
			break
		}
		if d.score >= minScore {
			pickedDocs = append(pickedDocs, d)
		}
	}
	for _, s := range skills {
		if len(pickedSkills) >= maxSkill {
			break
		}
		if s.score >= minScore {
			pickedSkills = append(pickedSkills, s)
		}
	}
	res.DocsSelected = len(pickedDocs)
	res.SkillsSelected = len(pickedSkills)

	sumAbove := in.Opts.SummarizeAboveRunes
	if sumAbove <= 0 {
		sumAbove = 6000
	}

	// Phase 1: read + summarize each picked candidate; collect into RankedCandidate list.
	allPicked := append(append([]scored(nil), pickedDocs...), pickedSkills...)
	ranked := make([]RankedCandidate, 0, len(allPicked))
	for _, row := range allPicked {
		abs := filepath.Join(repo, filepath.FromSlash(row.cand.RelPath))
		fullBytes, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		body := string(fullBytes)
		text := body
		rn := utf8.RuneCountInString(body)
		if rn > sumAbove && sumAbove > 0 {
			var used bool
			text, used, _ = SummarizeWithLLM(ctx, in.LLM, row.cand.RelPath, body)
			if used {
				res.LLMSummarizeCalls++
			}
		} else if rn > summarizeMaxOutRunes {
			text = ExtractiveSummary(body, summarizeMaxOutRunes)
			res.Truncations++
		}
		ranked = append(ranked, RankedCandidate{
			Candidate: row.cand,
			Score:     row.score,
			Content:   text,
		})
	}

	// Phase 2: batch-embed summaries when UseEmbeddingsRank is enabled.
	if in.Opts.UseEmbeddingsRank && in.Embedder != nil && len(ranked) > 0 {
		texts := make([]string, len(ranked))
		for i, rc := range ranked {
			texts[i] = rc.Content
		}
		vecs, embErr := in.Embedder.Embed(ctx, texts)
		if embErr == nil && len(vecs) == len(ranked) {
			for i := range ranked {
				ranked[i].DocEmbedding = vecs[i]
			}
			res.Mode = "embedding"
		}
		// On error: proceed without embeddings (graceful degradation).
	}

	res.Candidates = ranked

	// Phase 3: build the pre-computed markdown block (fallback / default path).
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
		for _, rc := range rows {
			b.WriteString("- **")
			b.WriteString(rc.RelPath)
			b.WriteString(fmt.Sprintf("** (score %.3f)\n\n", rc.Score))
			b.WriteString(rc.Content)
			b.WriteString("\n\n")
		}
	}

	var docCands, skillCands []RankedCandidate
	for _, rc := range ranked {
		if rc.Kind == DocKindSkill {
			skillCands = append(skillCands, rc)
		} else {
			docCands = append(docCands, rc)
		}
	}
	appendSection("Documentation", docCands)
	appendSection("Agent skills", skillCands)

	out := strings.TrimSpace(b.String())
	maxTotal := in.Opts.MaxTotalRunes
	if maxTotal <= 0 {
		maxTotal = 12000
	}
	if utf8.RuneCountInString(out) > maxTotal {
		out = truncateRunes(out, maxTotal)
		res.Truncations++
	}

	res.Snapshot = Snapshot{
		Markdown: out,
		Diagnostics: []string{
			fmt.Sprintf("candidates=%d docs=%d skills=%d", len(cands), res.DocsSelected, res.SkillsSelected),
		},
	}
	res.ApproxRunes = utf8.RuneCountInString(out)

	if in.Opts.CacheEnabled {
		var diskCands []diskCacheCandidate
		for _, rc := range ranked {
			diskCands = append(diskCands, diskCacheCandidate{
				RelPath:      rc.RelPath,
				Kind:         string(rc.Kind),
				Score:        rc.Score,
				Content:      rc.Content,
				DocEmbedding: append([]float32(nil), rc.DocEmbedding...),
			})
		}
		dc := diskCache{
			FilesFingerprint:     filesFP,
			RelevanceFingerprint: relFP,
			ConfigFingerprint:    cfgFP,
			Markdown:             out,
			Diagnostics:          append([]string(nil), res.Snapshot.Diagnostics...),
			ApproxRunes:          res.ApproxRunes,
			Candidates:           diskCands,
		}
		if err := saveCacheAtomic(repo, cachePath, dc); err != nil {
			res.Snapshot.Diagnostics = append(res.Snapshot.Diagnostics, "cache_write_error: "+err.Error())
		}
	}

	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}
