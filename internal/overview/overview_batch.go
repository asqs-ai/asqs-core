package overview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// defaultOverviewMaxFilesPerSlice caps how many source file paths go into one index snapshot for one LLM call.
// Large monorepos are split across many calls (Plan B) to avoid huge prompts per LLM call.
const defaultOverviewMaxFilesPerSlice = 400

// defaultOverviewMaxIndexRunesPerSlice caps UTF-8 runes in the index snapshot text for one overview LLM request
// (after grouping by overview_max_files_per_slice). Symbol-heavy slices can still be huge with few files; this
// triggers further file-list splitting or truncation so each LLM request stays bounded.
const defaultOverviewMaxIndexRunesPerSlice = 120_000

const overviewSliceIndexTruncatedTrailer = "... [truncated: this overview slice index exceeded indexer.overview_max_index_runes_per_slice; raise that value, lower overview_max_files_per_slice, or set overview_max_index_runes_per_slice to -1 to disable (may risk EOF on very large slices)]"

// OverviewLLMStats summarizes batched overview generation (metadata → LLM).
type OverviewLLMStats struct {
	Partitions       int
	TotalSourceFiles int
	TotalIndexRunes  int // sum of rune counts of per-slice index bodies sent to the LLM
}

func resolveOverviewMaxFilesPerSlice(n int) int {
	if n <= 0 {
		return defaultOverviewMaxFilesPerSlice
	}
	return n
}

// resolveOverviewMaxIndexRunesPerSlice returns the per-slice index body rune cap and whether it is active.
// cfgVal -1 disables splitting/clamping; 0 uses defaultOverviewMaxIndexRunesPerSlice; >0 uses cfgVal.
func resolveOverviewMaxIndexRunesPerSlice(cfgVal int) (maxRunes int, enabled bool) {
	if cfgVal < 0 {
		return 0, false
	}
	if cfgVal == 0 {
		return defaultOverviewMaxIndexRunesPerSlice, true
	}
	return cfgVal, true
}

