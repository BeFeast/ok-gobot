package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
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

	remoteStartupWindow           = 20 * time.Second
	remoteDiscoveryMaxAttempts    = 32
	remoteDiscoveryInitialBackoff = 200 * time.Millisecond
	remoteDiscoveryMaxBackoff     = 2 * time.Second
	remoteDiscoveryMaxBodySize    = 1 << 20
	remoteLivenessProbeTimeout    = 2 * time.Second
)

type profileConfig struct {
	name       string
	persistent bool
	headless   bool
}

type remoteDiscoveryPolicy struct {
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	startupWindow  time.Duration
}

type remoteTransportConnectFunc func(context.Context, string) (*profileInstance, error)

type remoteDiscoveryError struct {
	endpoint         string
	attempts         int
	cause            error
	lastAttemptCause error
}

type remoteProfileLaunch struct {
	ctx                context.Context
	done               chan struct{}
	cancel             context.CancelFunc
	accepting          bool
	waiters            int
	generation         uint64
	replacesGeneration uint64
	profileEpoch       uint64
	inst               *profileInstance
	err                error
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
	generation  uint64

	allocCtx      context.Context
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	// transportCtx owns the retained remote allocator/browser lifetime. It has
	// no deadline after successful setup, but can be cancelled explicitly when
	// the first attach times out, the connection is lost, or all waiters leave.
	transportCtx    context.Context
	transportCancel context.CancelFunc

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
	// nextRemoteGeneration gives each cached remote transport a stable identity.
	// Pointer checks remain authoritative; the number is structured log evidence.
	nextRemoteGeneration uint64
	// profileEpoch changes whenever StopProfile wins. An in-flight probe or
	// rebuild from an older epoch must never resurrect the stopped profile.
	profileEpoch map[string]uint64

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
	remoteLiveness func(ctx context.Context) error

	httpClient             *http.Client
	remoteDiscovery        remoteDiscoveryPolicy
	remoteDiscoveryWait    func(context.Context, time.Duration) error
	remoteTransportConnect remoteTransportConnectFunc
	remoteProbeTimeout     time.Duration

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
		profileEpoch:   make(map[string]uint64),
		snapshotCache:  make(map[string]snapshotCacheEntry),
		httpClient: &http.Client{
			Timeout: healthProbeTimeout,
		},
		remoteDiscovery: remoteDiscoveryPolicy{
			maxAttempts:    remoteDiscoveryMaxAttempts,
			initialBackoff: remoteDiscoveryInitialBackoff,
			maxBackoff:     remoteDiscoveryMaxBackoff,
			startupWindow:  remoteStartupWindow,
		},
		remoteProbeTimeout: remoteLivenessProbeTimeout,
		enableSignals:      enableSignals,
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
	m.remoteLiveness = func(ctx context.Context) error {
		_, err := m.listTargets(ctx)
		return err
	}
	m.remoteTransportConnect = m.connectRemoteTransport

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
	// Advance every valid profile epoch even when no instance has been
	// published yet, so Stop also wins the tiny pre-launch window.
	m.profileEpoch[ProfileOpenclaw]++
	m.profileEpoch[ProfileEphemeral]++

	profiles := make(map[string]struct{}, len(m.instances)+len(m.remoteLaunches))
	for name, launch := range m.remoteLaunches {
		profiles[name] = struct{}{}
		launch.accepting = false
		launch.cancel()
	}
	for name := range m.instances {
		profiles[name] = struct{}{}
	}
	for name := range profiles {
		m.stopProfileLocked(name)
	}
	m.clearSnapshotCache()
}

// StopProfile closes a single named profile if running.
func (m *Manager) StopProfile(profile string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profileEpoch[profile]++
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
		// This is intentionally only a cached-state hint. Action paths call
		// ensureRemoteProfile and perform the bounded CDP liveness preflight.
		return inst.browserCtx != nil && inst.browserCtx.Err() == nil
	}
	return m.healthFn(inst.debugPort) == nil
}

