package bot

import (
	"testing"
	"time"
)

func TestJobStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"pending", "⏳"},
		{"running", "🏃"},
		{"succeeded", "✅"},
		{"failed", "❌"},
		{"cancelled", "🛑"},
		{"timed_out", "⏰"},
		{"unknown", "🧾"},
	}
	for _, tc := range tests {
		if got := jobStatusIcon(tc.status); got != tc.want {
			t.Errorf("jobStatusIcon(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestParseJobTime(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"2024-01-15 09:30:00", true},
		{"2024-01-15T09:30:00Z", true},
		{"2024-01-15T09:30:00+00:00", true},
		{"not-a-time", false},
		{"", false},
	}
	for _, tc := range tests {
		got, err := parseJobTime(tc.input)
		if tc.valid && err != nil {
			t.Errorf("parseJobTime(%q) unexpected error: %v", tc.input, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("parseJobTime(%q) expected error, got %v", tc.input, got)
		}
		if tc.valid && got.IsZero() {
			t.Errorf("parseJobTime(%q) returned zero time", tc.input)
		}
	}
}

func TestParseJobTime_CorrectValue(t *testing.T) {
	got, err := parseJobTime("2024-03-15 14:30:00")
	if err != nil {
		t.Fatalf("parseJobTime error: %v", err)
	}
	want := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseJobTime = %v, want %v", got, want)
	}
}

func TestLoadBotRoles_NilPath(t *testing.T) {
	b := &Bot{}
	roles, err := b.loadBotRoles()
	if err != nil {
		t.Fatalf("loadBotRoles error: %v", err)
	}
	// Should still return bundled roles.
	if len(roles) == 0 {
		t.Error("expected at least bundled roles, got 0")
	}

	// Verify we can find a known bundled role.
	found := false
	for _, m := range roles {
		if m.Name == "researcher" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected bundled 'researcher' role")
	}
}

func TestFindBotRole_Found(t *testing.T) {
	b := &Bot{}
	m, err := b.findBotRole("researcher")
	if err != nil {
		t.Fatalf("findBotRole error: %v", err)
	}
	if m.Name != "researcher" {
		t.Errorf("expected name 'researcher', got %q", m.Name)
	}
}

func TestFindBotRole_NotFound(t *testing.T) {
	b := &Bot{}
	_, err := b.findBotRole("nonexistent-role-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
}
