package bot

import "testing"

func TestBuildVisionImageContent(t *testing.T) {
	t.Parallel()

	blocks := buildVisionImageContent([]byte("hello"), "image/jpeg", "caption")
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	image := blocks[0]
	if image.Type != "image" {
		t.Fatalf("expected first block type image, got %q", image.Type)
	}
	if image.Source == nil {
		t.Fatal("expected image source")
	}
	if image.Source.Type != "base64" {
		t.Fatalf("expected source type base64, got %q", image.Source.Type)
	}
	if image.Source.MediaType != "image/jpeg" {
		t.Fatalf("expected media type image/jpeg, got %q", image.Source.MediaType)
	}
	if image.Source.Data != "aGVsbG8=" {
		t.Fatalf("unexpected base64 payload: %q", image.Source.Data)
	}

	text := blocks[1]
	if text.Type != "text" || text.Text != "caption" {
		t.Fatalf("unexpected text block: %+v", text)
	}
}

func TestBuildVisionImageContentEmptyData(t *testing.T) {
	t.Parallel()

	if got := buildVisionImageContent(nil, "image/jpeg", "caption"); got != nil {
		t.Fatalf("expected nil blocks for empty data, got %#v", got)
	}
}