// UsesRemoteCDP reports whether this manager connects to an external browser.
func (m *Manager) UsesRemoteCDP() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.RemoteDebugURL != ""
}

// ProfileGeneration returns the current remote transport generation. Local
// profiles and profiles without a cached instance return zero.
func (m *Manager) ProfileGeneration(profile string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst := m.instances[profile]; inst != nil && m.RemoteDebugURL != "" {
		return inst.generation
	}
	return 0
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
	m.mu.Lock()
	cfg, err := m.profileConfigLocked(profile)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	requestEpoch := m.profileEpoch[profile]
	m.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		m.mu.Lock()
		if m.profileEpoch[profile] != requestEpoch {
			m.mu.Unlock()
			return nil, context.Canceled
		}
		if inst, ok := m.instances[profile]; ok {
			if inst.generation == 0 {
				m.nextRemoteGeneration++
				inst.generation = m.nextRemoteGeneration
			} else if inst.generation > m.nextRemoteGeneration {
				m.nextRemoteGeneration = inst.generation
			}
			m.mu.Unlock()

			probeErr := m.probeRemoteInstance(ctx, inst)
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			m.mu.Lock()
			if m.profileEpoch[profile] != requestEpoch {
				m.mu.Unlock()
				return nil, context.Canceled
			}
			if current := m.instances[profile]; current != inst {
				m.mu.Unlock()
				continue
			}
			if probeErr == nil {
				m.mu.Unlock()
				return inst, nil
			}

			// The pointer comparison above makes this the single winning stale
			// eviction. A replacement launch is published under the same lock,
			// so every concurrent caller joins exactly that generation.
			delete(m.instances, profile)
			log.Printf("[browser.cdp] event=stale_detected profile=%q generation=%d error=%q", profile, inst.generation, probeErr)
			launch, userDataDir, debugPort, launchErr := m.newRemoteProfileLaunchLocked(profile, cfg, requestEpoch, inst.generation)
			if launchErr == nil {
				log.Printf("[browser.cdp] event=rebuild_started profile=%q stale_generation=%d generation=%d", profile, inst.generation, launch.generation)
			}
			m.mu.Unlock()

			m.clearSnapshotCache()
			go m.cleanupInstance(inst)
			if launchErr != nil {
				return nil, launchErr
			}
			go m.runRemoteProfileLaunch(profile, launch, cfg, userDataDir, debugPort)
			return m.waitForRemoteProfileLaunch(ctx, profile, launch)
		}
		if launch, ok := m.remoteLaunches[profile]; ok {
			if !launch.accepting || launch.profileEpoch != requestEpoch {
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

		launch, userDataDir, debugPort, err := m.newRemoteProfileLaunchLocked(profile, cfg, requestEpoch, 0)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}

		go m.runRemoteProfileLaunch(profile, launch, cfg, userDataDir, debugPort)
		return m.waitForRemoteProfileLaunch(ctx, profile, launch)
	}
}

func (m *Manager) newRemoteProfileLaunchLocked(profile string, cfg profileConfig, profileEpoch, replacesGeneration uint64) (*remoteProfileLaunch, string, int, error) {
	userDataDir, err := m.prepareUserDataDirLocked(cfg)
	if err != nil {
		return nil, "", 0, err
	}
	debugPort, err := findAvailablePort()
	if err != nil {
		if !cfg.persistent {
			_ = os.RemoveAll(userDataDir)
		}
		return nil, "", 0, err
	}
	m.ensureSignalHandlerLocked()

	m.nextRemoteGeneration++
	launchCtx, launchCancel := context.WithCancel(context.Background())
	launch := &remoteProfileLaunch{
		ctx:                launchCtx,
		done:               make(chan struct{}),
		cancel:             launchCancel,
		accepting:          true,
		waiters:            1,
		generation:         m.nextRemoteGeneration,
		replacesGeneration: replacesGeneration,
		profileEpoch:       profileEpoch,
	}
	m.remoteLaunches[profile] = launch
	return launch, userDataDir, debugPort, nil
}

