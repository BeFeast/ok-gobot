package memory

import (
	"regexp"
	"strings"
)

const (
	// DefaultChunkSoftTargetTokens is the preferred chunk size target.
	DefaultChunkSoftTargetTokens = 512
	// DefaultChunkHardMaxTokens is the maximum allowed chunk size.
	DefaultChunkHardMaxTokens = 800
	approxCharsPerToken       = 4
)

var (
	atxHeaderRE = regexp.MustCompile(`^ {0,3}(#{1,3})[ \t]+(.+?)\s*$`)
	fenceStart  = regexp.MustCompile(`^ {0,3}(` + "```|~~~" + `)`)
)

// MarkdownChunk stores a single chunk of markdown memory text with indexing metadata.
type MarkdownChunk struct {
	SourceFile   string   `json:"source_file"`
	HeaderPath   []string `json:"header_path"`
	ChunkOrdinal int      `json:"chunk_ordinal"`
	Content      string   `json:"content"`
}

// ChunkerConfig configures markdown chunking behavior.
type ChunkerConfig struct {
	SoftTargetChars int
	HardMaxChars    int
}

// DefaultChunkerConfig returns the default chunking limits.
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{
		SoftTargetChars: DefaultChunkSoftTargetTokens * approxCharsPerToken,
		HardMaxChars:    DefaultChunkHardMaxTokens * approxCharsPerToken,
	}
}

// MarkdownChunker splits markdown files into header-aware chunks.
type MarkdownChunker struct {
	softTargetChars int
	hardMaxChars    int
}

// NewMarkdownChunker creates a chunker using default limits.
func NewMarkdownChunker() *MarkdownChunker {
	return NewMarkdownChunkerWithConfig(DefaultChunkerConfig())
}

// NewMarkdownChunkerWithConfig creates a chunker with custom limits.
func NewMarkdownChunkerWithConfig(cfg ChunkerConfig) *MarkdownChunker {
	defaults := DefaultChunkerConfig()
	if cfg.SoftTargetChars <= 0 {
		cfg.SoftTargetChars = defaults.SoftTargetChars
	}
	if cfg.HardMaxChars <= 0 {
		cfg.HardMaxChars = defaults.HardMaxChars
	}
	if cfg.HardMaxChars < cfg.SoftTargetChars {
		cfg.HardMaxChars = cfg.SoftTargetChars
	}
	return &MarkdownChunker{
		softTargetChars: cfg.SoftTargetChars,
		hardMaxChars:    cfg.HardMaxChars,
	}
}

// ChunkMarkdown chunks markdown with default chunker settings.
func ChunkMarkdown(sourceFile, markdown string) []MarkdownChunk {
	return NewMarkdownChunker().Chunk(sourceFile, markdown)
}

// Chunk splits markdown into header-aware chunks.
//
// A section starts at a header (#, ##, ###) and includes all text until the
// next same-or-higher header.
func (c *MarkdownChunker) Chunk(sourceFile, markdown string) []MarkdownChunk {
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	sections := buildSections(lines)

	var chunks []MarkdownChunk
	for _, section := range sections {
		parts := c.splitSection(section)
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			chunks = append(chunks, MarkdownChunk{
				SourceFile: sourceFile,
				HeaderPath: append([]string(nil), section.headerPath...),
				Content:    part,
			})
		}
	}

	for i := range chunks {
		chunks[i].ChunkOrdinal = i
	}

	return chunks
}

type markdownHeader struct {
	line  int
	level int
	title string
	path  []string
}

type markdownSection struct {
	headerLine string
	headerPath []string
	body       string
}

func buildSections(lines []string) []markdownSection {
	headers := collectHeaders(lines)
	if len(headers) == 0 {
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		if body == "" {
			return nil
		}
		return []markdownSection{{body: body}}
	}

	var sections []markdownSection
	if headers[0].line > 0 {
		preamble := strings.TrimSpace(strings.Join(lines[:headers[0].line], "\n"))
		if preamble != "" {
			sections = append(sections, markdownSection{body: preamble})
		}
	}

	for i, h := range headers {
		end := len(lines)
		for j := i + 1; j < len(headers); j++ {
			if headers[j].level <= h.level {
				end = headers[j].line
				break
			}
		}

		bodyLines := ""
		if h.line+1 < end {
			bodyLines = strings.TrimSpace(strings.Join(lines[h.line+1:end], "\n"))
		}

		sections = append(sections, markdownSection{
			headerLine: strings.TrimSpace(lines[h.line]),
			headerPath: append([]string(nil), h.path...),
			body:       bodyLines,
		})
	}

	return sections
}

