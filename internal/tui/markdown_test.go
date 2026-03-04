package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes ANSI escape sequences for test assertions.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// testRenderer creates a renderer with an explicit style (WithAutoStyle
// falls back to notty mode without a real terminal).
func testRenderer(width int) *mdRenderer {
	return newMDRenderer(width, glamour.WithStandardStyle("light"))
}

func TestNewMDRenderer(t *testing.T) {
	r := testRenderer(80)
	if r == nil {
		t.Fatal("newMDRenderer returned nil")
	}
	if r.renderer == nil {
		t.Fatal("renderer is nil")
	}
	if r.width != 80 {
		t.Fatalf("expected width 80, got %d", r.width)
	}
}

func TestRenderPlainText(t *testing.T) {
	r := testRenderer(80)
	out := stripANSI(r.render("hello world"))
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected output to contain 'hello world', got %q", out)
	}
}

func TestRenderBold(t *testing.T) {
	r := testRenderer(80)
	out := stripANSI(r.render("this is **bold** text"))
	if strings.Contains(out, "**") {
		t.Fatalf("expected bold markers to be rendered, got %q", out)
	}
	if !strings.Contains(out, "bold") {
		t.Fatalf("expected 'bold' in output, got %q", out)
	}
}

func TestRenderCodeBlock(t *testing.T) {
	r := testRenderer(80)
	md := "```go\nfmt.Println(\"hello\")\n```"
	out := stripANSI(r.render(md))
	if strings.Contains(out, "```") {
		t.Fatalf("expected code fences to be rendered, got %q", out)
	}
	if !strings.Contains(out, "Println") {
		t.Fatalf("expected code content in output, got %q", out)
	}
}

func TestRenderHeading(t *testing.T) {
	r := testRenderer(80)
	out := stripANSI(r.render("# My Heading"))
	if !strings.Contains(out, "My Heading") {
		t.Fatalf("expected heading text in output, got %q", out)
	}
	// Should not have raw # prefix
	trimmed := strings.TrimSpace(out)
	if strings.HasPrefix(trimmed, "# ") {
		t.Fatalf("expected heading marker to be removed, got %q", trimmed)
	}
}

func TestRenderList(t *testing.T) {
	r := testRenderer(80)
	md := "- item one\n- item two\n- item three"
	out := stripANSI(r.render(md))
	if !strings.Contains(out, "item one") {
		t.Fatalf("expected list items in output, got %q", out)
	}
}

func TestRenderNilRenderer(t *testing.T) {
	var r *mdRenderer
	out := r.render("fallback text")
	if out != "fallback text" {
		t.Fatalf("nil renderer should return original text, got %q", out)
	}
}

func TestRenderNoTrailingNewlines(t *testing.T) {
	r := testRenderer(80)
	out := r.render("simple text")
	if strings.HasSuffix(out, "\n") {
		t.Fatalf("output should not have trailing newlines, got %q", out)
	}
}
