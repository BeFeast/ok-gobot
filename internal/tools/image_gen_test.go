package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/ai"
)

// fakeImageGenerator implements ImageGenerator for tool tests.
type fakeImageGenerator struct {
	lastPrompt string
	lastOpts   ImageOptions
	result     *GeneratedImage
	err        error
}

func (f *fakeImageGenerator) Generate(_ context.Context, prompt string, opts ImageOptions) (*GeneratedImage, error) {
	f.lastPrompt = prompt
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeMediaSender implements MediaSender for chat delivery tests.
type fakeMediaSender struct {
	calls   int
	chatID  int64
	path    string
	caption string
	err     error
}

func (f *fakeMediaSender) SendPhotoToChat(chatID int64, filePath, caption string) error {
	f.calls++
	f.chatID = chatID
	f.path = filePath
	f.caption = caption
	return f.err
}

// imageCapableStubClient implements ai.Client plus native image generation.
type imageCapableStubClient struct {
	lastOpts ai.ImageGenOptions
}

func (c *imageCapableStubClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return "", nil
}

func (c *imageCapableStubClient) CompleteWithTools(_ context.Context, _ []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return &ai.ChatCompletionResponse{}, nil
}

func (c *imageCapableStubClient) GenerateImage(_ context.Context, _ string, opts ai.ImageGenOptions) (*ai.GeneratedImageResult, error) {
	c.lastOpts = opts
	return &ai.GeneratedImageResult{PNG: []byte("png-bytes"), RevisedPrompt: "revised"}, nil
}

// plainStubClient implements ai.Client without image generation.
type plainStubClient struct{}

func (c *plainStubClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return "", nil
}

func (c *plainStubClient) CompleteWithTools(_ context.Context, _ []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return &ai.ChatCompletionResponse{}, nil
}

func newFakeImageTool(gen *fakeImageGenerator) *ImageTool {
	return &ImageTool{generator: gen, tempDir: os.TempDir()}
}

// Compile-time interface guarantees relied on by the resolver and tool agent.
var (
	_ ChatScoped = (*ImageTool)(nil)
	_ ToolSchema = (*ImageTool)(nil)
)

func TestImageToolOwnsTimeout(t *testing.T) {
	t.Parallel()

	if !OwnsTimeout(newFakeImageTool(&fakeImageGenerator{})) {
		t.Error("image_gen must own its timeout")
	}
	if !OwnsTimeout(NewBrowserTaskTool(nil, 0)) {
		t.Error("browser_task must own its timeout")
	}
	if OwnsTimeout(&MessageTool{}) {
		t.Error("message tool must not own its timeout")
	}
}

func TestImageTool_Name(t *testing.T) {
	t.Parallel()

	if got := newFakeImageTool(&fakeImageGenerator{}).Name(); got != "image_gen" {
		t.Errorf("Name() = %q, want image_gen", got)
	}
}

func TestImageTool_GetSchema(t *testing.T) {
	t.Parallel()

	schema := newFakeImageTool(&fakeImageGenerator{}).GetSchema()
	if schema == nil {
		t.Fatal("GetSchema() returned nil")
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema properties missing")
	}
	for _, name := range []string{"prompt", "size", "quality"} {
		if _, ok := props[name]; !ok {
			t.Errorf("schema missing property %q", name)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "prompt" {
		t.Errorf("schema required = %v, want [prompt]", schema["required"])
	}
}

func TestImageTool_GetSchemaLegacyDallEVocabulary(t *testing.T) {
	t.Parallel()

	schema := NewImageTool("sk-test", "").GetSchema()
	props := schema["properties"].(map[string]interface{})
	sizeDesc := props["size"].(map[string]interface{})["description"].(string)
	qualityDesc := props["quality"].(map[string]interface{})["description"].(string)
	if !strings.Contains(sizeDesc, "1792x1024") || strings.Contains(sizeDesc, "1536x1024") {
		t.Errorf("DALL-E size description = %q, want dall-e-3 vocabulary", sizeDesc)
	}
	if !strings.Contains(qualityDesc, "standard") || strings.Contains(qualityDesc, "medium") {
		t.Errorf("DALL-E quality description = %q, want standard/hd vocabulary", qualityDesc)
	}
}

func TestImageTool_ExecuteJSONNamedParams(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	tool := newFakeImageTool(gen)

	out, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"prompt":  "a cat",
		"size":    "1536x1024",
		"quality": "high",
	})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if gen.lastPrompt != "a cat" {
		t.Errorf("prompt = %q, want a cat", gen.lastPrompt)
	}
	if gen.lastOpts.Size != "1536x1024" || gen.lastOpts.Quality != "high" {
		t.Errorf("opts = %+v, want size/quality passthrough", gen.lastOpts)
	}
	if !strings.Contains(out, "/tmp/img.png") {
		t.Errorf("output %q missing image path", out)
	}
}

func TestImageTool_ExecuteJSONRequiresPrompt(t *testing.T) {
	t.Parallel()

	tool := newFakeImageTool(&fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}})
	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{"size": "1024x1024"}); err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestImageTool_ExecuteFlagParsing(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	tool := newFakeImageTool(gen)

	if _, err := tool.Execute(context.Background(), "a", "cat", "--size", "512x512", "--quality", "hd"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gen.lastPrompt != "a cat" {
		t.Errorf("prompt = %q, want a cat", gen.lastPrompt)
	}
	if gen.lastOpts.Size != "512x512" || gen.lastOpts.Quality != "hd" {
		t.Errorf("opts = %+v, want parsed flags", gen.lastOpts)
	}
}

func TestImageTool_BindChatSendsPhoto(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	sender := &fakeMediaSender{}
	tool := newFakeImageTool(gen).BindChat(sender, 42).(*ImageTool)

	out, err := tool.ExecuteJSON(context.Background(), map[string]string{"prompt": "a cat"})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if sender.calls != 1 || sender.chatID != 42 || sender.path != "/tmp/img.png" || sender.caption != "a cat" {
		t.Errorf("sender got calls=%d chatID=%d path=%q caption=%q", sender.calls, sender.chatID, sender.path, sender.caption)
	}
	if !strings.Contains(out, "Delivered to the chat") {
		t.Errorf("output %q missing delivery confirmation", out)
	}
}

func TestImageTool_ChatDeliveryTruncatesLongCaption(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	sender := &fakeMediaSender{}
	tool := newFakeImageTool(gen).BindChat(sender, 42).(*ImageTool)

	longPrompt := strings.Repeat("картина ", 200) // ~1600 runes
	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{"prompt": longPrompt}); err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if got := len([]rune(sender.caption)); got > 1024 {
		t.Errorf("caption length = %d runes, must stay within Telegram's 1024 cap", got)
	}
	if !strings.HasSuffix(sender.caption, "…") {
		t.Errorf("truncated caption should end with an ellipsis, got %q…", sender.caption[len(sender.caption)-20:])
	}
}

