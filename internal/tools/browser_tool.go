package tools

import (
	"context"
	"strconv"

	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"ok-gobot/internal/browser"
	"ok-gobot/internal/logger"
)

// BrowserTool provides browser automation capabilities
type BrowserTool struct {
	manager *browser.Manager
	// startContext is a narrow seam for proving that cached tabs never bypass
	// manager preflight. Production always points at manager.StartContext.
	startContext func(context.Context) error

	mu         sync.Mutex
	tabs       map[string]*tabEntry // targetID -> entry
	active     string               // targetID of the focused tab
	profile    string               // current profile name
	generation uint64               // current remote CDP transport generation

	screenshotDir string
}

type tabEntry struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewBrowserTool creates a new browser tool
func NewBrowserTool(profilePath, chromePath, debugURL string) *BrowserTool {
	mgr := browser.NewManager(profilePath)
	if chromePath != "" {
		mgr.ChromePath = chromePath
	}
	if debugURL != "" {
		mgr.RemoteDebugURL = debugURL
		logger.Debugf("Browser: configured remote debug URL: %s", debugURL)
	} else {
		logger.Debugf("Browser: no remote debug URL, will launch locally")
	}
	b := &BrowserTool{
		manager: mgr,
		tabs:    make(map[string]*tabEntry),
		profile: browser.ProfileOpenclaw,
	}
	b.startContext = mgr.StartContext
	return b
}

func (b *BrowserTool) Name() string {
	return "browser"
}

func (b *BrowserTool) Description() string {
	return "Control a real Chrome browser. Commands: open [url], navigate <url>, screenshot, snapshot, click, type, fill, tabs, focus <target_id>, close [target_id], stop."
}

