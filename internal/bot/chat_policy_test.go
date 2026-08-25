package bot

// Chat response policy.
//
// Direct messages: every non-command message is an agent turn. The model — not
// a keyword table — decides whether the turn needs tools. Groups: the bot only
// answers when @-mentioned or replied to (standby), or always (active mode).
//
// These tests replace the rules-first keyword router. That router scored an
// English-only lexicon ("investigate", "repo", "failing test", …) to pick
// between a canned clarification, an inline reply, and a premium background
// job. The lexicon held no Cyrillic, so this bot's Russian traffic could never
// score as work — the classifier was structurally unable to serve its only
// user.

import (
	"context"
	"path/filepath"
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

// policyAnswer is the marker the stub model replies with. Seeing it in the
// Telegram transcript proves the turn reached the agent rather than a canned
// router response.
const policyAnswer = "AGENT-TURN-REACHED"

// policyAIClient records the user turns the agent was actually asked to answer.
type policyAIClient struct {
	mu   sync.Mutex
	seen []string
}

func (c *policyAIClient) Complete(context.Context, []ai.Message) (string, error) {
	return policyAnswer, nil
}

func (c *policyAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.mu.Lock()
	for _, m := range messages {
		if m.Role == ai.RoleUser {
			c.seen = append(c.seen, m.Content)
		}
	}
	c.mu.Unlock()
	return aiTextResponse(policyAnswer), nil
}

func (c *policyAIClient) sawUserText(needle string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.seen {
		if strings.Contains(got, needle) {
			return true
		}
	}
	return false
}

func (c *policyAIClient) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// waitForChatIdle blocks until the chat's run finishes, so the test store is
// not closed underneath an in-flight transcript write.
func waitForChatIdle(t *testing.T, b *Bot, chatID int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !b.queueManager.IsRunning(chatID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat %d did not finish its run", chatID)
}

func newChatPolicyTestBot(t *testing.T, tg *fakeTelegramAPI, groupMode string) (*Bot, *policyAIClient) {
	t.Helper()

	root := t.TempDir()
	store, err := storage.New(filepath.Join(root, "bot.db"))
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
		Files: map[string]string{
			"IDENTITY.md": "Test Bot 🤖",
			"SOUL.md":     "Answer the user.",
		},
	}

	aiClient := &policyAIClient{}
	registry := tools.NewRegistry()
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
		groupManager:   NewGroupManager(store, groupMode, "okgobot"),
		hub:            agent.NewRuntimeHub(resolver),
		debouncer:      NewDebouncer(1 * time.Millisecond),
		rateLimiter:    NewRateLimiter(100, time.Second),
		fragmentBuffer: NewFragmentBuffer(),
		mediaGroupBuf:  NewMediaGroupBuffer(),
		queueManager:   NewQueueManager(),
		ackManager:     NewAckHandleManager(),
	}, aiClient
}

// TestDirectMessagePolicy_EveryMessageIsAnAgentTurn is the core of the new
// policy: no DM text is intercepted before the model sees it.
func TestDirectMessagePolicy_EveryMessageIsAnAgentTurn(t *testing.T) {
	cases := []struct {
		name string
		text string
		// oldPolicy records what the deleted keyword router did with this
		// input, so the regression these tests guard stays legible.
		oldPolicy string
	}{
		{
			name:      "russian work request",
			text:      "Посмотри логи сервиса и почини падающие тесты в репозитории",
			oldPolicy: "reply (no Cyrillic in the lexicon — could never score as work)",
		},
		{
			name:      "russian short imperative",
			text:      "почини это",
			oldPolicy: "reply (the English twin 'fix it' was answered with a canned question)",
		},
		{
			name:      "english underspecified request",
			text:      "can you investigate this?",
			oldPolicy: "clarification",
		},
		{
			name:      "english heavy work request",
			text:      "Investigate the failing tests in the repo, check the logs, and open a PR with the fix.",
			oldPolicy: "launch_job",
		},
		{
			name:      "forced job prefix",
			text:      "job: investigate failing tests in internal/runtime and open a PR",
			oldPolicy: "launch_job",
		},
		{
			name:      "plain question",
			text:      "What does queue mode do?",
			oldPolicy: "reply",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, aiClient := newChatPolicyTestBot(t, tg, "standby")

			ctx := &fakeContext{msg: &telebot.Message{
				ID:     100,
				Text:   tc.text,
				Chat:   &telebot.Chat{ID: 5150, Type: telebot.ChatPrivate},
				Sender: &telebot.User{ID: 7, Username: "oleg"},
			}}

			if err := b.handleMessage(context.Background(), ctx); err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}

			tg.waitForText(t, policyAnswer, 5*time.Second)
			waitForChatIdle(t, b, 5150, 5*time.Second)

			if !aiClient.sawUserText(tc.text) {
				t.Fatalf("agent never received the user text (old policy: %s)", tc.oldPolicy)
			}
			if tg.hasText("What should I work on exactly") {
				t.Fatalf("a canned clarification was sent instead of an agent turn (old policy: %s)", tc.oldPolicy)
			}
			if tg.hasText("Working on it in the background") {
				t.Fatalf("the turn was diverted to a background job instead of an agent turn (old policy: %s)", tc.oldPolicy)
			}
		})
	}
}

