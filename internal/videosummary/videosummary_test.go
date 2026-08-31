package videosummary

import (
	"context"
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
		if got := r.Header.Get("Authorization"); got != "Bearer scribe-test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/jobs":
			if r.Method != http.MethodPost {
				t.Fatalf("submit method = %s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode submit body: %v", err)
			}
			if body["url"] != "https://youtu.be/abc123" || body["source"] != "ok-gobot" || body["summarize"] != true || body["notify"] != false || body["summary_prompt"] != "Detailed summary" {
				t.Fatalf("submit body = %#v", body)
			}
			writeJSON(t, w, map[string]any{
				"job_id": 123456,
				"title":  `We need/to talk: about OpenAI?`,
				"status": "queued",
			})
		case "/jobs/123456":
			writeJSON(t, w, map[string]any{
				"status": "completed",
				"title":  `We need/to talk: about OpenAI?`,
				"transcript": map[string]any{
					"id":    77,
					"title": `We need/to talk: about OpenAI?`,
				},
			})
		case "/transcripts/77/summary.md":
			_, _ = w.Write([]byte("> Transcript: [[_transcripts/we-need-to-talk-about-openai|Transcript]]\n\n# Summary"))
		case "/transcripts/77/transcript.md":
			_, _ = w.Write([]byte("# Transcript"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	submission, result, err := Run(t.Context(), "https://youtu.be/abc123", Config{
		ScribeURL:     server.URL,
		APIToken:      "scribe-test-token",
		SummaryPrompt: "Detailed summary",
		VaultDir:      vaultDir,
		PollInterval:  time.Millisecond,
		Timeout:       time.Second,
		HTTPClient:    server.Client(),
		Now:           func() time.Time { return now },
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if submission.JobID != "123456" {
		t.Fatalf("submission.JobID = %q", submission.JobID)
	}
	if want := server.URL + "/#/jobs/123456"; submission.QueueLink != want {
		t.Fatalf("submission.QueueLink = %q, want %q", submission.QueueLink, want)
	}

	wantSummary := filepath.Join(vaultDir, "_Assets", "Daily Notes", "2026", "05", "02", "We need to talk about OpenAI.md")
	wantTranscript := filepath.Join(vaultDir, "_Assets", "Daily Notes", "2026", "05", "02", "_transcripts", "we-need-to-talk-about-openai.md")
	for _, path := range []string{wantSummary, wantTranscript} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s: %v", path, err)
		}
	}
	if result.SummaryPath != wantSummary {
		t.Fatalf("SummaryPath = %q, want %q", result.SummaryPath, wantSummary)
	}
	if want := "obsidian://open?vault=Obsidian%20Vault&file=_Assets%2FDaily%20Notes%2F2026%2F05%2F02%2FWe%20need%20to%20talk%20about%20OpenAI.md"; result.SummaryLink != want {
		t.Fatalf("SummaryLink = %q, want %q", result.SummaryLink, want)
	}
	if want := server.URL + "/#/transcript/77"; result.ScribeLink != want {
		t.Fatalf("ScribeLink = %q, want %q", result.ScribeLink, want)
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

func TestTranscriptSlugFromSummarySanitizesTraversalAndCapsLength(t *testing.T) {
	malicious := "> Transcript: [[_transcripts/../../../../../outside\\" + strings.Repeat("x", 200) + "|Transcript]]"
	got := transcriptSlugFromSummary(malicious, "fallback title")
	if strings.ContainsAny(got, `\/.`) {
		t.Fatalf("transcript slug contains path characters: %q", got)
	}
	if len([]rune(got)) > maxSlugLength {
		t.Fatalf("transcript slug length = %d, want <= %d", len([]rune(got)), maxSlugLength)
	}
}

func TestUniquePathSanitizesServerJobIDSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.md")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write collision fixture: %v", err)
	}

	got := uniquePath(path, `../../escape\\bad:*?`+strings.Repeat("x", 100))
	assertPathWithin(t, dir, got)
	base := filepath.Base(got)
	if strings.ContainsAny(base, `\/:*?"<>|`) || strings.Contains(base, "..") {
		t.Fatalf("unique path contains unsafe job id characters: %q", base)
	}
	if suffix := strings.TrimSuffix(strings.TrimPrefix(base, "summary - "), ".md"); len([]rune(suffix)) > maxJobSuffixLength {
		t.Fatalf("job suffix length = %d, want <= %d", len([]rune(suffix)), maxJobSuffixLength)
	}
}

func TestWriteResultKeepsMaliciousNamesInsideDailyNotes(t *testing.T) {
	vaultDir := filepath.Join(t.TempDir(), "vault")
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/transcripts/91/summary.md":
			_, _ = w.Write([]byte("> Transcript: [[_transcripts/../../../../../outside\\escaped|Transcript]]\n\n# Summary"))
		case "/transcripts/91/transcript.md":
			_, _ = w.Write([]byte("# Transcript"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		ScribeURL:  server.URL,
		VaultDir:   vaultDir,
		HTTPClient: server.Client(),
		Now:        func() time.Time { return now },
	}
	submission := Submission{
		JobID:       `../../server\\job:*?` + strings.Repeat("z", 100),
		SubmittedAt: now,
	}
	status := scribeStatus{
		Status: "completed",
		Title:  `../../` + strings.Repeat("Very long title ", 20) + `\\escaped:*?`,
		Transcript: &scribeTranscript{
			ID: 91,
		},
	}

	first, err := writeResult(t.Context(), cfg, submission, status)
	if err != nil {
		t.Fatalf("first writeResult() error = %v", err)
	}
	second, err := writeResult(t.Context(), cfg, submission, status)
	if err != nil {
		t.Fatalf("collision writeResult() error = %v", err)
	}

	digestsDir := filepath.Join(vaultDir, "_Assets", "Daily Notes")
	for _, path := range []string{first.SummaryPath, first.TranscriptPath, second.SummaryPath, second.TranscriptPath} {
		assertPathWithin(t, digestsDir, path)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected output %q: %v", path, err)
		}
	}
	if first.SummaryPath == second.SummaryPath || first.TranscriptPath == second.TranscriptPath {
		t.Fatal("collision output paths were not made unique")
	}
	summaryBytes, err := os.ReadFile(first.SummaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if strings.Contains(string(summaryBytes), "_transcripts/../") {
		t.Fatalf("summary retained traversal backlink: %q", summaryBytes)
	}
	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "_Assets" {
		t.Fatalf("unexpected output at vault root: %#v", entries)
	}
}

func assertPathWithin(t *testing.T, root, path string) {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relative path from %q to %q: %v", root, path, err)
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path %q escapes root %q (relative %q)", path, root, rel)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}

func TestValidateIngestURLMirrorsScribeContract(t *testing.T) {
	// Scribe accepts any syntactically valid http(s) URL with a host and lets
	// its extractor decide. Anything stricter here rejects sources the service
	// can actually handle — the defect this test exists to prevent.
	accepted := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://youtu.be/abc",
		"https://x.com/someone/status/1234567890",
		"https://vimeo.com/123456",
		"http://example.com/clip.mp4",
		"  https://example.com/clip.mp4  ",
	}
	for _, raw := range accepted {
		if err := ValidateIngestURL(raw); err != nil {
			t.Errorf("ValidateIngestURL(%q) = %v, want nil", raw, err)
		}
	}

	rejected := []string{
		"",
		"   ",
		"not a url",
		"file:///etc/passwd",
		"ftp://example.com/clip.mp4",
		"javascript:alert(1)",
		"https://",
		"/relative/path.mp4",
	}
	for _, raw := range rejected {
		if err := ValidateIngestURL(raw); err == nil {
			t.Errorf("ValidateIngestURL(%q) = nil, want an error", raw)
		}
	}
}

func TestPreflightReportsScribeVerdict(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("url")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"supported":true,"extractor":"twitter"}`))
	}))
	defer server.Close()

	verdict, err := Preflight(context.Background(), "https://x.com/someone/status/1", Config{ScribeURL: server.URL})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !verdict.Supported || verdict.Extractor != "twitter" {
		t.Fatalf("verdict = %+v, want supported twitter", verdict)
	}
	if gotQuery != "https://x.com/someone/status/1" {
		t.Fatalf("preflight query = %q", gotQuery)
	}
}

func TestRunSurfacesUnsupportedSourceOnSubmitFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"unsupported"}`))
		case "/preflight":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"supported":false,"reason":"no extractor"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	_, _, err := Run(context.Background(), "https://example.com/not-media", Config{ScribeURL: server.URL}, nil)
	if err == nil {
		t.Fatal("expected Run to fail")
	}
	if !strings.Contains(err.Error(), "no extractor") {
		t.Fatalf("error should carry the scribe verdict, got: %v", err)
	}
}

func TestRunKeepsSubmitErrorWhenPreflightIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/preflight" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, _, err := Run(context.Background(), "https://example.com/clip.mp4", Config{ScribeURL: server.URL}, nil)
	if err == nil {
		t.Fatal("expected Run to fail")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("original submit error must survive a failed preflight, got: %v", err)
	}
	if strings.Contains(err.Error(), "extractor") {
		t.Fatalf("must not invent a verdict when preflight failed, got: %v", err)
	}
}