// Execute runs browser commands
func (b *BrowserTool) Execute(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 {
		return "", fmt.Errorf("usage: browser <open|navigate|snapshot|click|type|fill|screenshot|tabs|focus|close|stop>")
	}

	command := args[0]

	switch command {
	case "open", "start":
		url := ""
		if len(args) >= 2 {
			url = args[1]
		}
		return b.open(ctx, url)
	case "stop":
		return b.stop()
	case "navigate":
		if len(args) < 2 {
			return "", fmt.Errorf("URL required")
		}
		return b.navigate(ctx, args[1])
	case "snapshot":
		return b.snapshot(ctx)
	case "click":
		return b.clickDispatch(ctx, args[1:])
	case "type", "fill":
		return b.typeDispatch(ctx, args[1:])
	case "screenshot":
		return b.screenshotCmd(ctx)
	case "wait":
		if len(args) < 2 {
			return "", fmt.Errorf("selector required")
		}
		return b.wait(ctx, args[1])
	case "text":
		selector := ""
		if len(args) >= 2 {
			selector = args[1]
		}
		return b.getText(ctx, selector)
	case "tabs":
		return b.listTabs(ctx)
	case "focus":
		if len(args) < 2 {
			return "", fmt.Errorf("target_id required")
		}
		return b.focusTab(ctx, args[1])
	case "close":
		targetID := ""
		if len(args) >= 2 {
			targetID = args[1]
		}
		return b.closeTab(ctx, targetID)
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

// ExecuteJSON runs a browser command with structured JSON parameters.
func (b *BrowserTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := params["command"]
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	switch command {
	case "open", "start":
		return b.open(ctx, params["url"])
	case "stop":
		return b.stop()
	case "navigate":
		url := params["url"]
		if url == "" {
			return "", fmt.Errorf("url is required for navigate")
		}
		return b.navigate(ctx, url)
	case "snapshot":
		return b.snapshot(ctx)
	case "click":
		snapshotID := params["snapshot_id"]
		ref := params["ref"]
		selector := params["selector"]
		if snapshotID != "" && ref != "" {
			return b.clickByRef(ctx, snapshotID, ref)
		}
		if selector != "" {
			return b.clickCSS(ctx, selector)
		}
		return "", fmt.Errorf("click requires snapshot_id+ref or selector")
	case "type", "fill":
		value := params["value"]
		if value == "" {
			return "", fmt.Errorf("value is required for %s", command)
		}
		snapshotID := params["snapshot_id"]
		ref := params["ref"]
		selector := params["selector"]
		if snapshotID != "" && ref != "" {
			return b.typeByRef(ctx, snapshotID, ref, value)
		}
		if selector != "" {
			return b.fillCSS(ctx, selector, value)
		}
		return "", fmt.Errorf("%s requires snapshot_id+ref or selector", command)
	case "screenshot":
		return b.screenshotCmd(ctx)
	case "wait":
		// Models routinely call wait with no selector meaning "let the page
		// settle" — the first tool-call telemetry (2026-08-21) caught exactly
		// that as the only failure in an otherwise clean browser run. Treat it
		// as a bounded sleep instead of an error.
		selector := params["selector"]
		if selector == "" {
			return b.waitDuration(ctx, params["seconds"])
		}
		return b.wait(ctx, selector)
	case "text":
		return b.getText(ctx, params["selector"])
	case "tabs":
		return b.listTabs(ctx)
	case "focus":
		targetID := params["target_id"]
		if targetID == "" {
			return "", fmt.Errorf("target_id is required for focus")
		}
		return b.focusTab(ctx, targetID)
	case "close":
		return b.closeTab(ctx, params["target_id"])
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

func (b *BrowserTool) clickDispatch(ctx context.Context, args []string) (string, error) {
	switch len(args) {
	case 1:
		return b.clickCSS(ctx, args[0])
	case 2:
		return b.clickByRef(ctx, args[0], args[1])
	default:
		return "", fmt.Errorf("usage: browser click <selector> OR browser click <snapshot_id> <ref>")
	}
}

func (b *BrowserTool) typeDispatch(ctx context.Context, args []string) (string, error) {
	switch len(args) {
	case 2:
		return b.fillCSS(ctx, args[0], args[1])
	case 3:
		return b.typeByRef(ctx, args[0], args[1], args[2])
	default:
		return "", fmt.Errorf("usage: browser type <selector> <value> OR browser type <snapshot_id> <ref> <value>")
	}
}

// ensureRunning auto-starts browser and returns the active tab context.
func (b *BrowserTool) ensureRunning(ctx context.Context) (context.Context, error) {
	remote := b.manager.UsesRemoteCDP()
	if !remote && !b.manager.IsRunning() && !b.manager.IsChromeInstalled() {
		return nil, fmt.Errorf("Chrome not found. Please install Google Chrome.")
	}

	// Always enter the manager's preflight path before reusing a cached tab.
	// IsRunning is intentionally insufficient for remote CDP: a dead transport
	// can leave browserCtx.Err() nil until the next protocol command.
	logger.Debugf("Browser: preflighting browser transport")
	if err := b.startContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to start browser: %w", err)
	}
	generation := b.manager.ProfileGeneration(b.profile)

	b.mu.Lock()
	defer b.mu.Unlock()
	if remote && generation != b.generation {
		logger.Debugf("Browser: remote CDP generation changed: %d -> %d", b.generation, generation)
		b.clearTabsLocked()
		b.generation = generation
	}
	b.pruneDeadTabsLocked()

	if b.active != "" {
		if entry, ok := b.tabs[b.active]; ok {
			logger.Debugf("Browser: reusing active tab %s", b.active)
			return entry.ctx, nil
		} else {
			b.active = ""
		}
	}

	// Create an initial tab.
	logger.Debugf("Browser: creating new tab")
	tabCtx, cancel, err := b.manager.NewTabContext(ctx)
	if err != nil {
		return nil, err
	}

	targetID := b.targetIDFromCtx(tabCtx)
	b.tabs[targetID] = &tabEntry{ctx: tabCtx, cancel: cancel}
	b.active = targetID
	logger.Debugf("Browser: new tab created: %s", targetID)
	return tabCtx, nil
}

func (b *BrowserTool) open(ctx context.Context, url string) (string, error) {
	if !b.manager.UsesRemoteCDP() && !b.manager.IsChromeInstalled() {
		return "", fmt.Errorf("Chrome not found. Please install Google Chrome.")
	}

	if err := b.startContext(ctx); err != nil {
		return "", err
	}
	generation := b.manager.ProfileGeneration(b.profile)

	// Reset stale tab state after (re-)start.
	b.mu.Lock()
	b.clearTabsLocked()
	b.generation = generation
	b.mu.Unlock()

	if url != "" {
		return b.navigate(ctx, url)
	}
	return "Browser opened", nil
}

func (b *BrowserTool) stop() (string, error) {
	b.mu.Lock()
	b.clearTabsLocked()
	b.generation = 0
	b.mu.Unlock()

	b.manager.Stop()
	return "Browser stopped", nil
}

// NOTE: Root chromedp contexts treat cancellation as "close tab", so the persistent
// activeCtx must never be cancelled between tool calls. However, derived contexts
// created via context.WithTimeout(tabCtx, ...) only abort the current operation —
// they do NOT close the tab. Use browserOpCtx() to get a per-operation context.

const browserOpTimeout = 60 * time.Second

// axSnapshotTimeout caps Accessibility.getFullAXTree. Measured 2026-08-29:
// after navigate to ksp.co.il/web/account the AX call burned the full 60s
// op timeout twice, while Runtime.evaluate of document.body.innerText on the
// same settled tab returned in 2ms (orders already in the DOM).
const axSnapshotTimeout = 8 * time.Second

const pageTextTimeout = 5 * time.Second
const pageTextMaxChars = 12000

// cssProbeTimeout is the budget to ask the page whether a CSS selector exists
// and is visible. WaitVisible on a missing selector used the full
// browserOpTimeout (measured 2026-08-29: 16 clicks at 60s each burned two
// 10-minute browser_task runs against ksp.co.il/web/account).
const cssProbeTimeout = 2 * time.Second

// browserOpCtx returns a context for a single chromedp operation with a timeout.
// Cancelling this context aborts the operation but does NOT close the tab.
func browserOpCtx(tabCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(tabCtx, browserOpTimeout)
}

type cssProbeResult struct {
	Found   bool `json:"found"`
	Visible bool `json:"visible"`
}

func probeCSSSelector(tabCtx context.Context, selector string) (cssProbeResult, error) {
	var result cssProbeResult
	ctx, cancel := context.WithTimeout(tabCtx, cssProbeTimeout)
	defer cancel()

	js := fmt.Sprintf(`(() => {
		let el;
		try { el = document.querySelector(%s); } catch (e) { return {found:false, visible:false}; }
		if (!el) return {found:false, visible:false};
		const st = window.getComputedStyle(el);
		const r = el.getBoundingClientRect();
		const visible = st.visibility !== 'hidden' && st.display !== 'none' && st.opacity !== '0' && r.width > 0 && r.height > 0;
		return {found:true, visible:visible};
	})()`, strconv.Quote(selector))

	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
		return result, err
	}
	return result, nil
}

func cssSelectorUnavailableError(op, selector string, probe cssProbeResult) error {
	if !probe.Found {
		return fmt.Errorf("%s selector %q not found; do not retry this CSS selector. Snapshot the page or extract text instead", op, selector)
	}
	return fmt.Errorf("%s selector %q is not visible; do not retry this CSS selector. Snapshot the page or extract text instead", op, selector)
}

func requireVisibleCSSSelector(tabCtx context.Context, op, selector string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("%s selector is empty; snapshot the page or extract text instead", op)
	}
	probe, err := probeCSSSelector(tabCtx, selector)
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, selector, err)
	}
	if !probe.Found || !probe.Visible {
		return cssSelectorUnavailableError(op, selector, probe)
	}
	return nil
}

