package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	// ProfileOpenclaw is the persistent browser profile.
	ProfileOpenclaw = "openclaw"
	// ProfileEphemeral is a clean headless profile created per session.
	ProfileEphemeral = "ephemeral"

	startupHealthTimeout = 30 * time.Second
	healthProbeInterval  = 200 * time.Millisecond
	healthProbeTimeout   = 2 * time.Second
)

type profileConfig struct {
	name       string
	persistent bool
	headless   bool
}

type profileInstance struct {
	name        string
	persistent  bool
	userDataDir string
	debugPort   int

	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

// Manager handles Chrome browser profile instances.
type Manager struct {
	ProfilePath    string
	UserDataDir    string
	ChromePath     string // explicit path to Chrome/Chromium binary; empty = auto-detect
	RemoteDebugURL string // connect to existing browser instead of launching (e.g. http://127.0.0.1:9222)
	Headless       bool

	mu        sync.Mutex
	instances map[string]*profileInstance

	snapshotMu    sync.RWMutex
	snapshotCache map[string]snapshotCacheEntry

	resolveTabID   tabIDResolver
	getFullAXTree  fullAXTreeGetter
	resolveNodeIDs nodeIDsResolver
	clickByNodeID  clickByNodeIDFunc
	typeByNodeID   typeByNodeIDFunc

	launchFn       func(cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error)
	healthFn       func(port int) error
	listTargets    func(ctx context.Context) ([]*target.Info, error)
	activateTarget func(ctx context.Context, id target.ID) error
	closeTarget    func(ctx context.Context, id target.ID) error

	httpClient *http.Client

	enableSignals bool
	signalOnce    sync.Once
}

// NewManager creates a new browser manager
func NewManager(profilePath string) *Manager {
	return newManager(profilePath, true)
}

func newManager(profilePath string, enableSignals bool) *Manager {
	if profilePath == "" {
		homeDir, _ := os.UserHomeDir()
		profilePath = filepath.Join(homeDir, ".ok-gobot", "chrome-profile")
	}

	m := &Manager{
		ProfilePath:   profilePath,
		UserDataDir:   profilePath,
		Headless:      false, // Default to visible for user interaction
		instances:     make(map[string]*profileInstance),
		snapshotCache: make(map[string]snapshotCacheEntry),
		httpClient: &http.Client{
			Timeout: healthProbeTimeout,
		},
		enableSignals: enableSignals,
	}

	m.launchFn = m.launchProfile
	m.healthFn = m.healthCheckCDP
	m.resolveTabID = m.defaultTabIDForContext
	m.getFullAXTree = getFullAXTree
	m.resolveNodeIDs = pushNodesByBackendIDs
	m.clickByNodeID = m.defaultClickByNodeID
	m.typeByNodeID = m.defaultTypeByNodeID
	m.listTargets = m.defaultListTargets
	m.activateTarget = m.defaultActivateTarget
	m.closeTarget = m.defaultCloseTarget

	return m
}

// Start launches the default openclaw profile.
func (m *Manager) Start() error {
	return m.StartProfile(ProfileOpenclaw)
}

// StartProfile launches (or verifies) a named profile.
func (m *Manager) StartProfile(profile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.ensureProfileLocked(profile)
	return err
}

// Stop closes all running profile instances.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	profiles := make([]string, 0, len(m.instances))
	for name := range m.instances {
		profiles = append(profiles, name)
	}

	for _, name := range profiles {
		m.stopProfileLocked(name)
	}
	m.clearSnapshotCache()
}

// StopProfile closes a single named profile if running.
func (m *Manager) StopProfile(profile string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopProfileLocked(profile)
}

// IsRunning returns true if the default openclaw profile is running and healthy.
func (m *Manager) IsRunning() bool {
	return m.IsProfileRunning(ProfileOpenclaw)
}

// IsProfileRunning returns true if a named profile is running and healthy.
func (m *Manager) IsProfileRunning(profile string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.instances[profile]
	if !ok {
		return false
	}
	if m.RemoteDebugURL != "" {
		return inst.browserCtx != nil && inst.browserCtx.Err() == nil
	}
	return m.healthFn(inst.debugPort) == nil
}

// NewTab creates a new tab in the default openclaw profile.
func (m *Manager) NewTab() (context.Context, context.CancelFunc, error) {
	return m.NewTabForProfile(ProfileOpenclaw)
}

