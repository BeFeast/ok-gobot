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

func TestBuildQMDStatusStringShowsFallbackState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status memory.IndexStatus
		want   []string
	}{
		{
			name: "used",
			status: memory.IndexStatus{
				BackendType: "qmd",
				QMDStatus:   "used (primary=qmd, fallback=builtin)",
			},
			want: []string{"QMD: used", "Fallback: builtin"},
		},
		{
			name: "skipped",
			status: memory.IndexStatus{
				BackendType: memory.BackendSQLite,
				QMDStatus:   "skipped (memory.backend=builtin)",
			},
			want: []string{"QMD: skipped", "memory.backend=builtin"},
		},
		{
			name: "unavailable",
			status: memory.IndexStatus{
				BackendType: "qmd",
				QMDStatus:   "unavailable: qmd binary not found; fallback=builtin",
			},
			want: []string{"QMD: unavailable", "qmd binary not found", "Fallback: builtin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &Bot{memoryStatus: fakeMemoryStatusProvider{status: tt.status}}
			out := b.buildQMDStatusString(t.Context())
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("expected %q in output: %q", want, out)
				}
			}
		})
	}
}
