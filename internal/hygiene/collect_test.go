package hygiene

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectReusesPullRequestSnapshotForQueue(t *testing.T) {
	t.Parallel()

	var prListCalls int
	var issueListCalls int
	runner := func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch name {
		case "git":
			if len(args) >= 1 && args[0] == "rev-list" {
				return []byte("0\t0\n"), nil
			}
		case "gh":
			if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
				prListCalls++
				return []byte(`[{"number":42,"title":"ready pr","body":"","state":"OPEN","headRefName":"feat/ready","labels":[{"name":"ready"}],"updatedAt":"2026-05-01T12:00:00Z","closingIssuesReferences":[{"number":366}]}]`), nil
			}
			if len(args) >= 2 && args[0] == "issue" && args[1] == "list" {
				issueListCalls++
			}
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}

	snapshot, err := Collect(context.Background(), CollectOptions{
		RepoRoot:          "/repo",
		WorktreeStatePath: filepath.Join(t.TempDir(), "worktrees.json"),
		ReadyLabel:        "ready",
		Runner:            runner,
	})
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if prListCalls != 1 {
		t.Fatalf("gh pr list calls = %d, want 1", prListCalls)
	}
	if issueListCalls != 0 {
		t.Fatalf("gh issue list calls = %d, want 0", issueListCalls)
	}
	if snapshot.Queue.EligibleIssues != 1 || snapshot.Queue.SkippedIssues != 0 {
		t.Fatalf("queue = %+v, want one eligible PR candidate", snapshot.Queue)
	}
	if len(snapshot.PullRequests) != 1 || snapshot.PullRequests[0].IssueNumber != 366 || len(snapshot.PullRequests[0].Labels) != 1 {
		t.Fatalf("pull requests = %+v, want collected PR context", snapshot.PullRequests)
	}
}

func TestCollectIncludesLiveWorkersForDeadWorkerSuppression(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	runner := func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		switch name {
		case "git":
			if len(args) >= 1 && args[0] == "rev-list" {
				return []byte("0\t0\n"), nil
			}
		case "gh":
			if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
				return []byte(`[]`), nil
			}
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}

	snapshot, err := Collect(context.Background(), CollectOptions{
		RepoRoot:          "/repo",
		WorktreeStatePath: filepath.Join(t.TempDir(), "worktrees.json"),
		ReadyLabel:        "ready",
		Workers: []Worker{{
			SessionKey: "agent:worker:main",
			Running:    true,
			Alive:      true,
			LastSeenAt: now,
		}},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if len(snapshot.Workers) != 1 || snapshot.Workers[0].SessionKey != "agent:worker:main" {
		t.Fatalf("workers = %+v, want supplied live worker", snapshot.Workers)
	}

	snapshot.Jobs = []Job{{
		JobID:      "job-1",
		SessionKey: "agent:worker:main",
		Status:     "running",
		StartedAt:  now.Add(-45 * time.Minute),
	}}
	report := Analyze(snapshot, Options{Now: now, DeadWorkerAge: 30 * time.Minute})
	if len(report.SafeActions) != 0 || len(report.ApprovalRequired) != 0 {
		t.Fatalf("unexpected findings for live worker: safe=%+v approval=%+v", report.SafeActions, report.ApprovalRequired)
	}
}
