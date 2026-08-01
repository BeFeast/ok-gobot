package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// DefaultContextPackMaxChars bounds the rendered memory prompt section.
	DefaultContextPackMaxChars = 4000
	// DefaultContextPackMaxItems bounds the number of cited snippets in a pack.
	DefaultContextPackMaxItems = 5
	// DefaultContextPackSnippetChars bounds each snippet before final pack trimming.
	DefaultContextPackSnippetChars = 700
	// DefaultContextPackSearchTopK fetches extra hits so dedupe has room to work.
	DefaultContextPackSearchTopK = 12

	minContextPackItemChars = 120
)

// ContextPackSearcher is the search dependency used by ContextPackBuilder.
type ContextPackSearcher interface {
	Search(ctx context.Context, query string, topK int) ([]MemoryResult, error)
}

// ContextPackBuilder assembles ranked, cited memory snippets into a bounded
// prompt section.
type ContextPackBuilder struct {
	searcher ContextPackSearcher
}

// NewContextPackBuilder creates a reusable context pack builder.
func NewContextPackBuilder(searcher ContextPackSearcher) *ContextPackBuilder {
	return &ContextPackBuilder{searcher: searcher}
}

// ContextPackScope carries optional session metadata for traceability.
type ContextPackScope struct {
	SessionKey string
	ChatID     int64
	UserID     int64
	AgentName  string
	Surface    string
}

// ContextPackBudget controls search breadth and rendered output size.
type ContextPackBudget struct {
	MaxChars     int
	MaxTokens    int
	MaxItems     int
	SnippetChars int
	SearchTopK   int
}

// ContextPackRequest describes one memory pack build.
type ContextPackRequest struct {
	Query   string
	Scope   ContextPackScope
	Budget  ContextPackBudget
	Results []MemoryResult // Optional pre-fetched results from extra sources.
}

// ContextCitation identifies where a memory snippet came from.
type ContextCitation struct {
	SourceName string  `json:"source_name"`
	SourcePath string  `json:"source_path"`
	HeaderPath string  `json:"header_path"`
	Locator    string  `json:"locator"`
	Score      float32 `json:"score"`
}

// ContextPackItem is one cited memory snippet included in a pack.
type ContextPackItem struct {
	Citation      ContextCitation `json:"citation"`
	Snippet       string          `json:"snippet"`
	OriginalChars int             `json:"original_chars"`
	Truncated     bool            `json:"truncated"`
}

// ContextPackSource summarizes sources used by a rendered pack.
type ContextPackSource struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Count    int     `json:"count"`
	MaxScore float32 `json:"max_score"`
}

// ContextPackTruncation exposes dedupe and budget trimming metadata.
type ContextPackTruncation struct {
	Truncated         bool   `json:"truncated"`
	Reason            string `json:"reason"`
	BudgetChars       int    `json:"budget_chars"`
	UsedChars         int    `json:"used_chars"`
	OmittedResults    int    `json:"omitted_results"`
	DedupeDropped     int    `json:"dedupe_dropped"`
	SnippetsTruncated int    `json:"snippets_truncated"`
}

// ContextPack is the final bounded memory context section and its metadata.
type ContextPack struct {
	Query        string                `json:"query"`
	Scope        ContextPackScope      `json:"scope"`
	Budget       ContextPackBudget     `json:"budget"`
	Items        []ContextPackItem     `json:"items"`
	Sources      []ContextPackSource   `json:"sources"`
	Text         string                `json:"text"`
	TotalResults int                   `json:"total_results"`
	Truncation   ContextPackTruncation `json:"truncation"`
}

// Build creates a context pack by combining searched and pre-fetched results.
func (b *ContextPackBuilder) Build(ctx context.Context, req ContextPackRequest) (ContextPack, error) {
	budget := normalizeContextPackBudget(req.Budget)
	req.Budget = budget
	req.Query = strings.TrimSpace(req.Query)

	results := append([]MemoryResult(nil), req.Results...)
	if b != nil && b.searcher != nil && req.Query != "" {
		searched, err := b.searcher.Search(ctx, req.Query, budget.SearchTopK)
		if err != nil {
			return ContextPack{}, err
		}
		results = append(results, searched...)
	}

	return BuildContextPackFromResults(req, results), nil
}

