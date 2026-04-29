package rolejob

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/role"
	"ok-gobot/internal/runtime"
)

// Hub is the small RuntimeHub surface needed by a role job.
type Hub interface {
	Submit(agent.RunRequest) <-chan agent.RunEvent
}

type RunOptions struct {
	SessionKey   agent.SessionKey
	ChatID       int64
	OnToolEvent  func(agent.ToolEvent)
	OnDelta      func(string)
	OnDeltaReset func()
}

var (
	urlRE       = regexp.MustCompile(`https?://[^\s<>"']+`)
	imagePathRE = regexp.MustCompile(`(?i)(/[A-Za-z0-9._~!$&()+,;=:@%/-]+\.(png|jpe?g|gif|webp))`)
	filePathRE  = regexp.MustCompile(`(?i)(/[A-Za-z0-9._~!$&()+,;=:@%/-]+\.(html?|md|json|txt|log|pdf))`)
)

func BuildPrompt(m *role.Manifest, input string) string {
	if m == nil {
		return strings.TrimSpace(input)
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(m.Prompt))
	input = strings.TrimSpace(input)
	if input != "" {
		b.WriteString("\n\n## User Input\n\n")
		b.WriteString(input)
	}
	return strings.TrimSpace(b.String())
}

func BuildDelegationJob(m *role.Manifest) delegation.Job {
	if m == nil {
		return delegation.Job{}.WithDefaults()
	}
	job := m.ToDelegationJob()
	job.OutputFormat = delegation.OutputFormatMarkdown
	return job.WithDefaults()
}

func BuildSpec(m *role.Manifest, input, sessionKey, deliverySessionKey, workerOverride string) runtime.JobSpec {
	job := BuildDelegationJob(m)
	worker := ""
	roleName := ""
	model := ""
	if m != nil {
		worker = m.Worker
		roleName = m.Name
		model = m.Model
	}
	if workerOverride != "" {
		worker = strings.TrimSpace(workerOverride)
	}
	modelTier := worker
	if modelTier == "" {
		modelTier = model
	}
	if modelTier == "" {
		modelTier = "default"
	}
	description := "role"
	if roleName != "" {
		description = "role:" + roleName
	}
	if trimmed := strings.TrimSpace(input); trimmed != "" {
		description += " " + truncate(trimmed, 80)
	}
	return runtime.JobSpec{
		Kind:               "role",
		Worker:             worker,
		SessionKey:         sessionKey,
		DeliverySessionKey: deliverySessionKey,
		Description:        description,
		RoleName:           roleName,
		ModelTier:          modelTier,
		Timeout:            job.MaxDuration,
		MaxToolCalls:       job.MaxToolCalls,
		MaxAttempts:        1,
	}
}

func NewSessionKey(prefix string, chatID int64, roleName string) agent.SessionKey {
	prefix = safePart(prefix)
	if prefix == "" {
		prefix = "role"
	}
	roleName = safePart(roleName)
	if roleName == "" {
		roleName = "unknown"
	}
	return agent.SessionKey(fmt.Sprintf("%s:%d:%s:%d", prefix, chatID, roleName, time.Now().UnixNano()))
}

