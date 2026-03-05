// Package envfix suppresses the OSC 11 terminal background-color query that
// bubbletea/lipgloss/termenv send during package init().
//
// The problem: bubbletea's init() calls lipgloss.HasDarkBackground(), which
// lazily queries the terminal via OSC 11. Some terminals/PTYs never respond,
// causing a visible "Loading…" hang.
//
// The fix: this package sets TERM=dumb before any charmbracelet package inits.
// termenv.termStatusReport() returns immediately for TERM=dumb, skipping the
// OSC 11 query entirely. The original TERM is restored via Restore() before
// the TUI starts, so terminal capabilities (colors, alt screen) work normally.
//
// CRITICAL: this package must NOT import any charmbracelet package (lipgloss,
// bubbletea, termenv). If it did, Go's init order would no longer guarantee
// that envfix.init() runs first.
package envfix

import "os"

var origTERM string

func init() {
	// Save original TERM and set dumb to suppress OSC 11 during package init.
	origTERM = os.Getenv("TERM")
	os.Setenv("TERM", "dumb")
}

// Restore puts back the original TERM value. Must be called before starting
// the TUI so that bubbletea can detect terminal capabilities (colors, alt
// screen, mouse, etc).
func Restore() {
	if origTERM != "" {
		os.Setenv("TERM", origTERM)
	} else {
		os.Unsetenv("TERM")
	}
}
