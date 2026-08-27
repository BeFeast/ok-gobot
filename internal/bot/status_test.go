package bot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
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

func TestBackendStatusLineUsesDynamicHealthOverStaticStartupSnapshot(t *testing.T) {
	b := &Bot{aiConfig: AIConfig{
		Provider: "chatgpt",
		Model:    "primary",
		BackendHealth: ai.BackendHealth{
			Identity: ai.BackendIdentity{Backend: "chatgpt", Model: "primary"},
			Status:   ai.BackendHealthHealthy,
		},
		BackendHealthSnapshot: func() ai.BackendHealth {
			return ai.BackendHealth{
				Identity:    ai.BackendIdentity{Backend: "chatgpt", Model: "primary"},
				Status:      ai.BackendHealthUnavailable,
				FailureKind: ai.BackendFailureUnavailable,
				Fallback:    ai.FallbackDecision{Action: ai.FallbackActionFallback, ToModel: "fallback"},
			}
		},
	}}

	out := b.backendStatusLine()
	if !strings.Contains(out, "health=unavailable") || !strings.Contains(out, "fallback=fallback") {
		t.Fatalf("backend status did not override stale startup health: %q", out)
	}
	b.aiConfig.BackendHealthSnapshot = func() ai.BackendHealth { return ai.BackendHealth{} }
	if out := b.backendStatusLine(); !strings.Contains(out, "health=healthy") {
		t.Fatalf("empty dynamic snapshot did not retain static startup fallback: %q", out)
	}
}

func TestGetStatusExposesOAuthChatGPTRuntimeHealthWithoutAPIKey(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	dynamic := ai.BackendHealth{
		Identity:    ai.BackendIdentity{Backend: "chatgpt", Model: "gpt-5.6-sol"},
		Status:      ai.BackendHealthUnavailable,
		FailureKind: ai.BackendFailureUnavailable,
		Detail:      "runtime request failed",
	}
	b := &Bot{
		store:       store,
		personality: &agent.Personality{},
		aiConfig: AIConfig{
			Provider:              "chatgpt",
			Model:                 "gpt-5.6-sol",
			BackendHealthSnapshot: func() ai.BackendHealth { return dynamic },
		},
	}

	status := b.GetStatus()
	aiStatus, ok := status["ai"].(map[string]interface{})
	if !ok {
		t.Fatalf("GetStatus ai = %#v, want OAuth ChatGPT status without API key", status["ai"])
	}
	health, ok := aiStatus["health"].(ai.BackendHealth)
	if !ok || health.Status != ai.BackendHealthUnavailable || health.Identity.Model != "gpt-5.6-sol" {
		t.Fatalf("GetStatus health = %#v, want dynamic OAuth ChatGPT runtime health", aiStatus["health"])
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
	for _, provider := range []string{"chatgpt", " OpenAI-Codex "} {
		t.Run(provider, func(t *testing.T) {
			b := &Bot{aiConfig: AIConfig{Provider: provider}}
			label, ok := b.nonAPIKeyAuthLabel()
			if !ok || label != "Codex auth" {
				t.Fatalf("nonAPIKeyAuthLabel() = %q, %v; want Codex auth, true", label, ok)
			}
		})
	}
}

func TestSkillCompatibilityStatusLineAvoidsTelegramMarkdownUnderscore(t *testing.T) {
	loader := &bootstrap.Loader{Skills: []bootstrap.SkillEntry{{Compatibility: bootstrap.SkillCompatibilityTrustedWorkspace}}}
	out := skillCompatibilityStatusLine(loader)
	if strings.Contains(out, "trusted_workspace") || !strings.Contains(out, "trusted-workspace=1") {
		t.Fatalf("unexpected Telegram status label: %q", out)
	}
}
