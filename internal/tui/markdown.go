package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// mdRenderer caches a glamour TermRenderer at a given width.
type mdRenderer struct {
	renderer *glamour.TermRenderer
	width    int
}

// newMDRenderer creates a glamour renderer for the given width.
func newMDRenderer(width int, opts ...glamour.TermRendererOption) *mdRenderer {
	if len(opts) == 0 {
		// Use DarkStyle explicitly instead of AutoStyle to avoid glamour
		// calling termenv.HasDarkBackground() which sends an OSC 11 query.
		opts = []glamour.TermRendererOption{glamour.WithStandardStyle("dark")}
	}
	opts = append(opts, glamour.WithWordWrap(width))

	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return &mdRenderer{width: width}
	}
	return &mdRenderer{renderer: r, width: width}
}

// render converts markdown text to styled terminal output.
// Returns the original text unchanged if the renderer is nil.
func (mr *mdRenderer) render(md string) string {
	if mr == nil || mr.renderer == nil {
		return md
	}
	out, err := mr.renderer.Render(md)
	if err != nil {
		return md
	}
	// glamour adds trailing newlines; trim them for inline chat display.
	return strings.TrimRight(out, "\n")
}
