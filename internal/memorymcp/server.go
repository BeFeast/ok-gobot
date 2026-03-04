package memorymcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/memory"
)

var markdownHeaderRegexp = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// Config configures the optional memory MCP server.
type Config struct {
	Enabled     bool
	Host        string
	Port        int
	Endpoint    string
	AllowWrites bool
}

// DefaultConfig returns safe defaults:
// disabled, loopback bind, and writes denied.
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Host:        "127.0.0.1",
		Port:        9233,
		Endpoint:    "/mcp",
		AllowWrites: false,
	}
}

// Server exposes memory operations over MCP.
type Server struct {
	cfg           Config
	memoryManager *memory.MemoryManager
	dailyMemory   *agent.Memory
	mcpServer     *mcpserver.MCPServer
	httpServer    *mcpserver.StreamableHTTPServer
}

// New creates a memory MCP server instance.
func New(cfg Config, manager *memory.MemoryManager, dailyMemory *agent.Memory) *Server {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = defaults.Host
	}
	if cfg.Port <= 0 {
		cfg.Port = defaults.Port
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = defaults.Endpoint
	}
	if !strings.HasPrefix(cfg.Endpoint, "/") {
		cfg.Endpoint = "/" + cfg.Endpoint
	}

	s := &Server{
		cfg:           cfg,
		memoryManager: manager,
		dailyMemory:   dailyMemory,
	}

	mcpSrv := mcpserver.NewMCPServer(
		"ok-gobot-memory",
		"1.0.0",
		mcpserver.WithToolCapabilities(true),
	)

	mcpSrv.AddTool(mcp.NewTool(
		"memory_search",
		mcp.WithDescription("Semantic memory search over indexed memory."),
		mcp.WithString("query", mcp.Description("Search query"), mcp.Required()),
		mcp.WithNumber("topK", mcp.Description("Maximum number of results to return"), mcp.DefaultNumber(5)),
	), s.handleMemorySearch)

	mcpSrv.AddTool(mcp.NewTool(
		"memory_get",
		mcp.WithDescription("Read a memory markdown file, optionally by header path."),
		mcp.WithString("file", mcp.Description("Memory file path, for example MEMORY.md or memory/2026-03-04.md"), mcp.Required()),
		mcp.WithString("header_path", mcp.Description("Optional markdown header path, for example Projects > OK Gobot")),
	), s.handleMemoryGet)

	mcpSrv.AddTool(mcp.NewTool(
		"memory_capture",
		mcp.WithDescription("Capture memory content into daily or explicit memory file."),
		mcp.WithString("content", mcp.Description("Content to capture"), mcp.Required()),
		mcp.WithString("file", mcp.Description("Optional target file path. Defaults to today's memory note.")),
	), s.handleMemoryCapture)

	s.mcpServer = mcpSrv
	s.httpServer = mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithEndpointPath(cfg.Endpoint))

	return s
}

// Addr returns the server listen address.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
}

// URL returns the full MCP endpoint URL.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s%s", s.Addr(), s.cfg.Endpoint)
}

// Start runs the MCP HTTP server and blocks until stopped.
func (s *Server) Start(ctx context.Context) error {
	if s.httpServer == nil {
		return fmt.Errorf("memory mcp server is not initialized")
	}

	go func() {
		<-ctx.Done()
		_ = s.httpServer.Shutdown(context.Background())
	}()

	if err := s.httpServer.Start(s.Addr()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("memory mcp start failed: %w", err)
	}
	return nil
}

// Stop gracefully shuts down the MCP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleMemorySearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := strings.TrimSpace(request.GetString("query", ""))
	if query == "" {
		return mcp.NewToolResultError("memory_search requires a non-empty query"), nil
	}

	topK := request.GetInt("topK", 5)
	if topK <= 0 {
		topK = 5
	}

	if s.memoryManager == nil {
		return mcp.NewToolResultError("memory_search backend is not configured (enable memory.enabled)"), nil
	}

	results, err := s.memoryManager.Recall(ctx, query, topK)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_search failed: %v", err)), nil
	}

	type searchResult struct {
		ID         int64   `json:"id"`
		Content    string  `json:"content"`
		Category   string  `json:"category"`
		Similarity float32 `json:"similarity"`
		CreatedAt  string  `json:"created_at"`
	}

	out := make([]searchResult, 0, len(results))
	for _, r := range results {
		out = append(out, searchResult{
			ID:         r.ID,
			Content:    r.Content,
			Category:   r.Category,
			Similarity: r.Similarity,
			CreatedAt:  r.CreatedAt.Format(time.RFC3339),
		})
	}

	payload := map[string]interface{}{
		"query":   query,
		"topK":    topK,
		"results": out,
	}

	resp, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_search response encoding failed: %v", err)), nil
	}
	return resp, nil
}

