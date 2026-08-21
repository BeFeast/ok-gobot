package redact

import (
	"strings"
	"testing"
)

func TestAssignmentsMasksInlineSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		leak string // substring that must NOT survive
	}{
		{"flag equals", "curl -H --password=hunter2xyz http://x", "hunter2xyz"},
		{"env token", "TOKEN=ghp_abcdefg1234567 deploy.sh", "ghp_abcdefg1234567"},
		{"colon secret", "client_secret: s3cr3t-value-here", "s3cr3t-value-here"},
		{"api key space", "api_key abcdef0123456789", "abcdef0123456789"},
		{"bearer", "Authorization bearer zzzsecretzzz", "zzzsecretzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assignments(tc.in)
			if strings.Contains(got, tc.leak) {
				t.Errorf("secret survived redaction: input=%q output=%q", tc.in, got)
			}
			if !strings.Contains(got, "***") {
				t.Errorf("expected a *** mask, got: %q", got)
			}
		})
	}
}

func TestAssignmentsKeepsBenignWords(t *testing.T) {
	in := "keyword the description mentions a token holder"
	// "keyword" must not be treated as a "key" assignment.
	got := Assignments(in)
	if !strings.Contains(got, "keyword") {
		t.Errorf("benign word 'keyword' was mangled: %q", got)
	}
}