// refineOverviewBatchesByIndexRunes subdivides file batches whose built index snapshot exceeds maxRunes.
func refineOverviewBatchesByIndexRunes(ctx context.Context, meta overviewMetaReader, lang string, batches [][]string, maxRunes int) ([][]string, error) {
	if maxRunes <= 0 || len(batches) == 0 {
		return batches, nil
	}
	var out [][]string
	for _, b := range batches {
		sub, err := splitFileBatchByIndexRunes(ctx, meta, lang, b, maxRunes)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

func splitFileBatchByIndexRunes(ctx context.Context, meta overviewMetaReader, lang string, files []string, maxRunes int) ([][]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	body, err := buildOverviewContextForSourceFiles(ctx, meta, lang, files)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(body) <= maxRunes {
		return [][]string{files}, nil
	}
	if len(files) == 1 {
		fmt.Fprintf(os.Stderr, "[asqs-overview] batched: single file %q produces index snapshot > %d runes; slice will be clamped before LLM\n", files[0], maxRunes)
		return [][]string{files}, nil
	}
	mid := len(files) / 2
	if mid < 1 {
		mid = 1
	}
	left, err := splitFileBatchByIndexRunes(ctx, meta, lang, files[:mid], maxRunes)
	if err != nil {
		return nil, err
	}
	right, err := splitFileBatchByIndexRunes(ctx, meta, lang, files[mid:], maxRunes)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// partitionOverviewFileBatches returns disjoint batches of repo-relative source file paths (non-test, overview-eligible),
// ordered by module name then path. Each batch has at most maxFilesPerSlice paths; large modules are split across batches.
func partitionOverviewFileBatches(ctx context.Context, meta overviewMetaReader, lang string, maxFilesPerSlice int) ([][]string, error) {
	if meta == nil {
		return nil, fmt.Errorf("overview: Meta required")
	}
	lang = overviewCanonicalLang(lang)
	if lang == "" {
		return nil, fmt.Errorf("overview: lang required")
	}
	maxFilesPerSlice = resolveOverviewMaxFilesPerSlice(maxFilesPerSlice)

	isTest := false
	files, err := meta.ListFiles(ctx, lang, &isTest)
	if err != nil {
		return nil, err
	}
	byModule := make(map[string][]string)
	for _, f := range files {
		if isOverviewIgnoredPath(f.File) {
			continue
		}
		mod := f.Module
		if mod == "" {
			mod = "(default)"
		}
		byModule[mod] = append(byModule[mod], f.File)
	}
	var modKeys []string
	for k := range byModule {
		modKeys = append(modKeys, k)
	}
	sort.Strings(modKeys)

	var batches [][]string
	for _, mod := range modKeys {
		paths := byModule[mod]
		sort.Strings(paths)
		for start := 0; start < len(paths); start += maxFilesPerSlice {
			end := start + maxFilesPerSlice
			if end > len(paths) {
				end = len(paths)
			}
			chunk := append([]string(nil), paths[start:end]...)
			if len(chunk) > 0 {
				batches = append(batches, chunk)
			}
		}
	}
	return batches, nil
}

// overviewInterSliceCooldown is applied before slice 2+ of Plan B. The first POST often succeeds on a fresh
// TCP/TLS (or HTTP/2) connection; the very next POST typically reuses that connection from the pool. If the
// server or a proxy has already half-closed the socket, or an HTTP/2 stream ends uncleanly, the reused call fails
// with unexpected EOF while slice 1 already completed — a short pause makes a new connection more likely.
const overviewInterSliceCooldown = 800 * time.Millisecond

// overviewEOFResplitSiblingPause is a short pause between two LLM calls created by splitting one index slice after
// unexpected EOF (left then right sibling), to reduce immediate back-to-back reuse of the same connection.
const overviewEOFResplitSiblingPause = 200 * time.Millisecond

// overviewMaxEOFResplitDepth caps recursive file-list bisection after unexpected EOF to avoid runaway splitting.
const overviewMaxEOFResplitDepth = 16

// overviewBatchUserIndexHeading introduces the index snapshot in Plan B user messages. Neutral wording avoids
// priming the model to write about “slices” or “partitions” in the final document.
const overviewBatchUserIndexHeading = "## Indexed symbols and paths\n\n"

// overviewBatchedVoiceRules is appended to the system prompt for Plan B full narrative; internal sequencing must
// not leak into the Markdown output.
const overviewBatchedVoiceRules = `

**Output style (mandatory):** The merged document must read as **one unified repository overview**. In the Markdown you output, never mention or imply batched generation: no slices, portions, partitions, phases, installments, batches, “this section”, “the files listed above”, “the excerpt”, or similar. Do **not** hedge or speculate (e.g. likely, probably, perhaps, maybe, seems, appears to, could, might, or “may” when it signals uncertainty); describe only what the index listing shows, in direct language. Do not invent modules, files, or APIs not present in the user message.`

const overviewOutputContractSliceContinue = `## OUTPUT CONTRACT (mandatory — read last)

Your reply must be **only** Markdown. **Do not** use a single top-level # heading (use ## or ### as your highest-level heading). No preamble (“Here is…”). **Do not** wrap the **entire** reply in a Markdown code fence. **Do not** include Mermaid blocks or sections titled **Module and file structure** or **File dependency graph** — those are maintained automatically. **Do not** output JSON.

Continue the **same** repository overview as a single coherent document: do not mention slices, portions, batches, installments, “this section”, “the paths below”, or any generation mechanics. Do not hedge (likely, probably, perhaps, maybe, seems, appears to, could, might); state only what the index listing supports.`

// GenerateOverviewWithMeta builds the overview from metadata using **batched** LLM calls (Plan B): each HTTP request
// carries at most overview_max_files_per_slice source files’ index slice, then optionally splits further when the
// built index text exceeds overview_max_index_runes_per_slice (see resolveOverviewMaxIndexRunesPerSlice).
// Incremental mode runs one short delta LLM call per batch (same contract as single-slice delta).
func (g *LLMOverviewDocGenerator) GenerateOverviewWithMeta(ctx context.Context, meta overviewMetaReader, lang string, maxFilesPerSlice, maxIndexRunesPerSlice int, opts OverviewGenerateOpts) (content string, path string, stat OverviewLLMStats, err error) {
	if g.LLM == nil {
		return "", "", stat, fmt.Errorf("llm overview generator: ChatCompleter required")
	}
	if meta == nil {
		return "", "", stat, fmt.Errorf("llm overview generator: metadata store required")
	}
	outPath := g.Path
	if outPath == "" {
		outPath = DefaultOverviewPath
	}

	batches, err := partitionOverviewFileBatches(ctx, meta, lang, maxFilesPerSlice)
	if err != nil {
		return "", "", stat, err
	}
	indexRunesCap, indexCapOn := resolveOverviewMaxIndexRunesPerSlice(maxIndexRunesPerSlice)
	if indexCapOn {
		before := len(batches)
		batches, err = refineOverviewBatchesByIndexRunes(ctx, meta, lang, batches, indexRunesCap)
		if err != nil {
			return "", "", stat, err
		}
		if len(batches) != before {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched: refined %d file-batches -> %d by index_runes_cap=%d\n", before, len(batches), indexRunesCap)
		}
	}
	stat.Partitions = len(batches)
	for _, b := range batches {
		stat.TotalSourceFiles += len(b)
	}
	if len(batches) == 0 {
		return "", "", stat, fmt.Errorf("overview: no non-test source files for lang %q", overviewCanonicalLang(lang))
	}

	repoRoot := strings.TrimSpace(opts.RepoRoot)
	incremental := !g.FullRewrite && repoRoot != ""
	var existingOnDisk []byte
	if incremental {
		full := filepath.Join(repoRoot, filepath.FromSlash(outPath))
		if data, readErr := os.ReadFile(full); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
			existingOnDisk = data
		}
	}

	if incremental && len(existingOnDisk) > 0 {
		narrative, _ := SplitOverviewNarrativeAndVisuals(string(existingOnDisk))
		if strings.TrimSpace(narrative) != "" {
			return g.generateOverviewIncrementalBatched(ctx, meta, lang, batches, narrative, outPath, &stat, indexRunesCap, indexCapOn)
		}
		fmt.Fprintln(os.Stderr, "[asqs-overview] batched: incremental skipped (no narrative after split); using full batched narrative")
	} else if incremental && len(existingOnDisk) == 0 {
		fmt.Fprintln(os.Stderr, "[asqs-overview] batched: incremental skipped (no readable overview on disk); using full batched narrative")
	}

	return g.generateOverviewFullBatched(ctx, meta, lang, batches, outPath, &stat, indexRunesCap, indexCapOn)
}

func (g *LLMOverviewDocGenerator) generateOverviewFullBatched(ctx context.Context, meta overviewMetaReader, lang string, batches [][]string, outPath string, stat *OverviewLLMStats, indexBodyMaxRunes int, indexCapOn bool) (content string, path string, outStat OverviewLLMStats, err error) {
	maxTok := 8192
	if g.MaxCompletionTokensFull > 0 {
		maxTok = g.MaxCompletionTokensFull
	}
	baseSystem := strings.TrimSpace(g.Prompt)
	if baseSystem == "" {
		baseSystem = defaultOverviewPrompt
	}

	var sections []string
	for i, files := range batches {
		if i > 0 {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched full: waiting %v before slice %d/%d (avoid immediate reuse of connection from prior slice)\n", overviewInterSliceCooldown, i+1, len(batches))
			select {
			case <-ctx.Done():
				return "", "", *stat, ctx.Err()
			case <-time.After(overviewInterSliceCooldown):
			}
		}
		frag, err := g.completeOverviewFullSliceWithEOFResplit(ctx, meta, lang, i, len(batches), files, i == 0, baseSystem, maxTok, indexBodyMaxRunes, indexCapOn, 0, stat)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched full slice %d Complete failed: %v\n", i+1, err)
			return "", "", *stat, err
		}
		frag = strings.TrimSpace(frag)
		if frag == "" {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched full slice %d warning: empty assistant message\n", i+1)
			continue
		}
		sections = append(sections, frag)
	}
	out := strings.TrimSpace(strings.Join(sections, "\n\n"))
	if out == "" {
		return "", "", *stat, fmt.Errorf("overview: batched full narrative produced empty content")
	}
	return out, outPath, *stat, nil
}

func overviewFullBatchedSliceLabel(logicalIndex, logicalTotal, splitDepth, nFiles int) string {
	if splitDepth == 0 {
		return fmt.Sprintf("partition %d/%d (%d files)", logicalIndex+1, logicalTotal, nFiles)
	}
	return fmt.Sprintf("partition %d/%d EOF sub-split depth=%d (%d files)", logicalIndex+1, logicalTotal, splitDepth, nFiles)
}

func overviewFullBatchedMessages(baseSystem string, logicalIndex, logicalTotal int, allowTopLevelHash bool, body string) (system, user string) {
	if logicalIndex == 0 && allowTopLevelHash {
		system = baseSystem + overviewBatchedVoiceRules + "\n\nYou receive **index material 1 of " + strconv.Itoa(logicalTotal) + "** for the same overview (internal sequencing only — **never** mention numbering, installments, or batches in your Markdown). Produce Markdown with exactly **one** top-level `#` heading (repository or product name inferred only from paths and modules in the user message). Then use `##` / `###` for structure. Cover only what appears under \"Indexed symbols and paths\" in the user message."
		user = overviewBatchUserIndexHeading + body
		user = appendOverviewUserOutputContract(user, false)
		return system, user
	}
	system = baseSystem + overviewBatchedVoiceRules + "\n\nYou receive **index material " + strconv.Itoa(logicalIndex+1) + " of " + strconv.Itoa(logicalTotal) + "** for the same overview (internal sequencing only — **never** mention it in your Markdown). Output **only** Markdown that continues that overview: your highest-level heading must be `##` or `###` (do **not** use `#`). Cover only what appears under \"Indexed symbols and paths\" in the user message. No preamble."
	user = overviewBatchUserIndexHeading + body
	user = user + "\n\n" + overviewOutputContractSliceContinue
	return system, user
}

// completeOverviewFullSliceWithEOFResplit runs one Plan B full-narrative LLM call for the given file paths, or on
// unexpected EOF bisects the file list and runs two smaller calls (same logical slice index) so payload shrinks.
func (g *LLMOverviewDocGenerator) completeOverviewFullSliceWithEOFResplit(
	ctx context.Context,
	meta overviewMetaReader,
	lang string,
	logicalIndex, logicalTotal int,
	files []string,
	allowTopLevelHash bool,
	baseSystem string,
	maxTok int,
	indexBodyMaxRunes int,
	indexCapOn bool,
	splitDepth int,
	stat *OverviewLLMStats,
) (string, error) {
	if splitDepth > overviewMaxEOFResplitDepth {
		return "", fmt.Errorf("overview: batched full EOF resplit exceeded max depth %d", overviewMaxEOFResplitDepth)
	}
	body, err := buildOverviewContextForSourceFiles(ctx, meta, lang, files)
	if err != nil {
		return "", err
	}
	if indexCapOn && indexBodyMaxRunes > 0 {
		br := utf8.RuneCountInString(body)
		if br > indexBodyMaxRunes {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched full slice %d sub-depth=%d: clamping index body from %d to %d runes\n", logicalIndex+1, splitDepth, br, indexBodyMaxRunes)
			body = truncateUTF8ToMaxRunesWithTrailer(body, indexBodyMaxRunes, overviewSliceIndexTruncatedTrailer)
		}
	}
	stat.TotalIndexRunes += utf8.RuneCountInString(body)
	label := overviewFullBatchedSliceLabel(logicalIndex, logicalTotal, splitDepth, len(files))
	fmt.Fprintf(os.Stderr, "[asqs-overview] batched full slice %s index_runes=%d\n", label, utf8.RuneCountInString(body))

	system, user := overviewFullBatchedMessages(baseSystem, logicalIndex, logicalTotal, allowTopLevelHash, body)
	messages := []model.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	llLabel := fmt.Sprintf("batched_full_slice_%d", logicalIndex+1)
	if splitDepth > 0 {
		llLabel = fmt.Sprintf("%s_d%d", llLabel, splitDepth)
	}
	result, err := overviewLLMCompleteBatchedSlice(ctx, g.LLM, llLabel, messages, model.CompleteOptions{MaxTokens: maxTok})
	if err == nil {
		return strings.TrimSpace(result.Content), nil
	}
	if !isUnexpectedEOFChatError(err) || len(files) <= 1 {
		return "", err
	}
	mid := len(files) / 2
	if mid < 1 {
		mid = 1
	}
	fmt.Fprintf(os.Stderr, "[asqs-overview] batched full: partition %d/%d unexpected EOF with %d files — splitting into %d + %d (sub-depth %d): %v\n",
		logicalIndex+1, logicalTotal, len(files), mid, len(files)-mid, splitDepth, err)
	leftFiles := files[:mid]
	rightFiles := files[mid:]
	left, err := g.completeOverviewFullSliceWithEOFResplit(ctx, meta, lang, logicalIndex, logicalTotal, leftFiles, allowTopLevelHash, baseSystem, maxTok, indexBodyMaxRunes, indexCapOn, splitDepth+1, stat)
	if err != nil {
		return "", err
	}
	if len(rightFiles) > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(overviewEOFResplitSiblingPause):
		}
	}
	right, err := g.completeOverviewFullSliceWithEOFResplit(ctx, meta, lang, logicalIndex, logicalTotal, rightFiles, false, baseSystem, maxTok, indexBodyMaxRunes, indexCapOn, splitDepth+1, stat)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.Join([]string{strings.TrimSpace(left), strings.TrimSpace(right)}, "\n\n")), nil
}

