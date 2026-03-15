package bot

import (
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
)

func TestBuildSearchExpandedHistorySelectsMatchingBranch(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: "assistant", Content: "[Compacted conversation summary]\n\nBilling migration decisions and VAT handling."},
		{Role: "user", Content: "Let's define the invoice export."},
		{Role: "assistant", Content: "Use UTF-8 CSV for invoices."},
		{Role: "assistant", Content: "[Compacted conversation summary]\n\nSAP launch checklist, transports, and Tuesday deployment prep."},
		{Role: "user", Content: "Need the SAP launch checklist for Tuesday."},
		{Role: "assistant", Content: "Check transports, smoke tests, and rollback notes."},
	}

	history := buildSearchExpandedHistory(msgs, "SAP launch checklist Tuesday")
	if len(history) != 3 {
		t.Fatalf("expected 3 history messages, got %d", len(history))
	}

	if history[0].Content != msgs[3].Content {
		t.Fatalf("expected selected summary %q, got %q", msgs[3].Content, history[0].Content)
	}
	if history[1].Content != msgs[4].Content || history[2].Content != msgs[5].Content {
		t.Fatalf("expected matching raw branch, got %+v", history)
	}

	for _, msg := range history {
		if msg.Content == msgs[0].Content || msg.Content == msgs[1].Content || msg.Content == msgs[2].Content {
			t.Fatalf("history should not include unrelated branch content: %+v", history)
		}
	}
}

func TestBuildSearchExpandedHistoryUsesSummaryLayer(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: "assistant", Content: "[Compacted conversation summary]\n\nVAT numbers, billing migration, and customer tax handling."},
		{Role: "user", Content: "Okay, noted."},
		{Role: "assistant", Content: "Let's move on to deployment."},
	}

	history := buildSearchExpandedHistory(msgs, "VAT numbers")
	if len(history) != 1 {
		t.Fatalf("expected summary-only history, got %d messages", len(history))
	}
	if history[0].Content != msgs[0].Content {
		t.Fatalf("expected summary layer to be selected, got %q", history[0].Content)
	}
}

func TestBuildSearchExpandedHistorySkipsZeroMatchSummaryBranches(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: "assistant", Content: "[Compacted conversation summary]\n\nVAT numbers, billing migration, and customer tax handling."},
		{Role: "user", Content: "Okay, noted."},
		{Role: "assistant", Content: "Let's move on to deployment."},
	}

	history := buildSearchExpandedHistory(msgs, "Tuesday launch checklist")
	if len(history) != 0 {
		t.Fatalf("expected no targeted history for unrelated query, got %+v", history)
	}
}

func TestBuildRunHistoryFallsBackToFullHistoryWithoutSummary(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
	}

	history := buildRunHistory(msgs, "second question", "gpt-4o")
	if len(history) != len(msgs) {
		t.Fatalf("expected full history fallback, got %d messages", len(history))
	}
	for i, msg := range history {
		if msg.Role != msgs[i].Role || msg.Content != msgs[i].Content {
			t.Fatalf("history[%d] = %+v, want role=%q content=%q", i, msg, msgs[i].Role, msgs[i].Content)
		}
	}
}

func TestCollapseSelectedHistoryKeepsCompactionSummaryStandalone(t *testing.T) {
	msgs := []storage.SessionMessageV2{
		{Role: ai.RoleAssistant, Content: "[Compacted conversation summary]\n\nSAP checklist."},
		{Role: ai.RoleAssistant, Content: "Here are the SAP deployment steps."},
		{Role: ai.RoleUser, Content: "ok"},
	}

	history := collapseSelectedHistory(msgs, map[int]struct{}{0: {}, 1: {}, 2: {}})
	if len(history) != 3 {
		t.Fatalf("expected summary and assistant reply to stay separate, got %+v", history)
	}
	if history[0].Content != msgs[0].Content || history[1].Content != msgs[1].Content {
		t.Fatalf("expected standalone compaction summary boundary, got %+v", history)
	}
}

func TestScoreContextCandidateUsesRawPhraseMatch(t *testing.T) {
	query := "launch the SAP deployment"
	score := scoreContextCandidate(
		tokenizeContextSearchTerms(query),
		normalizeContextSearchPhrase(query),
		"We need to launch the SAP deployment tomorrow after smoke tests.",
	)
	if score != 15 {
		t.Fatalf("score = %d, want 15", score)
	}
}
