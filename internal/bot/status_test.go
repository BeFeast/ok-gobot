package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/memory"
)

type fakeMemoryStatusProvider struct {
	status memory.IndexStatus
	err    error
}

func (f fakeMemoryStatusProvider) Status(context.Context) (memory.IndexStatus, error) {
	return f.status, f.err
}

func TestBuildMemoryStatusStringIncludesCounts(t *testing.T) {
	b := &Bot{memoryStatus: fakeMemoryStatusProvider{status: memory.IndexStatus{
		Enabled:       true,
		State:         memory.MemoryStateOK,
		BackendType:   memory.BackendSQLite,
		WatcherState:  memory.WatcherStateActive,
		SourceCount:   2,
		ChunkCount:    12,
		LastIndexedAt: "2026-04-30 12:00:00",
	}}}

	out := b.buildMemoryStatusString(t.Context())
	for _, want := range []string{"Memory status: ok", "Indexed: 2 source(s), 12 chunk(s)", "Watcher: active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestBuildMemoryStatusStringShowsProviderError(t *testing.T) {
	b := &Bot{memoryStatus: fakeMemoryStatusProvider{
		status: memory.IndexStatus{
			Enabled:      true,
			BackendType:  memory.BackendSQLite,
			WatcherState: memory.WatcherStateError,
			LastError:    "initial index failed",
		},
		err: errors.New("memory store is not configured"),
	}}

	out := b.buildMemoryStatusString(t.Context())
	for _, want := range []string{"Memory status: error", "Last error: initial index failed", "Action:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestBuildQMDStatusStringShowsFallbackState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status memory.IndexStatus
		want   []string
	}{
		{
			name: "used",
			status: memory.IndexStatus{
				BackendType: "qmd",
				QMDStatus:   "used (primary=qmd, fallback=builtin)",
			},
			want: []string{"QMD: used", "Fallback: builtin"},
		},
		{
			name: "skipped",
			status: memory.IndexStatus{
				BackendType: memory.BackendSQLite,
				QMDStatus:   "skipped (memory.backend=builtin)",
			},
			want: []string{"QMD: skipped", "memory.backend=builtin"},
		},
		{
			name: "unavailable",
			status: memory.IndexStatus{
				BackendType: "qmd",
				QMDStatus:   "unavailable: qmd binary not found; fallback=builtin",
			},
			want: []string{"QMD: unavailable", "qmd binary not found", "Fallback: builtin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &Bot{memoryStatus: fakeMemoryStatusProvider{status: tt.status}}
			out := b.buildQMDStatusString(t.Context())
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("expected %q in output: %q", want, out)
				}
			}
		})
	}
}

func TestBuildQMDStatusStringUsesRuntimeQMDStatus(t *testing.T) {
	t.Parallel()

	reporter := memory.NewStatusReporter(nil, memory.StatusOptions{
		Enabled:      false,
		BackendType:  "qmd",
		QMDStatus:    "used (primary=qmd, fallback=builtin)",
		WatcherState: memory.WatcherStateDisabled,
	})
	reporter.SetQMDStatusFunc(func(context.Context) string {
		return "unavailable: server unavailable; fallback=builtin"
	})
	b := &Bot{memoryStatus: reporter}

	out := b.buildQMDStatusString(t.Context())
	for _, want := range []string{"QMD: unavailable", "server unavailable", "Fallback: builtin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
	if strings.Contains(out, "QMD: used") {
		t.Fatalf("expected runtime QMD status to replace startup status: %q", out)
	}
}

func TestBackendStatusLineIncludesModelTierEffortAndHealth(t *testing.T) {
	b := &Bot{aiConfig: AIConfig{
		Provider:        "anthropic",
		Model:           "claude-sonnet",
		ModelTier:       "premium",
		DefaultThinking: "high",
		BackendHealth: ai.BackendHealth{
			Identity: ai.BackendIdentity{Backend: "claude", Model: "claude-sonnet"},
			Status:   ai.BackendHealthHealthy,
			Fallback: ai.FallbackDecision{Action: ai.FallbackActionPrimary},
		},
	}}

	out := b.backendStatusLine()
	for _, want := range []string{"claude", "health=healthy", "tier=premium", "effort=high", "fallback=primary"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestSendTelegramMarkdownWithPlainFallback(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	tg.failNextMarkdownSend()

	api, err := telebot.NewBot(telebot.Settings{
		Token:   "TEST",
		URL:     tg.server.URL,
		Client:  tg.server.Client(),
		Offline: true,
	})
	if err != nil {
		t.Fatalf("telebot.NewBot() error = %v", err)
	}
	api.Me = &telebot.User{ID: 1, Username: "okgobot", IsBot: true}

	if err := sendTelegramMarkdownWithPlainFallback(api, &telebot.Chat{ID: 99}, "bad _markdown"); err != nil {
		t.Fatalf("sendTelegramMarkdownWithPlainFallback() error = %v", err)
	}

	var sends []telegramRequest
	for _, req := range tg.snapshotRequests() {
		if req.Method == "sendMessage" {
			sends = append(sends, req)
		}
	}
	if len(sends) != 2 {
		t.Fatalf("sendMessage request count = %d, want 2: %+v", len(sends), sends)
	}
	if sends[0].ParseMode != telebot.ModeMarkdown || sends[1].ParseMode != "" {
		t.Fatalf("parse modes = %q, %q; want Markdown then plain", sends[0].ParseMode, sends[1].ParseMode)
	}
}

func TestStatusRecognizesChatGPTCodexAuthWithoutAPIKey(t *testing.T) {
	b := &Bot{aiConfig: AIConfig{Provider: "chatgpt"}}
	label, ok := b.nonAPIKeyAuthLabel()
	if !ok || label != "Codex auth" {
		t.Fatalf("nonAPIKeyAuthLabel() = %q, %v; want Codex auth, true", label, ok)
	}
}

func TestSkillCompatibilityStatusLineAvoidsTelegramMarkdownUnderscore(t *testing.T) {
	loader := &bootstrap.Loader{Skills: []bootstrap.SkillEntry{{Compatibility: bootstrap.SkillCompatibilityTrustedWorkspace}}}
	out := skillCompatibilityStatusLine(loader)
	if strings.Contains(out, "trusted_workspace") || !strings.Contains(out, "trusted-workspace=1") {
		t.Fatalf("unexpected Telegram status label: %q", out)
	}
}
