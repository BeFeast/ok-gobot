package memory

import "testing"

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
