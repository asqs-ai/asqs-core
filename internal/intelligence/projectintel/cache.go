package projectintel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CacheFormatVersion bumps when on-disk JSON shape or ranking logic changes materially.
const CacheFormatVersion = 2

type diskCacheCandidate struct {
	RelPath      string    `json:"rel_path"`
	Kind         string    `json:"kind"`
	Score        float64   `json:"score"`
	Content      string    `json:"content"`
	DocEmbedding []float32 `json:"doc_embedding,omitempty"`
}

type diskCache struct {
	FormatVersion        int                  `json:"format_version"`
	FilesFingerprint     string               `json:"files_fingerprint"`
	RelevanceFingerprint string               `json:"relevance_fingerprint"`
	ConfigFingerprint    string               `json:"config_fingerprint"`
	CreatedAt            string               `json:"created_at"`
	Markdown             string               `json:"markdown"`
	Diagnostics          []string             `json:"diagnostics"`
	ApproxRunes          int                  `json:"approx_runes"`
	Candidates           []diskCacheCandidate `json:"candidates,omitempty"`
}

func filesFingerprintStat(cands []Candidate) string {
	lines := make([]string, 0, len(cands))
	for _, c := range cands {
		lines = append(lines, fmt.Sprintf("%s|%d|%d", c.RelPath, c.Size, c.ModTime.UnixNano()))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, ln := range lines {
		h.Write([]byte(ln))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func filesFingerprintContent(repoAbs string, cands []Candidate) string {
	h := sha256.New()
	const maxContent = 65536
	lines := make([]string, 0, len(cands))
	for _, c := range cands {
		abs := filepath.Join(repoAbs, filepath.FromSlash(c.RelPath))
		b, err := os.ReadFile(abs)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s|err", c.RelPath))
			continue
		}
		if len(b) > maxContent {
			b = b[:maxContent]
		}
		th := sha256.Sum256(b)
		lines = append(lines, fmt.Sprintf("%s|%x", c.RelPath, th))
	}
	sort.Strings(lines)
	for _, ln := range lines {
		h.Write([]byte(ln))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildFilesFingerprint(repoAbs, mode string, cands []Candidate) string {
	if strings.EqualFold(strings.TrimSpace(mode), "content") {
		return filesFingerprintContent(repoAbs, cands)
	}
	return filesFingerprintStat(cands)
}

func relevanceFingerprint(lang, tf, e2e, mono, layoutFP string) string {
	h := sha256.New()
	fmt.Fprintf(h, "lang=%s|tf=%s|e2e=%s|mono=%s|layout=%s",
		strings.ToLower(strings.TrimSpace(lang)),
		strings.TrimSpace(tf),
		strings.TrimSpace(e2e),
		normalizeMonoPrefix(mono),
		layoutFP,
	)
	return hex.EncodeToString(h.Sum(nil))
}

func tryLoadCache(repoAbs, cacheRel, filesFP, relFP, cfgFP string) (*Snapshot, []RankedCandidate, bool) {
	if cacheRel == "" {
		return nil, nil, false
	}
	abs := filepath.Join(repoAbs, filepath.FromSlash(cacheRel))
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, false
	}
	var dc diskCache
	if json.Unmarshal(b, &dc) != nil {
		return nil, nil, false
	}
	if dc.FormatVersion != CacheFormatVersion ||
		dc.FilesFingerprint != filesFP ||
		dc.RelevanceFingerprint != relFP ||
		dc.ConfigFingerprint != cfgFP {
		return nil, nil, false
	}
	snap := &Snapshot{Markdown: dc.Markdown, Diagnostics: append([]string(nil), dc.Diagnostics...)}
	var cands []RankedCandidate
	for _, cc := range dc.Candidates {
		cands = append(cands, RankedCandidate{
			Candidate:    Candidate{RelPath: cc.RelPath, Kind: DocKind(cc.Kind)},
			Score:        cc.Score,
			Content:      cc.Content,
			DocEmbedding: append([]float32(nil), cc.DocEmbedding...),
		})
	}
	return snap, cands, true
}

func saveCacheAtomic(repoAbs, cacheRel string, dc diskCache) error {
	if cacheRel == "" {
		return nil
	}
	dir := filepath.Join(repoAbs, filepath.FromSlash(filepath.Dir(cacheRel)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dc.FormatVersion = CacheFormatVersion
	dc.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(&dc, "", "  ")
	if err != nil {
		return err
	}
	dest := filepath.Join(repoAbs, filepath.FromSlash(cacheRel))
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	if err := replaceCacheFile(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func replaceCacheFile(tmp, dest string) error {
	if err := os.Rename(tmp, dest); err == nil {
		return nil
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("project intel cache: cannot replace %q: %w", dest, err)
	}
	return os.Rename(tmp, dest)
}
