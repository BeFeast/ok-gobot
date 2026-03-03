package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// TabInfo represents information about a browser tab.
type TabInfo struct {
	ID     string
	URL    string
	Title  string
	Active bool
}

// Tab holds a browser tab's context and metadata.
type Tab struct {
	ID     string
	Ctx    context.Context
	Cancel context.CancelFunc
}

// Manager handles Chrome browser instances with multi-tab support.
type Manager struct {
	ProfilePath   string
	Headless      bool
	ScreenshotDir string

	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc

	tabs      map[string]*Tab
	activeTab string
	tabSeq    int
	mu        sync.Mutex
}

// NewManager creates a new browser manager.
func NewManager(profilePath string) *Manager {
	if profilePath == "" {
		homeDir, _ := os.UserHomeDir()
		profilePath = filepath.Join(homeDir, ".ok-gobot", "chrome-profile")
	}

	homeDir, _ := os.UserHomeDir()
	screenshotDir := filepath.Join(homeDir, ".ok-gobot", "screenshots")

	return &Manager{
		ProfilePath:   profilePath,
		Headless:      false,
		ScreenshotDir: screenshotDir,
		tabs:          make(map[string]*Tab),
	}
}

// Start launches Chrome with the configured profile and opens an initial tab.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.allocCtx != nil {
		return nil // already running
	}

	if err := os.MkdirAll(m.ProfilePath, 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}
	if err := os.MkdirAll(m.ScreenshotDir, 0755); err != nil {
		return fmt.Errorf("failed to create screenshot directory: %w", err)
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(m.ProfilePath),
		chromedp.Flag("disable-web-security", false),
	}

	if m.Headless {
		opts = append(opts, chromedp.Headless)
	} else {
		opts = append(opts, chromedp.Flag("start-maximized", true))
	}

	chromePath := m.findChrome()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}

	m.allocCtx, m.allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	m.browserCtx, m.browserCancel = chromedp.NewContext(m.allocCtx)

	// Run a no-op to force the browser process to start.
	if err := chromedp.Run(m.browserCtx); err != nil {
		m.allocCancel()
		m.allocCtx = nil
		m.browserCtx = nil
		m.browserCancel = nil
		return fmt.Errorf("failed to start Chrome: %w", err)
	}

	// Track the initial tab.
	m.tabSeq++
	tabID := fmt.Sprintf("tab_%d", m.tabSeq)
	m.tabs[tabID] = &Tab{ID: tabID, Ctx: m.browserCtx, Cancel: m.browserCancel}
	m.activeTab = tabID

	return nil
}

// Stop closes the browser and all tabs.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := range m.tabs {
		delete(m.tabs, id)
	}
	m.activeTab = ""

	if m.allocCancel != nil {
		m.allocCancel()
	}
	m.allocCtx = nil
	m.allocCancel = nil
	m.browserCtx = nil
	m.browserCancel = nil
}

// IsRunning returns true if the browser allocator has been started.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allocCtx != nil
}

// ActiveTabID returns the ID of the currently active tab.
func (m *Manager) ActiveTabID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeTab
}

// ActiveTabCtx returns the context for the currently active tab.
func (m *Manager) ActiveTabCtx() (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeTab == "" {
		return nil, fmt.Errorf("no active tab")
	}
	tab, ok := m.tabs[m.activeTab]
	if !ok {
		return nil, fmt.Errorf("active tab not found")
	}
	return tab.Ctx, nil
}

// NewTab creates a new browser tab context (for snapshot-based browser tool).
// Returns the context and cancel function directly without storing the tab.
func (m *Manager) NewTab() (context.Context, context.CancelFunc, error) {
	if m.allocCtx == nil {
		return nil, nil, fmt.Errorf("browser not started")
	}

	ctx, cancel := chromedp.NewContext(m.allocCtx)
	return ctx, cancel, nil
}

// OpenTab creates a new browser tab and makes it active. Returns the new tab ID.
func (m *Manager) OpenTab() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browserCtx == nil {
		return "", fmt.Errorf("browser not started")
	}

	ctx, cancel := chromedp.NewContext(m.browserCtx)
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		return "", fmt.Errorf("failed to create tab: %w", err)
	}

	m.tabSeq++
	tabID := fmt.Sprintf("tab_%d", m.tabSeq)
	m.tabs[tabID] = &Tab{ID: tabID, Ctx: ctx, Cancel: cancel}
	m.activeTab = tabID

	return tabID, nil
}

