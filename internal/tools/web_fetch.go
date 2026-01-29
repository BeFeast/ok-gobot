package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebFetchTool fetches and extracts content from URLs
type WebFetchTool struct {
	client    *http.Client
	userAgent string
}

// NewWebFetchTool creates a new web fetch tool
func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		userAgent: "Mozilla/5.0 (compatible; OKGoBot/1.0)",
	}
}

func (w *WebFetchTool) Name() string {
	return "web_fetch"
}

func (w *WebFetchTool) Description() string {
	return "Fetch and extract content from a URL"
}

func (w *WebFetchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: web_fetch <url>")
	}

	urlStr := args[0]

	// Validate URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("only http/https URLs are supported")
	}

	// Fetch the page
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", w.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Read body with limit
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Extract content
	content, title := w.extractContent(string(body))

	// Format result
	result := fmt.Sprintf("**%s**\n\nURL: %s\n\n%s", title, urlStr, content)

	// Truncate if too long
	if len(result) > 8000 {
		result = result[:8000] + "\n\n... (truncated)"
	}

	return result, nil
}

// extractContent extracts main content from HTML
func (w *WebFetchTool) extractContent(htmlStr string) (string, string) {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return w.fallbackExtract(htmlStr), ""
	}

	var title string
	var content strings.Builder

	var extractText func(*html.Node)
	extractText = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// Skip script, style, nav, header, footer
			switch n.Data {
			case "script", "style", "nav", "header", "footer", "aside", "noscript":
				return
			case "title":
				if n.FirstChild != nil {
					title = n.FirstChild.Data
				}
				return
			}
		}

		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				content.WriteString(text)
				content.WriteString(" ")
			}
		}

		// Add newlines after block elements
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6", "li":
				content.WriteString("\n")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c)
		}

		// Add newlines after closing block elements
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6":
				content.WriteString("\n")
			}
		}
	}

	extractText(doc)

	// Clean up the content
	text := content.String()
	text = w.cleanText(text)

	return text, title
}

// fallbackExtract uses regex to extract text when HTML parsing fails
func (w *WebFetchTool) fallbackExtract(htmlStr string) string {
	// Remove script and style tags
	scriptRe := regexp.MustCompile(`(?is)<script.*?</script>`)
	htmlStr = scriptRe.ReplaceAllString(htmlStr, "")

	styleRe := regexp.MustCompile(`(?is)<style.*?</style>`)
	htmlStr = styleRe.ReplaceAllString(htmlStr, "")

	// Remove all HTML tags
	tagRe := regexp.MustCompile(`<[^>]+>`)
	text := tagRe.ReplaceAllString(htmlStr, " ")

	return w.cleanText(text)
}

// cleanText normalizes whitespace and removes excessive newlines
func (w *WebFetchTool) cleanText(text string) string {
	// Replace multiple spaces with single space
	spaceRe := regexp.MustCompile(`[ \t]+`)
	text = spaceRe.ReplaceAllString(text, " ")

	// Replace multiple newlines with double newline
	newlineRe := regexp.MustCompile(`\n{3,}`)
	text = newlineRe.ReplaceAllString(text, "\n\n")

	// Trim each line
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
