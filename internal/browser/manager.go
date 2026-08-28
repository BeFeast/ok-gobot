package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

	remoteDiscoveryMaxAttempts    = 3
	remoteDiscoveryInitialBackoff = 200 * time.Millisecond
	remoteDiscoveryMaxBodySize    = 1 << 20
)

type profileConfig struct {
	name       string
	persistent bool
	headless   bool
}

type remoteDiscoveryPolicy struct {
	maxAttempts    int
	initialBackoff time.Duration
	startupWindow  time.Duration
}

type remoteDiscoveryError struct {
	endpoint         string
	attempts         int
	cause            error
	lastAttemptCause error
}

type remoteProfileLaunch struct {
	ctx       context.Context
	done      chan struct{}
	cancel    context.CancelFunc
	accepting bool
	waiters   int
	inst      *profileInstance
	err       error
}

func (e *remoteDiscoveryError) Error() string {
	if e.lastAttemptCause != nil && !errors.Is(e.cause, e.lastAttemptCause) {
		return fmt.Sprintf("remote CDP discovery at %s failed after %d attempt(s): %v (last attempt: %v)", e.endpoint, e.attempts, e.cause, e.lastAttemptCause)
	}
	return fmt.Sprintf("remote CDP discovery at %s failed after %d attempt(s): %v", e.endpoint, e.attempts, e.cause)
}

func (e *remoteDiscoveryError) Unwrap() error {
	return e.cause
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

	// Dedicated long-lived tab shared by every tool call on this profile.
	// It must be created (and its CDP target materialised) on a context that
	// outlives individual operations — see NewTabForProfile.
	tabCtx    context.Context
	tabCancel context.CancelFunc
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
	// remoteLaunches serializes remote profile startup without holding mu
	// across discovery I/O. Waiters can therefore honor their own contexts.
	remoteLaunches map[string]*remoteProfileLaunch

	snapshotMu    sync.RWMutex
	snapshotCache map[string]snapshotCacheEntry

	resolveTabID   tabIDResolver
	getFullAXTree  fullAXTreeGetter
	resolveNodeIDs nodeIDsResolver
	clickByNodeID  clickByNodeIDFunc
	typeByNodeID   typeByNodeIDFunc

	launchFn       func(ctx context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error)
	healthFn       func(port int) error
	listTargets    func(ctx context.Context) ([]*target.Info, error)
	activateTarget func(ctx context.Context, id target.ID) error
	closeTarget    func(ctx context.Context, id target.ID) error

	httpClient      *http.Client
	remoteDiscovery remoteDiscoveryPolicy

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
		ProfilePath:    profilePath,
		UserDataDir:    profilePath,
		Headless:       false, // Default to visible for user interaction
		instances:      make(map[string]*profileInstance),
		remoteLaunches: make(map[string]*remoteProfileLaunch),
		snapshotCache:  make(map[string]snapshotCacheEntry),
		httpClient: &http.Client{
			Timeout: healthProbeTimeout,
		},
		remoteDiscovery: remoteDiscoveryPolicy{
			maxAttempts:    remoteDiscoveryMaxAttempts,
			initialBackoff: remoteDiscoveryInitialBackoff,
			startupWindow:  startupHealthTimeout,
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
	return m.StartContext(context.Background())
}

// StartContext launches the default openclaw profile and propagates caller
// cancellation through remote endpoint discovery. Browser actions themselves
// are not retried.
func (m *Manager) StartContext(ctx context.Context) error {
	return m.StartProfileContext(ctx, ProfileOpenclaw)
}

// StartProfile launches (or verifies) a named profile.
func (m *Manager) StartProfile(profile string) error {
	return m.StartProfileContext(context.Background(), profile)
}

// StartProfileContext launches (or verifies) a named profile while allowing
// remote discovery to stop promptly when the caller is cancelled.
func (m *Manager) StartProfileContext(ctx context.Context, profile string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := m.ensureProfile(ctx, profile)
	return err
}

// Stop closes all running profile instances.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, launch := range m.remoteLaunches {
		launch.accepting = false
		launch.cancel()
	}

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
	if launch, ok := m.remoteLaunches[profile]; ok {
		launch.accepting = false
		launch.cancel()
	}
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
	return m.NewTabContext(context.Background())
}

