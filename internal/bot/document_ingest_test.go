package bot

import (
	"strings"
	"testing"
)

func TestIsTextDocument(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, mime string
		want       bool
	}{
		{"CARDPUTERVOICERECORDER.md", "", true},
		{"notes.TXT", "application/octet-stream", true},
		{"data.bin", "text/plain", true},
		{"config", "application/json", true},
		{"photo.jpg", "image/jpeg", false},
		{"archive.zip", "application/zip", false},
	}
	for _, tc := range cases {
		if got := isTextDocument(tc.name, tc.mime); got != tc.want {
			t.Errorf("isTextDocument(%q, %q) = %v, want %v", tc.name, tc.mime, got, tc.want)
		}
	}
}

func TestSanitizeDocumentName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"CARDPUTERVOICERECORDER.md": "CARDPUTERVOICERECORDER.md",
		"../../etc/passwd":          "passwd",
		"dir\\evil:name.txt":        "evil_name.txt",
		"":                          "document",
		"...":                       "document",
	}
	for in, want := range cases {
		if got := sanitizeDocumentName(in); got != want {
			t.Errorf("sanitizeDocumentName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 200) + ".md"
	if got := sanitizeDocumentName(long); len(got) != 120 {
		t.Errorf("long name not truncated: len=%d", len(got))
	}
}

func TestDescribeSavedDocumentInlinesText(t *testing.T) {
	t.Parallel()
	saved := savedDocument{path: "/soul/inbox/20025-notes.md", size: 12, inline: "# Title\nbody\n"}
	got := describeSavedDocument("notes.md", "Tech review", saved)
	for _, want := range []string{
		"[Document: notes.md, 12 bytes] saved to /soul/inbox/20025-notes.md",
		"Tech review",
		"--- contents of notes.md ---",
		"# Title\nbody",
		"--- end of notes.md ---",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "read it with the file tool") {
		t.Errorf("inlined document should not ask the model to read the file:\n%s", got)
	}
}

func TestDescribeSavedDocumentPointsAtBinary(t *testing.T) {
	t.Parallel()
	saved := savedDocument{path: "/soul/inbox/7-report.pdf", size: 5000}
	got := describeSavedDocument("report.pdf", "", saved)
	if !strings.Contains(got, "read it with the file tool") {
		t.Errorf("binary document should point the model at the file tool:\n%s", got)
	}
	if strings.Contains(got, "--- contents") {
		t.Errorf("binary document must not be inlined:\n%s", got)
	}
}
