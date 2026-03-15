package bot

import "testing"

func TestAbbreviateForAck(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{
			name:  "keeps short text intact",
			input: "investigate repo tests",
			limit: 40,
			want:  "investigate repo tests",
		},
		{
			name:  "compacts whitespace before truncation",
			input: "investigate   repo\n\nlogs now",
			limit: 32,
			want:  "investigate repo logs now",
		},
		{
			name:  "truncates with ascii ellipsis",
			input: "investigate the failing tests in the repository and prepare a fix",
			limit: 24,
			want:  "investigate the faili...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := abbreviateForAck(tc.input, tc.limit); got != tc.want {
				t.Fatalf("abbreviateForAck() = %q, want %q", got, tc.want)
			}
		})
	}
}
