package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStylesDoNotUseAdaptiveColor(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}

	stylesPath := filepath.Join(filepath.Dir(thisFile), "styles.go")
	content, err := os.ReadFile(stylesPath)
	if err != nil {
		t.Fatalf("read styles.go: %v", err)
	}

	if strings.Contains(string(content), "AdaptiveColor") {
		t.Fatalf("styles.go must not use lipgloss.AdaptiveColor to avoid OSC 11 background probing")
	}
}