// NewTabContext creates a new tab in the default openclaw profile while
// propagating caller cancellation to remote endpoint discovery.
func (m *Manager) NewTabContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	return m.NewTabForProfileContext(ctx, ProfileOpenclaw)
}

// NewTabForProfile creates a new tab for a named profile.
func (m *Manager) NewTabForProfile(profile string) (context.Context, context.CancelFunc, error) {
	return m.NewTabForProfileContext(context.Background(), profile)
}

// NewTabForProfileContext creates a new tab for a named profile while
// propagating caller cancellation to remote endpoint discovery.
func (m *Manager) NewTabForProfileContext(ctx context.Context, profile string) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	inst, err := m.ensureProfile(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	browserCtx := inst.browserCtx
	m.mu.Unlock()

	if browserCtx == nil {
		return nil, nil, fmt.Errorf("profile %s has no browser context", profile)
	}

	// Handing out browserCtx itself was the long-standing bug behind
	// "Document needs to be requested first" / "browser is not running":
	// callers wrap the returned context in context.WithTimeout and cancel it
	// per operation, and chromedp binds the CDP target to whichever context
	// first ran an action — so the first cancel killed the browser's own
	// target and every later call failed. Remote allocators DO support new
	// targets (verified against a remote endpoint 2026-08-21).
	//
	// Keep ONE long-lived tab per remote profile: navigation state survives
	// across tool calls (open -> snapshot), and per-op timeouts derived from
	// it can be cancelled freely because the target is bound to this context.
	if m.RemoteDebugURL != "" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if inst.tabCtx != nil && inst.tabCtx.Err() == nil {
			return inst.tabCtx, func() {}, nil
		}
		ctx, cancel := chromedp.NewContext(browserCtx)
		// Materialise the target on the long-lived context before any
		// timeout-wrapped operation can claim it.
		if err := chromedp.Run(ctx); err != nil {
			cancel()
			return nil, nil, fmt.Errorf("open tab for profile %s: %w", profile, err)
		}
		m.attachNavigationInvalidation(ctx)
		inst.tabCtx, inst.tabCancel = ctx, cancel
		// Callers must not tear down the shared tab; it dies with the profile.
		return ctx, func() {}, nil
	}

	ctx, cancel := chromedp.NewContext(browserCtx)
	m.attachNavigationInvalidation(ctx)
	return ctx, cancel, nil
}

func (m *Manager) ensureProfile(ctx context.Context, profile string) (*profileInstance, error) {
	m.mu.Lock()
	remote := m.RemoteDebugURL != ""
	if !remote {
		defer m.mu.Unlock()
		return m.ensureProfileLocked(ctx, profile)
	}
	m.mu.Unlock()

	return m.ensureRemoteProfile(ctx, profile)
}

func (m *Manager) ensureRemoteProfile(ctx context.Context, profile string) (*profileInstance, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		m.mu.Lock()
		cfg, err := m.profileConfigLocked(profile)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if inst, ok := m.instances[profile]; ok {
			if err := ctx.Err(); err != nil {
				m.mu.Unlock()
				return nil, err
			}
			if inst.browserCtx != nil && inst.browserCtx.Err() == nil {
				m.mu.Unlock()
				return inst, nil
			}
			m.stopProfileLocked(profile)
		}
		if launch, ok := m.remoteLaunches[profile]; ok {
			if !launch.accepting {
				done := launch.done
				m.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-done:
					continue
				}
			}
			launch.waiters++
			m.mu.Unlock()
			return m.waitForRemoteProfileLaunch(ctx, profile, launch)
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}

		userDataDir, err := m.prepareUserDataDirLocked(cfg)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		debugPort, err := findAvailablePort()
		if err != nil {
			if !cfg.persistent {
				_ = os.RemoveAll(userDataDir)
			}
			m.mu.Unlock()
			return nil, err
		}
		m.ensureSignalHandlerLocked()

		launchCtx, launchCancel := context.WithCancel(context.Background())
		launch := &remoteProfileLaunch{
			ctx:       launchCtx,
			done:      make(chan struct{}),
			cancel:    launchCancel,
			accepting: true,
			waiters:   1,
		}
		m.remoteLaunches[profile] = launch
		m.mu.Unlock()

		go m.runRemoteProfileLaunch(profile, launch, cfg, userDataDir, debugPort)
		return m.waitForRemoteProfileLaunch(ctx, profile, launch)
	}
}

