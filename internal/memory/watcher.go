package memory

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultWatcherDebounceDelay = 500 * time.Millisecond
)

var trackedMemoryExtensions = map[string]struct{}{
	".md":   {},
	".txt":  {},
	".yaml": {},
}

var ignoredMemoryDirectories = map[string]struct{}{
	".git":         {},
	"node_modules": {},
}

// FileChangedEvent is emitted by Watcher when a tracked file changes.
type FileChangedEvent struct {
	Path         string
	RelativePath string
	Op           fsnotify.Op
}

type pendingFileEvent struct {
	op    fsnotify.Op
	timer *time.Timer
}

// Watcher watches a full workspace tree for memory-source file changes.
type Watcher struct {
	rootPath string
	watcher  *fsnotify.Watcher
	debounce time.Duration

	events chan FileChangedEvent
	errors chan error
	stopCh chan struct{}

	mu       sync.Mutex
	pending  map[string]*pendingFileEvent
	watched  map[string]struct{}
	stopOnce sync.Once
}

// NewWatcher creates a recursive filesystem watcher for the workspace.
func NewWatcher(rootPath string) (*Watcher, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("root path is empty")
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve root path: %w", err)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		rootPath: filepath.Clean(absRoot),
		watcher:  fsWatcher,
		debounce: defaultWatcherDebounceDelay,
		events:   make(chan FileChangedEvent, 256),
		errors:   make(chan error, 32),
		stopCh:   make(chan struct{}),
		pending:  make(map[string]*pendingFileEvent),
		watched:  make(map[string]struct{}),
	}

	if err := w.addRecursive(w.rootPath); err != nil {
		_ = fsWatcher.Close()
		return nil, err
	}

	go w.loop()
	return w, nil
}

// Events returns the stream of debounced file-change events.
func (w *Watcher) Events() <-chan FileChangedEvent {
	return w.events
}

// Errors returns asynchronous watcher errors.
func (w *Watcher) Errors() <-chan error {
	return w.errors
}

// Stop stops the watcher and releases resources.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}

	w.stopOnce.Do(func() {
		close(w.stopCh)

		w.mu.Lock()
		for key, pending := range w.pending {
			if pending != nil && pending.timer != nil {
				pending.timer.Stop()
			}
			delete(w.pending, key)
		}
		w.mu.Unlock()

		_ = w.watcher.Close()
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.stopCh:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			select {
			case w.errors <- err:
			default:
			}
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	if event.Op&fsnotify.Create == fsnotify.Create {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() && !isIgnoredMemoryPath(w.rootPath, event.Name) {
			_ = w.addRecursive(event.Name)
			return
		}
	}

	if !isTrackedMemoryEvent(w.rootPath, event.Name, event.Op) {
		return
	}
	w.debounceEvent(event.Name, event.Op)
}

func (w *Watcher) debounceEvent(path string, op fsnotify.Op) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	absPath = filepath.Clean(absPath)

	w.mu.Lock()
	defer w.mu.Unlock()

	if pending := w.pending[absPath]; pending != nil {
		pending.op |= op
		if pending.timer != nil {
			pending.timer.Reset(w.debounce)
		}
		return
	}

	pending := &pendingFileEvent{
		op: op,
	}
	pending.timer = time.AfterFunc(w.debounce, func() {
		w.emit(absPath)
	})
	w.pending[absPath] = pending
}

func (w *Watcher) emit(absPath string) {
	w.mu.Lock()
	pending := w.pending[absPath]
	if pending == nil {
		w.mu.Unlock()
		return
	}
	op := pending.op
	delete(w.pending, absPath)
	w.mu.Unlock()

	rel, err := filepath.Rel(w.rootPath, absPath)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return
	}

	event := FileChangedEvent{
		Path:         absPath,
		RelativePath: rel,
		Op:           op,
	}

	select {
	case <-w.stopCh:
		return
	case w.events <- event:
	}
}

func (w *Watcher) addRecursive(path string) error {
	return filepath.WalkDir(path, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		if current != w.rootPath && isIgnoredMemoryPath(w.rootPath, current) {
			return filepath.SkipDir
		}

		return w.addDirectory(current)
	})
}

func (w *Watcher) addDirectory(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absPath = filepath.Clean(absPath)

	w.mu.Lock()
	if _, exists := w.watched[absPath]; exists {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	if err := w.watcher.Add(absPath); err != nil {
		return fmt.Errorf("watch directory %s: %w", absPath, err)
	}

	w.mu.Lock()
	w.watched[absPath] = struct{}{}
	w.mu.Unlock()
	return nil
}

func isTrackedMemoryEvent(rootPath, path string, op fsnotify.Op) bool {
	if op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
		return false
	}
	if isIgnoredMemoryPath(rootPath, path) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := trackedMemoryExtensions[ext]
	return ok
}

func isIgnoredMemoryPath(rootPath, path string) bool {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return true
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	absRoot = filepath.Clean(absRoot)
	absPath = filepath.Clean(absPath)

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return true
	}
	if rel == "." {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}

	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if _, ignored := ignoredMemoryDirectories[part]; ignored {
			return true
		}
	}
	return false
}
