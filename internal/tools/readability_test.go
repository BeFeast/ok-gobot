package tools

import (
	"strings"
	"testing"
)

func TestExtractArticle(t *testing.T) {
	// Sample HTML with article content
	sampleHTML := `
<!DOCTYPE html>
<html>
<head>
	<title>Test Article Title</title>
	<meta name="author" content="John Doe">
</head>
<body>
	<header>
		<nav>
			<a href="/">Home</a>
			<a href="/about">About</a>
		</nav>
	</header>

	<article>
		<h1>Test Article Title</h1>
		<p class="byline">By John Doe</p>
		<p class="excerpt">This is a brief excerpt of the article.</p>

		<div class="content">
			<p>This is the first paragraph of the article. It contains important information
			that should be extracted by the readability parser.</p>

			<p>This is the second paragraph with more content. The readability algorithm
			should identify this as the main content and extract it properly.</p>

			<h2>A Subheading</h2>
			<p>More content under the subheading. This helps test that the parser can handle
			structured content with headings and paragraphs.</p>

			<ul>
				<li>First list item</li>
				<li>Second list item</li>
				<li>Third list item</li>
			</ul>

			<p>Final paragraph with concluding thoughts. This should also be included in
			the extracted content.</p>
		</div>
	</article>

	<aside>
		<h3>Related Articles</h3>
		<ul>
			<li><a href="/article1">Article 1</a></li>
			<li><a href="/article2">Article 2</a></li>
		</ul>
	</aside>

	<footer>
		<p>Copyright 2026</p>
	</footer>

	<script>
		console.log("This script should be removed");
	</script>

	<style>
		body { margin: 0; }
	</style>
</body>
</html>
`

	sourceURL := "https://example.com/article"

	article, err := ExtractArticle(sampleHTML, sourceURL)
	if err != nil {
		t.Fatalf("ExtractArticle failed: %v", err)
	}

	// Test that article was extracted
	if article == nil {
		t.Fatal("Expected article to be non-nil")
	}

	// Test title extraction
	if article.Title == "" {
		t.Error("Expected non-empty title")
	}
	t.Logf("Title: %s", article.Title)

	// Test content extraction
	if article.Content == "" {
		t.Error("Expected non-empty content")
	}
	if len(article.Content) < 50 {
		t.Errorf("Expected content length > 50, got %d", len(article.Content))
	}
	t.Logf("Content length: %d", len(article.Content))
	t.Logf("Content preview: %s", truncateString(article.Content, 200))

	// Test that main content is present
	if !strings.Contains(article.Content, "first paragraph") {
		t.Error("Expected content to contain 'first paragraph'")
	}
	if !strings.Contains(article.Content, "second paragraph") {
		t.Error("Expected content to contain 'second paragraph'")
	}

	// Test that navigation and footer are excluded
	if strings.Contains(article.Content, "Copyright 2026") {
		t.Error("Content should not contain footer text")
	}

	// Test that scripts are excluded
	if strings.Contains(article.Content, "console.log") {
		t.Error("Content should not contain script code")
	}

	// Log metadata
	if article.Byline != "" {
		t.Logf("Byline: %s", article.Byline)
	}
	if article.Excerpt != "" {
		t.Logf("Excerpt: %s", article.Excerpt)
	}
	if article.SiteName != "" {
		t.Logf("Site name: %s", article.SiteName)
	}
	t.Logf("Length: %d", article.Length)
}

func TestExtractArticle_EmptyHTML(t *testing.T) {
	emptyHTML := ""
	sourceURL := "https://example.com/empty"

	article, err := ExtractArticle(emptyHTML, sourceURL)
	if err == nil {
		// Some parsers might succeed but return minimal content
		if article != nil && article.Length > 0 {
			t.Logf("Parser handled empty HTML, length: %d", article.Length)
		}
	} else {
		t.Logf("Expected error for empty HTML: %v", err)
	}
}

func TestExtractArticle_InvalidURL(t *testing.T) {
	sampleHTML := "<html><body><p>Test content</p></body></html>"
	invalidURL := "://invalid"

	_, err := ExtractArticle(sampleHTML, invalidURL)
	if err == nil {
		t.Error("Expected error for invalid URL")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestCleanHTMLText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		notContains []string
	}{
		{
			name:  "Basic paragraph",
			input: "<p>Hello world</p>",
			contains: []string{"Hello world"},
			notContains: []string{"<p>", "</p>"},
		},
		{
			name:  "Multiple paragraphs",
			input: "<p>First</p><p>Second</p>",
			contains: []string{"First", "Second"},
			notContains: []string{"<p>"},
		},
		{
			name:  "Script removal",
			input: "<p>Content</p><script>alert('test')</script><p>More</p>",
			contains: []string{"Content", "More"},
			notContains: []string{"alert", "script"},
		},
		{
			name:  "Style removal",
			input: "<div>Text</div><style>body { color: red; }</style>",
			contains: []string{"Text"},
			notContains: []string{"color", "red", "style"},
		},
		{
			name:  "Nested tags",
			input: "<div><p><strong>Bold text</strong> normal text</p></div>",
			contains: []string{"Bold text", "normal text"},
			notContains: []string{"<strong>", "<p>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanHTMLText(tt.input)

			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("Expected result to contain '%s', got: %s", expected, result)
				}
			}

			for _, notExpected := range tt.notContains {
				if strings.Contains(result, notExpected) {
					t.Errorf("Expected result NOT to contain '%s', got: %s", notExpected, result)
				}
			}
		})
	}
}

// Helper function to truncate string for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
