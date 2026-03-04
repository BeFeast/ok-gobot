package configschema

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	canonicalStartMarker = "<!-- CONFIG_CANONICAL:START -->"
	canonicalEndMarker   = "<!-- CONFIG_CANONICAL:END -->"
)

// Node defines one canonical config key in ARCHITECTURE-v2 section 8.
// It intentionally mirrors JSON Schema fields used by this project.
type Node struct {
	Type                 string           `json:"type"`
	Default              any              `json:"default"`
	Description          string           `json:"description"`
	Enum                 []string         `json:"enum,omitempty"`
	Properties           map[string]*Node `json:"properties,omitempty"`
	Items                *Node            `json:"items,omitempty"`
	AdditionalProperties *Node            `json:"additionalProperties,omitempty"`
}

// LoadCanonicalNodeFromFile extracts and parses the canonical config block.
func LoadCanonicalNodeFromFile(path string) (*Node, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read canonical source: %w", err)
	}
	return LoadCanonicalNode(content)
}

// LoadCanonicalNode extracts and parses the canonical config block.
func LoadCanonicalNode(markdown []byte) (*Node, error) {
	raw, err := extractCanonicalJSON(markdown)
	if err != nil {
		return nil, err
	}

	var root Node
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse canonical json: %w", err)
	}

	if err := root.Validate("root"); err != nil {
		return nil, err
	}
	return &root, nil
}

// GenerateSchemaFromFile loads the canonical source markdown and returns
// a stable, indented JSON Schema payload.
func GenerateSchemaFromFile(path string) ([]byte, error) {
	root, err := LoadCanonicalNodeFromFile(path)
	if err != nil {
		return nil, err
	}
	return root.MarshalSchema()
}

// MarshalSchema renders a JSON Schema document for this canonical node.
func (n *Node) MarshalSchema() ([]byte, error) {
	doc := n.toSchema()
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["title"] = "ok-gobot configuration"

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return append(encoded, '\n'), nil
}

// Validate enforces canonical block invariants.
func (n *Node) Validate(path string) error {
	if n.Type == "" {
		return fmt.Errorf("%s.type is required", path)
	}
	if n.Default == nil {
		return fmt.Errorf("%s.default is required", path)
	}
	if strings.TrimSpace(n.Description) == "" {
		return fmt.Errorf("%s.description is required", path)
	}

	switch n.Type {
	case "object":
		if len(n.Properties) == 0 && n.AdditionalProperties == nil {
			return fmt.Errorf("%s: object must define properties or additionalProperties", path)
		}
		for key, child := range n.Properties {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("%s.properties contains an empty key", path)
			}
			if child == nil {
				return fmt.Errorf("%s.properties.%s is nil", path, key)
			}
			if err := child.Validate(path + "." + key); err != nil {
				return err
			}
		}
		if n.Items != nil {
			return fmt.Errorf("%s: object must not define items", path)
		}
		if n.AdditionalProperties != nil {
			if err := n.AdditionalProperties.Validate(path + ".*"); err != nil {
				return err
			}
		}
	case "array":
		if n.Items == nil {
			return fmt.Errorf("%s.items is required for array", path)
		}
		if err := n.Items.Validate(path + "[]"); err != nil {
			return err
		}
		if len(n.Properties) > 0 {
			return fmt.Errorf("%s: array must not define properties", path)
		}
		if n.AdditionalProperties != nil {
			return fmt.Errorf("%s: array must not define additionalProperties", path)
		}
	case "string", "integer", "number", "boolean":
		if n.Items != nil {
			return fmt.Errorf("%s: scalar must not define items", path)
		}
		if len(n.Properties) > 0 {
			return fmt.Errorf("%s: scalar must not define properties", path)
		}
		if n.AdditionalProperties != nil {
			return fmt.Errorf("%s: scalar must not define additionalProperties", path)
		}
	default:
		return fmt.Errorf("%s.type=%q is not supported", path, n.Type)
	}

	return nil
}

func (n *Node) toSchema() map[string]any {
	out := map[string]any{
		"type":        n.Type,
		"default":     n.Default,
		"description": n.Description,
	}

	if len(n.Enum) > 0 {
		out["enum"] = n.Enum
	}

	switch n.Type {
	case "object":
		if len(n.Properties) > 0 {
			properties := make(map[string]any, len(n.Properties))
			keys := make([]string, 0, len(n.Properties))
			for key := range n.Properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				properties[key] = n.Properties[key].toSchema()
			}
			out["properties"] = properties
		}

		if n.AdditionalProperties != nil {
			out["additionalProperties"] = n.AdditionalProperties.toSchema()
		} else {
			out["additionalProperties"] = false
		}
	case "array":
		out["items"] = n.Items.toSchema()
	}

	return out
}

func extractCanonicalJSON(markdown []byte) ([]byte, error) {
	src := string(markdown)
	start := strings.Index(src, canonicalStartMarker)
	if start == -1 {
		return nil, fmt.Errorf("canonical start marker %q not found", canonicalStartMarker)
	}
	end := strings.Index(src, canonicalEndMarker)
	if end == -1 {
		return nil, fmt.Errorf("canonical end marker %q not found", canonicalEndMarker)
	}
	if end <= start {
		return nil, fmt.Errorf("canonical markers are malformed")
	}

	section := src[start+len(canonicalStartMarker) : end]
	fenceStart := strings.Index(section, "```json")
	if fenceStart == -1 {
		return nil, fmt.Errorf("canonical JSON fence not found")
	}
	body := section[fenceStart+len("```json"):]
	fenceEnd := strings.Index(body, "```")
	if fenceEnd == -1 {
		return nil, fmt.Errorf("canonical JSON fence is not closed")
	}

	payload := strings.TrimSpace(body[:fenceEnd])
	if payload == "" {
		return nil, fmt.Errorf("canonical JSON payload is empty")
	}
	return []byte(payload), nil
}