func (s *Server) handleMemoryGet(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	file := strings.TrimSpace(request.GetString("file", ""))
	if file == "" {
		return mcp.NewToolResultError("memory_get requires file"), nil
	}

	headerPath := strings.TrimSpace(request.GetString("header_path", ""))
	content, resolvedPath, err := s.readMemoryFile(file, headerPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_get failed: %v", err)), nil
	}

	payload := map[string]interface{}{
		"file":          file,
		"resolved_file": resolvedPath,
		"header_path":   headerPath,
		"content":       content,
	}

	resp, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_get response encoding failed: %v", err)), nil
	}
	return resp, nil
}

func (s *Server) handleMemoryCapture(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.cfg.AllowWrites {
		return mcp.NewToolResultError("memory_capture is disabled; set memory.mcp.allow_writes=true to enable writes"), nil
	}

	content := strings.TrimSpace(request.GetString("content", ""))
	if content == "" {
		return mcp.NewToolResultError("memory_capture requires non-empty content"), nil
	}

	file := strings.TrimSpace(request.GetString("file", ""))
	resolvedPath, err := s.captureMemory(content, file)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_capture failed: %v", err)), nil
	}

	payload := map[string]interface{}{
		"status": "captured",
		"file":   resolvedPath,
	}

	resp, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("memory_capture response encoding failed: %v", err)), nil
	}
	return resp, nil
}

func (s *Server) readMemoryFile(file, headerPath string) (string, string, error) {
	resolvedPath, err := resolveWithinBase(s.basePath(), file)
	if err != nil {
		return "", "", err
	}

	contentBytes, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", "", err
	}
	content := string(contentBytes)
	if headerPath == "" {
		return content, resolvedPath, nil
	}

	section, err := extractMarkdownSectionByHeaderPath(content, headerPath)
	if err != nil {
		return "", "", err
	}
	return section, resolvedPath, nil
}

func (s *Server) captureMemory(content, file string) (string, error) {
	if strings.TrimSpace(file) == "" {
		if s.dailyMemory != nil {
			if err := s.dailyMemory.AppendToToday(content); err != nil {
				return "", err
			}
			note, err := s.dailyMemory.GetTodayNote()
			if err != nil {
				return "", err
			}
			return note.Path, nil
		}
		today := time.Now().Format("2006-01-02")
		file = filepath.ToSlash(filepath.Join("memory", today+".md"))
	}

	resolvedPath, err := resolveWithinBase(s.basePath(), file)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", err
	}

	prefix := content
	if !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}

	if existing, err := os.ReadFile(resolvedPath); err == nil && len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n" + prefix
	}

	f, err := os.OpenFile(resolvedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(prefix); err != nil {
		return "", err
	}
	return resolvedPath, nil
}

func (s *Server) basePath() string {
	if s.dailyMemory != nil && strings.TrimSpace(s.dailyMemory.BasePath) != "" {
		return s.dailyMemory.BasePath
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(homeDir, "ok-gobot-soul")
}

func resolveWithinBase(basePath, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	cleanBase := filepath.Clean(basePath)
	var fullPath string
	if filepath.IsAbs(path) {
		fullPath = filepath.Clean(path)
	} else {
		fullPath = filepath.Join(cleanBase, filepath.Clean(path))
	}

	basePrefix := cleanBase + string(os.PathSeparator)
	if fullPath != cleanBase && !strings.HasPrefix(fullPath, basePrefix) {
		return "", fmt.Errorf("path %q escapes memory base path %q", path, cleanBase)
	}
	return fullPath, nil
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
