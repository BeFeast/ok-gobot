package tools

import (
	"context"
	"testing"
)

type stubEmergencyStopProvider struct {
	enabled bool
}

func (s stubEmergencyStopProvider) IsEmergencyStopEnabled() (bool, error) {
	return s.enabled, nil
}

type stubTool struct {
	name   string
	called int
}

func (s *stubTool) Name() string {
	return s.name
}

func (s *stubTool) Description() string {
	return "stub"
}

func (s *stubTool) Execute(context.Context, ...string) (string, error) {
	s.called++
	return "ok", nil
}

type stubJSONTool struct {
	*stubTool
	jsonCalled int
}

func (s *stubJSONTool) ExecuteJSON(context.Context, map[string]string) (string, error) {
	s.jsonCalled++
	return "ok", nil
}

func TestRegistryBlocksDangerousToolsWhenEmergencyStopEnabled(t *testing.T) {
	t.Parallel()

	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: true})
	tool := &stubTool{name: "message"}
	reg.Register(tool)

	_, err := reg.Execute(context.Background(), "message", "alice", "hello")
	if err == nil {
		t.Fatal("expected estop to block message tool")
	}
	denial := IsToolDenial(err)
	if denial == nil {
		t.Fatalf("expected ToolDenial error, got %v", err)
	}
	if denial.ToolName != "message" {
		t.Fatalf("expected ToolName=message, got %s", denial.ToolName)
	}
	if denial.Family != "message" {
		t.Fatalf("expected Family=message, got %s", denial.Family)
	}
	if tool.called != 0 {
		t.Fatalf("expected wrapped tool not to execute, called=%d", tool.called)
	}
}

func TestChildRegistryPreservesEmergencyStopForJSONTools(t *testing.T) {
	t.Parallel()

	parent := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: true})
	child := parent.Child()
	tool := &stubJSONTool{stubTool: &stubTool{name: "browser_task"}}
	child.Register(tool)

	got, ok := child.Get("browser_task")
	if !ok {
		t.Fatal("expected browser_task to be registered")
	}

	jsonExec, ok := got.(interface {
		ExecuteJSON(context.Context, map[string]string) (string, error)
	})
	if !ok {
		t.Fatal("expected wrapped browser_task to preserve ExecuteJSON")
	}

	_, err := jsonExec.ExecuteJSON(context.Background(), map[string]string{"task": "visit example.com"})
	if err == nil {
		t.Fatal("expected estop to block browser_task")
	}
	denial := IsToolDenial(err)
	if denial == nil {
		t.Fatalf("expected ToolDenial error, got %v", err)
	}
	if denial.Family != "browser" {
		t.Fatalf("expected Family=browser, got %s", denial.Family)
	}
	if tool.jsonCalled != 0 {
		t.Fatalf("expected wrapped JSON tool not to execute, called=%d", tool.jsonCalled)
	}
}

func TestAsLocalCommandUnwrapsEmergencyStopGuard(t *testing.T) {
	t.Parallel()

	reg := NewRegistryWithEmergencyStop(stubEmergencyStopProvider{enabled: false})
	local := &LocalCommand{}
	reg.Register(local)

	got, ok := reg.Get("local")
	if !ok {
		t.Fatal("expected local tool to be registered")
	}

	unwrapped, ok := AsLocalCommand(got)
	if !ok {
		t.Fatal("expected AsLocalCommand to unwrap guarded local tool")
	}
	if unwrapped != local {
		t.Fatal("expected AsLocalCommand to return the original local command")
	}
}
