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
		opts = []glamour.TermRendererOption{glamour.WithAutoStyle()}
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