// validateBrowserURL blocks dangerous URL schemes and private/loopback destinations.
// The network allowlist is enforced by the networkPolicyGuard wrapper before
// Execute is called, so this function only handles baseline SSRF protection.
// When allowInternal is true (via AllowInternalNetworks in the capability
// policy), loopback/private/internal hostname checks are skipped.
func validateBrowserURL(rawURL string, allowInternal bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "file" {
		return fmt.Errorf("file:// URLs are not allowed in the browser tool")
	}
	if scheme == "" {
		return fmt.Errorf("browser navigation requires an http or https URL")
	}
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %s", scheme)
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return fmt.Errorf("invalid URL: missing hostname")
	}
	if !allowInternal {
		if isHostPrivateOrLoopback(hostname) {
			return fmt.Errorf("navigation to private/loopback/link-local hosts is not allowed")
		}
	}
	return nil
}

func (b *BrowserTool) navigate(ctx context.Context, navURL string) (string, error) {
	if policy := NetworkPolicyFromContext(ctx); policy != nil {
		if denial := CheckNetworkTarget("browser", navURL, policy); denial != nil {
			return "", denial
		}
	} else {
		if err := validateBrowserURL(navURL, false); err != nil {
			return "", err
		}
	}

	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}

	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()

	if err := chromedp.Run(ctx, chromedp.Navigate(navURL)); err != nil {
		return "", fmt.Errorf("failed to navigate: %w", err)
	}

	// Wait briefly for page to settle
	if err := chromedp.Run(ctx, chromedp.WaitReady("body")); err != nil {
		logger.Debugf("Browser: WaitReady after navigate: %v", err)
	}

	return fmt.Sprintf("Navigated to %s", navURL), nil
}

