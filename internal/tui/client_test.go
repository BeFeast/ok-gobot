package tui

import (
	"strings"
	"testing"
	"time"
)

func TestDialWS_TimeoutOnUnreachable(t *testing.T) {
	// Dial a port where nothing is listening — should fail within ~5s.
	start := time.Now()
	_, err := dialWS("127.0.0.1:19999")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when dialling unreachable server, got nil")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("dialWS took %v; expected ≤ 5s timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "could not connect to ok-gobot server") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if !strings.Contains(err.Error(), "is it running?") {
		t.Fatalf("error message missing user-friendly hint: %v", err)
	}
}
