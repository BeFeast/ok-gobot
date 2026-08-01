package memory

import (
	"strings"
	"testing"
)

func TestFormatStatusCLIDisabledIsActionable(t *testing.T) {
	status, err := CollectStatus(t.Context(), nil, StatusOptions{Enabled: false})
	if err != nil {
		t.Fatalf("CollectStatus returned error: %v", err)
	}

	out := FormatStatusCLI(status)
	for _, want := range []string{
		"Memory: disabled",
		"Enabled: false",
		"Sources: 0",
		"Chunks: 0",
		"Action: Set memory.enabled: true",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestFormatStatusTelegramIncludesErrorAndConfiguredPaths(t *testing.T) {
	status := IndexStatus{
		Enabled:      true,
		State:        MemoryStateError,
		BackendType:  BackendSQLite,
		WatcherState: WatcherStateError,
		SourceCount:  3,
		ChunkCount:   42,
		LastError:    "embed batch failed",
		ExtraPaths:   []string{"notes", "runbooks"},
		QMDStatus:    "configured",
		Action:       "Fix the error, then run ok-gobot memory index --force.",
	}

	out := FormatStatusTelegram(status)
	for _, want := range []string{
		"Memory status: error",
		"Indexed: 3 source(s), 42 chunk(s)",
		"Extra paths: notes, runbooks",
		"QMD: configured",
		"Last error: embed batch failed",
		"Action: Fix the error",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}