func (b *BrowserTool) snapshot(ctx context.Context) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}

	text, textErr := readPageText(tabCtx)

	axCtx, axCancel := context.WithTimeout(tabCtx, axSnapshotTimeout)
	defer axCancel()
	snapshotID, nodes, axErr := b.manager.Snapshot(axCtx)
	if nodes == nil {
		nodes = []browser.AXNode{}
	}

	if axErr != nil && (textErr != nil || strings.TrimSpace(text) == "") {
		return "", fmt.Errorf("failed to create snapshot: accessibility tree: %v; page text: %v", axErr, textErr)
	}

	return encodeBrowserSnapshot(snapshotID, nodes, text, axErr)
}

func encodeBrowserSnapshot(snapshotID string, nodes []browser.AXNode, text string, axErr error) (string, error) {
	payload := map[string]interface{}{
		"snapshot_id": snapshotID,
		"nodes":       nodes,
		"text":        text,
	}
	if axErr != nil {
		payload["ax_error"] = axErr.Error()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode snapshot response: %w", err)
	}
	return string(encoded), nil
}

func readPageText(tabCtx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(tabCtx, pageTextTimeout)
	defer cancel()
	var text string
	js := fmt.Sprintf(`(() => {
		const root = document.body || document.documentElement;
		const t = (root && root.innerText) || '';
		return String(t).slice(0, %d);
	})()`, pageTextMaxChars)
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &text)); err != nil {
		return "", err
	}
	return text, nil
}

func (b *BrowserTool) clickByRef(ctx context.Context, snapshotID, ref string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()
	if err := b.manager.ClickByRef(ctx, snapshotID, ref); err != nil {
		return "", fmt.Errorf("click failed: %w", err)
	}
	return fmt.Sprintf("Clicked ref %s (snapshot %s)", ref, snapshotID), nil
}

func (b *BrowserTool) clickCSS(ctx context.Context, selector string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}
	if err := clickCSSOnTab(tabCtx, selector); err != nil {
		return "", err
	}
	return fmt.Sprintf("Clicked %s", selector), nil
}

func clickCSSOnTab(tabCtx context.Context, selector string) error {
	if err := requireVisibleCSSSelector(tabCtx, "click", selector); err != nil {
		return fmt.Errorf("failed to click: %w", err)
	}
	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Click(selector, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("failed to click %q: %w", selector, err)
	}
	return nil
}

func (b *BrowserTool) typeByRef(ctx context.Context, snapshotID, ref, value string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()
	if err := b.manager.TypeByRef(ctx, snapshotID, ref, value); err != nil {
		return "", fmt.Errorf("type failed: %w", err)
	}
	return fmt.Sprintf("Typed into ref %s (snapshot %s)", ref, snapshotID), nil
}

