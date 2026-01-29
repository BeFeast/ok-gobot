package tools

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-shiori/go-readability"
)

// Article represents extracted article content
type Article struct {
	Title    string
	Content  string
	Byline   string
	Excerpt  string
	SiteName string
	Length   int
}

// ExtractArticle extracts article content from HTML using readability
func ExtractArticle(htmlContent string, sourceURL string) (*Article, error) {
	// Parse the source URL
	parsedURL, err := url.Parse(sourceURL)
	if err != nil {
		return nil, fmt.Errorf("invalid source URL: %w", err)
	}

	// Parse HTML using readability
	article, err := readability.FromReader(strings.NewReader(htmlContent), parsedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse article: %w", err)
	}

	// Extract text content from HTML
	textContent := extractTextFromHTML(article.Content)

	return &Article{
		Title:    article.Title,
		Content:  textContent,
		Byline:   article.Byline,
		Excerpt:  article.Excerpt,
		SiteName: article.SiteName,
		Length:   article.Length,
	}, nil
}

// extractTextFromHTML extracts plain text from HTML content
func extractTextFromHTML(htmlContent string) string {
	// Use simple text extraction from HTML string
	return cleanHTMLText(htmlContent)
}

// cleanHTMLText removes HTML tags and extracts text
func cleanHTMLText(htmlStr string) string {
	// Remove script and style content
	htmlStr = removeTagContent(htmlStr, "script")
	htmlStr = removeTagContent(htmlStr, "style")

	// Replace common block elements with newlines
	blockElements := []string{"p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr"}
	for _, elem := range blockElements {
		htmlStr = strings.ReplaceAll(htmlStr, "</"+elem+">", "\n")
		htmlStr = strings.ReplaceAll(htmlStr, "<"+elem+">", "\n")
	}

	// Remove all remaining HTML tags
	var result strings.Builder
	inTag := false
	for _, ch := range htmlStr {
		if ch == '<' {
			inTag = true
			continue
		}
		if ch == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(ch)
		}
	}

	text := result.String()

	// Clean up whitespace
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}

	return strings.Join(cleaned, "\n")
}

// removeTagContent removes content between opening and closing tags
func removeTagContent(html, tag string) string {
	openTag := "<" + tag
	closeTag := "</" + tag + ">"

	result := html
	for {
		start := strings.Index(result, openTag)
		if start == -1 {
			break
		}

		// Find the end of the opening tag
		tagEnd := strings.Index(result[start:], ">")
		if tagEnd == -1 {
			break
		}
		tagEnd += start + 1

		// Find the closing tag
		end := strings.Index(result[tagEnd:], closeTag)
		if end == -1 {
			break
		}
		end += tagEnd + len(closeTag)

		// Remove the content
		result = result[:start] + result[end:]
	}

	return result
}

// fallbackTextExtract is a simple regex-based text extractor
func fallbackTextExtract(htmlStr string) string {
	return cleanHTMLText(htmlStr)
}
