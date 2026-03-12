package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserToolName(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	if bt.Name() != "browser" {
		t.Fatalf("expected name 'browser', got %q", bt.Name())
	}
}

func TestBrowserToolDescription(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	desc := bt.Description()
	for _, keyword := range []string{"open", "navigate", "screenshot", "tabs", "focus", "close"} {
		if !strings.Contains(desc, keyword) {
			t.Errorf("description missing keyword %q", keyword)
		}
	}
}

func TestBrowserToolSchema(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	schema := bt.GetSchema()

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing 'properties'")
	}

	for _, field := range []string{"command", "url", "snapshot_id", "ref", "selector", "value", "target_id"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema missing property %q", field)
		}
	}

	cmdProp, ok := props["command"].(map[string]interface{})
	if !ok {
		t.Fatal("command property not a map")
	}
	enumRaw, ok := cmdProp["enum"].([]string)
	if !ok {
		t.Fatal("command enum not []string")
	}
	enumSet := make(map[string]bool, len(enumRaw))
	for _, e := range enumRaw {
		enumSet[e] = true
	}
	for _, cmd := range []string{"open", "navigate", "tabs", "focus", "close", "screenshot", "stop"} {
		if !enumSet[cmd] {
			t.Errorf("schema command enum missing %q", cmd)
		}
	}
}

func TestBrowserToolExecuteNoArgs(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background())
	if err == nil {
		t.Fatal("expected error with no args")
	}
}

func TestBrowserToolExecuteUnknownCommand(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected 'unknown command' error, got %v", err)
	}
}

func TestBrowserToolExecuteJSONMissingCommand(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.ExecuteJSON(context.Background(), map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("expected 'command is required' error, got %v", err)
	}
}

func TestBrowserToolStopWhenNotRunning(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	result, err := bt.Execute(context.Background(), "stop")
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if result != "Browser stopped" {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestBrowserToolTabsWhenNotRunning(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "tabs")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected 'not running' error, got %v", err)
	}
}

func TestBrowserToolFocusWhenNotRunning(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "focus", "some-id")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected 'not running' error, got %v", err)
	}
}

func TestBrowserToolCloseWhenNotRunning(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "close", "some-id")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected 'not running' error, got %v", err)
	}
}

func TestBrowserToolExecuteJSONNavigateMissingURL(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.ExecuteJSON(context.Background(), map[string]string{"command": "navigate"})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("expected 'url is required' error, got %v", err)
	}
}

func TestBrowserToolExecuteJSONClickMissingParams(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.ExecuteJSON(context.Background(), map[string]string{"command": "click"})
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected requirement error, got %v", err)
	}
}

func TestBrowserToolExecuteJSONTypeMissingValue(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.ExecuteJSON(context.Background(), map[string]string{
		"command":  "type",
		"selector": "input",
	})
	if err == nil || !strings.Contains(err.Error(), "value is required") {
		t.Fatalf("expected 'value is required' error, got %v", err)
	}
}

func TestBrowserToolExecuteJSONFocusMissingTargetID(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.ExecuteJSON(context.Background(), map[string]string{"command": "focus"})
	if err == nil || !strings.Contains(err.Error(), "target_id is required") {
		t.Fatalf("expected 'target_id is required' error, got %v", err)
	}
}

func TestBrowserToolIsRunningFalseByDefault(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	if bt.IsRunning() {
		t.Fatal("expected IsRunning to be false before start")
	}
}

func TestBrowserToolExecuteNavigateMissingURL(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "navigate")
	if err == nil || !strings.Contains(err.Error(), "URL required") {
		t.Fatalf("expected 'URL required' error, got %v", err)
	}
}

func TestBrowserToolExecuteFocusMissingID(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	_, err := bt.Execute(context.Background(), "focus")
	if err == nil || !strings.Contains(err.Error(), "target_id required") {
		t.Fatalf("expected 'target_id required' error, got %v", err)
	}
}

func TestBrowserToolSchemaCommandEnumIsValid(t *testing.T) {
	bt := NewBrowserTool(t.TempDir(), "")
	schemaBytes, err := json.Marshal(bt.GetSchema())
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(schemaBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal schema JSON: %v", err)
	}

	// Verify it's valid JSON by round-tripping.
	if _, ok := parsed["properties"]; !ok {
		t.Fatal("schema missing properties after round-trip")
	}
}
