package bot

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
)

type finalDeliveryAIClient struct{ reply string }

func (c *finalDeliveryAIClient) Complete(context.Context, []ai.Message) (string, error) {
	return c.reply, nil
}

func (c *finalDeliveryAIClient) CompleteWithTools(context.Context, []ai.ChatMessage, []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return aiTextResponse(c.reply), nil
}

// Exercise ingress, model execution, Rich Markdown delivery, and ack cleanup
// together. A model answer reporting an unmet request is still a valid reply,
// but must never acquire a separate success claim from the transport.
func TestFinalReplyRemovesProgressOnlyAfterDelivery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"ordinary reply", "Here is the answer."},
		{"download not started", "Resident Evil (2026) добавлен в Radarr, но подходящий release не найден — скачивание не началось."},
		{"multiple chunks", strings.Repeat("x", maxTelegramRichMessageLen+20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b := runFinalDeliveryTurn(t, tg, tc.reply)
			requests := tg.snapshotRequests()
			ackID := finalDeliveryAckID(t, requests)
			var delivered strings.Builder
			deletes := 0
			for _, req := range requests {
				switch req.Method {
				case "sendRichMessage":
					delivered.WriteString(req.Text)
				case "deleteMessage":
					deletes++
					if req.MessageID != ackID || delivered.String() != tc.reply {
						t.Fatalf("deleted message %d before the complete reply was delivered (ack %d)", req.MessageID, ackID)
					}
				}
			}
			if deletes != 1 || delivered.String() != tc.reply {
				t.Fatalf("deletes = %d, delivered length = %d, want one cleanup after %d characters", deletes, delivered.Len(), len(tc.reply))
			}
			assertNoDoneAck(t, requests)
			if b.ackManager.Exists(4242) {
				t.Fatal("delivered turn retained its tracked ack")
			}
			messages, err := b.store.GetSessionMessagesV2(string(agent.NewDMSessionKey(4242)), 10)
			if err != nil || len(messages) != 2 || messages[1].Content != tc.reply {
				t.Fatalf("final answer was not preserved in the transcript: messages = %v, err = %v", messages, err)
			}
		})
	}
}

func TestFinalReplyDeliveryFailureKeepsFailurePlaceholder(t *testing.T) {
	for _, failedChunk := range []int{1, 2} {
		t.Run(string(rune('0'+failedChunk)), func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			richCalls := 0
			tg.rejectRequest = func(req telegramRequest) (int, string) {
				if req.Method == "sendRichMessage" {
					richCalls++
					if richCalls == failedChunk {
						return http.StatusInternalServerError, "Internal Server Error"
					}
				}
				return 0, ""
			}
			runFinalDeliveryTurn(t, tg, strings.Repeat("x", maxTelegramRichMessageLen+20))
			requests := tg.snapshotRequests()
			ackID := finalDeliveryAckID(t, requests)
			foundFailure := false
			for _, req := range requests {
				if req.Method == "deleteMessage" {
					t.Fatal("incomplete delivery removed the progress message")
				}
				if req.Method == "editMessageText" && req.MessageID == ackID && strings.Contains(req.Text, replyDeliveryFailureDetail) {
					foundFailure = true
				}
			}
			if !foundFailure {
				t.Fatal("incomplete delivery did not leave a failure on the placeholder")
			}
			assertNoDoneAck(t, requests)
		})
	}
}

func TestFinalReplyDeleteFailureLeavesNeutralReceipt(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	tg.rejectRequest = func(req telegramRequest) (int, string) {
		if req.Method == "deleteMessage" {
			return http.StatusBadRequest, "Bad Request: message can't be deleted"
		}
		return 0, ""
	}
	runFinalDeliveryTurn(t, tg, "No suitable release was found; the download has not started.")
	requests := tg.snapshotRequests()
	ackID := finalDeliveryAckID(t, requests)
	foundReceipt := false
	for _, req := range requests {
		if req.Method == "editMessageText" && req.MessageID == ackID && req.Text == replyDeliveredDetail {
			foundReceipt = true
		}
	}
	if !foundReceipt {
		t.Fatal("failed deletion left stale progress instead of a neutral delivery receipt")
	}
	assertNoDoneAck(t, requests)
}

func runFinalDeliveryTurn(t *testing.T, tg *fakeTelegramAPI, reply string) *Bot {
	t.Helper()
	b, _ := newChatPolicyTestBot(t, tg, "standby")
	client := &finalDeliveryAIClient{reply: reply}
	b.ai = client
	b.resolver.AIConfig.DefaultClient = client
	c := &fakeContext{msg: &telebot.Message{
		ID:     200,
		Text:   "скачай resident evil 2026 года",
		Chat:   &telebot.Chat{ID: 4242, Type: telebot.ChatPrivate},
		Sender: &telebot.User{ID: 7, Username: "tester"},
	}}
	if err := b.handleMessage(context.Background(), c); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	needle := reply
	if len(needle) > 100 {
		needle = needle[:100]
	}
	tg.waitForText(t, needle, 5*time.Second)
	waitForChatIdle(t, b, 4242, 5*time.Second)
	return b
}

func finalDeliveryAckID(t *testing.T, requests []telegramRequest) int {
	t.Helper()
	for _, req := range requests {
		if req.Method == "sendMessage" && req.Text == telegramStatusPhrase(jobStatusAccepted) {
			return req.MessageID
		}
	}
	t.Fatal("missing initial progress acknowledgement")
	return 0
}

func assertNoDoneAck(t *testing.T, requests []telegramRequest) {
	t.Helper()
	for _, req := range requests {
		if req.Text == telegramStatusPhrase(jobStatusCompleted) {
			t.Fatalf("a model answer acquired a false success acknowledgement: %+v", req)
		}
	}
}