// NewTabForProfile creates a new tab for a named profile.
func (m *Manager) NewTabForProfile(profile string) (context.Context, context.CancelFunc, error) {
	m.mu.Lock()
	inst, err := m.ensureProfileLocked(profile)
	if err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	browserCtx := inst.browserCtx
	m.mu.Unlock()

	if browserCtx == nil {
		return nil, nil, fmt.Errorf("profile %s has no browser context", profile)
	}

	// For remote connections, reuse the browserCtx directly — remote
	// allocators may not support creating new targets via NewContext.
	if m.RemoteDebugURL != "" {
		noop := func() {}
		m.attachNavigationInvalidation(browserCtx)
		return browserCtx, noop, nil
	}

	ctx, cancel := chromedp.NewContext(browserCtx)
	m.attachNavigationInvalidation(ctx)
	return ctx, cancel, nil
}

func (m *Manager) ensureProfileLocked(profile string) (*profileInstance, error) {
	cfg, err := m.profileConfigLocked(profile)
	if err != nil {
		return nil, err
	}

	if inst, ok := m.instances[profile]; ok {
		// For remote connections, check if the browserCtx is still alive
		// instead of probing an HTTP port (which may not match).
		if m.RemoteDebugURL != "" {
			if inst.browserCtx != nil && inst.browserCtx.Err() == nil {
				return inst, nil
			}
		} else if err := m.healthFn(inst.debugPort); err == nil {
			return inst, nil
		}
		// Instance is unhealthy; restart this profile.
		m.stopProfileLocked(profile)
	}

	userDataDir, err := m.prepareUserDataDirLocked(cfg)
	if err != nil {
		return nil, err
	}
	debugPort, err := findAvailablePort()
	if err != nil {
		if !cfg.persistent {
			_ = os.RemoveAll(userDataDir)
		}
		return nil, err
	}

	m.ensureSignalHandlerLocked()

	inst, err := m.launchFn(cfg, userDataDir, debugPort)
	if err != nil {
		if !cfg.persistent {
			_ = os.RemoveAll(userDataDir)
		}
		return nil, err
	}

	inst.name = cfg.name
	inst.persistent = cfg.persistent
	if inst.userDataDir == "" {
		inst.userDataDir = userDataDir
	}
	if inst.debugPort == 0 {
		inst.debugPort = debugPort
	}

	// Skip waitForHealthy after fresh launch — launchProfile already
	// verified the browser via chromedp.Run(Navigate("about:blank")).
	// The HTTP /json endpoint may not be reachable even when CDP works
	// fine over the WebSocket managed by chromedp internally.

	m.instances[profile] = inst
	return inst, nil
}

func (m *Manager) stopProfileLocked(profile string) {
	inst, ok := m.instances[profile]
	if !ok {
		return
	}
	m.cleanupInstance(inst)
	delete(m.instances, profile)
}

func (m *Manager) cleanupInstance(inst *profileInstance) {
	if inst.browserCancel != nil {
		inst.browserCancel()
	}
	if inst.allocCancel != nil {
		inst.allocCancel()
	}
	if !inst.persistent && inst.userDataDir != "" {
		_ = os.RemoveAll(inst.userDataDir)
	}
}

func (m *Manager) profileConfigLocked(profile string) (profileConfig, error) {
	switch profile {
	case ProfileOpenclaw:
		return profileConfig{
			name:       ProfileOpenclaw,
			persistent: true,
			headless:   m.Headless,
		}, nil
	case ProfileEphemeral:
		return profileConfig{
			name:       ProfileEphemeral,
			persistent: false,
			headless:   true,
		}, nil
	default:
		return profileConfig{}, fmt.Errorf("unknown browser profile: %s", profile)
	}
}

func (m *Manager) prepareUserDataDirLocked(cfg profileConfig) (string, error) {
	if cfg.persistent {
		if err := os.MkdirAll(m.ProfilePath, 0o755); err != nil {
			return "", fmt.Errorf("failed to create profile directory: %w", err)
		}
		return m.ProfilePath, nil
	}
	dir, err := os.MkdirTemp("", "ok-gobot-ephemeral-*")
	if err != nil {
		return "", fmt.Errorf("failed to create ephemeral profile directory: %w", err)
	}
	return dir, nil
}

func (m *Manager) ensureSignalHandlerLocked() {
	if !m.enableSignals {
		return
	}
	m.signalOnce.Do(func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		go func() {
			for range sigCh {
				m.Stop()
			}
		}()
	})
}

func (m *Manager) launchProfile(cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
	// Remote mode: connect to an already-running browser via CDP.
	if m.RemoteDebugURL != "" {
		return m.connectRemote(cfg, debugPort)
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.UserDataDir(userDataDir),
		chromedp.Flag("disable-web-security", false),
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		chromedp.Flag("remote-debugging-port", fmt.Sprintf("%d", debugPort)),
	}

	if cfg.headless {
		opts = append(opts, chromedp.Headless)
	} else {
		opts = append(opts, chromedp.Flag("start-maximized", true))
	}

	if chromePath := m.findChrome(); chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	launchCtx, cancel := context.WithTimeout(browserCtx, startupHealthTimeout)
	defer cancel()
	var title string
	if err := chromedp.Run(launchCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Title(&title),
	); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("failed to launch %s profile: %w", cfg.name, err)
	}

	return &profileInstance{
		name:          cfg.name,
		persistent:    cfg.persistent,
		userDataDir:   userDataDir,
		debugPort:     debugPort,
		allocCtx:      allocCtx,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
	}, nil
}

