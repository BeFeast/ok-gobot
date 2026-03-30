package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// hermesToolCallRe matches <tool_call>...</tool_call> blocks in model output.
// The Hermes format (NousResearch) embeds tool calls as XML-like tags:
//
//	<tool_call>
//	{"name": "fn", "arguments": {...}}
//	</tool_call>
var hermesToolCallRe = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)

// hermesCallJSON is the JSON payload inside a <tool_call> tag.
type hermesCallJSON struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// IsHermesModel reports whether a model name indicates a Hermes-series model.
// Detection is case-insensitive and matches any model name containing "hermes"
// (e.g. "hermes3", "nous-hermes-2-mistral-7b", "openhermes-2.5-mistral-7b").
func IsHermesModel(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "hermes")
}

// ParseHermesToolCalls extracts <tool_call>...</tool_call> blocks from content
// produced by Hermes-series models. It returns:
//   - cleanContent: the original content with all <tool_call> blocks removed and trimmed
//   - toolCalls: the parsed ToolCall slice (nil when no valid blocks are found)
//
// Blocks whose inner payload is not valid JSON are left in the content as-is.
func ParseHermesToolCalls(content string) (string, []ToolCall) {
	allMatches := hermesToolCallRe.FindAllStringSubmatchIndex(content, -1)
	if len(allMatches) == 0 {
		return content, nil
	}

	var toolCalls []ToolCall
	var cleaned strings.Builder
	lastEnd := 0
	callIndex := 0

	for _, match := range allMatches {
		fullStart, fullEnd := match[0], match[1]
		innerStart, innerEnd := match[2], match[3]
		innerJSON := strings.TrimSpace(content[innerStart:innerEnd])

		var tc hermesCallJSON
		if err := json.Unmarshal([]byte(innerJSON), &tc); err != nil || tc.Name == "" {
			// Not a valid tool call — keep original text
			cleaned.WriteString(content[lastEnd:fullEnd])
			lastEnd = fullEnd
			continue
		}

		// Normalise arguments: empty/null → "{}"
		argsJSON := strings.TrimSpace(string(tc.Arguments))
		if argsJSON == "" || argsJSON == "null" {
			argsJSON = "{}"
		}

		// Append content before this match (skip the block itself)
		cleaned.WriteString(content[lastEnd:fullStart])
		lastEnd = fullEnd

		toolCalls = append(toolCalls, ToolCall{
			ID:   fmt.Sprintf("call_%d", callIndex),
			Type: "function",
			Function: FunctionCall{
				Name:      tc.Name,
				Arguments: argsJSON,
			},
		})
		callIndex++
	}

	// Append remaining content after the last match
	cleaned.WriteString(content[lastEnd:])

	return strings.TrimSpace(cleaned.String()), toolCalls
}
