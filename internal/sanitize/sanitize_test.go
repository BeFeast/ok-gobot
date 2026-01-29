package sanitize

import (
	"testing"
)

func TestSanitizeShellArg(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "command injection attempt",
			input:    "test; rm -rf /",
			expected: "test\\; rm -rf /",
		},
		{
			name:     "backtick command substitution",
			input:    "test `whoami`",
			expected: "test \\\\`whoami\\\\`",
		},
		{
			name:     "dollar command substitution",
			input:    "test $(whoami)",
			expected: "test \\\\$\\(whoami\\)",
		},
		{
			name:     "pipe command",
			input:    "ls | grep test",
			expected: "ls \\| grep test",
		},
		{
			name:     "background process",
			input:    "sleep 10 &",
			expected: "sleep 10 \\&",
		},
		{
			name:     "redirect output",
			input:    "echo test > file.txt",
			expected: "echo test \\> file.txt",
		},
		{
			name:     "redirect input",
			input:    "cat < input.txt",
			expected: "cat \\< input.txt",
		},
		{
			name:     "newline injection",
			input:    "test\nmalicious",
			expected: "test\\\nmalicious",
		},
		{
			name:     "wildcard characters",
			input:    "rm *.txt",
			expected: "rm \\*.txt",
		},
		{
			name:     "home directory",
			input:    "cd ~/test",
			expected: "cd \\~/test",
		},
		{
			name:     "comment injection",
			input:    "test # comment",
			expected: "test \\# comment",
		},
		{
			name:     "variable expansion",
			input:    "echo $PATH",
			expected: "echo \\\\$PATH",
		},
		{
			name:     "double quotes",
			input:    "echo \"test\"",
			expected: "echo \\\\\"test\\\\\"",
		},
		{
			name:     "backslash escape",
			input:    "test\\nvalue",
			expected: "test\\\\nvalue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeShellArg(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeShellArg(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeTelegramMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "underscore (italic)",
			input:    "hello_world",
			expected: "hello\\_world",
		},
		{
			name:     "asterisk (bold)",
			input:    "hello*world",
			expected: "hello\\*world",
		},
		{
			name:     "square brackets (link)",
			input:    "[link text](url)",
			expected: "\\[link text\\]\\(url\\)",
		},
		{
			name:     "backtick (code)",
			input:    "`code here`",
			expected: "\\`code here\\`",
		},
		{
			name:     "tilde (strikethrough)",
			input:    "~strikethrough~",
			expected: "\\~strikethrough\\~",
		},
		{
			name:     "greater than (quote)",
			input:    "> quoted text",
			expected: "\\> quoted text",
		},
		{
			name:     "hash (header)",
			input:    "# Header",
			expected: "\\# Header",
		},
		{
			name:     "plus sign",
			input:    "1 + 1 = 2",
			expected: "1 \\+ 1 \\= 2",
		},
		{
			name:     "minus/hyphen",
			input:    "item - description",
			expected: "item \\- description",
		},
		{
			name:     "equals sign",
			input:    "x = 5",
			expected: "x \\= 5",
		},
		{
			name:     "pipe (table)",
			input:    "col1 | col2",
			expected: "col1 \\| col2",
		},
		{
			name:     "curly braces",
			input:    "{json: value}",
			expected: "\\{json: value\\}",
		},
		{
			name:     "period/dot",
			input:    "end of sentence.",
			expected: "end of sentence\\.",
		},
		{
			name:     "exclamation mark",
			input:    "Hello!",
			expected: "Hello\\!",
		},
		{
			name:     "mixed markdown",
			input:    "*bold* _italic_ `code`",
			expected: "\\*bold\\* \\_italic\\_ \\`code\\`",
		},
		{
			name:     "all special chars",
			input:    "_*[]()~`>#+-=|{}.!",
			expected: "\\_\\*\\[\\]\\(\\)\\~\\`\\>\\#\\+\\-\\=\\|\\{\\}\\.\\!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeTelegramMarkdown(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeTelegramMarkdown(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestStripControlChars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "with newline",
			input:    "line1\nline2",
			expected: "line1\nline2",
		},
		{
			name:     "with tab",
			input:    "col1\tcol2",
			expected: "col1\tcol2",
		},
		{
			name:     "with carriage return",
			input:    "line1\r\nline2",
			expected: "line1\r\nline2",
		},
		{
			name:     "with null byte",
			input:    "test\x00value",
			expected: "testvalue",
		},
		{
			name:     "with bell character",
			input:    "test\x07value",
			expected: "testvalue",
		},
		{
			name:     "with backspace",
			input:    "test\x08value",
			expected: "testvalue",
		},
		{
			name:     "with escape sequence",
			input:    "test\x1b[31mred\x1b[0m",
			expected: "test[31mred[0m",
		},
		{
			name:     "with vertical tab",
			input:    "test\x0bvalue",
			expected: "testvalue",
		},
		{
			name:     "with form feed",
			input:    "test\x0cvalue",
			expected: "testvalue",
		},
		{
			name:     "with delete character",
			input:    "test\x7fvalue",
			expected: "testvalue",
		},
		{
			name:     "mixed control chars",
			input:    "test\x00\x01\x02\x03value\x1b[0m",
			expected: "testvalue[0m",
		},
		{
			name:     "unicode with control",
			input:    "Привет\x00мир",
			expected: "Приветмир",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripControlChars(tt.input)
			if result != tt.expected {
				t.Errorf("StripControlChars(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "markdown with control chars",
			input:    "*bold*\x00text\x1b[31m",
			expected: "\\*bold\\*text\\[31m",
		},
		{
			name:     "complex mixed input",
			input:    "Hello_world!\nNew\x00line\x1b[0m",
			expected: "Hello\\_world\\!\nNewline\\[0m",
		},
		{
			name:     "user input with injection attempt",
			input:    "normal text\x00\x1b]0;malicious\x07*bold*",
			expected: "normal text\\]0;malicious\\*bold\\*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeForDisplay(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeForDisplay(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// Benchmark tests
func BenchmarkSanitizeShellArg(b *testing.B) {
	input := "test; rm -rf / & echo $PATH | grep bin"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SanitizeShellArg(input)
	}
}

func BenchmarkSanitizeTelegramMarkdown(b *testing.B) {
	input := "*bold* _italic_ `code` [link](url) ~strike~ > quote # header + - = | {}"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SanitizeTelegramMarkdown(input)
	}
}

func BenchmarkStripControlChars(b *testing.B) {
	input := "test\x00\x01\x02\x03value\x1b[31mcolored\x1b[0m\nline2"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripControlChars(input)
	}
}

func BenchmarkSanitizeForDisplay(b *testing.B) {
	input := "*bold*\x00text\x1b[31mcolored\x1b[0m_italic_"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SanitizeForDisplay(input)
	}
}
