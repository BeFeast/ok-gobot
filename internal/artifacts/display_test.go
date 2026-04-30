package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"ok-gobot/internal/storage"
)

func TestSerializerAllowsLocalImageInsideRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shot.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           42,
		JobID:        "job-1",
		Name:         "Home page",
		ArtifactType: "screenshot",
		MimeType:     "image/png",
		URI:          path,
		CreatedAt:    "2026-04-30T10:00:00Z",
	})

	if info.Type != "screenshot" || info.Label != "Home page" || info.Path != path || info.URI != path {
		t.Fatalf("unexpected artifact info: %+v", info)
	}
	if info.Display.Kind != KindImage || !info.Display.Safe || !info.Display.Preview {
		t.Fatalf("unexpected display metadata: %+v", info.Display)
	}
	if info.Display.Href != "/api/artifacts/42/content" {
		t.Fatalf("Display.Href = %q", info.Display.Href)
	}
}

func TestSerializerBlocksLocalPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("png"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           7,
		JobID:        "job-1",
		Name:         "outside",
		ArtifactType: "screenshot",
		MimeType:     "image/png",
		URI:          outside,
	})

	if info.Path != "" || info.URI != "" || info.Display.Href != "" {
		t.Fatalf("unsafe local path was exposed: %+v", info)
	}
	if info.Display.Safe || info.Display.Reason == "" {
		t.Fatalf("expected unsafe display metadata, got %+v", info.Display)
	}
}

func TestSerializerBlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("png"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if path, ok := SafeLocalPath(link, []string{root}); ok {
		t.Fatalf("symlink escape resolved as safe: %s", path)
	}
}

func TestSerializerClassifiesURLAndTextReport(t *testing.T) {
	serializer := NewSerializer([]string{t.TempDir()}, "/api/artifacts")

	urlInfo := serializer.Serialize(storage.JobArtifact{
		ID:           1,
		Name:         "Pull request",
		ArtifactType: "url",
		URI:          "https://github.com/BeFeast/ok-gobot/pull/1",
	})
	if urlInfo.URL == "" || urlInfo.Display.Kind != KindURL || !urlInfo.Display.Safe || urlInfo.Display.Href != urlInfo.URL {
		t.Fatalf("unexpected URL artifact info: %+v", urlInfo)
	}

	reportInfo := serializer.Serialize(storage.JobArtifact{
		ID:           2,
		Name:         "Verification",
		ArtifactType: "text_report",
		MimeType:     "text/markdown",
		Content:      "tests passed",
	})
	if reportInfo.Display.Kind != KindTextReport || !reportInfo.Display.Safe || !reportInfo.Display.Inline || reportInfo.Content != "tests passed" {
		t.Fatalf("unexpected text report artifact info: %+v", reportInfo)
	}
}
