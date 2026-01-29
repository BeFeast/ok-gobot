package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/chromedp/chromedp"
)

// Manager handles Chrome browser instances
type Manager struct {
	ProfilePath string
	UserDataDir string
	Headless    bool
	allocCtx    context.Context
	cancel      context.CancelFunc
}

// NewManager creates a new browser manager
func NewManager(profilePath string) *Manager {
	if profilePath == "" {
		homeDir, _ := os.UserHomeDir()
		profilePath = filepath.Join(homeDir, ".ok-gobot", "chrome-profile")
	}

	return &Manager{
		ProfilePath: profilePath,
		Headless:    false, // Default to visible for user interaction
	}
}

// Start launches Chrome with the configured profile
func (m *Manager) Start() error {
	// Ensure profile directory exists
	if err := os.MkdirAll(m.ProfilePath, 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}

	// Build Chrome options
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(m.ProfilePath),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-web-security", false),
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
	}

	if m.Headless {
		opts = append(opts, chromedp.Headless)
	} else {
		opts = append(opts, chromedp.Flag("start-maximized", true))
	}

	// Find Chrome executable
	chromePath := m.findChrome()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}

	// Create allocator context
	m.allocCtx, m.cancel = chromedp.NewExecAllocator(context.Background(), opts...)

	return nil
}

// Stop closes the browser
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// NewTab creates a new browser tab/context
func (m *Manager) NewTab() (context.Context, context.CancelFunc, error) {
	if m.allocCtx == nil {
		return nil, nil, fmt.Errorf("browser not started")
	}

	ctx, cancel := chromedp.NewContext(m.allocCtx)
	return ctx, cancel, nil
}

// Navigate navigates to a URL
func (m *Manager) Navigate(url string) chromedp.NavigateAction {
	return chromedp.Navigate(url)
}

// Click clicks on an element
func (m *Manager) Click(selector string) chromedp.QueryAction {
	return chromedp.Click(selector)
}

// Fill fills a form field
func (m *Manager) Fill(selector, value string) chromedp.QueryAction {
	return chromedp.SendKeys(selector, value)
}

// Screenshot takes a screenshot of the page
func (m *Manager) Screenshot(buf *[]byte) chromedp.EmulateAction {
	return chromedp.FullScreenshot(buf, 90)
}

// WaitVisible waits for an element to be visible
func (m *Manager) WaitVisible(selector string) chromedp.QueryAction {
	return chromedp.WaitVisible(selector)
}

// GetText extracts text from an element
func (m *Manager) GetText(selector string, text *string) chromedp.QueryAction {
	return chromedp.Text(selector, text)
}

// Execute runs JavaScript on the page
func (m *Manager) Execute(script string, result interface{}) chromedp.EvaluateAction {
	return chromedp.Evaluate(script, result)
}

// findChrome locates Chrome/Chromium executable
func (m *Manager) findChrome() string {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}

	if runtime.GOOS == "windows" {
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Try to find in PATH
	if cmd, err := exec.LookPath("google-chrome"); err == nil {
		return cmd
	}
	if cmd, err := exec.LookPath("chromium"); err == nil {
		return cmd
	}
	if cmd, err := exec.LookPath("chrome"); err == nil {
		return cmd
	}

	return ""
}

// IsChromeInstalled checks if Chrome is available
func (m *Manager) IsChromeInstalled() bool {
	return m.findChrome() != ""
}

// GetProfileInfo returns information about the Chrome profile
func (m *Manager) GetProfileInfo() (*ProfileInfo, error) {
	info := &ProfileInfo{
		Path:       m.ProfilePath,
		Exists:     false,
		History:    false,
		Extensions: 0,
	}

	// Check if profile exists
	if _, err := os.Stat(m.ProfilePath); err == nil {
		info.Exists = true

		// Check for history
		historyPath := filepath.Join(m.ProfilePath, "Default", "History")
		if _, err := os.Stat(historyPath); err == nil {
			info.History = true
		}

		// Count extensions
		extensionsPath := filepath.Join(m.ProfilePath, "Default", "Extensions")
		if entries, err := os.ReadDir(extensionsPath); err == nil {
			info.Extensions = len(entries)
		}
	}

	return info, nil
}

// ProfileInfo holds information about a Chrome profile
type ProfileInfo struct {
	Path       string
	Exists     bool
	History    bool
	Extensions int
}
