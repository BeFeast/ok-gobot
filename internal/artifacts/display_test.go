package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestSerializerBlocksMissingLocalArtifact(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.png")

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           8,
		JobID:        "job-1",
		Name:         "missing",
		ArtifactType: "screenshot",
		MimeType:     "image/png",
		URI:          missing,
	})

	if info.Path != "" || info.URI != "" || info.Display.Href != "" || info.Display.Safe {
		t.Fatalf("missing local path was exposed: %+v", info)
	}
	if !strings.Contains(info.Display.Reason, "not found") {
		t.Fatalf("expected not found reason, got %+v", info.Display)
	}
	if hint := FormatProofHint(info); containsAny(hint, missing, root) || !strings.Contains(hint, "hidden") {
		t.Fatalf("missing artifact hint leaked path or reason: %q", hint)
	}
}

func TestSerializerBlocksPathTraversalOutsideRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}
	traversal := filepath.Join(root, "..", "outside.txt")

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           9,
		JobID:        "job-1",
		Name:         "traversal",
		ArtifactType: "file",
		URI:          traversal,
	})

	if info.Path != "" || info.URI != "" || info.Display.Href != "" || info.Display.Safe {
		t.Fatalf("path traversal artifact was exposed: %+v", info)
	}
	if !strings.Contains(info.Display.Reason, "outside configured artifact roots") {
		t.Fatalf("expected outside-root reason, got %+v", info.Display)
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

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           10,
		JobID:        "job-1",
		Name:         "link",
		ArtifactType: "screenshot",
		MimeType:     "image/png",
		URI:          link,
	})
	if info.Path != "" || info.URI != "" || info.Display.Href != "" || info.Display.Safe {
		t.Fatalf("symlink escape artifact was exposed: %+v", info)
	}
}

func TestSerializerBlocksUnsupportedLocalArtifactKind(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proof.bin")
	if err := os.WriteFile(path, []byte("opaque"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           11,
		JobID:        "job-1",
		Name:         "opaque",
		ArtifactType: "binary_blob",
		MimeType:     "application/octet-stream",
		URI:          path,
	})

	if info.Path != "" || info.URI != "" || info.Display.Href != "" || info.Display.Safe {
		t.Fatalf("unsupported local artifact was exposed: %+v", info)
	}
	if !strings.Contains(info.Display.Reason, "unsupported artifact kind") {
		t.Fatalf("expected unsupported-kind reason, got %+v", info.Display)
	}
}

func TestSerializerVerifiesPersistedLocalArtifactMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proof.txt")
	if err := os.WriteFile(path, []byte("proof"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	createdAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	row := storage.JobArtifact{
		ID:           12,
		JobID:        "job-1",
		Name:         "proof",
		ArtifactType: "file",
		MimeType:     "text/plain",
		URI:          path,
		CreatedAt:    createdAt.Format(time.RFC3339),
	}
	metadata := BuildMetadata(row, "role:auditor", createdAt)
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	row.Metadata = string(raw)

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(row)
	if !info.Display.Safe || info.Metadata == nil {
		t.Fatalf("expected safe artifact metadata, got %+v", info)
	}
	expectedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval path: %v", err)
	}
	if info.Metadata.NormalizedPath != expectedPath || info.Metadata.Producer != "role:auditor" || info.Metadata.SHA256 == "" {
		t.Fatalf("unexpected metadata: %+v", info.Metadata)
	}
	if info.Metadata.SizeBytes == nil || *info.Metadata.SizeBytes != int64(len("proof")) {
		t.Fatalf("unexpected metadata size: %+v", info.Metadata)
	}

	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}
	info = NewSerializer([]string{root}, "/api/artifacts").Serialize(row)
	if info.Display.Safe || info.Path != "" || info.Metadata != nil {
		t.Fatalf("tampered artifact was exposed: %+v", info)
	}
	if !strings.Contains(info.Display.Reason, "metadata") {
		t.Fatalf("expected metadata verification reason, got %+v", info.Display)
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

func TestFormatProofHintsAvoidsLocalPaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shot.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	info := NewSerializer([]string{root}, "/api/artifacts").Serialize(storage.JobArtifact{
		ID:           42,
		Name:         "Screenshot",
		ArtifactType: "screenshot",
		MimeType:     "image/png",
		URI:          path,
	})

	hints := FormatProofHints([]Info{info}, 5)
	if len(hints) != 1 {
		t.Fatalf("expected one hint, got %#v", hints)
	}
	if got := hints[0]; got == "" || containsAny(got, path, root) {
		t.Fatalf("hint leaked local path: %q", got)
	}
	if got := hints[0]; !containsAll(got, "Screenshot", "#42", "safe local image") {
		t.Fatalf("hint missing artifact details: %q", got)
	}
}

func TestFirstSafeLocalImageRequiresSafeLocalPath(t *testing.T) {
	root := t.TempDir()
	safePath := filepath.Join(root, "shot.png")
	if err := os.WriteFile(safePath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	serializer := NewSerializer([]string{root}, "/api/artifacts")
	safe := serializer.Serialize(storage.JobArtifact{ID: 1, Name: "safe", ArtifactType: "screenshot", MimeType: "image/png", URI: safePath})
	unsafe := serializer.Serialize(storage.JobArtifact{ID: 2, Name: "outside", ArtifactType: "screenshot", MimeType: "image/png", URI: filepath.Join(t.TempDir(), "outside.png")})
	remote := serializer.Serialize(storage.JobArtifact{ID: 3, Name: "remote", ArtifactType: "screenshot", MimeType: "image/png", URI: "https://example.com/shot.png"})

	got, ok := FirstSafeLocalImage([]Info{unsafe, remote, safe})
	if !ok {
		t.Fatal("expected safe local image")
	}
	if got.ID != safe.ID || got.Path != safePath {
		t.Fatalf("FirstSafeLocalImage = %+v, want safe artifact", got)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