func (m *Manager) probeRemoteInstance(callerCtx context.Context, inst *profileInstance) error {
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	if err := callerCtx.Err(); err != nil {
		return err
	}
	if inst == nil || inst.browserCtx == nil {
		return errors.New("remote browser context is missing")
	}

	timeout := m.remoteProbeTimeout
	if timeout <= 0 {
		timeout = remoteLivenessProbeTimeout
	}

	// The first target materialisation must use a long-lived transport context.
	// The browser transport may already have been allocated by the retrying
	// Browser.getVersion handshake while Target is still nil. Running that first
	// target attach on a timeout child would bind Target.run to the short context,
	// then cache an already-cancelled target as the shared tool tab.
	if chromedpCtx := chromedp.FromContext(inst.browserCtx); chromedpCtx != nil && chromedpCtx.Target == nil {
		if inst.transportCtx == nil || inst.transportCancel == nil {
			return errors.New("uninitialized remote transport has no lifetime context")
		}

		timedOut := make(chan struct{})
		timer := time.AfterFunc(timeout, func() {
			inst.transportCancel()
			close(timedOut)
		})
		stopCallerBridge := context.AfterFunc(callerCtx, inst.transportCancel)
		err := m.remoteLiveness(inst.transportCtx)
		if !timer.Stop() {
			<-timedOut
		}
		stopCallerBridge()
		if callerErr := callerCtx.Err(); callerErr != nil {
			inst.transportCancel()
			return callerErr
		}
		select {
		case <-timedOut:
			return context.DeadlineExceeded
		default:
		}
		if err != nil {
			inst.transportCancel()
		}
		return err
	}

	probeCtx, cancelProbe := context.WithTimeout(inst.browserCtx, timeout)
	stopCallerBridge := context.AfterFunc(callerCtx, cancelProbe)
	err := m.remoteLiveness(probeCtx)
	stopCallerBridge()
	cancelProbe()
	if callerErr := callerCtx.Err(); callerErr != nil {
		return callerErr
	}
	return err
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
		inst.generation = launch.generation
		if inst.userDataDir == "" {
			inst.userDataDir = userDataDir
		}
		if inst.debugPort == 0 {
			inst.debugPort = debugPort
		}
		if probeErr := m.probeRemoteInstance(launch.ctx, inst); probeErr != nil {
			launchErr = fmt.Errorf("remote CDP generation %d failed liveness validation: %w", launch.generation, probeErr)
		} else if chromedpCtx := chromedp.FromContext(inst.browserCtx); chromedpCtx != nil && chromedpCtx.Target != nil {
			// Validation materialises one long-lived remote target. Reuse it as
			// the shared tool tab instead of creating an otherwise visible extra
			// about:blank target on the first browser command.
			m.attachNavigationInvalidation(inst.browserCtx)
			inst.tabCtx = inst.browserCtx
		}
	}
	if launchErr != nil && !cfg.persistent {
		_ = os.RemoveAll(userDataDir)
	}

	var cleanup *profileInstance
	m.mu.Lock()
	if ctxErr := launch.ctx.Err(); ctxErr != nil {
		if inst != nil {
			cleanup = inst
			inst = nil
		}
		if launchErr == nil || !errors.Is(launchErr, ctxErr) {
			launchErr = ctxErr
		}
	} else if m.profileEpoch[profile] != launch.profileEpoch || m.remoteLaunches[profile] != launch {
		if inst != nil {
			cleanup = inst
			inst = nil
		}
		launchErr = context.Canceled
	} else if launchErr != nil && inst != nil {
		cleanup = inst
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
	if launch.replacesGeneration != 0 {
		if launchErr == nil {
			log.Printf("[browser.cdp] event=rebuild_completed profile=%q stale_generation=%d generation=%d success=true", profile, launch.replacesGeneration, launch.generation)
		} else {
			log.Printf("[browser.cdp] event=rebuild_completed profile=%q stale_generation=%d generation=%d success=false error=%q", profile, launch.replacesGeneration, launch.generation, launchErr)
		}
	}
	close(launch.done)
	m.mu.Unlock()
	if cleanup != nil {
		m.cleanupInstance(cleanup)
	}
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
	if inst.transportCancel != nil {
		inst.transportCancel()
		inst.transportCtx, inst.transportCancel = nil, nil
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
// Only the safe pre-action discovery, WebSocket dial, and Browser.getVersion
// handshake retry. Allocator target creation and every browser action remain
// single-attempt.
func (m *Manager) connectRemote(ctx context.Context, cfg profileConfig, debugPort int) (*profileInstance, error) {
	policy := m.normalizedRemoteDiscoveryPolicy()

	startupCtx, cancel := context.WithTimeout(ctx, policy.startupWindow)
	defer cancel()

	connectTransport := m.remoteTransportConnect
	if connectTransport == nil {
		connectTransport = m.connectRemoteTransport
	}
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		webSocketURL, err := m.discoverRemoteWebSocketURL(startupCtx, policy)
		if err != nil {
			return nil, err
		}

		dialURL, err := normalizeRemoteWebSocketURL(startupCtx, webSocketURL)
		if err == nil {
			var inst *profileInstance
			inst, err = connectTransport(startupCtx, dialURL)
			if err == nil && inst == nil {
				err = errors.New("runtime transport connector returned a nil instance")
			}
			if err == nil {
				inst.name = cfg.name
				inst.persistent = cfg.persistent
				inst.debugPort = debugPort
				return inst, nil
			}
			err = fmt.Errorf("connect runtime browser transport: %w", err)
		} else {
			err = fmt.Errorf("prepare runtime WebSocket URL: %w", err)
		}

		err = remoteDiagnosticContextError(startupCtx, err)
		if retryErr := m.waitForRemoteHandshakeRetry(startupCtx, policy, attempt, err); retryErr != nil {
			return nil, remoteCheckFailure(RemoteCheckWebSocket, retryErr)
		}
	}

	return nil, remoteCheckFailure(RemoteCheckWebSocket, errors.New("remote CDP runtime transport exhausted its retry policy"))
}

// remoteRuntimeHandshakeValidator observes the exact bytes read and written by
// the chromedp.Conn owned by the retained Browser. It is enabled only around
// Browser.getVersion. The callback never formats or logs protocol frames.
type remoteRuntimeHandshakeValidator struct {
	active atomic.Bool

	mu               sync.Mutex
	cancelTransport  context.CancelFunc
	requestID        int64
	requestObserved  bool
	responseObserved bool
	firstErr         error
}

func (v *remoteRuntimeHandshakeValidator) activate(cancelTransport context.CancelFunc) {
	v.mu.Lock()
	v.cancelTransport = cancelTransport
	v.requestID = 0
	v.requestObserved = false
	v.responseObserved = false
	v.firstErr = nil
	v.active.Store(true)
	v.mu.Unlock()
}

func (v *remoteRuntimeHandshakeValidator) disable() (protocolErr error, requestObserved, responseObserved bool) {
	// Store false before acquiring the mutex. A callback that already observed
	// true must acquire the same mutex and re-check it, so returning from this
	// method is also a barrier against in-flight validation.
	v.active.Store(false)
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.firstErr, v.requestObserved, v.responseObserved
}

func (v *remoteRuntimeHandshakeValidator) debugf(format string, args ...any) {
	if !v.active.Load() {
		return
	}
	payload, ok := remoteDiagnosticDebugPayload(format, args...)
	if !ok {
		return
	}

	var message cdproto.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		v.fail(fmt.Errorf("decode %s Browser.getVersion frame: %w", remoteDiagnosticFrameDirection(format), err))
		return
	}
	shape, err := parseRemoteDiagnosticFrameShape(payload)
	if err != nil {
		v.fail(fmt.Errorf("decode %s Browser.getVersion frame shape: %w", remoteDiagnosticFrameDirection(format), err))
		return
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.active.Load() {
		return
	}
	if format == "-> %s" {
		v.validateOutboundLocked(message, shape)
		return
	}
	v.validateInboundLocked(message, shape)
}

func remoteDiagnosticFrameDirection(format string) string {
	if format == "-> %s" {
		return "outbound"
	}
	return "inbound"
}

func (v *remoteRuntimeHandshakeValidator) validateOutboundLocked(message cdproto.Message, shape remoteDiagnosticFrameShape) {
	if !shape.methodPresent {
		v.failLocked(errors.New("outbound Browser.getVersion command has no method field"))
		return
	}
	if err := validateRemoteDiagnosticNonEmptyString("method", shape.method); err != nil {
		v.failLocked(err)
		return
	}
	if message.Method != cdpbrowser.CommandGetVersion {
		v.failLocked(fmt.Errorf("unexpected outbound method %q during Browser.getVersion handshake", message.Method))
		return
	}
	if !shape.idPresent {
		v.failLocked(errors.New("outbound Browser.getVersion command has no positive request ID"))
		return
	}
	if err := validateRemoteDiagnosticPositiveID(shape.id); err != nil {
		v.failLocked(err)
		return
	}
	if shape.sessionIDPresent {
		v.failLocked(fmt.Errorf("outbound Browser.getVersion command has session ID %q, want empty", message.SessionID))
		return
	}
	if shape.resultPresent || shape.errorPresent {
		v.failLocked(errors.New("outbound Browser.getVersion command contains response result or error"))
		return
	}
	if shape.paramsPresent {
		if err := validateRemoteDiagnosticObject("command params", shape.params); err != nil {
			v.failLocked(err)
			return
		}
	}
	if v.requestObserved {
		v.failLocked(errors.New("duplicate outbound Browser.getVersion command during one handshake"))
		return
	}
	v.requestID = message.ID
	v.requestObserved = true
}

func (v *remoteRuntimeHandshakeValidator) validateInboundLocked(message cdproto.Message, shape remoteDiagnosticFrameShape) {
	isEvent, err := classifyRemoteDiagnosticFrameShape(message, shape)
	if err != nil {
		v.failLocked(err)
		return
	}
	if isEvent {
		return
	}
	if !v.requestObserved {
		v.failLocked(fmt.Errorf("Browser.getVersion response ID %d arrived before its outbound request was observed", message.ID))
		return
	}
	if message.ID != v.requestID {
		v.failLocked(fmt.Errorf("Browser.getVersion response ID %d, want %d", message.ID, v.requestID))
		return
	}
	if shape.sessionIDPresent {
		v.failLocked(fmt.Errorf("Browser.getVersion response session ID %q, want empty", message.SessionID))
		return
	}
	if v.responseObserved {
		v.failLocked(fmt.Errorf("duplicate Browser.getVersion response ID %d", message.ID))
		return
	}
	v.responseObserved = true
}

func (v *remoteRuntimeHandshakeValidator) fail(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.active.Load() {
		return
	}
	v.failLocked(err)
}

func (v *remoteRuntimeHandshakeValidator) failLocked(err error) {
	if v.firstErr != nil {
		return
	}
	v.firstErr = fmt.Errorf("invalid remote CDP handshake: %w", err)
	if v.cancelTransport != nil {
		v.cancelTransport()
	}
}

// connectRemoteTransport establishes the exact browser transport retained by
// the runtime. Calling Allocator.Allocate directly performs the real WebSocket
// dial without chromedp.Run, so Browser.getVersion remains safely retryable and
// no target/context action has started yet.
func (m *Manager) connectRemoteTransport(ctx context.Context, webSocketURL string) (_ *profileInstance, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(
		context.Background(),
		webSocketURL,
		chromedp.NoModifyURL,
	)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	transportCtx, transportCancel := context.WithCancel(browserCtx)
	inst := &profileInstance{
		allocCtx:        allocCtx,
		allocCancel:     allocCancel,
		browserCtx:      browserCtx,
		browserCancel:   browserCancel,
		transportCtx:    transportCtx,
		transportCancel: transportCancel,
	}
	defer func() {
		if retErr != nil {
			m.cleanupInstance(inst)
		}
	}()

	// The allocator must own a deadline-free lifetime context after startup,
	// while the startup deadline must still be able to interrupt its real dial.
	stopStartupCancellation := context.AfterFunc(ctx, transportCancel)
	defer stopStartupCancellation()

	chromedpCtx := chromedp.FromContext(transportCtx)
	if chromedpCtx == nil || chromedpCtx.Allocator == nil {
		return nil, chromedp.ErrInvalidContext
	}

	validator := &remoteRuntimeHandshakeValidator{}
	browserOpts := []chromedp.BrowserOption{chromedp.WithBrowserDebugf(validator.debugf)}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		browserOpts = append(browserOpts, chromedp.WithDialTimeout(remaining))
	}
	connectedBrowser, err := chromedpCtx.Allocator.Allocate(transportCtx, browserOpts...)
	if err != nil {
		return nil, remoteDiagnosticContextError(ctx, err)
	}
	chromedpCtx.Browser = connectedBrowser

	handshakeCtx, cancelHandshake := context.WithCancel(ctx)
	stopTransportCancellation := context.AfterFunc(transportCtx, cancelHandshake)
	validator.activate(transportCancel)
	protocolVersion, product, _, _, _, err := cdpbrowser.GetVersion().Do(
		cdp.WithExecutor(handshakeCtx, connectedBrowser),
	)
	protocolErr, requestObserved, responseObserved := validator.disable()
	stopTransportCancellation()
	cancelHandshake()
	if protocolErr != nil {
		return nil, protocolErr
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if transportCtx.Err() != nil {
			return nil, fmt.Errorf("browser transport closed during Browser.getVersion: %w", io.EOF)
		}
		return nil, remoteDiagnosticContextError(ctx, err)
	}
	if !requestObserved || !responseObserved {
		return nil, fmt.Errorf(
			"Browser.getVersion raw handshake observation incomplete: request=%t response=%t",
			requestObserved,
			responseObserved,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := transportCtx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(product) == "" || strings.TrimSpace(protocolVersion) == "" {
		return nil, errors.New("Browser.getVersion returned incomplete version data")
	}
	if !stopStartupCancellation() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("remote CDP startup cancellation raced with Browser.getVersion")
	}

	return inst, nil
}

func (m *Manager) normalizedRemoteDiscoveryPolicy() remoteDiscoveryPolicy {
	policy := m.remoteDiscovery
	if policy.maxAttempts <= 0 {
		policy.maxAttempts = remoteDiscoveryMaxAttempts
	}
	if policy.initialBackoff < 0 {
		policy.initialBackoff = 0
	}
	if policy.maxBackoff <= 0 {
		policy.maxBackoff = remoteDiscoveryMaxBackoff
	}
	if policy.startupWindow <= 0 {
		policy.startupWindow = remoteStartupWindow
	}
	return policy
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

		backoff := cappedRemoteDiscoveryBackoff(policy, attempt)
		if err := m.waitForRemoteDiscoveryRetry(ctx, backoff); err != nil {
			return "", &remoteDiscoveryError{endpoint: endpoint, attempts: attempt, cause: err, lastAttemptCause: lastErr}
		}
	}

	return "", &remoteDiscoveryError{endpoint: endpoint, attempts: policy.maxAttempts, cause: lastErr}
}

