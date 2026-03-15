package cli

import (
	"testing"

	"ok-gobot/internal/storage"
)

func TestIsTerminalJobStatus(t *testing.T) {
	tests := []struct {
		status storage.JobStatus
		want   bool
	}{
		{status: storage.JobQueued, want: false},
		{status: storage.JobRunning, want: false},
		{status: storage.JobWaiting, want: false},
		{status: storage.JobDone, want: true},
		{status: storage.JobFailed, want: true},
		{status: storage.JobCancelled, want: true},
	}

	for _, tc := range tests {
		if got := isTerminalJobStatus(tc.status); got != tc.want {
			t.Fatalf("isTerminalJobStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
