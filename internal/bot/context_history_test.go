package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/ai"
)

func msg(role, content string) ai.ChatMessage {
	return ai.ChatMessage{Role: role, Content: content}
}

func compactionMsg(summary string) ai.ChatMessage {
	return ai.ChatMessage{
		Role:    "assistant",
		Content: compactionPrefix + "\n\n" + summary,
	}
}

// ---------- hasCompactions ----------

func TestHasCompactions_NoCompactions(t *testing.T) {
	history := []ai.ChatMessage{
		msg("user", "hello"),
		msg("assistant", "hi there"),
	}
	if hasCompactions(history) {
		t.Fatal("expected false for history without compactions")
	}
}

func TestHasCompactions_WithCompaction(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("summary of past chat"),
		msg("user", "what about X?"),
		msg("assistant", "X is ..."),
	}
	if !hasCompactions(history) {
		t.Fatal("expected true for history with compaction")
	}
}

// ---------- isCompactionSummary ----------

func TestIsCompactionSummary(t *testing.T) {
	tests := []struct {
		msg  ai.ChatMessage
		want bool
	}{
		{compactionMsg("any summary"), true},
		{msg("assistant", "regular reply"), false},
		{msg("user", compactionPrefix), false}, // wrong role
		{msg("assistant", ""), false},
	}
	for _, tt := range tests {
		got := isCompactionSummary(&tt.msg)
		if got != tt.want {
			t.Errorf("isCompactionSummary(%q, %q) = %v, want %v",
				tt.msg.Role, tt.msg.Content[:min(30, len(tt.msg.Content))], got, tt.want)
		}
	}
}

// ---------- splitHistoryBranches ----------

func TestSplitHistoryBranches_NoBranches(t *testing.T) {
	history := []ai.ChatMessage{
		msg("user", "hello"),
		msg("assistant", "hi"),
	}
	branches := splitHistoryBranches(history)
	if len(branches) != 1 {
		t.Fatalf("expected 1 branch, got %d", len(branches))
	}
	if branches[0].summary != nil {
		t.Error("expected nil summary for first branch")
	}
	if len(branches[0].messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(branches[0].messages))
	}
}

func TestSplitHistoryBranches_SingleCompaction(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("talked about weather"),
		msg("user", "what about rain?"),
		msg("assistant", "rain is likely"),
	}
	branches := splitHistoryBranches(history)
	if len(branches) != 1 {
		t.Fatalf("expected 1 branch, got %d", len(branches))
	}
	if branches[0].summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if len(branches[0].messages) != 2 {
		t.Errorf("expected 2 raw messages, got %d", len(branches[0].messages))
	}
}

func TestSplitHistoryBranches_MultipleCompactions(t *testing.T) {
	history := []ai.ChatMessage{
		msg("user", "early message"),
		msg("assistant", "early reply"),
		compactionMsg("first compaction summary about recipes"),
		msg("user", "what about pasta?"),
		msg("assistant", "pasta is great"),
		compactionMsg("second compaction summary about travel"),
		msg("user", "tell me about Paris"),
		msg("assistant", "Paris is lovely"),
	}
	branches := splitHistoryBranches(history)
	if len(branches) != 3 {
		t.Fatalf("expected 3 branches, got %d", len(branches))
	}

	// First branch: raw messages before any compaction
	if branches[0].summary != nil {
		t.Error("first branch should have nil summary")
	}
	if len(branches[0].messages) != 2 {
		t.Errorf("first branch: expected 2 messages, got %d", len(branches[0].messages))
	}

	// Second branch: first compaction + its messages
	if branches[1].summary == nil {
		t.Error("second branch should have a summary")
	}
	if len(branches[1].messages) != 2 {
		t.Errorf("second branch: expected 2 messages, got %d", len(branches[1].messages))
	}

	// Third branch: second compaction + its messages
	if branches[2].summary == nil {
		t.Error("third branch should have a summary")
	}
	if len(branches[2].messages) != 2 {
		t.Errorf("third branch: expected 2 messages, got %d", len(branches[2].messages))
	}
}

// ---------- tokenizeSearchTerms ----------

func TestTokenizeSearchTerms_FiltersStopwords(t *testing.T) {
	terms := tokenizeSearchTerms("What is the recipe for pasta?")
	// "what", "is", "the", "for" are stop words; "recipe" and "pasta" remain
	for _, term := range terms {
		if stopWords[term] {
			t.Errorf("stop word %q should have been filtered", term)
		}
	}
	found := map[string]bool{}
	for _, term := range terms {
		found[term] = true
	}
	if !found["recipe"] {
		t.Error("expected 'recipe' in terms")
	}
	if !found["pasta"] {
		t.Error("expected 'pasta' in terms")
	}
}