func cappedRemoteDiscoveryBackoff(policy remoteDiscoveryPolicy, attempt int) time.Duration {
	backoff := policy.initialBackoff
	for step := 1; step < attempt && backoff < policy.maxBackoff; step++ {
		if backoff > policy.maxBackoff/2 {
			return policy.maxBackoff
		}
		backoff *= 2
	}
	if backoff > policy.maxBackoff {
		return policy.maxBackoff
	}
	return backoff
}

func (m *Manager) waitForRemoteDiscoveryRetry(ctx context.Context, delay time.Duration) error {
	if m.remoteDiscoveryWait != nil {
		return m.remoteDiscoveryWait(ctx, delay)
	}
	return waitForRemoteDiscoveryRetry(ctx, delay)
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
	return m.ListTabsContext(context.Background(), profile)
}

// ListTabsContext lists page targets after remote transport preflight while
// preserving caller cancellation. The target-list command itself is never
// retried after dispatch.
func (m *Manager) ListTabsContext(ctx context.Context, profile string) ([]TabInfo, error) {
	inst, err := m.profileForAction(ctx, profile)
	if err != nil {
		return nil, err
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

func (m *Manager) profileForAction(ctx context.Context, profile string) (*profileInstance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	remote := m.RemoteDebugURL != ""
	inst, ok := m.instances[profile]
	m.mu.Unlock()
	if remote {
		return m.ensureRemoteProfile(ctx, profile)
	}
	if !ok {
		return nil, fmt.Errorf("profile %s is not running", profile)
	}
	return inst, nil
}

// FocusTab activates a tab by target ID.
func (m *Manager) FocusTab(profile string, targetID string) error {
	return m.FocusTabContext(context.Background(), profile, targetID)
}

// FocusTabContext preflights a remote transport, then dispatches one activate.
func (m *Manager) FocusTabContext(ctx context.Context, profile string, targetID string) error {
	inst, err := m.profileForAction(ctx, profile)
	if err != nil {
		return err
	}
	return m.activateTarget(inst.browserCtx, target.ID(targetID))
}

// CloseTab closes a tab by target ID.
func (m *Manager) CloseTab(profile string, targetID string) error {
	return m.CloseTabContext(context.Background(), profile, targetID)
}

// CloseTabContext preflights a remote transport, then dispatches one close.
func (m *Manager) CloseTabContext(ctx context.Context, profile string, targetID string) error {
	inst, err := m.profileForAction(ctx, profile)
	if err != nil {
		return err
	}
	return m.closeTarget(inst.browserCtx, target.ID(targetID))
}

// ContextForTarget returns a chromedp context attached to the given target.
func (m *Manager) ContextForTarget(profile string, targetID string) (context.Context, context.CancelFunc, error) {
	return m.ContextForTargetContext(context.Background(), profile, targetID)
}

// ContextForTargetContext attaches only after remote transport preflight.
func (m *Manager) ContextForTargetContext(ctx context.Context, profile string, targetID string) (context.Context, context.CancelFunc, error) {
	inst, err := m.profileForAction(ctx, profile)
	if err != nil {
		return nil, nil, err
	}

	tabCtx, cancel := chromedp.NewContext(inst.browserCtx, chromedp.WithTargetID(target.ID(targetID)))
	m.attachNavigationInvalidation(tabCtx)
	return tabCtx, cancel, nil
}

func (m *Manager) defaultListTargets(ctx context.Context) ([]*target.Info, error) {
	var targets []*target.Info
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		chromedpCtx := chromedp.FromContext(actionCtx)
		if chromedpCtx == nil || chromedpCtx.Browser == nil {
			return chromedp.ErrInvalidContext
		}
		var err error
		targets, err = target.GetTargets().Do(cdp.WithExecutor(actionCtx, chromedpCtx.Browser))
		return err
	}))
	return targets, err
}

func (m *Manager) defaultActivateTarget(ctx context.Context, id target.ID) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		chromedpCtx := chromedp.FromContext(actionCtx)
		if chromedpCtx == nil || chromedpCtx.Browser == nil {
			return chromedp.ErrInvalidContext
		}
		return target.ActivateTarget(id).Do(cdp.WithExecutor(actionCtx, chromedpCtx.Browser))
	}))
}

func (m *Manager) defaultCloseTarget(ctx context.Context, id target.ID) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		chromedpCtx := chromedp.FromContext(actionCtx)
		if chromedpCtx == nil || chromedpCtx.Browser == nil {
			return chromedp.ErrInvalidContext
		}
		return target.CloseTarget(id).Do(cdp.WithExecutor(actionCtx, chromedpCtx.Browser))
	}))
}
