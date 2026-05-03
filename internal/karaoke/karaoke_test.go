package karaoke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubmitAndWaitCompletesWithVerifiedLinks(t *testing.T) {
	t.Parallel()

	var submitted bool
	var statusCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/jobs/youtube":
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload["url"] != "https://youtu.be/example" || payload["profile"] != "karaoke" || payload["lyrics_mode"] != "plain" || payload["keep_full_stems"] != true {
				t.Fatalf("unexpected payload: %#v", payload)
			}
			submitted = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"job_id":"job-123","title":"Example Song","status":"queued","stage":"download","share":{"page_url":"` + serverURL(r) + `/share/job-123"}}`))
		case "/status/job-123":
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			statusCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"job_id":"job-123","title":"Example Song","status":"completed","stage":"done","share":{"page_url":"` + serverURL(r) + `/share/job-123","karaoke_mp3_url":"` + serverURL(r) + `/media/karaoke.mp3","vocals_mp3_url":"` + serverURL(r) + `/media/vocals.mp3","lyrics_txt_url":"` + serverURL(r) + `/media/lyrics.txt"}}`))
		case "/share/job-123":
			_, _ = w.Write([]byte("share"))
		case "/media/karaoke.mp3", "/media/vocals.mp3":
			if got := r.Header.Get("Range"); got != "bytes=0-0" {
				t.Fatalf("Range = %q", got)
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
		case "/media/lyrics.txt":
			_, _ = w.Write([]byte("lyrics"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		ServiceURL:   server.URL,
		Token:        "test-token",
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
		Now:          func() time.Time { return time.Unix(100, 0) },
	}
	submission, err := Submit(context.Background(), "https://youtu.be/example", cfg)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if !submitted {
		t.Fatal("expected submit request")
	}
	if submission.JobID != "job-123" {
		t.Fatalf("JobID = %q", submission.JobID)
	}

	result, err := Wait(context.Background(), submission, cfg)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if statusCalls != 1 {
		t.Fatalf("statusCalls = %d", statusCalls)
	}
	if result.Title != "Example Song" || !strings.HasSuffix(result.KaraokeMP3URL, "/media/karaoke.mp3") || !strings.HasSuffix(result.LyricsTextURL, "/media/lyrics.txt") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSubmitRequiresToken(t *testing.T) {
	t.Parallel()

	_, err := Submit(context.Background(), "https://www.youtube.com/watch?v=abc", Config{ServiceURL: "http://127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "token is not configured") {
		t.Fatalf("Submit() error = %v", err)
	}
}

func TestWaitWithProgressReportsStatusStageChangesOnlyBeforeTerminal(t *testing.T) {
	t.Parallel()

	statuses := []string{"queued", "running", "completed"}
	stages := []string{"waiting", "separating", "done"}
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/job-123":
			idx := statusCalls
			if idx >= len(statuses) {
				idx = len(statuses) - 1
			}
			statusCalls++
			writeKaraokeStatus(t, w, r, statuses[idx], stages[idx])
		case "/share/job-123":
			_, _ = w.Write([]byte("share"))
		case "/media/karaoke.mp3", "/media/vocals.mp3":
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
		case "/media/lyrics.txt":
			_, _ = w.Write([]byte("lyrics"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var got []Progress
	_, err := WaitWithProgress(t.Context(), Submission{
		JobID:     "job-123",
		Title:     "Example Song",
		StatusURL: server.URL + "/status/job-123",
	}, Config{
		ServiceURL:   server.URL,
		Token:        "test-token",
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
		HTTPClient:   server.Client(),
	}, func(progress Progress) {
		got = append(got, progress)
	})
	if err != nil {
		t.Fatalf("WaitWithProgress() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("progress count = %d, want 2: %#v", len(got), got)
	}
	if got[0].Status != "queued" || got[0].Stage != "waiting" || got[1].Status != "running" || got[1].Stage != "separating" {
		t.Fatalf("unexpected progress: %#v", got)
	}
}

func TestValidateYouTubeURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
		"https://www.youtube.com/live/dQw4w9WgXcQ",
	}
	for _, raw := range valid {
		if err := ValidateYouTubeURL(raw); err != nil {
			t.Fatalf("ValidateYouTubeURL(%q) error = %v", raw, err)
		}
	}

	invalid := []string{
		"https://example.com/watch?v=abc",
		"https://youtu.be/",
		"https://youtu.be",
		"https://www.youtube.com/",
		"https://www.youtube.com/watch",
		"https://www.youtube.com/watch?v=",
		"https://www.youtube.com/shorts/",
	}
	for _, raw := range invalid {
		if err := ValidateYouTubeURL(raw); err == nil {
			t.Fatalf("ValidateYouTubeURL(%q) expected error", raw)
		}
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func writeKaraokeStatus(t *testing.T, w http.ResponseWriter, r *http.Request, status, stage string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"job_id":"job-123","title":"Example Song","status":"` + status + `","stage":"` + stage + `","share":{"page_url":"` + serverURL(r) + `/share/job-123","karaoke_mp3_url":"` + serverURL(r) + `/media/karaoke.mp3","vocals_mp3_url":"` + serverURL(r) + `/media/vocals.mp3","lyrics_txt_url":"` + serverURL(r) + `/media/lyrics.txt"}}`))
}