func TestTokenizeSearchTerms_ShortTokensFiltered(t *testing.T) {
	terms := tokenizeSearchTerms("go is ok to do")
	// "go", "is", "ok", "to", "do" are all <=2 chars or stop words
	if len(terms) != 0 {
		t.Errorf("expected empty terms, got %v", terms)
	}
}

func TestTokenizeSearchTerms_Deduplication(t *testing.T) {
	terms := tokenizeSearchTerms("pasta pasta pasta recipe")
	count := 0
	for _, t := range terms {
		if t == "pasta" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'pasta' once, got %d", count)
	}
}

// ---------- scoreContextCandidate ----------

func TestScoreContextCandidate_ExactPhraseMatch(t *testing.T) {
	terms := tokenizeSearchTerms("pasta recipe")
	score := scoreContextCandidate("I found a pasta recipe yesterday", "pasta recipe", terms)
	if score < 6 {
		t.Errorf("expected score >= 6 for exact phrase match, got %d", score)
	}
}

func TestScoreContextCandidate_TokenMatch(t *testing.T) {
	terms := tokenizeSearchTerms("Paris travel guide")
	score := scoreContextCandidate("We discussed travel options and Paris hotels", "Paris travel guide", terms)
	// "paris" and "travel" should match (+3 each = 6), "guide" not found
	if score < 6 {
		t.Errorf("expected score >= 6, got %d", score)
	}
}

func TestScoreContextCandidate_NoMatch(t *testing.T) {
	terms := tokenizeSearchTerms("quantum physics")
	score := scoreContextCandidate("We talked about cooking and recipes", "quantum physics", terms)
	if score != 0 {
		t.Errorf("expected score 0, got %d", score)
	}
}

func TestScoreContextCandidate_EmptyInputs(t *testing.T) {
	if s := scoreContextCandidate("", "query", []string{"query"}); s != 0 {
		t.Errorf("empty candidate should score 0, got %d", s)
	}
	if s := scoreContextCandidate("text", "query", nil); s != 0 {
		t.Errorf("nil terms should score 0, got %d", s)
	}
}

func TestScoreContextCandidate_MultiOccurrenceBonus(t *testing.T) {
	terms := tokenizeSearchTerms("pasta")
	single := scoreContextCandidate("pasta is good", "pasta", terms)
	multi := scoreContextCandidate("pasta with pasta sauce and more pasta", "pasta", terms)
	if multi <= single {
		t.Errorf("multiple occurrences should score higher: single=%d, multi=%d", single, multi)
	}
}

// ---------- rawWindowBounds ----------

func TestRawWindowBounds(t *testing.T) {
	tests := []struct {
		anchor, length, radius int
		wantLo, wantHi         int
	}{
		{5, 10, 2, 3, 8},  // normal case
		{0, 10, 2, 0, 3},  // clamped at start
		{9, 10, 2, 7, 10}, // clamped at end
		{0, 1, 2, 0, 1},   // single element
		{3, 5, 0, 3, 4},   // zero radius
		{2, 5, 10, 0, 5},  // radius larger than array
	}
	for _, tt := range tests {
		lo, hi := rawWindowBounds(tt.anchor, tt.length, tt.radius)
		if lo != tt.wantLo || hi != tt.wantHi {
			t.Errorf("rawWindowBounds(%d, %d, %d) = (%d, %d), want (%d, %d)",
				tt.anchor, tt.length, tt.radius, lo, hi, tt.wantLo, tt.wantHi)
		}
	}
}

// ---------- collapseConsecutiveSameRole ----------

func TestCollapseConsecutiveSameRole_NoConsecutive(t *testing.T) {
	msgs := []ai.ChatMessage{
		msg("user", "hello"),
		msg("assistant", "hi"),
		msg("user", "bye"),
	}
	result := collapseConsecutiveSameRole(msgs)
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}
}

