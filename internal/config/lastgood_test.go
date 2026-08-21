package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLastGoodPath(t *testing.T) {
	if got := LastGoodPath("/home/u/.ok-gobot/config.yaml"); got != "/home/u/.ok-gobot/config.last-good.yaml" {
		t.Errorf("got %q", got)
	}
	if got := LastGoodPath(""); got != "" {
		t.Errorf("empty path must stay empty, got %q", got)
	}
}

func TestSaveLastGoodAtomic(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("telegram:\n  token: abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	SaveLastGood(cfg)

	data, err := os.ReadFile(LastGoodPath(cfg))
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if !strings.Contains(string(data), "token: abc") {
		t.Errorf("snapshot content wrong: %q", data)
	}
	if _, err := os.Stat(LastGoodPath(cfg) + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
	// a missing source must not panic or leave debris
	SaveLastGood(filepath.Join(dir, "nope.yaml"))
}
