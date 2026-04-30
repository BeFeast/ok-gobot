package web

import (
	"strings"
	"testing"
)

func TestIndexIncludesArtifactGalleryRenderers(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		"function renderArtifactPreview",
		"kind === 'image'",
		"kind === 'url'",
		"kind === 'text_report'",
		"function artifactHref",
		"proof_artifact_count",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}
}
