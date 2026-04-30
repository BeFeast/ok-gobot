package memory

import (
	"errors"
	"testing"
)

func TestStatusReporterWatcherStateNormalizesEmptyState(t *testing.T) {
	reporter := NewStatusReporter(nil, StatusOptions{Enabled: true})

	if got := reporter.WatcherState(); got != WatcherStateUnknown {
		t.Fatalf("WatcherState()=%q, want %q", got, WatcherStateUnknown)
	}
}

func TestStatusReporterWatcherStateReturnsCurrentState(t *testing.T) {
	reporter := NewStatusReporter(nil, StatusOptions{Enabled: true})
	reporter.SetWatcherState(WatcherStateError)

	if got := reporter.WatcherState(); got != WatcherStateError {
		t.Fatalf("WatcherState()=%q, want %q", got, WatcherStateError)
	}
}

func TestStatusReporterClearLastErrorIfPrefixClearsMatchingError(t *testing.T) {
	reporter := NewStatusReporter(nil, StatusOptions{Enabled: true})
	reporter.SetLastError(`extra path "notes" watcher error`, errors.New("boom"))

	if !reporter.ClearLastErrorIfPrefix(`extra path "notes" watcher error`) {
		t.Fatalf("expected matching last error to be cleared")
	}
	if got := reporterLastError(reporter); got != "" {
		t.Fatalf("LastError=%q, want empty", got)
	}
}

func TestStatusReporterClearLastErrorIfPrefixKeepsDifferentError(t *testing.T) {
	reporter := NewStatusReporter(nil, StatusOptions{Enabled: true})
	reporter.SetLastError("memory watcher error", errors.New("boom"))

	if reporter.ClearLastErrorIfPrefix(`extra path "notes" watcher error`) {
		t.Fatalf("expected different last error to remain")
	}
	if got := reporterLastError(reporter); got == "" {
		t.Fatalf("expected LastError to remain")
	}
}

func reporterLastError(reporter *StatusReporter) string {
	reporter.mu.RLock()
	defer reporter.mu.RUnlock()
	return reporter.opts.LastError
}