func TestPruneOldImages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oldFile := dir + "/img_old.png"
	newFile := dir + "/img_new.png"
	for _, f := range []string{oldFile, newFile} {
		if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-2 * maxImageAge)
	if err := os.Chtimes(oldFile, stale, stale); err != nil {
		t.Fatal(err)
	}

	pruneOldImages(dir)

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("stale image was not pruned")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Errorf("fresh image must survive pruning: %v", err)
	}
}

func TestImageTool_BindChatDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	sender := &fakeMediaSender{}
	original := newFakeImageTool(gen)
	_ = original.BindChat(sender, 42)

	if _, err := original.ExecuteJSON(context.Background(), map[string]string{"prompt": "a cat"}); err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if sender.calls != 0 {
		t.Errorf("original tool delivered to chat, calls = %d", sender.calls)
	}
}

func TestImageTool_DeliveryFailureReportedInResponse(t *testing.T) {
	t.Parallel()

	gen := &fakeImageGenerator{result: &GeneratedImage{Path: "/tmp/img.png"}}
	sender := &fakeMediaSender{err: fmt.Errorf("telegram down")}
	tool := newFakeImageTool(gen).BindChat(sender, 42).(*ImageTool)

	out, err := tool.ExecuteJSON(context.Background(), map[string]string{"prompt": "a cat"})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if !strings.Contains(out, "Failed to deliver") || !strings.Contains(out, "telegram down") {
		t.Errorf("output %q missing delivery failure report", out)
	}
	if !strings.Contains(out, "/tmp/img.png") {
		t.Errorf("output %q must still include the local path", out)
	}
}

func TestNativeImageGenerator_WritesPNGAndMergesOptions(t *testing.T) {
	t.Parallel()

	client := &imageCapableStubClient{}
	gen := &nativeImageGenerator{
		client:  client,
		tempDir: t.TempDir(),
		model:   "gpt-image-2",
		size:    "1024x1024",
		quality: "medium",
	}

	result, err := gen.Generate(context.Background(), "a cat", ImageOptions{Size: "1536x1024"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if string(data) != "png-bytes" {
		t.Errorf("file content = %q, want png-bytes", data)
	}
	if result.RevisedPrompt != "revised" {
		t.Errorf("RevisedPrompt = %q, want revised", result.RevisedPrompt)
	}
	// Per-call size overrides the configured default; model/quality fall back.
	if client.lastOpts.Model != "gpt-image-2" || client.lastOpts.Size != "1536x1024" || client.lastOpts.Quality != "medium" {
		t.Errorf("client opts = %+v, want merged defaults", client.lastOpts)
	}
}

func TestLoadFromConfig_NativeImageToolRegistration(t *testing.T) {
	t.Parallel()

	// Native capability on the AI client registers the native tool.
	registry, err := LoadFromConfigWithOptions(t.TempDir(), &ToolsConfig{AIClient: &imageCapableStubClient{}})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}
	tool, ok := registry.Get("image_gen")
	if !ok {
		t.Fatal("image_gen tool is not registered with an image-capable AI client")
	}
	if tool.Description() == "" {
		t.Error("image_gen has empty description")
	}

	// No capability and no API key: tool absent.
	registry, err = LoadFromConfigWithOptions(t.TempDir(), &ToolsConfig{AIClient: &plainStubClient{}})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}
	if _, ok := registry.Get("image_gen"); ok {
		t.Fatal("image_gen must not be registered without capability or API key")
	}

	// No capability but an OpenAI API key: legacy DALL-E path registers.
	registry, err = LoadFromConfigWithOptions(t.TempDir(), &ToolsConfig{AIClient: &plainStubClient{}, OpenAIAPIKey: "sk-test"})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}
	if _, ok := registry.Get("image_gen"); !ok {
		t.Fatal("image_gen legacy path must register with an OpenAI API key")
	}
}
