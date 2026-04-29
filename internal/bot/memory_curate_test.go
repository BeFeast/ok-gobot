package bot

import (
	"strings"
	"testing"
)

func TestParseDaysArg(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"7", 7, false},
		{"7d", 7, false},
		{"  14d ", 14, false},
		{"0", 1, false},    // clamped up to minimum 1
		{"500", 90, false}, // clamped down to max 90
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseDaysArg(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDaysArg(%q): expected error, got %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDaysArg(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDaysArg(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestMemoryCurateUsage_DescribesAllSubcommands(t *testing.T) {
	usage := memoryCurateUsage()
	for _, want := range []string{"draft", "range", "list", "show", "apply", "reject", "delete"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing subcommand %q:\n%s", want, usage)
		}
	}
	// The literal `yes` confirmation token must be documented so admins know
	// it is required.
	if !strings.Contains(usage, "yes") {
		t.Error("usage should mention the explicit `yes` confirmation token")
	}
}
