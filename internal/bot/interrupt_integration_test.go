package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

func TestHandleMessage_InterruptDefaultRespondsDuringLongToolCall(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	testBot, store, slow := newInterruptTestBot(t, tg)
	t.Cleanup(func() { testBot.asyncWg.Wait() })

	const chatID int64 = 4242
	if err := store.SaveSession(chatID, "existing session state"); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	first := &fakeContext{
		msg: &telebot.Message{
			ID:   100,
			Text: "start slow tool",
			Chat: &telebot.Chat{ID: chatID, Type: telebot.ChatPrivate},
			Sender: &telebot.User{
				ID:       7,
				Username: "tester",
			},
		},
	}
	second := &fakeContext{
		msg: &telebot.Message{
			ID:   101,
			Text: "interrupt now",
			Chat: &telebot.Chat{ID: chatID, Type: telebot.ChatPrivate},
			Sender: &telebot.User{
				ID:       7,
				Username: "tester",
			},
		},
	}

	if err := testBot.handleMessage(context.Background(), first); err != nil {
		t.Fatalf("first handleMessage() error = %v", err)
	}

	select {
	case <-slow.started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow tool did not start")
	}

	start := time.Now()
	if err := testBot.handleMessage(context.Background(), second); err != nil {
		t.Fatalf("second handleMessage() error = %v", err)
	}

	req := tg.waitForText(t, "Second message handled immediately.", 3*time.Second)
	if req.Method != "sendMessage" && req.Method != "editMessageText" {
		t.Fatalf("unexpected telegram method for final response: %+v", req)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("second response arrived too slowly: %s", elapsed)
	}
	if tg.hasText("queued — previous run in progress") {
		t.Fatal("interrupt-mode scenario should not send queued placeholder")
	}

	select {
	case <-slow.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("slow tool was not cancelled after interrupt")
	}
}

type fakeTelegramAPI struct {
	server        *httptest.Server
	mu            sync.Mutex
	requests      []telegramRequest
	nextMessageID int
	updates       chan struct{}
}

type telegramRequest struct {
	Method    string
	Text      string
	Action    string
	ChatID    int64
	MessageID int
}

func newFakeTelegramAPI(t *testing.T) *fakeTelegramAPI {
	t.Helper()

	f := &fakeTelegramAPI{
		nextMessageID: 1000,
		updates:       make(chan struct{}, 128),
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelegramAPI) handle(w http.ResponseWriter, r *http.Request) {
	var payload map[string]string
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	req := telegramRequest{
		Method: path.Base(r.URL.Path),
		Text:   payload["text"],
		Action: payload["action"],
		ChatID: parseInt64(payload["chat_id"]),
	}
	if payload["message_id"] != "" {
		req.MessageID, _ = strconv.Atoi(payload["message_id"])
	}

	f.mu.Lock()
	switch req.Method {
	case "sendMessage":
		f.nextMessageID++
		req.MessageID = f.nextMessageID
	case "editMessageText":
		if req.MessageID == 0 {
			f.nextMessageID++
			req.MessageID = f.nextMessageID
		}
	}
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	select {
	case f.updates <- struct{}{}:
	default:
	}

	switch req.Method {
	case "sendMessage", "editMessageText":
		writeTelegramOK(w, map[string]any{
			"message_id": req.MessageID,
			"date":       0,
			"chat": map[string]any{
				"id":   req.ChatID,
				"type": "private",
			},
			"text": req.Text,
		})
	case "sendChatAction", "deleteMessage":
		writeTelegramOK(w, true)
	default:
		writeTelegramOK(w, true)
	}
}

func writeTelegramOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	})
}

func (f *fakeTelegramAPI) waitForText(t *testing.T, needle string, timeout time.Duration) telegramRequest {
	t.Helper()

	deadline := time.After(timeout)
	for {
		if req, ok := f.findText(needle); ok {
			return req
		}

		select {
		case <-f.updates:
		case <-deadline:
			f.mu.Lock()
			defer f.mu.Unlock()
			t.Fatalf("timed out waiting for %q; requests=%+v", needle, f.requests)
		}
	}
}

func (f *fakeTelegramAPI) hasText(needle string) bool {
	_, ok := f.findText(needle)
	return ok
}

func (f *fakeTelegramAPI) findText(needle string) (telegramRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, req := range f.requests {
		if strings.Contains(req.Text, needle) {
			return req, true
		}
	}
	return telegramRequest{}, false
}

func parseInt64(raw string) int64 {
	if raw == "" {
		return 0
	}
	val, _ := strconv.ParseInt(raw, 10, 64)
	return val
}

func newInterruptTestBot(t *testing.T, tg *fakeTelegramAPI) (*Bot, *storage.Store, *blockingTool) {
	t.Helper()

	root, err := os.MkdirTemp("", "interrupt-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	store, err := storage.New(path.Join(root, "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})

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
		Files: map[string]string{
			"IDENTITY.md": "Test Bot 🤖",
			"SOUL.md":     "Stay responsive.",
			"AGENTS.md":   "Interrupt long work when the user sends a new message.",
		},
	}

	slow := &blockingTool{
		name:     "slow_tool",
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	registry := tools.NewRegistry()
	registry.Register(slow)

	aiClient := &interruptAIClient{toolName: slow.name}
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
	}, store, slow
}

type blockingTool struct {
	name     string
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (t *blockingTool) Name() string        { return t.name }
func (t *blockingTool) Description() string { return "blocks until the context is canceled" }
func (t *blockingTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{"type": "string"},
		},
	}
}

func (t *blockingTool) Execute(ctx context.Context, args ...string) (string, error) {
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	close(t.canceled)
	return "", ctx.Err()
}

type interruptAIClient struct {
	toolName string
}

func (c *interruptAIClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return "", nil
}

func (c *interruptAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	last := messages[len(messages)-1]

	if last.Role == ai.RoleUser {
		switch last.Content {
		case "start slow tool":
			return aiToolCallResponse(c.toolName, `{"input":"hold"}`), nil
		case "interrupt now":
			return aiTextResponse("Second message handled immediately."), nil
		}
	}

	return aiTextResponse("ok"), nil
}

func aiToolCallResponse(name, args string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: ai.ChatMessage{
					Role: ai.RoleAssistant,
					ToolCalls: []ai.ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: ai.FunctionCall{
								Name:      name,
								Arguments: args,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
}

func aiTextResponse(text string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Index:        0,
				Message:      ai.ChatMessage{Role: ai.RoleAssistant, Content: text},
				FinishReason: "stop",
			},
		},
	}
}
