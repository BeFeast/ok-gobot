package configschema

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateMatchesCheckedInSchema(t *testing.T) {
	root := repoRoot(t)
	architecturePath := filepath.Join(root, "docs", "ARCHITECTURE-v2.md")
	schemaPath := filepath.Join(root, "config.schema.json")

	got, err := GenerateSchemaFromFile(architecturePath)
	if err != nil {
		t.Fatalf("GenerateSchemaFromFile failed: %v", err)
	}

	want, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read checked-in schema: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("config.schema.json is stale; run `go run ./cmd/gen-config-schema`")
	}
}

func TestCanonicalIncludesPRDExtensions(t *testing.T) {
	root := repoRoot(t)
	architecturePath := filepath.Join(root, "docs", "ARCHITECTURE-v2.md")

	node, err := LoadCanonicalNodeFromFile(architecturePath)
	if err != nil {
		t.Fatalf("LoadCanonicalNodeFromFile failed: %v", err)
	}

	required := []string{
		"runtime.mode",
		"runtime.session_queue_limit",
		"session.dm_scope",
	}
	for _, key := range required {
		if _, ok := lookup(node, key); !ok {
			t.Fatalf("canonical key missing: %s", key)
		}
	}
}

func TestCanonicalEveryKeyHasTypeDefaultDescription(t *testing.T) {
	root := repoRoot(t)
	architecturePath := filepath.Join(root, "docs", "ARCHITECTURE-v2.md")

	node, err := LoadCanonicalNodeFromFile(architecturePath)
	if err != nil {
		t.Fatalf("LoadCanonicalNodeFromFile failed: %v", err)
	}

	if err := node.Validate("root"); err != nil {
		t.Fatalf("canonical validation failed: %v", err)
	}
}

func lookup(root *Node, path string) (*Node, bool) {
	cur := root
	for _, segment := range strings.Split(path, ".") {
		if cur == nil || cur.Type != "object" {
			return nil, false
		}
		next, ok := cur.Properties[segment]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
