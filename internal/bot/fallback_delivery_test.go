package bot

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"

	telebot "gopkg.in/telebot.v4"
)

// emptyFinalAIClient answers the first round with a tool call and every round
// after it with an empty final turn — the shape of the 2026-08-27 incident.
type emptyFinalAIClient struct {
	toolName string
}

func (c *emptyFinalAIClient) Complete(context.Context, []ai.Message) (string, error) {
	return "", nil
}

func (c *emptyFinalAIClient) SupportsVision() bool { return false }

func (c *emptyFinalAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	calledTool := false
	for _, m := range messages {
		if m.Role == ai.RoleTool {
			calledTool = true
		}
	}
	msg := ai.ChatMessage{Role: "assistant"}
	finish := "stop"
	if !calledTool {
		msg.ToolCalls = []ai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: ai.FunctionCall{Name: c.toolName, Arguments: `{"input":"x"}`},
		}}
		finish = "tool_calls"
	}
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{{Message: msg, FinishReason: finish}},
	}, nil
}

// A run that produced no answer must not be announced as a success, and must not
// take the user's own question down with it.
func TestFallbackReplyKeepsTheQuestionAndDoesNotClaimDone(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	testBot, store := newEmptyFinalTestBot(t, tg)

	const chatID int64 = 4343
	const question = "why did we pick Forgejo"

	ctx := &fakeContext{
		msg: &telebot.Message{
			ID:     200,
			Text:   question,
			Chat:   &telebot.Chat{ID: chatID, Type: telebot.ChatPrivate},
			Sender: &telebot.User{ID: 7, Username: "tester"},
		},
	}
	if err := testBot.handleMessage(context.Background(), ctx); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var stored []string
	for time.Now().Before(deadline) {
		msgs, err := store.GetSessionMessagesV2(string(agent.NewDMSessionKey(chatID)), 10)
		if err == nil && len(msgs) > 0 {
			stored = nil
			for _, m := range msgs {
				stored = append(stored, m.Role+":"+m.Content)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(stored) == 0 {
		t.Fatal("the user's question was not persisted — a failed run erased it from history")
	}
	foundQuestion := false
	for _, row := range stored {
		if strings.Contains(row, question) {
			foundQuestion = true
		}
		if strings.Contains(row, "could not turn the results") || strings.Contains(row, "empty final message") {
			t.Fatalf("the synthetic apology was replayed into history: %q", row)
		}
	}
	if !foundQuestion {
		t.Fatalf("question missing from transcript: %v", stored)
	}

	if tg.hasText("✅ Done") {
		t.Fatal("a run that produced no answer was reported as Done")
	}
}

// newEmptyFinalTestBot mirrors newInterruptTestBot but wires an AI client that
// returns an empty final turn and a tool that returns immediately.
func newEmptyFinalTestBot(t *testing.T, tg *fakeTelegramAPI) (*Bot, *storage.Store) {
	t.Helper()

	root := t.TempDir()
	store, err := storage.New(path.Join(root, "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

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

	personality := &agent.Personality{
		BasePath: root,
		Files:    map[string]string{"IDENTITY.md": "Test Bot"},
	}

	registry := tools.NewRegistry()
	registry.Register(&instantTool{name: "probe"})

	aiClient := &emptyFinalAIClient{toolName: "probe"}
	resolver := &agent.RunResolver{
		Store:              store,
		DefaultPersonality: personality,
		AIConfig: agent.AIResolverConfig{
			Provider:      "test",
			Model:         "gpt-4o",
			DefaultClient: aiClient,
			ModelAliases:  map[string]string{},
		},
		ToolRegistry: registry,
	}

	return &Bot{
		api:          api,
		store:        store,
		ai:           aiClient,
		aiConfig:     AIConfig{Provider: "test", Model: "gpt-4o"},
		personality:  personality,
		toolRegistry: registry,
		safety:       agent.NewSafety(),
		memory:       agent.NewMemory(root),
		authManager: &AuthManager{
			store:        store,
			config:       config.AuthConfig{Mode: "open"},
			pairingCodes: make(map[string]*PairingCode),
		},
		groupManager:   NewGroupManager(store, "active", "okgobot"),
		hub:            agent.NewRuntimeHub(resolver),
		debouncer:      NewDebouncer(1 * time.Millisecond),
		rateLimiter:    NewRateLimiter(100, time.Second),
		fragmentBuffer: NewFragmentBuffer(),
		mediaGroupBuf:  NewMediaGroupBuffer(),
		queueManager:   NewQueueManager(),
		ackManager:     NewAckHandleManager(),
	}, store
}

type instantTool struct{ name string }

func (t *instantTool) Name() string        { return t.name }
func (t *instantTool) Description() string { return "returns immediately" }
func (t *instantTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"input": map[string]interface{}{"type": "string"}},
	}
}
func (t *instantTool) Execute(context.Context, ...string) (string, error) {
	return "fetched content worth keeping", nil
}
