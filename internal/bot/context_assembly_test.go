package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
)

func TestTrimHistoryToTokenBudgetProtectsFreshTail(t *testing.T) {
	history := []ai.ChatMessage{
		{Role: ai.RoleUser, Content: strings.Repeat("a", 2000)},
		{Role: ai.RoleAssistant, Content: strings.Repeat("b", 2000)},
		{Role: ai.RoleUser, Content: strings.Repeat("c", 2000)},
		{Role: ai.RoleAssistant, Content: strings.Repeat("d", 2000)},
		{Role: ai.RoleUser, Content: strings.Repeat("e", 2000)},
		{Role: ai.RoleAssistant, Content: strings.Repeat("f", 2000)},
		{Role: ai.RoleUser, Content: strings.Repeat("g", 2000)},
		{Role: ai.RoleAssistant, Content: strings.Repeat("h", 2000)},
	}

	trimmed := trimHistoryToTokenBudget(history, "gpt-4")

	if len(trimmed) != chatProtectedTailMessages {
		t.Fatalf("trimmed len = %d, want %d", len(trimmed), chatProtectedTailMessages)
	}
	wantFirst := history[len(history)-chatProtectedTailMessages].Content
	if trimmed[0].Content != wantFirst {
		t.Fatalf("expected first protected message to survive trimming")
	}
	if trimmed[len(trimmed)-1].Content != history[len(history)-1].Content {
		t.Fatalf("expected newest message to survive trimming")
	}
}

func TestTrimHistoryToTokenBudgetStillEnforcesBudgetForShortHistory(t *testing.T) {
	history := []ai.ChatMessage{
		{Role: ai.RoleUser, Content: strings.Repeat("a", 120000)},
		{Role: ai.RoleAssistant, Content: strings.Repeat("b", 120000)},
	}

	trimmed := trimHistoryToTokenBudget(history, "gpt-4")
	if len(trimmed) >= len(history) {
		t.Fatalf("expected short history to be trimmed to fit budget, got %d messages", len(trimmed))
	}
	if got, budget := countChatMessageTokens(trimmed), modelContextBudget("gpt-4", chatHistoryBudgetFraction); got > budget {
		t.Fatalf("trimmed short history tokens = %d, want <= %d", got, budget)
	}
}

func TestBuildJobContextPackFromTranscriptSelectsRelevantOlderTurns(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: ai.RoleUser, Content: "Payments API timed out after deploy."},
		{Role: ai.RoleAssistant, Content: "We suspected the payments worker and retry queue."},
		{Role: ai.RoleUser, Content: "Let's order pizza tonight."},
		{Role: ai.RoleAssistant, Content: "Sure, pizza sounds good."},
		{Role: ai.RoleUser, Content: "Remember that job tg-1 failed on payments yesterday."},
		{Role: ai.RoleAssistant, Content: "Background job summary: payments queue saturation on shard 3.", RunID: "tg-1"},
		{Role: ai.RoleUser, Content: "By the way, I'm in meetings for the next hour."},
		{Role: ai.RoleAssistant, Content: "Noted."},
	}

	pack := buildJobContextPackFromTranscript(msgs, "Investigate the payments queue timeout regression", "gpt-4")

	if !strings.Contains(pack, "RELEVANT OLDER TURNS") {
		t.Fatalf("expected relevant older section in pack, got %q", pack)
	}
	if !strings.Contains(pack, "payments worker and retry queue") {
		t.Fatalf("expected payments troubleshooting turn in pack, got %q", pack)
	}
	if strings.Contains(pack, "pizza sounds good") {
		t.Fatalf("unrelated pizza turn should not be included, got %q", pack)
	}
	if !strings.Contains(pack, "FRESH RECENT TAIL") {
		t.Fatalf("expected fresh tail section in pack, got %q", pack)
	}
	if !strings.Contains(pack, "I'm in meetings") {
		t.Fatalf("expected fresh tail to remain in pack, got %q", pack)
	}
	if !strings.Contains(pack, "[assistant job]") {
		t.Fatalf("expected run_id-tagged assistant output in pack, got %q", pack)
	}
}

func TestScoreTranscriptTurnRequiresLexicalMatchBeforeRunIDBonus(t *testing.T) {
	noMatch := transcriptTurn{
		messages: []storage.SessionMessageV2{
			{Role: ai.RoleAssistant, Content: "Background job summary: shard 3 saturation.", RunID: "tg-1"},
		},
	}
	if got := scoreTranscriptTurn(noMatch, []string{"payments"}); got != 0 {
		t.Fatalf("score without lexical match = %d, want 0", got)
	}

	withMatch := transcriptTurn{
		messages: []storage.SessionMessageV2{
			{Role: ai.RoleAssistant, Content: "Background job summary: payments queue saturation on shard 3.", RunID: "tg-1"},
		},
	}
	if got := scoreTranscriptTurn(withMatch, []string{"payments"}); got != 11 {
		t.Fatalf("score with lexical match + run bonus = %d, want 11", got)
	}
}