// TestGroupPolicy_OnlyMentionsAndRepliesAreAnswered pins the group half of the
// policy: standby groups answer @-mentions and replies to the bot, nothing else.
func TestGroupPolicy_OnlyMentionsAndRepliesAreAnswered(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	gm := NewGroupManager(store, "standby", "okgobot")
	group := &telebot.Chat{ID: -100, Type: telebot.ChatGroup}

	cases := []struct {
		name string
		msg  *telebot.Message
		want bool
	}{
		{
			name: "plain english chatter is ignored",
			msg:  &telebot.Message{Text: "so anyway I rebuilt the whole repo last night", Chat: group},
			want: false,
		},
		{
			name: "plain russian chatter is ignored",
			msg:  &telebot.Message{Text: "почини пожалуйста тесты в репозитории", Chat: group},
			want: false,
		},
		{
			name: "explicit mention is answered",
			msg: &telebot.Message{
				Text:     "@okgobot почини тесты",
				Chat:     group,
				Entities: []telebot.MessageEntity{{Type: telebot.EntityMention, Offset: 0, Length: 8}},
			},
			want: true,
		},
		{
			name: "reply to the bot is answered",
			msg: &telebot.Message{
				Text:    "и ещё раз, пожалуйста",
				Chat:    group,
				ReplyTo: &telebot.Message{Sender: &telebot.User{IsBot: true, Username: "okgobot"}},
			},
			want: true,
		},
		{
			name: "reply to another human is ignored",
			msg: &telebot.Message{
				Text:    "согласен",
				Chat:    group,
				ReplyTo: &telebot.Message{Sender: &telebot.User{IsBot: false, Username: "someone"}},
			},
			want: false,
		},
		{
			name: "direct messages are always answered",
			msg:  &telebot.Message{Text: "привет", Chat: &telebot.Chat{ID: 42, Type: telebot.ChatPrivate}},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gm.ShouldRespond(tc.msg.Chat.ID, tc.msg, "okgobot"); got != tc.want {
				t.Fatalf("ShouldRespond() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGroupPolicy_UnmentionedMessageNeverReachesTheAgent is the end-to-end
// counterpart: standby group traffic must not be answered, ingested, or run.
func TestGroupPolicy_UnmentionedMessageNeverReachesTheAgent(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, aiClient := newChatPolicyTestBot(t, tg, "standby")

	ctx := &fakeContext{msg: &telebot.Message{
		ID:     100,
		Text:   "почини пожалуйста падающие тесты в репозитории",
		Chat:   &telebot.Chat{ID: -1001, Type: telebot.ChatSuperGroup},
		Sender: &telebot.User{ID: 7, Username: "oleg"},
	}}

	if err := b.handleMessage(context.Background(), ctx); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	// Give any (incorrectly) spawned run a chance to surface before asserting.
	time.Sleep(200 * time.Millisecond)

	if n := aiClient.calls(); n != 0 {
		t.Fatalf("agent was invoked %d times for an unmentioned group message", n)
	}
	if len(ctx.sent) != 0 {
		t.Fatalf("bot replied to an unmentioned group message: %#v", ctx.sent)
	}
	if tg.hasText(policyAnswer) {
		t.Fatal("bot delivered an agent answer to an unmentioned group message")
	}
}

// TestGroupPolicy_MentionedMessageReachesTheAgent is the positive end-to-end
// case: an @-mention in a standby group is a full agent turn.
func TestGroupPolicy_MentionedMessageReachesTheAgent(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, aiClient := newChatPolicyTestBot(t, tg, "standby")

	const text = "@okgobot почини падающие тесты в репозитории"
	ctx := &fakeContext{msg: &telebot.Message{
		ID:       101,
		Text:     text,
		Chat:     &telebot.Chat{ID: -1002, Type: telebot.ChatSuperGroup},
		Sender:   &telebot.User{ID: 7, Username: "oleg"},
		Entities: []telebot.MessageEntity{{Type: telebot.EntityMention, Offset: 0, Length: 8}},
	}}

	if err := b.handleMessage(context.Background(), ctx); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	tg.waitForText(t, policyAnswer, 5*time.Second)
	waitForChatIdle(t, b, -1002, 5*time.Second)

	if !aiClient.sawUserText("почини падающие тесты") {
		t.Fatal("agent never received the mentioned group message")
	}
}