func collectHeaders(lines []string) []markdownHeader {
	var headers []markdownHeader
	var stack []markdownHeader
	inFence := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fenceStart.MatchString(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		matches := atxHeaderRE.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}

		level := len(matches[1])
		title := normalizeHeaderTitle(matches[2])
		if title == "" {
			continue
		}

		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			stack = stack[:len(stack)-1]
		}

		header := markdownHeader{
			line:  i,
			level: level,
			title: title,
		}
		stack = append(stack, header)
		header.path = make([]string, len(stack))
		for idx := range stack {
			header.path[idx] = stack[idx].title
		}
		headers = append(headers, header)
	}

	return headers
}

func normalizeHeaderTitle(raw string) string {
	title := strings.TrimSpace(raw)
	title = strings.TrimRight(title, "#")
	return strings.TrimSpace(title)
}

func (c *MarkdownChunker) splitSection(section markdownSection) []string {
	headerLine := strings.TrimSpace(section.headerLine)
	body := strings.TrimSpace(section.body)

	if headerLine == "" {
		return c.packBody("", body)
	}
	if body == "" {
		return []string{headerLine}
	}
	return c.packBody(headerLine, body)
}

func (c *MarkdownChunker) packBody(headerLine, body string) []string {
	separator := ""
	if headerLine != "" {
		separator = "\n\n"
	}

	prefixLen := len(headerLine) + len(separator)
	softBodyLimit := c.softTargetChars - prefixLen
	hardBodyLimit := c.hardMaxChars - prefixLen
	if softBodyLimit <= 0 {
		softBodyLimit = 1
	}
	if hardBodyLimit <= 0 {
		hardBodyLimit = 1
	}
	if hardBodyLimit < softBodyLimit {
		hardBodyLimit = softBodyLimit
	}

	paragraphs := splitParagraphs(body)
	if len(paragraphs) == 0 {
		if headerLine == "" {
			return nil
		}
		return []string{headerLine}
	}

	var bodyParts []string
	current := ""
	appendCurrent := func() {
		if strings.TrimSpace(current) == "" {
			return
		}
		bodyParts = append(bodyParts, current)
		current = ""
	}

	for _, paragraph := range paragraphs {
		segments := []string{paragraph}
		if len(paragraph) > hardBodyLimit {
			segments = splitByHardLimit(paragraph, hardBodyLimit)
		}

		for _, segment := range segments {
			if current == "" {
				current = segment
				continue
			}

			candidate := current + "\n\n" + segment
			if len(candidate) <= softBodyLimit {
				current = candidate
				continue
			}

			appendCurrent()
			current = segment
		}
	}
	appendCurrent()

	var chunks []string
	for _, part := range bodyParts {
		if headerLine == "" {
			chunks = append(chunks, part)
			continue
		}
		chunks = append(chunks, headerLine+separator+part)
	}
	if len(chunks) == 0 && headerLine != "" {
		chunks = append(chunks, headerLine)
	}
	return chunks
}

func splitParagraphs(body string) []string {
	parts := strings.Split(body, "\n\n")
	paragraphs := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		paragraphs = append(paragraphs, trimmed)
	}
	return paragraphs
}

func splitByHardLimit(text string, hardLimit int) []string {
	if hardLimit <= 0 || len(text) <= hardLimit {
		return []string{strings.TrimSpace(text)}
	}

	remaining := strings.TrimSpace(text)
	var out []string
	for len(remaining) > hardLimit {
		cut := pickSplitPoint(remaining, hardLimit)
		part := strings.TrimSpace(remaining[:cut])
		if part == "" {
			cut = hardLimit
			part = strings.TrimSpace(remaining[:cut])
		}
		if part != "" {
			out = append(out, part)
		}
		remaining = strings.TrimSpace(remaining[cut:])
	}
	if remaining != "" {
		out = append(out, remaining)
	}
	return out
}

func pickSplitPoint(text string, hardLimit int) int {
	if hardLimit >= len(text) {
		return len(text)
	}
	min := hardLimit / 2
	if min < 1 {
		min = 1
	}

	for i := hardLimit; i > min; i-- {
		if text[i] == '\n' {
			return i
		}
	}
	for i := hardLimit; i > min; i-- {
		if text[i] == ' ' {
			return i
		}
	}

	return hardLimit
}