// connectRemote attaches to an already-running browser via its CDP endpoint.
func (m *Manager) connectRemote(cfg profileConfig, debugPort int) (*profileInstance, error) {
	// Discover the DevTools WebSocket URL from the /json/version endpoint.
	resp, err := http.Get(m.RemoteDebugURL + "/json/version")
	if err != nil {
		return nil, fmt.Errorf("cannot reach browser at %s: %w", m.RemoteDebugURL, err)
	}
	defer resp.Body.Close()

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to parse /json/version: %w", err)
	}
	if info.WebSocketDebuggerURL == "" {
		return nil, fmt.Errorf("no webSocketDebuggerUrl in /json/version response")
	}

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), info.WebSocketDebuggerURL)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	return &profileInstance{
		name:          cfg.name,
		persistent:    true,
		debugPort:     debugPort,
		allocCtx:      allocCtx,
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
	}, nil
}

func (m *Manager) healthCheckCDP(port int) error {
	if port <= 0 {
		return errors.New("invalid debug port")
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/json", port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected CDP status: %s", resp.Status)
	}

	var targets []json.RawMessage
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&targets); err != nil {
		return fmt.Errorf("invalid CDP response: %w", err)
	}

	return nil
}

func (m *Manager) waitForHealthy(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := m.healthFn(port); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(healthProbeInterval)
	}

	if lastErr == nil {
		lastErr = errors.New("timed out waiting for CDP health endpoint")
	}
	return fmt.Errorf("profile health check failed: %w", lastErr)
}

func findAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to reserve debugging port: %w", err)
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		return 0, errors.New("failed to parse reserved debugging port")
	}
	return addr.Port, nil
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

// findChrome locates Chrome/Chromium executable.
// If ChromePath is set explicitly (via config), it is used directly.
func (m *Manager) findChrome() string {
	if m.ChromePath != "" {
		if _, err := os.Stat(m.ChromePath); err == nil {
			return m.ChromePath
		}
	}

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

// TabInfo describes an open browser tab.
type TabInfo struct {
	TargetID string `json:"target_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

// ListTabs returns all page-type targets in the given profile.
func (m *Manager) ListTabs(profile string) ([]TabInfo, error) {
	m.mu.Lock()
	inst, ok := m.instances[profile]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("profile %s is not running", profile)
	}

	targets, err := m.listTargets(inst.browserCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to list targets: %w", err)
	}

	var tabs []TabInfo
	for _, t := range targets {
		if t.Type != "page" {
			continue
		}
		tabs = append(tabs, TabInfo{
			TargetID: string(t.TargetID),
			Title:    t.Title,
			URL:      t.URL,
		})
	}
	return tabs, nil
}

// FocusTab activates a tab by target ID.
func (m *Manager) FocusTab(profile string, targetID string) error {
	m.mu.Lock()
	inst, ok := m.instances[profile]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("profile %s is not running", profile)
	}

	return m.activateTarget(inst.browserCtx, target.ID(targetID))
}

// CloseTab closes a tab by target ID.
func (m *Manager) CloseTab(profile string, targetID string) error {
	m.mu.Lock()
	inst, ok := m.instances[profile]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("profile %s is not running", profile)
	}

	return m.closeTarget(inst.browserCtx, target.ID(targetID))
}

// ContextForTarget returns a chromedp context attached to the given target.
func (m *Manager) ContextForTarget(profile string, targetID string) (context.Context, context.CancelFunc, error) {
	m.mu.Lock()
	inst, ok := m.instances[profile]
	m.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("profile %s is not running", profile)
	}

	ctx, cancel := chromedp.NewContext(inst.browserCtx, chromedp.WithTargetID(target.ID(targetID)))
	m.attachNavigationInvalidation(ctx)
	return ctx, cancel, nil
}

func (m *Manager) defaultListTargets(ctx context.Context) ([]*target.Info, error) {
	return target.GetTargets().Do(ctx)
}

func (m *Manager) defaultActivateTarget(ctx context.Context, id target.ID) error {
	return target.ActivateTarget(id).Do(ctx)
}

func (m *Manager) defaultCloseTarget(ctx context.Context, id target.ID) error {
	return chromedp.Run(ctx, target.CloseTarget(id))
}
