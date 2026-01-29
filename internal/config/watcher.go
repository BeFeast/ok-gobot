package config

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigWatcher watches configuration file for changes
type ConfigWatcher struct {
	configPath string
	watcher    *fsnotify.Watcher
	onChange   func(*Config)
	stopCh     chan struct{}
	mu         sync.Mutex
	debounce   *time.Timer
}

// NewConfigWatcher creates a new configuration watcher
func NewConfigWatcher(configPath string, onChange func(*Config)) (*ConfigWatcher, error) {
	if configPath == "" {
		return nil, fmt.Errorf("config path is empty")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	// Watch the directory containing the config file
	// This is necessary because some editors delete and recreate files on save
	configDir := filepath.Dir(configPath)
	if err := watcher.Add(configDir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to watch directory %s: %w", configDir, err)
	}

	cw := &ConfigWatcher{
		configPath: configPath,
		watcher:    watcher,
		onChange:   onChange,
		stopCh:     make(chan struct{}),
	}

	// Start watching in a goroutine
	go cw.watch()

	log.Printf("Config watcher started for: %s", configPath)
	return cw, nil
}

// watch monitors file changes
func (cw *ConfigWatcher) watch() {
	for {
		select {
		case event, ok := <-cw.watcher.Events:
			if !ok {
				return
			}

			// Check if the event is for our config file
			// Match both exact path and basename (for editor recreate scenarios)
			configBase := filepath.Base(cw.configPath)
			eventBase := filepath.Base(event.Name)

			if event.Name == cw.configPath || eventBase == configBase {
				// Only trigger on write or create events
				if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
					log.Printf("Config file changed: %s (op: %s)", event.Name, event.Op)
					cw.debounceReload()
				}
			}

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Config watcher error: %v", err)

		case <-cw.stopCh:
			return
		}
	}
}

// debounceReload debounces reload events to avoid multiple reloads
func (cw *ConfigWatcher) debounceReload() {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	// Cancel existing timer
	if cw.debounce != nil {
		cw.debounce.Stop()
	}

	// Create new timer
	cw.debounce = time.AfterFunc(500*time.Millisecond, func() {
		cw.reload()
	})
}

// reload reads and validates the new configuration
func (cw *ConfigWatcher) reload() {
	log.Println("Reloading configuration...")

	cfg, err := cw.loadConfig()
	if err != nil {
		log.Printf("Failed to reload config: %v", err)
		return
	}

	if err := cfg.Validate(); err != nil {
		log.Printf("Config validation failed: %v", err)
		return
	}

	log.Println("Configuration reloaded successfully")

	// Call the onChange callback
	if cw.onChange != nil {
		cw.onChange(cfg)
	}
}

// loadConfig loads configuration from the watched config file
func (cw *ConfigWatcher) loadConfig() (*Config, error) {
	return LoadFrom(cw.configPath)
}

// TriggerReload manually triggers a configuration reload
func (cw *ConfigWatcher) TriggerReload() error {
	log.Println("Manual config reload triggered")

	cfg, err := cw.loadConfig()
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	log.Println("Configuration reloaded successfully")

	// Call the onChange callback
	if cw.onChange != nil {
		cw.onChange(cfg)
	}

	return nil
}

// Stop stops the watcher and cleans up resources
func (cw *ConfigWatcher) Stop() {
	log.Println("Stopping config watcher...")

	cw.mu.Lock()
	if cw.debounce != nil {
		cw.debounce.Stop()
	}
	cw.mu.Unlock()

	close(cw.stopCh)
	cw.watcher.Close()

	log.Println("Config watcher stopped")
}