func (m *Manager) waitForRemoteProfileLaunch(ctx context.Context, profile string, launch *remoteProfileLaunch) (*profileInstance, error) {
	select {
	case <-ctx.Done():
		m.releaseRemoteProfileWaiter(profile, launch)
		return nil, ctx.Err()
	case <-launch.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return launch.inst, launch.err
	}
}

func (m *Manager) releaseRemoteProfileWaiter(profile string, launch *remoteProfileLaunch) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.remoteLaunches[profile]
	if !ok || current != launch || !launch.accepting {
		return
	}
	if launch.waiters > 0 {
		launch.waiters--
	}
	if launch.waiters == 0 {
		launch.accepting = false
		launch.cancel()
	}
}

func (m *Manager) runRemoteProfileLaunch(profile string, launch *remoteProfileLaunch, cfg profileConfig, userDataDir string, debugPort int) {
	inst, launchErr := m.launchFn(launch.ctx, cfg, userDataDir, debugPort)
	if launchErr == nil && inst == nil {
		launchErr = errors.New("browser launch returned nil instance")
	}
	if launchErr == nil {
		inst.name = cfg.name
		inst.persistent = cfg.persistent
		if inst.userDataDir == "" {
			inst.userDataDir = userDataDir
		}
		if inst.debugPort == 0 {
			inst.debugPort = debugPort
		}
	}
	if launchErr != nil && !cfg.persistent {
		_ = os.RemoveAll(userDataDir)
	}

	m.mu.Lock()
	if ctxErr := launch.ctx.Err(); ctxErr != nil {
		if inst != nil {
			m.cleanupInstance(inst)
			inst = nil
		}
		if launchErr == nil || !errors.Is(launchErr, ctxErr) {
			launchErr = ctxErr
		}
	} else if launchErr != nil && inst != nil {
		m.cleanupInstance(inst)
		inst = nil
	}
	if launchErr == nil {
		m.instances[profile] = inst
	}
	launch.accepting = false
	launch.inst = inst
	launch.err = launchErr
	if current, ok := m.remoteLaunches[profile]; ok && current == launch {
		delete(m.remoteLaunches, profile)
	}
	close(launch.done)
	m.mu.Unlock()
	launch.cancel()
}

