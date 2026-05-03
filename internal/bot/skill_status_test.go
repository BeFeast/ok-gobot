package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/storage"
)

func TestFormatNativeSkillJobListFiltersNativeKinds(t *testing.T) {
	out := formatNativeSkillJobList([]storage.Job{
		{JobID: "job-role", Kind: "role", Status: "succeeded", Description: "role:researcher"},
		{JobID: "job-vs", Kind: videoSummaryKind, Status: "succeeded", Description: "video summary: https://youtu.be/a"},
		{JobID: "job-k", Kind: karaokeKind, Status: "running", Description: "karaoke: https://youtu.be/b"},
	}, 10)

	for _, want := range []string{"job-vs", "video-summary", "job-k", "karaoke", "/skill_status <job_id>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "job-role") {
		t.Fatalf("generic role job leaked into native skill list: %q", out)
	}
}

func TestFormatNativeSkillJobListEmpty(t *testing.T) {
	out := formatNativeSkillJobList([]storage.Job{{JobID: "job-role", Kind: "role", Status: "succeeded"}}, 10)
	if out != "No native skill jobs found." {
		t.Fatalf("out=%q", out)
	}
}

func TestFormatNativeSkillJobDetailsIncludesArtifactsAndRecovery(t *testing.T) {
	out := formatNativeSkillJobDetails(storage.Job{
		JobID:       "job-k",
		Kind:        karaokeKind,
		Status:      "succeeded",
		Description: "karaoke: https://youtu.be/x",
		Summary:     "Karaoke completed",
		StartedAt:   "2026-05-02 20:00:00",
		CompletedAt: "2026-05-02 20:03:07",
	}, []storage.JobArtifact{
		{Name: "share-page", URI: "http://slava/share/job"},
		{Name: "karaoke-mp3", URI: "http://slava/share/job/karaoke.mp3"},
	})

	for _, want := range []string{
		"karaoke job",
		"Job: job-k",
		"Status: succeeded",
		"Duration: 3m7s",
		"share-page: http://slava/share/job",
		"karaoke-mp3: http://slava/share/job/karaoke.mp3",
		"Recovery: /skill_status job-k",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestNativeSkillJobKindHelpers(t *testing.T) {
	for _, kind := range []string{videoSummaryKind, karaokeKind} {
		if !isNativeSkillJobKind(kind) {
			t.Fatalf("%q should be native skill kind", kind)
		}
	}
	if isNativeSkillJobKind("role") {
		t.Fatal("role must not be a native skill kind")
	}
	if nativeSkillJobLabel(videoSummaryKind) != "video-summary" {
		t.Fatal("unexpected video summary label")
	}
}
