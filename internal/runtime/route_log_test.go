package runtime

import "testing"

func TestRouteLogRecordAndRecent(t *testing.T) {
	rl := NewRouteLog(5)

	for i := 0; i < 7; i++ {
		rl.Record("session:test", "input", ChatRouteDecision{
			Action: ChatActionReply,
			Reason: "default_reply",
		})
	}

	// Should only keep 5 (max size).
	records := rl.Recent(10)
	if len(records) != 5 {
		t.Fatalf("expected 5 records, got %d", len(records))
	}
}

func TestRouteLogRecentLimitRespected(t *testing.T) {
	rl := NewRouteLog(100)
	for i := 0; i < 10; i++ {
		rl.Record("s", "input", ChatRouteDecision{Action: ChatActionReply, Reason: "r"})
	}

	records := rl.Recent(3)
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
}

func TestRouteLogNewestFirst(t *testing.T) {
	rl := NewRouteLog(10)

	rl.Record("s", "first", ChatRouteDecision{Action: ChatActionReply, Reason: "a"})
	rl.Record("s", "second", ChatRouteDecision{Action: ChatActionLaunchJob, Reason: "b"})

	records := rl.Recent(2)
	if records[0].Input != "second" {
		t.Errorf("expected newest first, got %q", records[0].Input)
	}
	if records[1].Input != "first" {
		t.Errorf("expected oldest second, got %q", records[1].Input)
	}
}

func TestRouteLogTruncatesInput(t *testing.T) {
	rl := NewRouteLog(5)
	longInput := make([]byte, 1000)
	for i := range longInput {
		longInput[i] = 'a'
	}
	rl.Record("s", string(longInput), ChatRouteDecision{Action: ChatActionReply, Reason: "r"})

	records := rl.Recent(1)
	if len(records[0].Input) > 512 {
		t.Errorf("expected truncated input, got length %d", len(records[0].Input))
	}
}