func overviewIncrementalBatchedUserMessage(today, narrative, body string) string {
	return appendOverviewUserOutputContract(
		"Today's date (UTC): "+today+"\n\nEXISTING DOCUMENT:\n\n"+narrative+"\n\n---\n\n"+overviewBatchUserIndexHeading+body,
		true,
	)
}

func normalizeIncrementalDeltaContent(raw string) string {
	rawDelta := strings.TrimSpace(raw)
	delta := strings.TrimSpace(extractCodeBlockContent(rawDelta))
	if delta == "" && rawDelta != "" {
		return rawDelta
	}
	return delta
}

// completeOverviewIncrementalSliceWithEOFResplit runs one batched incremental delta LLM call for the given file
// paths, or on unexpected EOF bisects the file list into two smaller calls (same logical partition).
func (g *LLMOverviewDocGenerator) completeOverviewIncrementalSliceWithEOFResplit(
	ctx context.Context,
	meta overviewMetaReader,
	lang string,
	logicalIndex, logicalTotal int,
	files []string,
	narrative, system, today string,
	indexBodyMaxRunes int,
	indexCapOn bool,
	splitDepth int,
	stat *OverviewLLMStats,
) (string, error) {
	if splitDepth > overviewMaxEOFResplitDepth {
		return "", fmt.Errorf("overview: batched incremental EOF resplit exceeded max depth %d", overviewMaxEOFResplitDepth)
	}
	body, err := buildOverviewContextForSourceFiles(ctx, meta, lang, files)
	if err != nil {
		return "", err
	}
	if indexCapOn && indexBodyMaxRunes > 0 {
		br := utf8.RuneCountInString(body)
		if br > indexBodyMaxRunes {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched incremental slice %d sub-depth=%d: clamping index body from %d to %d runes\n", logicalIndex+1, splitDepth, br, indexBodyMaxRunes)
			body = truncateUTF8ToMaxRunesWithTrailer(body, indexBodyMaxRunes, overviewSliceIndexTruncatedTrailer)
		}
	}
	stat.TotalIndexRunes += utf8.RuneCountInString(body)
	label := overviewFullBatchedSliceLabel(logicalIndex, logicalTotal, splitDepth, len(files))
	fmt.Fprintf(os.Stderr, "[asqs-overview] batched incremental slice %s index_runes=%d\n", label, utf8.RuneCountInString(body))

	user := overviewIncrementalBatchedUserMessage(today, narrative, body)
	messages := []model.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	llLabel := fmt.Sprintf("batched_incremental_slice_%d", logicalIndex+1)
	if splitDepth > 0 {
		llLabel = fmt.Sprintf("%s_d%d", llLabel, splitDepth)
	}
	result, err := overviewLLMCompleteBatchedSlice(ctx, g.LLM, llLabel, messages, model.CompleteOptions{MaxTokens: 2048})
	if err == nil {
		d := normalizeIncrementalDeltaContent(result.Content)
		if strings.EqualFold(strings.TrimSpace(d), "NO_UPDATES") {
			return "", nil
		}
		return d, nil
	}
	if !isUnexpectedEOFChatError(err) || len(files) <= 1 {
		return "", err
	}
	mid := len(files) / 2
	if mid < 1 {
		mid = 1
	}
	fmt.Fprintf(os.Stderr, "[asqs-overview] batched incremental: partition %d/%d unexpected EOF with %d files — splitting into %d + %d (sub-depth %d): %v\n",
		logicalIndex+1, logicalTotal, len(files), mid, len(files)-mid, splitDepth, err)
	leftFiles := files[:mid]
	rightFiles := files[mid:]
	left, err := g.completeOverviewIncrementalSliceWithEOFResplit(ctx, meta, lang, logicalIndex, logicalTotal, leftFiles, narrative, system, today, indexBodyMaxRunes, indexCapOn, splitDepth+1, stat)
	if err != nil {
		return "", err
	}
	if len(rightFiles) > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(overviewEOFResplitSiblingPause):
		}
	}
	right, err := g.completeOverviewIncrementalSliceWithEOFResplit(ctx, meta, lang, logicalIndex, logicalTotal, rightFiles, narrative, system, today, indexBodyMaxRunes, indexCapOn, splitDepth+1, stat)
	if err != nil {
		return "", err
	}
	var parts []string
	if strings.TrimSpace(left) != "" {
		parts = append(parts, strings.TrimSpace(left))
	}
	if strings.TrimSpace(right) != "" {
		parts = append(parts, strings.TrimSpace(right))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (g *LLMOverviewDocGenerator) generateOverviewIncrementalBatched(ctx context.Context, meta overviewMetaReader, lang string, batches [][]string, narrative, outPath string, stat *OverviewLLMStats, indexBodyMaxRunes int, indexCapOn bool) (content string, path string, outStat OverviewLLMStats, err error) {
	system := strings.TrimSpace(g.DeltaPrompt)
	if system == "" {
		system = defaultOverviewDeltaPrompt
	}
	today := time.Now().UTC().Format("2006-01-02")

	var deltas []string
	for i, files := range batches {
		if i > 0 {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched incremental: waiting %v before slice %d/%d (avoid immediate reuse of connection from prior slice)\n", overviewInterSliceCooldown, i+1, len(batches))
			select {
			case <-ctx.Done():
				return "", "", *stat, ctx.Err()
			case <-time.After(overviewInterSliceCooldown):
			}
		}
		delta, err := g.completeOverviewIncrementalSliceWithEOFResplit(ctx, meta, lang, i, len(batches), files, narrative, system, today, indexBodyMaxRunes, indexCapOn, 0, stat)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[asqs-overview] batched incremental slice %d Complete failed: %v\n", i+1, err)
			return "", "", *stat, err
		}
		delta = strings.TrimSpace(delta)
		if delta == "" || strings.EqualFold(delta, "NO_UPDATES") {
			continue
		}
		deltas = append(deltas, delta)
	}
	if len(deltas) == 0 {
		return narrative, outPath, *stat, nil
	}
	out := narrative + "\n\n" + strings.Join(deltas, "\n\n")
	return strings.TrimSpace(out), outPath, *stat, nil
}
