package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/memory"
)

// Tool represents an executable tool
type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, args ...string) (string, error)
}

// ToolSchema defines the JSON Schema interface for tools that support it
type ToolSchema interface {
	Tool
	// GetSchema returns the JSON Schema for the tool's parameters
	GetSchema() map[string]interface{}
}

// SSHTool executes commands on remote hosts via SSH
type SSHTool struct {
	Host     string
	User     string
	Password string // Optional, prefer key-based auth
	KeyPath  string // SSH key path
}

// NewSSHTool creates a new SSH tool from TOOLS.md configuration
func NewSSHTool(host, user string) *SSHTool {
	return &SSHTool{
		Host: host,
		User: user,
	}
}

func (s *SSHTool) Name() string {
	return "ssh"
}

func (s *SSHTool) Description() string {
	return fmt.Sprintf("Execute commands on %s@%s", s.User, s.Host)
}

func (s *SSHTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no command specified")
	}

	cmdArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
	}

	if s.KeyPath != "" {
		cmdArgs = append(cmdArgs, "-i", s.KeyPath)
	}

	cmdArgs = append(cmdArgs, fmt.Sprintf("%s@%s", s.User, s.Host))
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "ssh", cmdArgs...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// LocalCommand executes local shell commands
type LocalCommand struct {
	WorkDir      string
	ApprovalFunc func(command string) (bool, error)
}

func (l *LocalCommand) Name() string {
	return "local"
}

func (l *LocalCommand) Description() string {
	return "Execute local shell commands"
}

func (l *LocalCommand) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no command specified")
	}

	command := strings.Join(args, " ")

	// Check if command needs approval
	if l.ApprovalFunc != nil {
		approved, err := l.ApprovalFunc(command)
		if err != nil {
			return "", fmt.Errorf("approval check failed: %w", err)
		}
		if !approved {
			return "Command denied by user", nil
		}
	}

	// Use bash -c for complex commands
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	if l.WorkDir != "" {
		cmd.Dir = l.WorkDir
	}

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// FileTool provides file operations
type FileTool struct {
	BasePath string
}

func (f *FileTool) Name() string {
	return "file"
}

func (f *FileTool) Description() string {
	return "Read and write files"
}

