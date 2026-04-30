package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// StatusReporter combines persisted index counters with runtime health state.
type StatusReporter struct {
	mu        sync.RWMutex
	store     *MemoryStore
	opts      StatusOptions
	qmdStatus func(context.Context) string
}

// NewStatusReporter creates a reporter for memory health diagnostics.
func NewStatusReporter(store *MemoryStore, opts StatusOptions) *StatusReporter {
	return &StatusReporter{store: store, opts: normalizeStatusOptions(opts)}
}

// SetStore updates the store used for persisted index counters.
func (r *StatusReporter) SetStore(store *MemoryStore) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
}

// SetQMDStatusFunc updates the runtime QMD status provider used by Status.
func (r *StatusReporter) SetQMDStatusFunc(fn func(context.Context) string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.qmdStatus = fn
}

// SetWatcherState records the memory watcher lifecycle state.
func (r *StatusReporter) SetWatcherState(state string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts.WatcherState = strings.TrimSpace(state)
}

// WatcherState returns the current watcher lifecycle state.
func (r *StatusReporter) WatcherState() string {
	if r == nil {
		return WatcherStateUnknown
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return normalizeStatusOptions(r.opts).WatcherState
}

// SetLastError records the latest memory indexing or watcher error.
func (r *StatusReporter) SetLastError(message string, err error) {
	if r == nil {
		return
	}
	text := strings.TrimSpace(message)
	if err != nil {
		if text == "" {
			text = err.Error()
		} else {
			text = fmt.Sprintf("%s: %v", text, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts.LastError = text
}

// ClearLastError clears any previously recorded runtime error.
func (r *StatusReporter) ClearLastError() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts.LastError = ""
}

// ClearLastErrorIfPrefix clears the runtime error only when it belongs to a known source.
func (r *StatusReporter) ClearLastErrorIfPrefix(prefixes ...string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lastError := strings.TrimSpace(r.opts.LastError)
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix != "" && strings.HasPrefix(lastError, prefix) {
			r.opts.LastError = ""
			return true
		}
	}
	return false
}

// Status returns the current memory health snapshot.
func (r *StatusReporter) Status(ctx context.Context) (IndexStatus, error) {
	if r == nil {
		return CollectStatus(ctx, nil, StatusOptions{Enabled: false, BackendType: "none", WatcherState: WatcherStateDisabled})
	}
	r.mu.RLock()
	store := r.store
	opts := normalizeStatusOptions(r.opts)
	qmdStatus := r.qmdStatus
	r.mu.RUnlock()
	if qmdStatus != nil {
		opts.QMDStatus = strings.TrimSpace(qmdStatus(ctx))
	}
	return CollectStatus(ctx, store, opts)
}

func normalizeStatusOptions(opts StatusOptions) StatusOptions {
	opts.BackendType = strings.TrimSpace(opts.BackendType)
	if opts.BackendType == "" {
		opts.BackendType = BackendSQLite
	}
	opts.WatcherState = strings.TrimSpace(opts.WatcherState)
	if opts.WatcherState == "" {
		if opts.Enabled {
			opts.WatcherState = WatcherStateUnknown
		} else {
			opts.WatcherState = WatcherStateDisabled
		}
	}
	opts.LastError = strings.TrimSpace(opts.LastError)
	opts.QMDStatus = strings.TrimSpace(opts.QMDStatus)
	opts.ExtraPaths = append([]string(nil), opts.ExtraPaths...)
	return opts
}
