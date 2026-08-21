package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// A malformed config used to be an instant death sentence: the service exited
// with 2/INVALIDARGUMENT and stayed down until a human noticed. The audit of
// 2026-08-11..21 counted 8 such failed starts in a single day of live editing.
// Keeping the last config that actually booted lets the bot come back up on it
// — loudly — instead of leaving the user with nothing.

// DefaultConfigPath mirrors the lookup order used by Load().
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".ok-gobot", "config")
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return base + ".yaml"
}

// LastGoodPath returns the sidecar path for a given config file.
func LastGoodPath(configPath string) string {
	if configPath == "" {
		return ""
	}
	dir := filepath.Dir(configPath)
	return filepath.Join(dir, "config.last-good"+filepath.Ext(configPath))
}

// SaveLastGood snapshots a config file that has proven itself by booting.
// Failures are logged, never fatal: this is a safety net, not a dependency.
func SaveLastGood(configPath string) {
	dst := LastGoodPath(configPath)
	if configPath == "" || dst == "" {
		return
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("[config] could not snapshot last-good config: %v", err)
		return
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[config] could not write last-good config: %v", err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		log.Printf("[config] could not commit last-good config: %v", err)
		_ = os.Remove(tmp)
	}
}

var bootedFromLastGood bool

// BootedFromLastGood reports whether this process started from the fallback
// snapshot rather than the real config.
func BootedFromLastGood() bool { return bootedFromLastGood }

// LoadWithLastGoodFallback loads the config and, when it is unparseable, falls
// back to the last snapshot that booted. The returned bool reports whether the
// fallback was used so callers can surface the degraded state.
func LoadWithLastGoodFallback() (*Config, bool, error) {
	cfg, err := Load()
	if err == nil {
		return cfg, false, nil
	}

	primary := DefaultConfigPath()
	fallback := LastGoodPath(primary)
	if fallback == "" {
		return nil, false, err
	}
	if _, statErr := os.Stat(fallback); statErr != nil {
		return nil, false, err
	}

	log.Printf("⚠️ [config] %s is unusable (%v)", primary, err)
	log.Printf("⚠️ [config] starting from last-good snapshot %s — FIX THE REAL CONFIG, edits to it are ignored until it parses", fallback)
	cfg, fbErr := LoadFrom(fallback)
	if fbErr != nil {
		return nil, false, fmt.Errorf("config %s is broken (%v) and last-good snapshot also failed: %w", primary, err, fbErr)
	}
	bootedFromLastGood = true
	return cfg, true, nil
}
