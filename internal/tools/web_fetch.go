package tools

import (
	"context"
	"fmt"
	"io"
	"net"
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

	// Validate URL and check for SSRF
	if err := validateURL(urlStr); err != nil {
		return "", err
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

	// Try readability extraction first
	article, err := ExtractArticle(string(body), urlStr)
	var content, title string
	var metadata strings.Builder

	if err == nil && article.Length > 100 {
		// Readability succeeded with meaningful content
		title = article.Title
		content = article.Content

		// Add metadata if available
		if article.Byline != "" {
			metadata.WriteString(fmt.Sprintf("Author: %s\n", article.Byline))
		}
		if article.SiteName != "" {
			metadata.WriteString(fmt.Sprintf("Site: %s\n", article.SiteName))
		}
		if article.Excerpt != "" {
			metadata.WriteString(fmt.Sprintf("Excerpt: %s\n", article.Excerpt))
		}
	} else {
		// Fall back to basic extraction
		content, title = w.extractContent(string(body))
	}

	// Format result
	result := fmt.Sprintf("**%s**\n\nURL: %s\n%s\n%s", title, urlStr, metadata.String(), content)

	// Truncate if too long (increased from 8KB to 12KB)
	if len(result) > 12000 {
		result = result[:12000] + "\n\n... (truncated)"
	}

	return result, nil
}

// isPrivateIP checks if an IP address is private/internal
func isPrivateIP(ip net.IP) bool {
	// Check for loopback addresses
	if ip.IsLoopback() {
		return true
	}

	// Check for link-local addresses
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Normalize to IPv4 if it's an IPv4-mapped IPv6 address
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Check for private IPv4 ranges
	privateIPv4Ranges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"0.0.0.0/8",
		"169.254.0.0/16", // link-local
	}

	for _, cidr := range privateIPv4Ranges {
		_, subnet, _ := net.ParseCIDR(cidr)
		if subnet != nil && subnet.Contains(ip) {
			return true
		}
	}

	// Check for private IPv6 ranges (only if it's actually IPv6)
	if len(ip) == net.IPv6len {
		privateIPv6Ranges := []string{
			"::1/128",   // loopback
			"fe80::/10", // link-local
			"fc00::/7",  // unique local address
			"ff00::/8",  // multicast
		}

		for _, cidr := range privateIPv6Ranges {
			_, subnet, _ := net.ParseCIDR(cidr)
			if subnet != nil && subnet.Contains(ip) {
				return true
			}
		}
	}

	return false
}

// validateURL validates a URL and checks for SSRF vulnerabilities
func validateURL(rawURL string) error {
	// Parse URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Only allow http/https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are supported")
	}

	// Extract hostname
	hostname := parsedURL.Hostname()
	if hostname == "" {
		return fmt.Errorf("invalid URL: missing hostname")
	}

	// Block localhost variations
	if hostname == "localhost" || hostname == "0.0.0.0" {
		return fmt.Errorf("requests to localhost are not allowed")
	}

	// Resolve hostname to IP addresses
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("failed to resolve hostname: %w", err)
	}

	// Check each resolved IP
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("requests to private IP addresses are not allowed: %s", ip.String())
		}
	}

	return nil
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