func RunWithHub(ctx context.Context, hub Hub, m *role.Manifest, input string, opts RunOptions) (runtime.JobRunResult, error) {
	if hub == nil {
		return runtime.JobRunResult{}, fmt.Errorf("role runtime hub is not configured")
	}
	if m == nil {
		return runtime.JobRunResult{}, fmt.Errorf("role manifest is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job := BuildDelegationJob(m)
	sessionKey := opts.SessionKey
	if sessionKey == "" {
		sessionKey = NewSessionKey("role", opts.ChatID, m.Name)
	}

	var toolArtifacts []runtime.JobArtifactSpec
	onToolEvent := func(event agent.ToolEvent) {
		if opts.OnToolEvent != nil {
			opts.OnToolEvent(event)
		}
		toolArtifacts = append(toolArtifacts, extractToolArtifacts(event)...)
	}

	events := hub.Submit(agent.RunRequest{
		SessionKey:   sessionKey,
		ChatID:       opts.ChatID,
		Content:      BuildPrompt(m, input),
		Context:      ctx,
		OnToolEvent:  onToolEvent,
		OnDelta:      opts.OnDelta,
		OnDeltaReset: opts.OnDeltaReset,
		Job:          &job,
		IsSubagent:   true,
	})

	select {
	case ev, ok := <-events:
		if !ok {
			if ctx.Err() != nil {
				return runtime.JobRunResult{}, ctx.Err()
			}
			return runtime.JobRunResult{}, fmt.Errorf("role runtime ended without a result")
		}
		switch ev.Type {
		case agent.RunEventDone:
			if ev.Result == nil {
				return runtime.JobRunResult{}, fmt.Errorf("role runtime returned nil result")
			}
			if ev.Result.IsFallback {
				return runtime.JobRunResult{}, fmt.Errorf("role runtime returned fallback output after a model/tool-call failure")
			}
			summary := strings.TrimSpace(ev.Result.Message)
			if summary == "" {
				summary = fmt.Sprintf("role %q completed with no output", m.Name)
			}
			if rendered, err := m.RenderReport(map[string]any{
				"Summary": summary,
				"Role":    m.Name,
				"Date":    time.Now().Format(time.RFC3339),
			}); err == nil && strings.TrimSpace(rendered) != "" {
				summary = strings.TrimSpace(rendered)
			}
			artifacts := mergeArtifacts(ExtractArtifacts(summary), toolArtifacts)
			if requiresScreenshotProof(m) && !hasArtifactType(artifacts, runtime.JobArtifactTypeScreenshot) {
				return runtime.JobRunResult{}, fmt.Errorf("role %q completed without screenshot proof from frontend_verify", m.Name)
			}
			return runtime.JobRunResult{
				Summary:   summary,
				Artifacts: artifacts,
			}, nil
		case agent.RunEventError:
			if ev.Err == nil {
				return runtime.JobRunResult{}, fmt.Errorf("role runtime failed")
			}
			return runtime.JobRunResult{}, ev.Err
		default:
			return runtime.JobRunResult{}, fmt.Errorf("unexpected role runtime event: %s", ev.Type)
		}
	case <-ctx.Done():
		return runtime.JobRunResult{}, ctx.Err()
	}
}

func ExtractArtifacts(text string) []runtime.JobArtifactSpec {
	text = strings.TrimSpace(text)
	var artifacts []runtime.JobArtifactSpec
	if text != "" {
		artifacts = append(artifacts, runtime.JobArtifactSpec{
			Name:     "final-report",
			Type:     runtime.JobArtifactTypeTextReport,
			MimeType: "text/markdown",
			Content:  text,
		})
	}

	seen := map[string]struct{}{}
	for _, match := range imagePathRE.FindAllString(text, -1) {
		path := cleanPath(match)
		if path == "" || !regularFileExists(path) {
			continue
		}
		if _, ok := seen["image:"+path]; ok {
			continue
		}
		seen["image:"+path] = struct{}{}
		artifacts = append(artifacts, runtime.JobArtifactSpec{
			Name:     "screenshot-" + fmt.Sprint(len(artifacts)),
			Type:     runtime.JobArtifactTypeScreenshot,
			MimeType: mimeType(path),
			Content:  path,
			URI:      "file://" + path,
			Metadata: map[string]any{"path": path},
		})
	}

	for _, match := range urlRE.FindAllString(text, -1) {
		u := strings.TrimRight(match, ".,;:)")
		if u == "" {
			continue
		}
		if _, ok := seen["url:"+u]; ok {
			continue
		}
		seen["url:"+u] = struct{}{}
		artifacts = append(artifacts, runtime.JobArtifactSpec{
			Name: "url-" + fmt.Sprint(len(artifacts)),
			Type: runtime.JobArtifactTypeURL,
			URI:  u,
		})
	}

	for _, match := range filePathRE.FindAllString(text, -1) {
		path := cleanPath(match)
		if path == "" || !regularFileExists(path) {
			continue
		}
		if _, ok := seen["image:"+path]; ok {
			continue
		}
		if _, ok := seen["file:"+path]; ok {
			continue
		}
		seen["file:"+path] = struct{}{}
		artifacts = append(artifacts, runtime.JobArtifactSpec{
			Name:     "file-" + fmt.Sprint(len(artifacts)),
			Type:     runtime.JobArtifactTypeFile,
			MimeType: mimeType(path),
			Content:  path,
			URI:      "file://" + path,
			Metadata: map[string]any{"path": path},
		})
	}

	return artifacts
}

func extractToolArtifacts(event agent.ToolEvent) []runtime.JobArtifactSpec {
	if event.Type != agent.ToolEventFinished || event.ToolName != "frontend_verify" || event.Err != nil {
		return nil
	}
	path := frontendVerifyScreenshotPath(event.Output)
	if path == "" || !regularFileExists(path) {
		return nil
	}
	return []runtime.JobArtifactSpec{{
		Name:     "frontend-verify-screenshot",
		Type:     runtime.JobArtifactTypeScreenshot,
		MimeType: mimeType(path),
		Content:  path,
		URI:      "file://" + path,
		Metadata: map[string]any{"path": path, "source": "frontend_verify"},
	}}
}

func frontendVerifyScreenshotPath(output string) string {
	var out struct {
		ScreenshotPath string `json:"screenshot_path"`
	}
	if err := json.Unmarshal([]byte(output), &out); err == nil {
		if path := cleanPath(out.ScreenshotPath); path != "" {
			return path
		}
	}
	for _, match := range imagePathRE.FindAllString(output, -1) {
		if path := cleanPath(match); path != "" {
			return path
		}
	}
	return ""
}

func mergeArtifacts(base []runtime.JobArtifactSpec, extra []runtime.JobArtifactSpec) []runtime.JobArtifactSpec {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, a := range base {
		seen[artifactKey(a)] = struct{}{}
	}
	for _, a := range extra {
		key := artifactKey(a)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, a)
	}
	return base
}

func artifactKey(a runtime.JobArtifactSpec) string {
	if path := artifactPath(a.Content, a.URI); path != "" {
		return a.Type + ":path:" + path
	}
	if a.URI != "" {
		return a.Type + ":uri:" + a.URI
	}
	return a.Type + ":name:" + a.Name
}

func requiresScreenshotProof(m *role.Manifest) bool {
	return m != nil && m.Name == "prototype-builder"
}

func hasArtifactType(artifacts []runtime.JobArtifactSpec, typ string) bool {
	for _, a := range artifacts {
		if a.Type == typ {
			return true
		}
	}
	return false
}

func IsLocalImageArtifact(a runtime.JobArtifactSpec) (string, bool) {
	if a.Type != runtime.JobArtifactTypeScreenshot {
		return "", false
	}
	path := artifactPath(a.Content, a.URI)
	if path == "" || !regularFileExists(path) {
		return "", false
	}
	return path, true
}

func ArtifactPath(content, uri string) string {
	return artifactPath(content, uri)
}

func artifactPath(content, uri string) string {
	if strings.HasPrefix(uri, "file://") {
		return strings.TrimPrefix(uri, "file://")
	}
	if filepath.IsAbs(content) {
		return content
	}
	return ""
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mimeType(path string) string {
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); mt != "" {
		return mt
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".md":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "`'\"")
	path = strings.TrimRight(path, ".,;:)")
	if !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

func safePart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ':':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-_:")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
