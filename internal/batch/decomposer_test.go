package batch

import (
	"testing"
)

func TestParseSubtasks(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{
			name: "plain JSON array",
			input: `[
				{"title": "Update imports", "description": "Replace v1 imports with v2"},
				{"title": "Run tests", "description": "Execute test suite after changes"}
			]`,
			wantLen: 2,
		},
		{
			name:    "JSON inside markdown fences",
			input:   "```json\n[\n{\"title\":\"Task 1\",\"description\":\"Do something\"}\n]\n```",
			wantLen: 1,
		},
		{
			name:    "empty JSON array",
			input:   "[]",
			wantLen: 0,
		},
		{
			name:    "no JSON array",
			input:   "Here are some subtasks but no JSON.",
			wantErr: true,
		},
		{
			name: "filters empty entries",
			input: `[
				{"title": "", "description": "missing title"},
				{"title": "Valid task", "description": "has both fields"},
				{"title": "No description", "description": ""}
			]`,
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSubtasks(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("got %d subtasks, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestSlugifyExported(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"add unit tests for all functions", "add-unit-tests-for-all-functions"},
		{"  spaces  ", "spaces"},
		{"special!@#chars", "special-chars"},
	}
	for _, tt := range tests {
		got := SlugifyExported(tt.input)
		if got != tt.want {
			t.Errorf("SlugifyExported(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSlugifyMaxLength(t *testing.T) {
	long := "this-is-a-very-long-task-description-that-exceeds-forty-characters"
	got := SlugifyExported(long)
	if len(got) > 40 {
		t.Errorf("slug length %d > 40: %q", len(got), got)
	}
}
