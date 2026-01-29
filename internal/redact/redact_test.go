package redact

import (
	"testing"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "OpenAI API key",
			input:    "Using API key sk-1234567890abcdefghijklmnop",
			expected: "Using API key sk-123456***",
		},
		{
			name:     "OpenRouter API key",
			input:    "Config: sk-or-v1-abc123def456ghi789jkl012mno345pqr678stu901vwx234yz",
			expected: "Config: sk-or-v1-***",
		},
		{
			name:     "Generic key- prefix",
			input:    "Authentication with key-abc123def456ghi789jkl",
			expected: "Authentication with key-abc123***",
		},
		{
			name:     "Bearer token",
			input:    "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			expected: "Authorization: Bearer ***",
		},
		{
			name:     "Telegram bot token",
			input:    "Bot token: 123456789:ABCdefGHIjklMNOpqrsTUVwxyz",
			expected: "Bot token: 123456***",
		},
		{
			name:     "Long hex string (secret)",
			input:    "Secret: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			expected: "Secret: a1b2c3***",
		},
		{
			name:     "Base64 secret with padding",
			input:    "Token: SGVsbG8vV29ybGQrVGhpc0lzQUxvbmdCYXNlNjRTdHJpbmdXaXRoU3BlY2lhbENoYXJzKzEyMzQ1Njc4OTA=",
			expected: "Token: SGVsbG***",
		},
		{
			name:     "Base64-like secret without special chars (not redacted)",
			input:    "Token: dGhpc2lzYXZlcnlsb25nc2VjcmV0dG9rZW50aGF0c2hvdWxkYmVyZWRhY3RlZA==",
			expected: "Token: dGhpc2***",
		},
		{
			name:     "Multiple secrets in one string",
			input:    "API: sk-test123456789012345 and bot: 987654321:ABCDEFGHIJabcdefghij",
			expected: "API: sk-test12*** and bot: 987654***",
		},
		{
			name:     "No secrets",
			input:    "This is a normal log message with no secrets",
			expected: "This is a normal log message with no secrets",
		},
		{
			name:     "UUID should not be redacted",
			input:    "Request ID: 550e8400-e29b-41d4-a716-446655440000",
			expected: "Request ID: 550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:     "Short hex strings should not be redacted",
			input:    "Color: #ff5733 and hash: abc123",
			expected: "Color: #ff5733 and hash: abc123",
		},
		{
			name:     "Normal words should not be redacted",
			input:    "The quick brown fox jumps over the lazy dog multiple times",
			expected: "The quick brown fox jumps over the lazy dog multiple times",
		},
		{
			name:     "Mixed content",
			input:    "Connecting to API with sk-proj123456789abcdef and Bearer SomeVeryLongTokenHere123",
			expected: "Connecting to API with sk-proj12*** and Bearer ***",
		},
		{
			name:     "Real-world log message",
			input:    "2024-01-29 10:15:30 INFO [bot] Authenticating with token 1234567890:ABCdef-123456789012345678901234567890",
			expected: "2024-01-29 10:15:30 INFO [bot] Authenticating with token 123456***",
		},
		{
			name:     "OpenAI key in JSON",
			input:    `{"api_key":"sk-1234567890abcdefghijklmnopqrstuvwxyz"}`,
			expected: `{"api_key":"sk-123456***"}`,
		},
		{
			name:     "Multiple API keys",
			input:    "Primary: sk-abc123def456789012 Secondary: sk-or-v1-xyz789abc123def456",
			expected: "Primary: sk-abc123*** Secondary: sk-or-v1-***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Redact(tt.input)
			if result != tt.expected {
				t.Errorf("Redact() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestRedactEmptyString(t *testing.T) {
	result := Redact("")
	if result != "" {
		t.Errorf("Redact(\"\") = %q, want \"\"", result)
	}
}

func TestRedactNoModification(t *testing.T) {
	inputs := []string{
		"Hello, World!",
		"Error: connection timeout",
		"Processing request #12345",
		"User logged in at 2024-01-29",
		"Database query took 150ms",
	}

	for _, input := range inputs {
		result := Redact(input)
		if result != input {
			t.Errorf("Redact(%q) modified string to %q, expected no change", input, result)
		}
	}
}

func BenchmarkRedact(b *testing.B) {
	testString := "Authenticating with sk-test123456789012345678901234567890 and bot token 123456789:ABCdefGHIjklMNOpqrsTUVwxyz"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Redact(testString)
	}
}

func BenchmarkRedactNoSecrets(b *testing.B) {
	testString := "This is a normal log message with no secrets at all, just regular text"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Redact(testString)
	}
}
