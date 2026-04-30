package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ok-gobot/internal/memory"
)

type fakeMemoryStatusProvider struct {
	status memory.IndexStatus
	err    error
}

func (f fakeMemoryStatusProvider) Status(context.Context) (memory.IndexStatus, error) {
	return f.status, f.err
}

func TestBuildMemoryStatusStringIncludesCounts(t *testing.T) {
	b := &Bot{memoryStatus: fakeMemoryStatusProvider{status: memory.IndexStatus{
		Enabled:       true,
		State:         memory.MemoryStateOK,
		BackendType:   memory.BackendSQLite,
		WatcherState:  memory.WatcherStateActive,
		SourceCount:   2,
		ChunkCount:    12,
		LastIndexedAt: "2026-04-30 12:00:00",
	}}}

	out := b.buildMemoryStatusString(t.Context())
	for _, want := range []string{"Memory status: ok", "Indexed: 2 source(s), 12 chunk(s)", "Watcher: active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestBuildMemoryStatusStringShowsProviderError(t *testing.T) {
	b := &Bot{memoryStatus: fakeMemoryStatusProvider{
		status: memory.IndexStatus{
			Enabled:      true,
			BackendType:  memory.BackendSQLite,
			WatcherState: memory.WatcherStateError,
			LastError:    "initial index failed",
		},
		err: errors.New("memory store is not configured"),
	}}

	out := b.buildMemoryStatusString(t.Context())
	for _, want := range []string{"Memory status: error", "Last error: initial index failed", "Action:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}
