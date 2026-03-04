package memorymcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"ok-gobot/internal/agent"
)

func TestMCPServerRegistersExpectedToolsAndSupportsMemoryGet(t *testing.T) {
	tmpDir := t.TempDir()
	memoryFile := filepath.Join(tmpDir, "MEMORY.md")
	markdown := strings.Join([]string{
		"# Memory",
		"",
		"## Projects",
		"Shared notes",
		"",
		"### Alpha",
		"Important detail",
		"",
		"## Personal",
		"Other section",
	}, "\n")
	if err := os.WriteFile(memoryFile, []byte(markdown), 0o644); err != nil {
		t.Fatalf("failed to write MEMORY.md: %v", err)
	}

	srv := New(Config{AllowWrites: false}, nil, agent.NewMemory(tmpDir))
	c := newInProcessClient(t, srv)

	toolsResult, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	found := map[string]bool{}
	for _, tool := range toolsResult.Tools {
		found[tool.Name] = true
	}

	for _, name := range []string{"memory_search", "memory_get", "memory_capture"} {
		if !found[name] {
			t.Fatalf("expected tool %q to be registered", name)
		}
	}

	call := mcp.CallToolRequest{}
	call.Params.Name = "memory_get"
	call.Params.Arguments = map[string]any{
		"file":        "MEMORY.md",
		"header_path": "Projects > Alpha",
	}

	result, err := c.CallTool(context.Background(), call)
	if err != nil {
		t.Fatalf("memory_get call failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("memory_get returned error result: %s", mcp.GetTextFromContent(result.Content[0]))
	}

	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected structured content map, got %T", result.StructuredContent)
	}
	content, _ := structured["content"].(string)
	if !strings.Contains(content, "### Alpha") {
		t.Fatalf("expected section heading in memory_get output, got: %s", content)
	}
	if strings.Contains(content, "## Personal") {
		t.Fatalf("expected section-scoped content from memory_get")
	}
}

func TestMemoryCaptureRequiresWriteFlag(t *testing.T) {
	srv := New(Config{AllowWrites: false}, nil, agent.NewMemory(t.TempDir()))
	c := newInProcessClient(t, srv)

	call := mcp.CallToolRequest{}
	call.Params.Name = "memory_capture"
	call.Params.Arguments = map[string]any{"content": "remember this"}

	result, err := c.CallTool(context.Background(), call)
	if err != nil {
		t.Fatalf("memory_capture call failed: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected memory_capture to be blocked when writes are disabled")
	}
}

func TestMemoryCaptureWritesToTodayNoteWhenEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	daily := agent.NewMemory(tmpDir)
	srv := New(Config{AllowWrites: true}, nil, daily)
	c := newInProcessClient(t, srv)

	call := mcp.CallToolRequest{}
	call.Params.Name = "memory_capture"
	call.Params.Arguments = map[string]any{"content": "capture line"}

	result, err := c.CallTool(context.Background(), call)
	if err != nil {
		t.Fatalf("memory_capture call failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful memory_capture, got error: %s", mcp.GetTextFromContent(result.Content[0]))
	}

	note, err := daily.GetTodayNote()
	if err != nil {
		t.Fatalf("failed to load today note: %v", err)
	}

	content, err := os.ReadFile(note.Path)
	if err != nil {
		t.Fatalf("failed to read note file: %v", err)
	}
	if !strings.Contains(string(content), "capture line") {
		t.Fatalf("expected captured text in note file, got:\n%s", string(content))
	}
}

func newInProcessClient(t *testing.T, srv *Server) *client.Client {
	t.Helper()

	c, err := client.NewInProcessClient(srv.mcpServer)
	if err != nil {
		t.Fatalf("failed to create in-process client: %v", err)
	}

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start in-process client: %v", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "memorymcp-test-client", Version: "1.0.0"}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("failed to initialize in-process client: %v", err)
	}

	t.Cleanup(func() {
		_ = c.Close()
	})

	return c
}
