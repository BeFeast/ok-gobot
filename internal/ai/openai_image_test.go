package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newImageTestClient(baseURL string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		config:     ProviderConfig{Name: "openai", BaseURL: baseURL, APIKey: "test-key"},
		httpClient: &http.Client{},
	}
}

// The whole point of this method is that AsImageGenerator finds it: image_gen
// registers by capability, so a client that cannot be discovered is the same as
// no image generation at all.
func TestOpenAICompatibleClientIsDiscoverableAsImageGenerator(t *testing.T) {
	t.Parallel()

	var client Client = newImageTestClient("http://example.invalid/v1")
	if _, ok := AsImageGenerator(client); !ok {
		t.Fatal("OpenAI-compatible client is not discoverable as an image generator")
	}
}

func TestOpenAICompatibleGenerateImage(t *testing.T) {
	t.Parallel()

	want := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	var gotPath, gotAuth string
	var gotBody openAIImageRequest
	var sawResponseFormat bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, sawResponseFormat = raw["response_format"]
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(want) + `","revised_prompt":"a tidier prompt"}]}`))
	}))
	defer server.Close()

	res, err := newImageTestClient(server.URL+"/v1").GenerateImage(
		context.Background(), "  a green circle  ", ImageGenOptions{Model: "gpt-image-2", Size: "1536x1024", Quality: "high"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if string(res.PNG) != string(want) {
		t.Fatalf("PNG bytes = %q", res.PNG)
	}
	if res.RevisedPrompt != "a tidier prompt" {
		t.Fatalf("RevisedPrompt = %q", res.RevisedPrompt)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want /v1/images/generations", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody.Prompt != "a green circle" {
		t.Fatalf("prompt not trimmed: %q", gotBody.Prompt)
	}
	if gotBody.Model != "gpt-image-2" || gotBody.Size != "1536x1024" || gotBody.Quality != "high" {
		t.Fatalf("body = %+v", gotBody)
	}
	// gpt-image models reject response_format on OpenAI itself, so sending it
	// would break the direct endpoint while changing nothing on the proxy.
	if sawResponseFormat {
		t.Fatal("request must not send response_format")
	}
}

func TestOpenAICompatibleGenerateImageDefaultsAndOmissions(t *testing.T) {
	t.Parallel()

	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte("x")) + `"}]}`))
	}))
	defer server.Close()

	if _, err := newImageTestClient(server.URL).GenerateImage(context.Background(), "prompt", ImageGenOptions{}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if raw["model"] != defaultChatGPTImageModel {
		t.Fatalf("model = %v, want default %s", raw["model"], defaultChatGPTImageModel)
	}
	if raw["size"] != defaultImageSize {
		t.Fatalf("size = %v, want default %s", raw["size"], defaultImageSize)
	}
	if _, ok := raw["quality"]; ok {
		t.Fatal("empty quality must be omitted, not sent as an empty string")
	}
}

func TestOpenAICompatibleGenerateImageFailures(t *testing.T) {
	t.Parallel()

	if _, err := newImageTestClient("http://example.invalid").GenerateImage(context.Background(), "   ", ImageGenOptions{}); err == nil {
		t.Fatal("expected an empty prompt to fail before any request")
	}

	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"http error", http.StatusUnauthorized, `{"error":"nope"}`, "status 401"},
		{"no data", http.StatusOK, `{"data":[]}`, "no image data"},
		{"empty b64", http.StatusOK, `{"data":[{"b64_json":""}]}`, "no image data"},
		{"bad base64", http.StatusOK, `{"data":[{"b64_json":"!!!not-base64!!!"}]}`, "decode base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, err := newImageTestClient(server.URL).GenerateImage(context.Background(), "prompt", ImageGenOptions{})
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}
