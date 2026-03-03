package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches bootstrap files for changes.
type Watcher struct {
	basePath string
	watcher  *fsnotify.Watcher
	onChange func()
	stopCh   chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	debounce *time.Timer
}

// NewWatcher creates a bootstrap watcher rooted at basePath.
func NewWatcher(basePath string, onChange func()) (*Watcher, error) {
	if basePath == "" {
		basePath = DefaultPath
	}
	basePath = ExpandPath(basePath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create bootstrap watcher: %w", err)
	}

	bw := &Watcher{
		basePath: basePath,
		watcher:  watcher,
		onChange: onChange,
		stopCh:   make(chan struct{}),
	}

	if err := bw.watchPath(basePath); err != nil {
		watcher.Close()
		return nil, err
	}
	if err := bw.watchPath(filepath.Join(basePath, "memory")); err != nil && !os.IsNotExist(err) {
		watcher.Close()
		return nil, err
	}
	if err := bw.watchSkills(); err != nil {
		watcher.Close()
		return nil, err
	}

	go bw.watch()
	return bw, nil
}

// TriggerReload manually invokes the bootstrap reload callback.
func (bw *Watcher) TriggerReload() error {
	if bw == nil {
		return nil
	}
	if bw.onChange != nil {
		bw.onChange()
	}
	return nil
}

// Stop stops the watcher and releases resources.
func (bw *Watcher) Stop() {
	if bw == nil {
		return
	}

	bw.stopOnce.Do(func() {
		bw.mu.Lock()
		if bw.debounce != nil {
			bw.debounce.Stop()
		}
		bw.mu.Unlock()

		close(bw.stopCh)
		_ = bw.watcher.Close()
	})
}

func (bw *Watcher) watch() {
	for {
		select {
		case event, ok := <-bw.watcher.Events:
			if !ok {
				return
			}

			if event.Op&fsnotify.Create == fsnotify.Create {
				bw.watchNewSkillDir(event.Name)
			}

			if bw.isBootstrapEvent(event.Name) &&
				(event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename)) != 0 {
				bw.debounceReload()
			}
		case _, ok := <-bw.watcher.Errors:
			if !ok {
				return
			}
		case <-bw.stopCh:
			return
		}
	}
}

func (bw *Watcher) debounceReload() {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	if bw.debounce != nil {
		bw.debounce.Stop()
	}

	bw.debounce = time.AfterFunc(300*time.Millisecond, func() {
		if bw.onChange != nil {
			bw.onChange()
		}
	})
}

func (bw *Watcher) watchSkills() error {
	skillsPath := filepath.Join(bw.basePath, "skills")
	if err := bw.watchPath(skillsPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	entries, err := os.ReadDir(skillsPath)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := bw.watchPath(filepath.Join(skillsPath, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	return nil
}

func (bw *Watcher) watchNewSkillDir(path string) {
	skillsPath := filepath.Join(bw.basePath, "skills")
	if filepath.Dir(path) != skillsPath {
		return
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	_ = bw.watchPath(path)
}

func (bw *Watcher) watchPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return bw.watcher.Add(path)
}

func (bw *Watcher) isBootstrapEvent(path string) bool {
	rel, err := filepath.Rel(bw.basePath, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return false
	}

	for _, filename := range managedFiles {
		if rel == filename {
			return true
		}
	}

	if strings.HasPrefix(rel, "memory/") && strings.HasSuffix(rel, ".md") {
		return true
	}

	return strings.HasPrefix(rel, "skills/") && filepath.Base(rel) == "SKILL.md"
}