// BuildContextPack creates a context pack using this memory manager's searcher.
func (m *MemoryManager) BuildContextPack(ctx context.Context, req ContextPackRequest) (ContextPack, error) {
	if m == nil {
		return BuildContextPackFromResults(req, req.Results), nil
	}
	return NewContextPackBuilder(m).Build(ctx, req)
}

// BuildContextPackFromResults builds a pack from already-collected memory hits.
func BuildContextPackFromResults(req ContextPackRequest, results []MemoryResult) ContextPack {
	budget := normalizeContextPackBudget(req.Budget)
	req.Budget = budget
	req.Query = strings.TrimSpace(req.Query)

	pack := ContextPack{
		Query:        req.Query,
		Scope:        req.Scope,
		Budget:       budget,
		TotalResults: len(results),
		Truncation: ContextPackTruncation{
			BudgetChars: budget.MaxChars,
		},
	}

	ranked := rankContextPackResults(results)
	candidates := make([]ContextPackItem, 0, budget.MaxItems)
	seenNormalized := make([]string, 0, len(ranked))

	for _, result := range ranked {
		if strings.TrimSpace(result.Content) == "" {
			continue
		}

		normalized := normalizeContextPackText(result.Content)
		if normalized == "" {
			continue
		}
		if isNearDuplicateContext(normalized, seenNormalized) {
			pack.Truncation.DedupeDropped++
			continue
		}
		seenNormalized = append(seenNormalized, normalized)

		if len(candidates) >= budget.MaxItems {
			pack.Truncation.OmittedResults++
			continue
		}

		item := contextPackItemFromResult(result, budget.SnippetChars)
		if item.Truncated {
			pack.Truncation.SnippetsTruncated++
		}
		candidates = append(candidates, item)
	}

	pack.Items = candidates
	renderContextPack(&pack)
	return pack
}

// HasContent reports whether the pack contains memory items.
func (p ContextPack) HasContent() bool {
	return len(p.Items) > 0
}

// SourceSummary returns a compact operator-facing source explanation.
func (p ContextPack) SourceSummary() string {
	if len(p.Sources) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(p.Sources))
	for _, source := range p.Sources {
		label := source.Path
		if label == "" {
			label = source.Name
		}
		if label == "" {
			label = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%s (%d, max %.2f)", label, source.Count, source.MaxScore))
	}
	return strings.Join(parts, "; ")
}

func normalizeContextPackBudget(budget ContextPackBudget) ContextPackBudget {
	if budget.MaxChars <= 0 && budget.MaxTokens > 0 {
		budget.MaxChars = budget.MaxTokens * approxCharsPerToken
	}
	if budget.MaxChars <= 0 {
		budget.MaxChars = DefaultContextPackMaxChars
	}
	if budget.MaxItems <= 0 {
		budget.MaxItems = DefaultContextPackMaxItems
	}
	if budget.SnippetChars <= 0 {
		budget.SnippetChars = DefaultContextPackSnippetChars
	}
	if budget.SearchTopK <= 0 {
		budget.SearchTopK = DefaultContextPackSearchTopK
	}
	return budget
}

