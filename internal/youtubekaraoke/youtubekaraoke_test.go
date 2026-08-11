package youtubekaraoke

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

func TestRunUsesCurrentKaraokeAPIAndDownloadsArtifacts(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/jobs":
			if got := r.Header.Get("Authorization"); got != "Bearer karaoke-test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode submit: %v", err)
			}
			if body["url"] != "https://youtu.be/abc123" {
				t.Fatalf("submit body = %#v", body)
			}
			writeJSON(t, w, map[string]any{
				"id": 42, "job_token": "unlisted-token", "source_url": body["url"],
				"status": "queued", "progress": 0, "share_url": "https://karaoke.example/share/unlisted-token",
				"artifacts": []any{},
			})
		case "/jobs/42/status":
			if got := r.Header.Get("Authorization"); got != "Bearer karaoke-test-token" {
				t.Fatalf("status Authorization = %q", got)
			}
			writeJSON(t, w, map[string]any{
				"id": 42, "job_token": "unlisted-token", "source_url": "https://youtu.be/abc123",
				"title": `My / Karaoke: Song?`, "status": "completed", "progress": 100,
				"share_url": "https://karaoke.example/share/unlisted-token",
				"artifacts": []map[string]any{
					{"kind": "karaoke", "name": "karaoke.mp3"},
					{"kind": "vocals", "name": "vocals.mp3"},
					{"kind": "lyrics_lrc", "name": "lyrics.lrc"},
				},
			})
		case "/share/unlisted-token/karaoke.mp3":
			_, _ = w.Write([]byte("instrumental"))
		case "/share/unlisted-token/vocals.mp3":
			_, _ = w.Write([]byte("vocals"))
		case "/share/unlisted-token/lyrics.lrc":
			_, _ = w.Write([]byte("[00:01.00]Hello"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var progress []string
	result, err := Run(t.Context(), "https://youtu.be/abc123", Config{
		BaseURL:      server.URL,
		APIToken:     "karaoke-test-token",
		OutputDir:    filepath.Join(t.TempDir(), "karaoke"),
		PollInterval: time.Millisecond,
		Timeout:      time.Second,
		HTTPClient:   server.Client(),
		Now:          func() time.Time { return time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC) },
	}, func(message string) { progress = append(progress, message) })
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.JobID != "42" || result.JobToken != "unlisted-token" {
		t.Fatalf("unexpected job identity: %+v", result)
	}
	if result.Title != "My Karaoke Song" {
		t.Fatalf("Title = %q", result.Title)
	}
	if result.ShareURL != "https://karaoke.example/share/unlisted-token" {
		t.Fatalf("ShareURL = %q", result.ShareURL)
	}
	for path, want := range map[string]string{
		result.KaraokePath:   "instrumental",
		result.VocalsPath:    "vocals",
		result.LyricsLRCPath: "[00:01.00]Hello",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q", path, data)
		}
	}
	if result.PrimaryArtifactPath() != result.KaraokePath {
		t.Fatalf("PrimaryArtifactPath = %q", result.PrimaryArtifactPath())
	}
	if !strings.Contains(strings.Join(progress, "\n"), "karaoke status: completed") {
		t.Fatalf("progress = %#v", progress)
	}
	if len(requests) != 5 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestRunSurfacesTerminalFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jobs" {
			writeJSON(t, w, map[string]any{"id": 7, "job_token": "token", "status": "queued", "artifacts": []any{}})
			return
		}
		writeJSON(t, w, map[string]any{
			"id": 7, "job_token": "token", "status": "failed", "error": "gpu capacity unavailable", "artifacts": []any{},
		})
	}))
	defer server.Close()

	_, err := Run(t.Context(), "https://youtu.be/abc123", Config{
		BaseURL: server.URL, OutputDir: t.TempDir(), PollInterval: time.Millisecond,
		Timeout: time.Second, HTTPClient: server.Client(),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "gpu capacity unavailable") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestValidateYouTubeURLRejectsNonYouTube(t *testing.T) {
	if err := ValidateYouTubeURL("https://example.com/watch?v=abc"); err == nil {
		t.Fatal("expected non-YouTube URL to fail")
	}
	if err := ValidateYouTubeURL("https://www.youtube.com/watch?v=abc"); err != nil {
		t.Fatalf("expected YouTube URL to pass: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}