func (b *BrowserTool) fillCSS(ctx context.Context, selector, value string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}
	if err := requireVisibleCSSSelector(tabCtx, "fill", selector); err != nil {
		return "", fmt.Errorf("failed to fill: %w", err)
	}
	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.SendKeys(selector, value, chromedp.ByQuery)); err != nil {
		return "", fmt.Errorf("failed to fill %q: %w", selector, err)
	}
	return fmt.Sprintf("Filled %s", selector), nil
}

func (b *BrowserTool) screenshotCmd(ctx context.Context) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}

	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}

	dir := b.screenshotDir
	if dir == "" {
		homeDir, _ := os.UserHomeDir()
		dir = filepath.Join(homeDir, ".ok-gobot", "screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create screenshot directory: %w", err)
	}

	filename := fmt.Sprintf("screenshot_%s.png", time.Now().Format("20060102_150405"))
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("failed to save screenshot: %w", err)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"path":       path,
		"size_bytes": len(buf),
	})
	return string(payload), nil
}

// waitDuration sleeps for a bounded time when wait is called without a
// selector. Capped so a hallucinated "seconds": 600 cannot burn the task budget.
func (b *BrowserTool) waitDuration(ctx context.Context, secondsParam string) (string, error) {
	seconds := 2.0
	if secondsParam != "" {
		if v, err := strconv.ParseFloat(secondsParam, 64); err == nil && v > 0 {
			seconds = v
		}
	}
	if seconds > 10 {
		seconds = 10
	}
	if _, err := b.ensureRunning(ctx); err != nil {
		return "", err
	}
	time.Sleep(time.Duration(seconds * float64(time.Second)))
	return fmt.Sprintf("Waited %.1fs", seconds), nil
}

func (b *BrowserTool) wait(ctx context.Context, selector string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}

	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()

	if err := chromedp.Run(ctx, chromedp.WaitVisible(selector)); err != nil {
		return "", fmt.Errorf("timeout waiting for element: %w", err)
	}

	return fmt.Sprintf("Element %s is visible", selector), nil
}

func (b *BrowserTool) getText(ctx context.Context, selector string) (string, error) {
	tabCtx, err := b.ensureRunning(ctx)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(selector)
	if trimmed == "" || trimmed == "body" || trimmed == "html" {
		text, err := readPageText(tabCtx)
		if err != nil {
			return "", fmt.Errorf("failed to get page text: %w", err)
		}
		return text, nil
	}
	if err := requireVisibleCSSSelector(tabCtx, "text", selector); err != nil {
		return "", fmt.Errorf("failed to get text: %w", err)
	}

	ctx, cancel := browserOpCtx(tabCtx)
	defer cancel()

	var text string
	if err := chromedp.Run(ctx, chromedp.Text(selector, &text, chromedp.ByQuery)); err != nil {
		return "", fmt.Errorf("failed to get text %q: %w", selector, err)
	}

	return text, nil
}

// --- Tab management ---

