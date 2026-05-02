package videosummary

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWritesObsidianFilesAndLinks(t *testing.T) {
	vaultDir := filepath.Join(t.TempDir(), "Obsidian Vault")
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/transcribe":
			writeJSON(t, w, map[string]any{
				"id":     "scribe-job-123456",
				"title":  `We need/to talk: about OpenAI?`,
				"status": "queued",
			})
		case "/status/scribe-job-123456":
			writeJSON(t, w, map[string]any{
				"status": "completed",
				"title":  `We need/to talk: about OpenAI?`,
				"artifacts": map[string]string{
					"summary_markdown":    server.URL + "/artifacts/summary.md",
					"transcript_markdown": server.URL + "/artifacts/transcript.md",
				},
			})
		case "/artifacts/summary.md":
			_, _ = w.Write([]byte("> Transcript: [[_transcripts/we-need-to-talk-about-openai|Transcript]]\n\n# Summary"))
		case "/artifacts/transcript.md":
			_, _ = w.Write([]byte("# Transcript"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	submission, result, err := Run(t.Context(), "https://youtu.be/abc123", Config{
		ScribeURL:    server.URL,
		VaultDir:     vaultDir,
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
		HTTPClient:   server.Client(),
		Now:          func() time.Time { return now },
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if submission.JobID != "scribe-job-123456" {
		t.Fatalf("submission.JobID = %q", submission.JobID)
	}

	wantSummary := filepath.Join(vaultDir, "Digests", "2026-05-02", "We need to talk about OpenAI.md")
	wantTranscript := filepath.Join(vaultDir, "Digests", "2026-05-02", "_transcripts", "we-need-to-talk-about-openai.md")
	for _, path := range []string{wantSummary, wantTranscript} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s: %v", path, err)
		}
	}
	if result.SummaryPath != wantSummary {
		t.Fatalf("SummaryPath = %q, want %q", result.SummaryPath, wantSummary)
	}
	if want := "obsidian://open?vault=Obsidian%20Vault&file=Digests%2F2026-05-02%2FWe%20need%20to%20talk%20about%20OpenAI.md"; result.SummaryLink != want {
		t.Fatalf("SummaryLink = %q, want %q", result.SummaryLink, want)
	}
}

func TestValidateYouTubeURLRejectsOtherHosts(t *testing.T) {
	if err := ValidateYouTubeURL("https://example.com/watch?v=abc"); err == nil {
		t.Fatal("expected non-YouTube URL to fail")
	}
	if err := ValidateYouTubeURL("https://www.youtube.com/watch?v=abc"); err != nil {
		t.Fatalf("expected YouTube URL to pass: %v", err)
	}
}

func TestSanitizeTitleFallback(t *testing.T) {
	got := sanitizeTitle(`Bad / title: "ok"?`)
	if strings.ContainsAny(got, `\/:*?"<>|`) {
		t.Fatalf("sanitizeTitle left unsafe characters: %q", got)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}