func TestCollapseConsecutiveSameRole_MergesAdjacent(t *testing.T) {
	msgs := []ai.ChatMessage{
		msg("assistant", "part one"),
		msg("assistant", "part two"),
		msg("user", "question"),
	}
	result := collapseConsecutiveSameRole(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if !strings.Contains(result[0].Content, "part one") || !strings.Contains(result[0].Content, "part two") {
		t.Error("expected merged content")
	}
	if result[0].Role != "assistant" {
		t.Error("expected role assistant")
	}
}

func TestCollapseConsecutiveSameRole_Empty(t *testing.T) {
	result := collapseConsecutiveSameRole(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestCollapseConsecutiveSameRole_SingleMessage(t *testing.T) {
	msgs := []ai.ChatMessage{msg("user", "hello")}
	result := collapseConsecutiveSameRole(msgs)
	if len(result) != 1 {
		t.Errorf("expected 1, got %d", len(result))
	}
}

// ---------- buildRunHistory ----------

func TestBuildRunHistory_NoHistory(t *testing.T) {
	result := buildRunHistory(nil, "hello", "gpt-4o")
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestBuildRunHistory_NoCompactions_FallsBackToTrim(t *testing.T) {
	history := []ai.ChatMessage{
		msg("user", "hello"),
		msg("assistant", "hi there"),
	}
	result := buildRunHistory(history, "hello again", "gpt-4o")
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	// Without compactions, should just return trimmed history.
	if len(result) > len(history) {
		t.Error("result should not be larger than input")
	}
}

func TestBuildRunHistory_WithCompactions_ExpandsRelevantBranch(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("Discussed Italian cooking: pasta, risotto, tiramisu recipes"),
		msg("user", "What about pizza dough?"),
		msg("assistant", "Pizza dough needs flour, water, yeast, and salt"),
		msg("user", "And the sauce?"),
		msg("assistant", "Tomato sauce: San Marzano tomatoes, garlic, basil"),
		compactionMsg("Discussed travel plans: flights to Tokyo, hotel bookings, itinerary"),
		msg("user", "What about the bullet train?"),
		msg("assistant", "The Shinkansen runs from Tokyo to Osaka in 2.5 hours"),
		msg("user", "How much does it cost?"),
		msg("assistant", "About 14,000 yen one way"),
	}

	// Query about cooking should prefer the cooking branch.
	result := buildRunHistory(history, "Tell me more about pasta recipes", "gpt-4o")
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// The result should contain cooking-related content.
	joined := ""
	for _, m := range result {
		joined += m.Content + " "
	}
	if !strings.Contains(strings.ToLower(joined), "pasta") && !strings.Contains(strings.ToLower(joined), "cooking") {
		t.Error("expected result to contain cooking branch content")
	}
}

func TestBuildRunHistory_WithCompactions_IncludesRecentTail(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("Discussed recipes for pasta and pizza"),
		msg("user", "What spices do you recommend?"),
		msg("assistant", "Oregano, basil, and thyme work well"),
		compactionMsg("Discussed travel plans to Japan"),
		msg("user", "What about trains?"),
		msg("assistant", "Shinkansen is the fastest option"),
	}

	// Query targets the first branch (recipes), but result should also
	// include the most recent branch tail for recency.
	result := buildRunHistory(history, "Tell me about pasta spices", "gpt-4o")
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Should contain both recipe content (matched branch) and
	// travel content (recent tail).
	joined := ""
	for _, m := range result {
		joined += m.Content + " "
	}
	joinedLower := strings.ToLower(joined)
	if !strings.Contains(joinedLower, "spice") && !strings.Contains(joinedLower, "oregano") {
		t.Error("expected matched branch content (spices/oregano)")
	}
	if !strings.Contains(joinedLower, "shinkansen") && !strings.Contains(joinedLower, "train") {
		t.Error("expected recent tail content (trains/shinkansen)")
	}
}

func TestBuildRunHistory_QueryMatchesLastBranch_NoDuplicate(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("Old chat about weather"),
		msg("user", "Is it going to rain?"),
		msg("assistant", "Yes, 80% chance of rain tomorrow"),
		compactionMsg("Recent chat about programming in Go"),
		msg("user", "How do goroutines work?"),
		msg("assistant", "Goroutines are lightweight threads managed by the Go runtime"),
	}

	result := buildRunHistory(history, "Tell me about goroutines", "gpt-4o")
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Since query matches the last branch, there should be no duplicate
	// tail appended.
	goroutineCount := 0
	for _, m := range result {
		goroutineCount += strings.Count(strings.ToLower(m.Content), "goroutine")
	}
	if goroutineCount > 2 {
		t.Errorf("goroutine content appears duplicated (%d occurrences)", goroutineCount)
	}
}

// ---------- buildSearchExpandedHistory ----------

func TestBuildSearchExpandedHistory_NoTerms(t *testing.T) {
	history := []ai.ChatMessage{
		compactionMsg("some summary"),
		msg("user", "hello"),
	}
	// Empty query should return nil (no usable search terms).
	result := buildSearchExpandedHistory(history, "", "gpt-4o")
	if result != nil {
		t.Errorf("expected nil for empty query, got %d messages", len(result))
	}
}