// CloseTab closes a specific tab by ID.
func (m *Manager) CloseTab(tabID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tab, ok := m.tabs[tabID]
	if !ok {
		return fmt.Errorf("tab not found: %s", tabID)
	}
	if len(m.tabs) == 1 {
		return fmt.Errorf("cannot close the last tab")
	}

	tab.Cancel()
	delete(m.tabs, tabID)

	if m.activeTab == tabID {
		for id := range m.tabs {
			m.activeTab = id
			break
		}
	}
	return nil
}

// FocusTab switches the active tab to the given ID.
func (m *Manager) FocusTab(tabID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tabs[tabID]; !ok {
		return fmt.Errorf("tab not found: %s", tabID)
	}
	m.activeTab = tabID
	return nil
}

// ListTabs returns info about all open tabs.
func (m *Manager) ListTabs() []TabInfo {
	m.mu.Lock()
	type snap struct {
		id     string
		ctx    context.Context
		active bool
	}
	snaps := make([]snap, 0, len(m.tabs))
	for id, tab := range m.tabs {
		snaps = append(snaps, snap{id: id, ctx: tab.Ctx, active: id == m.activeTab})
	}
	m.mu.Unlock()

	tabs := make([]TabInfo, 0, len(snaps))
	for _, s := range snaps {
		info := TabInfo{ID: s.id, Active: s.active}
		var url, title string
		if err := chromedp.Run(s.ctx,
			chromedp.Location(&url),
			chromedp.Title(&title),
		); err == nil {
			info.URL = url
			info.Title = title
		}
		tabs = append(tabs, info)
	}
	return tabs
}

// TabCount returns the number of open tabs.
func (m *Manager) TabCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tabs)
}

// Navigate navigates the active tab to the given URL.
func (m *Manager) Navigate(url string) error {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return err
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
		return fmt.Errorf("failed to navigate: %w", err)
	}
	// Best-effort wait for page load.
	_ = chromedp.Run(ctx, chromedp.WaitReady("body"))
	return nil
}

// Screenshot takes a full-page screenshot and saves it to the screenshot directory.
// Returns the file path.
func (m *Manager) Screenshot() (string, error) {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return "", err
	}

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}

	filename := fmt.Sprintf("screenshot_%s.png", time.Now().Format("20060102_150405"))
	savePath := filepath.Join(m.ScreenshotDir, filename)
	if err := os.WriteFile(savePath, buf, 0644); err != nil {
		return "", fmt.Errorf("failed to save screenshot: %w", err)
	}
	return savePath, nil
}

// Click clicks an element matching the CSS selector in the active tab.
func (m *Manager) Click(selector string) error {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return err
	}
	return chromedp.Run(ctx, chromedp.WaitVisible(selector), chromedp.Click(selector))
}

// Fill fills a form field in the active tab.
func (m *Manager) Fill(selector, value string) error {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return err
	}
	return chromedp.Run(ctx, chromedp.WaitVisible(selector), chromedp.SendKeys(selector, value))
}

// WaitVisible waits for an element to be visible in the active tab.
func (m *Manager) WaitVisible(selector string) error {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return err
	}
	return chromedp.Run(ctx, chromedp.WaitVisible(selector))
}

// GetText extracts text from an element in the active tab.
func (m *Manager) GetText(selector string) (string, error) {
	ctx, err := m.ActiveTabCtx()
	if err != nil {
		return "", err
	}
	var text string
	if err := chromedp.Run(ctx, chromedp.Text(selector, &text)); err != nil {
		return "", err
	}
	return text, nil
}

// IsChromeInstalled checks if Chrome is available on the system.
func (m *Manager) IsChromeInstalled() bool {
	return m.findChrome() != ""
}

// findChrome locates Chrome/Chromium executable.
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

// GetProfileInfo returns information about the Chrome profile.
func (m *Manager) GetProfileInfo() (*ProfileInfo, error) {
	info := &ProfileInfo{
		Path:       m.ProfilePath,
		Exists:     false,
		History:    false,
		Extensions: 0,
	}

	if _, err := os.Stat(m.ProfilePath); err == nil {
		info.Exists = true

		historyPath := filepath.Join(m.ProfilePath, "Default", "History")
		if _, err := os.Stat(historyPath); err == nil {
			info.History = true
		}

		extensionsPath := filepath.Join(m.ProfilePath, "Default", "Extensions")
		if entries, err := os.ReadDir(extensionsPath); err == nil {
			info.Extensions = len(entries)
		}
	}

	return info, nil
}

// ProfileInfo holds information about a Chrome profile.
type ProfileInfo struct {
	Path       string
	Exists     bool
	History    bool
	Extensions int
}