func rankContextPackResults(results []MemoryResult) []MemoryResult {
	ranked := append([]MemoryResult(nil), results...)
	sort.SliceStable(ranked, func(i, j int) bool {
		leftScore := contextResultScore(ranked[i])
		rightScore := contextResultScore(ranked[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if ranked[i].SourceFile != ranked[j].SourceFile {
			return ranked[i].SourceFile < ranked[j].SourceFile
		}
		if ranked[i].HeaderPath != ranked[j].HeaderPath {
			return ranked[i].HeaderPath < ranked[j].HeaderPath
		}
		return ranked[i].ChunkOrdinal < ranked[j].ChunkOrdinal
	})
	return ranked
}

func contextPackItemFromResult(result MemoryResult, snippetChars int) ContextPackItem {
	snippet, truncated := makeContextSnippet(result.Content, snippetChars)
	return ContextPackItem{
		Citation: ContextCitation{
			SourceName: contextSourceName(result),
			SourcePath: contextSourcePath(result),
			HeaderPath: strings.TrimSpace(result.HeaderPath),
			Locator:    contextLocator(result),
			Score:      contextResultScore(result),
		},
		Snippet:       snippet,
		OriginalChars: len(result.Content),
		Truncated:     truncated,
	}
}

func contextResultScore(result MemoryResult) float32 {
	if result.Score != 0 {
		return result.Score
	}
	if result.HybridScore != 0 {
		return result.HybridScore
	}
	return result.Similarity
}

func contextSourceName(result MemoryResult) string {
	source := strings.TrimSpace(result.Source)
	if source == "" {
		source = strings.TrimSpace(result.SourceFile)
	}
	if source == "" {
		return "unknown"
	}
	base := filepath.Base(source)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return source
	}
	return base
}

func contextSourcePath(result MemoryResult) string {
	path := strings.TrimSpace(result.SourceFile)
	if path == "" {
		path = strings.TrimSpace(result.Source)
	}
	return path
}

func contextLocator(result MemoryResult) string {
	switch {
	case result.StartLine > 0 && result.EndLine > result.StartLine:
		return fmt.Sprintf("lines %d-%d", result.StartLine, result.EndLine)
	case result.StartLine > 0 && result.EndLine == result.StartLine:
		return fmt.Sprintf("line %d", result.StartLine)
	default:
		return fmt.Sprintf("chunk %d", result.ChunkOrdinal)
	}
}

func makeContextSnippet(content string, limit int) (string, bool) {
	content = collapseContextWhitespace(content)
	if limit <= 0 || len(content) <= limit {
		return content, false
	}
	if limit <= 3 {
		return content[:limit], true
	}
	return strings.TrimSpace(content[:limit-3]) + "...", true
}

func renderContextPack(pack *ContextPack) {
	limit := pack.Budget.MaxChars
	if limit <= 0 {
		limit = DefaultContextPackMaxChars
	}

	var out strings.Builder
	if !appendContextWithinBudget(&out, limit, "## Memory Context Pack\n\n") {
		pack.Text = truncateContextText("## Memory Context Pack\n", limit)
		pack.Truncation.Truncated = true
		pack.Truncation.Reason = "budget"
		pack.Truncation.UsedChars = len(pack.Text)
		pack.Items = nil
		pack.Sources = nil
		return
	}

	if pack.Query != "" {
		appendContextWithinBudget(&out, limit, fmt.Sprintf("Query: %s\n", truncateContextLine(pack.Query, 180)))
	}
	if scope := formatContextScope(pack.Scope); scope != "" {
		appendContextWithinBudget(&out, limit, "Scope: "+scope+"\n")
	}
	appendContextWithinBudget(&out, limit, fmt.Sprintf("Budget: %d chars\n\n", limit))

	if len(pack.Items) == 0 {
		if !appendContextWithinBudget(&out, limit, "No relevant memory found.\n") {
			pack.Truncation.Truncated = true
			pack.Truncation.Reason = "budget"
		}
		pack.Text = out.String()
		pack.Truncation.UsedChars = len(pack.Text)
		pack.Sources = nil
		return
	}

	included := make([]ContextPackItem, 0, len(pack.Items))
	for i, item := range pack.Items {
		block := formatContextPackItem(i+1, item)
		if appendContextWithinBudget(&out, limit, block) {
			included = append(included, item)
			continue
		}

		remaining := limit - out.Len()
		if remaining >= minContextPackItemChars {
			trimmed := item
			trimmed.Snippet, trimmed.Truncated = makeContextSnippet(item.Snippet, remaining/2)
			block = formatContextPackItem(i+1, trimmed)
			if len(block) > remaining {
				block = truncateContextText(block, remaining)
			}
			if appendContextWithinBudget(&out, limit, block) {
				included = append(included, trimmed)
				pack.Truncation.SnippetsTruncated++
			}
		}

		pack.Truncation.Truncated = true
		pack.Truncation.Reason = "budget"
		pack.Truncation.OmittedResults += len(pack.Items) - len(included)
		break
	}

	if pack.Truncation.Truncated {
		appendContextWithinBudget(&out, limit, "Pack truncated by memory context budget.\n")
	}

	pack.Items = included
	pack.Sources = aggregateContextPackSources(included)
	pack.Text = out.String()
	pack.Truncation.UsedChars = len(pack.Text)
}

func formatContextPackItem(index int, item ContextPackItem) string {
	var out strings.Builder
	c := item.Citation
	out.WriteString(fmt.Sprintf("[%d] Source: %s", index, emptyContextValue(c.SourceName)))
	if c.SourcePath != "" {
		out.WriteString(fmt.Sprintf(" (path: %s", c.SourcePath))
		if c.Locator != "" {
			out.WriteString(", ")
			out.WriteString(c.Locator)
		}
		out.WriteString(fmt.Sprintf(", score: %.2f)", c.Score))
	} else {
		out.WriteString(fmt.Sprintf(" (%s, score: %.2f)", emptyContextValue(c.Locator), c.Score))
	}
	out.WriteString("\n")
	if c.HeaderPath != "" && c.HeaderPath != defaultHeaderPath {
		out.WriteString("Header: ")
		out.WriteString(c.HeaderPath)
		out.WriteString("\n")
	}
	out.WriteString("Snippet: ")
	out.WriteString(item.Snippet)
	out.WriteString("\n\n")
	return out.String()
}

func aggregateContextPackSources(items []ContextPackItem) []ContextPackSource {
	type key struct{ name, path string }
	seen := make(map[key]int)
	sources := make([]ContextPackSource, 0, len(items))
	for _, item := range items {
		k := key{item.Citation.SourceName, item.Citation.SourcePath}
		idx, ok := seen[k]
		if !ok {
			seen[k] = len(sources)
			sources = append(sources, ContextPackSource{
				Name:     item.Citation.SourceName,
				Path:     item.Citation.SourcePath,
				Count:    1,
				MaxScore: item.Citation.Score,
			})
			continue
		}
		sources[idx].Count++
		if item.Citation.Score > sources[idx].MaxScore {
			sources[idx].MaxScore = item.Citation.Score
		}
	}
	return sources
}

func formatContextScope(scope ContextPackScope) string {
	parts := make([]string, 0, 5)
	if scope.SessionKey != "" {
		parts = append(parts, "session="+scope.SessionKey)
	}
	if scope.ChatID != 0 {
		parts = append(parts, fmt.Sprintf("chat=%d", scope.ChatID))
	}
	if scope.UserID != 0 {
		parts = append(parts, fmt.Sprintf("user=%d", scope.UserID))
	}
	if scope.AgentName != "" {
		parts = append(parts, "agent="+scope.AgentName)
	}
	if scope.Surface != "" {
		parts = append(parts, "surface="+scope.Surface)
	}
	return strings.Join(parts, ", ")
}

func appendContextWithinBudget(out *strings.Builder, limit int, text string) bool {
	if limit <= 0 {
		out.WriteString(text)
		return true
	}
	if out.Len()+len(text) > limit {
		return false
	}
	out.WriteString(text)
	return true
}

func truncateContextLine(text string, limit int) string {
	text = collapseContextWhitespace(text)
	return truncateContextText(text, limit)
}

func truncateContextText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func emptyContextValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func collapseContextWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func normalizeContextPackText(text string) string {
	var out strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
			lastSpace = false
		case !lastSpace:
			out.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func isNearDuplicateContext(normalized string, existing []string) bool {
	for _, prev := range existing {
		if normalized == prev {
			return true
		}
		shorter, longer := normalized, prev
		if len(shorter) > len(longer) {
			shorter, longer = longer, shorter
		}
		if len(shorter) >= 80 && strings.Contains(longer, shorter) {
			return true
		}
		if tokenJaccard(normalized, prev) >= 0.82 {
			return true
		}
	}
	return false
}

func tokenJaccard(a, b string) float64 {
	aTokens := strings.Fields(a)
	bTokens := strings.Fields(b)
	if len(aTokens) == 0 || len(bTokens) == 0 {
		return 0
	}

	aSet := make(map[string]struct{}, len(aTokens))
	for _, token := range aTokens {
		aSet[token] = struct{}{}
	}
	bSet := make(map[string]struct{}, len(bTokens))
	for _, token := range bTokens {
		bSet[token] = struct{}{}
	}

	intersection := 0
	for token := range aSet {
		if _, ok := bSet[token]; ok {
			intersection++
		}
	}
	union := len(aSet) + len(bSet) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
