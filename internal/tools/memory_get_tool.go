package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var markdownHeaderRegexp = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// MemoryGetTool reads markdown memory content by source and header path.
type MemoryGetTool struct {
	basePath string
}

// NewMemoryGetTool creates a memory_get tool.
func NewMemoryGetTool(basePath string) *MemoryGetTool {
	return &MemoryGetTool{basePath: basePath}
}

func (m *MemoryGetTool) Name() string {
	return "memory_get"
}

func (m *MemoryGetTool) Description() string {
	return "Read markdown memory content by source file and optional header path."
}

func (m *MemoryGetTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: memory_get <source> [header_path]")
	}

	source := strings.TrimSpace(args[0])
	headerPath := ""
	if len(args) > 1 {
		headerPath = strings.TrimSpace(args[1])
	}

	fullPath, err := resolvePath(m.basePath, source)
	if err != nil {
		return "", err
	}

	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read source %q: %w", source, err)
	}
	content := string(contentBytes)

	if headerPath == "" {
		return content, nil
	}

	section, err := extractMarkdownSectionByHeaderPath(content, headerPath)
	if err != nil {
		return "", err
	}

	return section, nil
}

func (m *MemoryGetTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"source": map[string]interface{}{
				"type":        "string",
				"description": "Relative markdown source path (for example MEMORY.md or memory/2026-03-04.md)",
			},
			"header_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional header path to a markdown section (for example \"Projects > Ok-Gobot\")",
			},
		},
		"required": []string{"source"},
	}
}

func extractMarkdownSectionByHeaderPath(markdown, headerPath string) (string, error) {
	target := normalizeHeaderPath(parseHeaderPath(headerPath))
	if len(target) == 0 {
		return strings.TrimSpace(markdown), nil
	}

	lines := strings.Split(markdown, "\n")
	headerStack := make([]string, 0, 6)

	sectionStart := -1
	sectionEnd := len(lines)
	targetLevel := 0

	for idx, line := range lines {
		matches := markdownHeaderRegexp.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}

		level := len(matches[1])
		title := normalizeHeaderToken(cleanMarkdownHeaderTitle(matches[2]))

		if level-1 < len(headerStack) {
			headerStack = headerStack[:level-1]
		}
		headerStack = append(headerStack, title)

		if sectionStart >= 0 && level <= targetLevel {
			sectionEnd = idx
			break
		}

		if headersMatch(headerStack, target) {
			sectionStart = idx
			targetLevel = level
		}
	}

	if sectionStart < 0 {
		return "", fmt.Errorf("header path %q not found", headerPath)
	}

	section := strings.TrimSpace(strings.Join(lines[sectionStart:sectionEnd], "\n"))
	if section == "" {
		return "", fmt.Errorf("header path %q resolved to an empty section", headerPath)
	}

	return section, nil
}

func parseHeaderPath(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	parts := strings.Split(path, ">")
	if len(parts) == 1 {
		parts = strings.Split(path, "/")
	}

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeHeaderPath(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizeHeaderToken(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func headersMatch(current, target []string) bool {
	if len(current) < len(target) {
		return false
	}
	start := len(current) - len(target)
	for i := range target {
		if current[start+i] != target[i] {
			return false
		}
	}
	return true
}

func cleanMarkdownHeaderTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimRight(title, "#")
	return strings.TrimSpace(title)
}

func normalizeHeaderToken(s string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	return strings.Join(fields, " ")
}