func (m *Manager) ensureProfileLocked(ctx context.Context, profile string) (*profileInstance, error) {
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

	inst, err := m.launchFn(ctx, cfg, userDataDir, debugPort)
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
	if inst.tabCancel != nil {
		inst.tabCancel()
		inst.tabCtx, inst.tabCancel = nil, nil
	}
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

func (m *Manager) launchProfile(ctx context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
	// Remote mode: connect to an already-running browser via CDP.
	if m.RemoteDebugURL != "" {
		return m.connectRemote(ctx, cfg, debugPort)
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
// Only the idempotent /json/version discovery request is retried; allocator,
// tab, navigation, click, and type operations remain single-attempt.
func (m *Manager) connectRemote(ctx context.Context, cfg profileConfig, debugPort int) (*profileInstance, error) {
	policy := m.remoteDiscovery
	if policy.maxAttempts <= 0 {
		policy.maxAttempts = remoteDiscoveryMaxAttempts
	}
	if policy.initialBackoff < 0 {
		policy.initialBackoff = 0
	}
	if policy.startupWindow <= 0 {
		policy.startupWindow = startupHealthTimeout
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, policy.startupWindow)
	defer cancel()

	webSocketURL, err := m.discoverRemoteWebSocketURL(discoveryCtx, policy)
	if err != nil {
		return nil, err
	}

	// Validation guarantees the /devtools/browser/ form. For that form,
	// chromedp's URL modifier performs only its required hostname-to-IP rewrite
	// (real Chrome rejects other Host headers); it does not issue another
	// /json/version request.
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), webSocketURL)
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

func (m *Manager) discoverRemoteWebSocketURL(ctx context.Context, policy remoteDiscoveryPolicy) (string, error) {
	endpoint := strings.TrimRight(m.RemoteDebugURL, "/") + "/json/version"
	var lastErr error

	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		webSocketURL, retryable, err := m.fetchRemoteWebSocketURL(ctx, endpoint)
		if err == nil {
			return webSocketURL, nil
		}
		lastErr = err
		if !retryable || attempt == policy.maxAttempts {
			return "", &remoteDiscoveryError{endpoint: endpoint, attempts: attempt, cause: lastErr}
		}

		backoff := policy.initialBackoff << (attempt - 1)
		if err := waitForRemoteDiscoveryRetry(ctx, backoff); err != nil {
			return "", &remoteDiscoveryError{endpoint: endpoint, attempts: attempt, cause: err, lastAttemptCause: lastErr}
		}
	}

	return "", &remoteDiscoveryError{endpoint: endpoint, attempts: policy.maxAttempts, cause: lastErr}
}

func (m *Manager) fetchRemoteWebSocketURL(ctx context.Context, endpoint string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, fmt.Errorf("create remote CDP discovery request: %w", err)
	}

	client := m.httpClient
	if client == nil {
		client = &http.Client{Timeout: healthProbeTimeout}
	}
	// Validate the discovery response itself. The default http.Client follows
	// redirects, which could otherwise hide a non-2xx response and issue extra
	// discovery GETs outside this attempt's accounting.
	discoveryClient := *client
	discoveryClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := discoveryClient.Do(req)
	if err != nil {
		return "", isRetryableRemoteTransportError(ctx, err), fmt.Errorf("request remote CDP discovery: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		err := fmt.Errorf("remote CDP discovery returned %s", resp.Status)
		retryable := resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout
		return "", retryable, err
	}

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, remoteDiscoveryMaxBodySize+1))
	if err != nil {
		return "", isRetryableRemoteTransportError(ctx, err), fmt.Errorf("read remote /json/version response: %w", err)
	}
	if len(body) > remoteDiscoveryMaxBodySize {
		return "", false, fmt.Errorf("remote /json/version response exceeds %d bytes", remoteDiscoveryMaxBodySize)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", false, fmt.Errorf("failed to parse remote /json/version response: %w", err)
	}
	if info.WebSocketDebuggerURL == "" {
		return "", false, errors.New("no webSocketDebuggerUrl in remote /json/version response")
	}
	if err := validateRemoteWebSocketURL(info.WebSocketDebuggerURL); err != nil {
		return "", false, fmt.Errorf("invalid webSocketDebuggerUrl in remote /json/version response: %q: %w", info.WebSocketDebuggerURL, err)
	}

	return info.WebSocketDebuggerURL, false, nil
}

func validateRemoteWebSocketURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return errors.New("missing hostname")
	}
	if parsed.Fragment != "" {
		return errors.New("fragments are not allowed")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("empty port")
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("invalid port %q", port)
		}
	}

	const browserPathMarker = "/devtools/browser/"
	browserPathIndex := strings.LastIndex(parsed.Path, browserPathMarker)
	if browserPathIndex < 0 || strings.TrimSpace(strings.Trim(parsed.Path[browserPathIndex+len(browserPathMarker):], "/")) == "" {
		return errors.New("missing browser target ID")
	}
	return nil
}

func isRetryableRemoteTransportError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	for _, target := range []error{
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		syscall.ETIMEDOUT,
		io.EOF,
		io.ErrUnexpectedEOF,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func waitForRemoteDiscoveryRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