func (f *FileTool) Read(path string) (string, error) {
	// Ensure path is within base path (security)
	fullPath := filepath.Join(f.BasePath, path)
	if !strings.HasPrefix(fullPath, f.BasePath) {
		return "", fmt.Errorf("path outside allowed directory")
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (f *FileTool) Write(path string, content string) error {
	fullPath := filepath.Join(f.BasePath, path)
	if !strings.HasPrefix(fullPath, f.BasePath) {
		return fmt.Errorf("path outside allowed directory")
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(fullPath, []byte(content), 0644)
}

func (f *FileTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: file <read|write> <path> [content]")
	}

	operation := args[0]
	path := args[1]

	switch operation {
	case "read":
		return f.Read(path)
	case "write":
		if len(args) < 3 {
			return "", fmt.Errorf("content required for write")
		}
		return "", f.Write(path, strings.Join(args[2:], " "))
	default:
		return "", fmt.Errorf("unknown operation: %s", operation)
	}
}

// Registry holds all available tools
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a new tool registry
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry
func (r *Registry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools
func (r *Registry) List() []Tool {
	var list []Tool
	for _, tool := range r.tools {
		list = append(list, tool)
	}
	return list
}

// ToolsConfig holds configuration for optional tools
type ToolsConfig struct {
	OpenAIAPIKey   string
	OpenAIBaseURL  string
	BraveAPIKey    string
	ExaAPIKey      string
	SearchEngine   string // "brave" or "exa"
	TTSProvider    string // "openai" or "edge"
	TTSVoice       string // Default TTS voice
	CronScheduler  CronScheduler
	MessageSender  MessageSender
	CurrentChatID  int64
	MemoryManager  *memory.MemoryManager
}

// LoadFromConfig loads tools from TOOLS.md
func LoadFromConfig(basePath string) (*Registry, error) {
	return LoadFromConfigWithOptions(basePath, nil)
}

// LoadFromConfigWithOptions loads tools with additional configuration
func LoadFromConfigWithOptions(basePath string, cfg *ToolsConfig) (*Registry, error) {
	registry := NewRegistry()

	// Always register local command
	registry.Register(&LocalCommand{})

	// Try to load TOOLS.md
	toolsPath := filepath.Join(basePath, "TOOLS.md")
	content, err := os.ReadFile(toolsPath)
	if err == nil {
		// Parse SSH hosts from TOOLS.md
		lines := strings.Split(string(content), "\n")
		inSSHTable := false
		for _, line := range lines {
			line = strings.TrimSpace(line)

			// Look for SSH section
			if strings.Contains(line, "## SSH") {
				inSSHTable = true
				continue
			}

			if inSSHTable && strings.HasPrefix(line, "|") && strings.Contains(line, "ssh") {
				// Parse table row: | Alias | Host | User | Notes |
				parts := strings.Split(line, "|")
				if len(parts) >= 4 {
					host := strings.TrimSpace(parts[2])
					user := strings.TrimSpace(parts[3])
					if host != "" && user != "" && host != "Host" {
						sshTool := NewSSHTool(host, user)
						registry.Register(sshTool)
					}
				}
			}

			// Exit SSH section on empty line or new header
			if inSSHTable && (line == "" || strings.HasPrefix(line, "## ")) {
				inSSHTable = false
			}
		}
	}

	// Register file tool with soul directory
	if basePath != "" {
		registry.Register(&FileTool{BasePath: basePath})
		registry.Register(NewPatchTool(basePath))
		registry.Register(NewSearchFileTool(basePath))
	}

	// Register Obsidian tool (vault in standard location)
	homeDir, _ := os.UserHomeDir()
	obsidianVault := filepath.Join(homeDir, "Obsidian")
	if _, err := os.Stat(obsidianVault); err == nil {
		registry.Register(NewObsidianTool(obsidianVault))
	}

	// Register web fetch tool (no config needed)
	registry.Register(NewWebFetchTool())

	// Register browser tool (Chrome automation via CDP)
	registry.Register(NewBrowserTool(filepath.Join(homeDir, ".ok-gobot", "chrome-profile")))

	// Register optional tools based on config
	if cfg != nil {
		// Search tool
		if cfg.BraveAPIKey != "" || cfg.ExaAPIKey != "" {
			apiKey := cfg.BraveAPIKey
			engine := "brave"
			if cfg.SearchEngine == "exa" && cfg.ExaAPIKey != "" {
				apiKey = cfg.ExaAPIKey
				engine = "exa"
			}
			registry.Register(NewSearchTool(apiKey, engine))
		}

		// Image generation tool
		if cfg.OpenAIAPIKey != "" {
			registry.Register(NewImageTool(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL))
		}

		// TTS tool
		if cfg.OpenAIAPIKey != "" {
			ttsProvider := cfg.TTSProvider
			if ttsProvider == "" {
				ttsProvider = "openai"
			}
			ttsVoice := cfg.TTSVoice
			registry.Register(NewTTSTool(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, ttsProvider, ttsVoice))
		}

		// Cron tool
		if cfg.CronScheduler != nil {
			registry.Register(NewCronTool(cfg.CronScheduler, cfg.CurrentChatID))
		}

		// Message tool
		if cfg.MessageSender != nil {
			registry.Register(NewMessageTool(cfg.MessageSender))
		}

		// Memory tool
		if cfg.MemoryManager != nil {
			registry.Register(NewMemoryTool(cfg.MemoryManager))
		}
	}

	return registry, nil
}

// Execute runs a tool by name with given arguments
func (r *Registry) Execute(ctx context.Context, toolName string, args ...string) (string, error) {
	tool, ok := r.Get(toolName)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", toolName)
	}

	return tool.Execute(ctx, args...)
}

// SafeExecute runs a tool with safety checks
func (r *Registry) SafeExecute(ctx context.Context, toolName string, requiresApproval bool, args ...string) (string, error) {
	// If tool requires approval, we'd check here
	// For now, just execute
	return r.Execute(ctx, toolName, args...)
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// ToOpenAITools converts a list of tools to OpenAI tool definitions
func ToOpenAITools(tools []Tool) []ai.ToolDefinition {
	definitions := make([]ai.ToolDefinition, 0, len(tools))

	for _, tool := range tools {
		// Check if tool provides custom schema
		var parameters json.RawMessage
		if schemaTool, ok := tool.(ToolSchema); ok {
			schema := schemaTool.GetSchema()
			parametersBytes, _ := json.Marshal(schema)
			parameters = parametersBytes
		} else {
			// Default schema: single "input" string parameter
			defaultSchema := map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{
						"type":        "string",
						"description": "Input for the tool",
					},
				},
				"required": []string{"input"},
			}
			parametersBytes, _ := json.Marshal(defaultSchema)
			parameters = parametersBytes
		}

		definitions = append(definitions, ai.ToolDefinition{
			Type: "function",
			Function: ai.FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  parameters,
			},
		})
	}

	return definitions
}

// GetSchema returns the JSON Schema for file tool parameters
func (f *FileTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Operation: read or write",
				"enum":        []string{"read", "write"},
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path (relative to base directory)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to write (for write command)",
			},
		},
		"required": []string{"command", "path"},
	}
}

// GetSchema returns the JSON Schema for local command parameters
func (l *LocalCommand) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to execute",
			},
		},
		"required": []string{"input"},
	}
}

// GetSchema returns the JSON Schema for SSH tool parameters
func (s *SSHTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute on remote host",
			},
		},
		"required": []string{"input"},
	}
}