func (b *BrowserTool) listTabs(ctx context.Context) (string, error) {
	tabs, err := b.manager.ListTabsContext(ctx, b.profile)
	if err != nil {
		return "", err
	}
	b.syncRemoteGeneration()

	b.mu.Lock()
	activeID := b.active
	b.mu.Unlock()

	type tabOut struct {
		TargetID string `json:"target_id"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		Active   bool   `json:"active"`
	}
	out := make([]tabOut, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, tabOut{
			TargetID: t.TargetID,
			Title:    t.Title,
			URL:      t.URL,
			Active:   t.TargetID == activeID,
		})
	}

	payload, _ := json.Marshal(out)
	return string(payload), nil
}

func (b *BrowserTool) focusTab(ctx context.Context, targetID string) (string, error) {
	if err := b.manager.FocusTabContext(ctx, b.profile, targetID); err != nil {
		return "", fmt.Errorf("failed to focus tab: %w", err)
	}
	b.syncRemoteGeneration()

	b.mu.Lock()
	if entry, ok := b.tabs[targetID]; ok && entry.ctx.Err() == nil {
		b.active = targetID
		b.mu.Unlock()
		return fmt.Sprintf("Focused tab %s", targetID), nil
	}
	b.mu.Unlock()

	// Attach outside b.mu because remote preflight can wait for a coordinated
	// replacement. The focus action above is not retried.
	tabCtx, cancel, err := b.manager.ContextForTargetContext(ctx, b.profile, targetID)
	if err != nil {
		return "", fmt.Errorf("failed to attach to tab: %w", err)
	}
	generation := b.manager.ProfileGeneration(b.profile)

	b.mu.Lock()
	if b.manager.UsesRemoteCDP() && generation != b.generation {
		b.clearTabsLocked()
		b.generation = generation
	}
	if entry, ok := b.tabs[targetID]; ok && entry.ctx.Err() == nil {
		cancel()
	} else {
		b.tabs[targetID] = &tabEntry{ctx: tabCtx, cancel: cancel}
	}
	b.active = targetID
	b.mu.Unlock()

	return fmt.Sprintf("Focused tab %s", targetID), nil
}

func (b *BrowserTool) closeTab(ctx context.Context, targetID string) (string, error) {
	b.mu.Lock()
	if targetID == "" {
		targetID = b.active
	}
	b.mu.Unlock()

	if targetID == "" {
		return "", fmt.Errorf("no active tab to close; specify a target_id")
	}

	if err := b.manager.CloseTabContext(ctx, b.profile, targetID); err != nil {
		return "", fmt.Errorf("failed to close tab: %w", err)
	}
	b.syncRemoteGeneration()

	b.mu.Lock()
	if entry, ok := b.tabs[targetID]; ok {
		entry.cancel()
		delete(b.tabs, targetID)
	}
	if b.active == targetID {
		b.active = ""
	}
	b.mu.Unlock()

	return fmt.Sprintf("Closed tab %s", targetID), nil
}

// clearTabsLocked cancels all tab contexts and resets state. Must hold b.mu.
func (b *BrowserTool) clearTabsLocked() {
	for _, entry := range b.tabs {
		entry.cancel()
	}
	b.tabs = make(map[string]*tabEntry)
	b.active = ""
}

func (b *BrowserTool) pruneDeadTabsLocked() {
	for targetID, entry := range b.tabs {
		if entry.ctx.Err() == nil {
			continue
		}
		logger.Debugf("Browser: tab context %s is dead: %v", targetID, entry.ctx.Err())
		entry.cancel()
		delete(b.tabs, targetID)
		if b.active == targetID {
			b.active = ""
		}
	}
}

func (b *BrowserTool) syncRemoteGeneration() {
	if !b.manager.UsesRemoteCDP() {
		return
	}
	generation := b.manager.ProfileGeneration(b.profile)
	b.mu.Lock()
	defer b.mu.Unlock()
	if generation != b.generation {
		logger.Debugf("Browser: remote CDP generation changed: %d -> %d", b.generation, generation)
		b.clearTabsLocked()
		b.generation = generation
	}
	b.pruneDeadTabsLocked()
}

func (b *BrowserTool) targetIDFromCtx(ctx context.Context) string {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return ""
	}
	return string(c.Target.TargetID)
}

// GetSchema returns the JSON Schema for browser tool parameters
func (b *BrowserTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Browser command to execute",
				"enum":        []string{"open", "navigate", "snapshot", "click", "type", "fill", "screenshot", "text", "wait", "tabs", "focus", "close", "stop"},
			},
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to navigate to (for 'open' or 'navigate')",
			},
			"snapshot_id": map[string]interface{}{
				"type":        "string",
				"description": "Snapshot ID returned by browser snapshot (for ref-based actions)",
			},
			"ref": map[string]interface{}{
				"type":        "string",
				"description": "Node ref from snapshot tree",
			},
			"selector": map[string]interface{}{
				"type":        "string",
				"description": "CSS selector (for click/type/text; for wait, omit to just pause)",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "Value to type (for type/fill)",
			},
			"target_id": map[string]interface{}{
				"type":        "string",
				"description": "Tab target ID (for focus/close)",
			},
		},
		"required": []string{"command"},
	}
}

// IsRunning returns true if browser is running
func (b *BrowserTool) IsRunning() bool {
	return b.manager != nil && b.manager.IsRunning()
}
