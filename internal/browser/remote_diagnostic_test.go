package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type fakeRemoteCDPOptions struct {
	discoveryStatuses    []int
	discoveryBody        string
	webSocketURL         func(defaultURL string) string
	beforeResultFrame    func(command fakeRemoteCDPRequest) map[string]any
	beforeLifecycleFrame func(sessionID string) map[string]any
	mutateResultFrame    func(command fakeRemoteCDPRequest, response map[string]any)
	resultFramePayload   func(command fakeRemoteCDPRequest, response map[string]any) []byte
	failMethod           string
	closeOnMethod        string
	closeMethodCalls     int32
	blockMethod          string
	versionResult        any
	lifecycleDelay       time.Duration
	staleLifecycleFirst  bool
}

type fakeRemoteCDP struct {
	server *httptest.Server
	opts   fakeRemoteCDPOptions

	discoveryCalls  atomic.Int32
	connectionCalls atomic.Int32
	closedMethods   atomic.Int32
	disconnected    chan struct{}
	disconnectOnce  sync.Once
	serverErrors    chan error
	writeMu         sync.Mutex
	eventWG         sync.WaitGroup
	lifecycleReady  chan struct{}
	lifecycleOnce   sync.Once

	mu                  sync.Mutex
	methods             []string
	disposeOnDetach     bool
	closeTargetCalls    int
	disposeContextCalls int
	sawDiagnosticURL    bool
	sawEvaluation       bool
}

type fakeRemoteCDPRequest struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
}

func newFakeRemoteCDP(t *testing.T, opts fakeRemoteCDPOptions) *fakeRemoteCDP {
	t.Helper()

	fake := &fakeRemoteCDP{
		opts:           opts,
		disconnected:   make(chan struct{}),
		serverErrors:   make(chan error, 8),
		lifecycleReady: make(chan struct{}),
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/json/version":
			call := int(fake.discoveryCalls.Add(1))
			if call <= len(opts.discoveryStatuses) {
				status := opts.discoveryStatuses[call-1]
				if status != http.StatusOK {
					http.Error(w, http.StatusText(status), status)
					return
				}
			}
			if opts.discoveryBody != "" {
				_, _ = w.Write([]byte(opts.discoveryBody))
				return
			}
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/fake"
			if opts.webSocketURL != nil {
				wsURL = opts.webSocketURL(wsURL)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
		case "/devtools/browser/fake":
			fake.serveWebSocket(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	fake.server = server
	t.Cleanup(func() {
		fake.server.Close()
		fake.eventWG.Wait()
		close(fake.serverErrors)
		for err := range fake.serverErrors {
			t.Errorf("fake CDP server: %v", err)
		}
	})
	return fake
}

func (f *fakeRemoteCDP) serveWebSocket(w http.ResponseWriter, req *http.Request) {
	f.connectionCalls.Add(1)
	conn, _, _, err := ws.UpgradeHTTP(req, w)
	if err != nil {
		f.recordServerError(fmt.Errorf("upgrade WebSocket: %w", err))
		return
	}
	defer func() {
		_ = conn.Close()
		f.disconnectOnce.Do(func() { close(f.disconnected) })
	}()

	for {
		payload, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			return
		}
		if op != ws.OpText {
			f.recordServerError(fmt.Errorf("unexpected WebSocket opcode %v", op))
			return
		}

		var command fakeRemoteCDPRequest
		if err := json.Unmarshal(payload, &command); err != nil {
			f.recordServerError(fmt.Errorf("decode command: %w", err))
			return
		}
		f.recordCommand(command)

		if command.Method == f.opts.closeOnMethod {
			closed := f.closedMethods.Add(1)
			if f.opts.closeMethodCalls == 0 || closed <= f.opts.closeMethodCalls {
				return
			}
		}
		if command.Method == f.opts.blockMethod {
			// A second read blocks until the diagnostic deadline closes the
			// transport. The handler returning records that teardown occurred.
			_, _, _ = wsutil.ReadClientData(conn)
			return
		}
		if command.Method == f.opts.failMethod {
			if err := f.writeError(conn, command, "forced protocol failure"); err != nil {
				f.recordServerError(err)
				return
			}
			continue
		}

		result, err := f.resultFor(command)
		if err != nil {
			if writeErr := f.writeError(conn, command, err.Error()); writeErr != nil {
				f.recordServerError(writeErr)
				return
			}
			continue
		}
		if f.opts.beforeResultFrame != nil {
			if frame := f.opts.beforeResultFrame(command); frame != nil {
				if err := f.writeJSONFrame(conn, frame); err != nil {
					f.recordServerError(err)
					return
				}
			}
		}
		if err := f.writeResult(conn, command, result); err != nil {
			f.recordServerError(err)
			return
		}
		if command.Method == page.CommandNavigate {
			f.emitLifecycleEvent(conn, command.SessionID)
		}
	}
}

func (f *fakeRemoteCDP) resultFor(command fakeRemoteCDPRequest) (any, error) {
	switch command.Method {
	case cdpbrowser.CommandGetVersion:
		if f.opts.versionResult != nil {
			return f.opts.versionResult, nil
		}
		return map[string]any{
			"protocolVersion": "1.3",
			"product":         "Chrome/140.0.0.0",
			"revision":        "test",
			"userAgent":       "fake",
			"jsVersion":       "test",
		}, nil
	case target.CommandCreateBrowserContext:
		var params struct {
			DisposeOnDetach bool `json:"disposeOnDetach"`
		}
		if err := json.Unmarshal(command.Params, &params); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.disposeOnDetach = params.DisposeOnDetach
		f.mu.Unlock()
		if !params.DisposeOnDetach {
			return nil, errors.New("disposeOnDetach was not enabled")
		}
		return map[string]any{"browserContextId": "context-1"}, nil
	case target.CommandCreateTarget:
		var params struct {
			URL              string `json:"url"`
			BrowserContextID string `json:"browserContextId"`
			Background       bool   `json:"background"`
		}
		if err := json.Unmarshal(command.Params, &params); err != nil {
			return nil, err
		}
		if params.URL != "about:blank" || params.BrowserContextID != "context-1" || !params.Background {
			return nil, fmt.Errorf("unexpected createTarget params: %+v", params)
		}
		return map[string]any{"targetId": "target-1"}, nil
	case target.CommandAttachToTarget:
		var params struct {
			TargetID string `json:"targetId"`
			Flatten  bool   `json:"flatten"`
		}
		if err := json.Unmarshal(command.Params, &params); err != nil {
			return nil, err
		}
		if params.TargetID != "target-1" || !params.Flatten {
			return nil, fmt.Errorf("unexpected attach params: %+v", params)
		}
		return map[string]any{"sessionId": "session-1"}, nil
	case page.CommandEnable, page.CommandSetLifecycleEventsEnabled:
		return map[string]any{}, nil
	case page.CommandNavigate:
		var params struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(command.Params, &params); err != nil {
			return nil, err
		}
		if command.SessionID != "session-1" || params.URL != remoteDiagnosticPage {
			return nil, fmt.Errorf("unexpected diagnostic navigation: session=%q url=%q", command.SessionID, params.URL)
		}
		f.mu.Lock()
		f.sawDiagnosticURL = true
		f.mu.Unlock()
		return map[string]any{"frameId": "frame-1", "loaderId": "loader-1"}, nil
	case runtime.CommandEvaluate:
		select {
		case <-f.lifecycleReady:
		default:
			return nil, errors.New("Runtime.evaluate arrived before the new document load event")
		}
		var params struct {
			Expression    string `json:"expression"`
			AwaitPromise  bool   `json:"awaitPromise"`
			ReturnByValue bool   `json:"returnByValue"`
		}
		if err := json.Unmarshal(command.Params, &params); err != nil {
			return nil, err
		}
		if command.SessionID != "session-1" || !params.AwaitPromise || !params.ReturnByValue || !strings.Contains(params.Expression, "ok-gobot-cdp-check") {
			return nil, fmt.Errorf("unexpected evaluation params: session=%q %+v", command.SessionID, params)
		}
		f.mu.Lock()
		f.sawEvaluation = true
		f.mu.Unlock()
		return map[string]any{"result": map[string]any{"type": "boolean", "value": true}}, nil
	case target.CommandCloseTarget:
		return map[string]any{"success": true}, nil
	case target.CommandDisposeBrowserContext:
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("unexpected method %q", command.Method)
	}
}

func (f *fakeRemoteCDP) emitLifecycleEvent(conn net.Conn, sessionID string) {
	emit := func() {
		defer f.eventWG.Done()
		if f.opts.beforeLifecycleFrame != nil {
			if frame := f.opts.beforeLifecycleFrame(sessionID); frame != nil {
				if err := f.writeJSONFrame(conn, frame); err != nil {
					f.recordServerError(err)
					return
				}
			}
		}
		if f.opts.staleLifecycleFirst {
			if err := f.writeLifecycleEvent(conn, sessionID, "stale-loader"); err != nil {
				f.recordServerError(err)
				return
			}
		}
		if f.opts.lifecycleDelay > 0 {
			time.Sleep(f.opts.lifecycleDelay)
		}
		// The matching lifecycle event describes a document that is already
		// ready. Publish that state before putting the event on the socket so a
		// fast client cannot legitimately evaluate between the write and this
		// test server's readiness bookkeeping.
		f.lifecycleOnce.Do(func() { close(f.lifecycleReady) })
		if err := f.writeLifecycleEvent(conn, sessionID, "loader-1"); err != nil {
			f.recordServerError(err)
			return
		}
	}

	f.eventWG.Add(1)
	if f.opts.lifecycleDelay > 0 {
		go emit()
		return
	}
	emit()
}

func (f *fakeRemoteCDP) writeLifecycleEvent(conn net.Conn, sessionID, loaderID string) error {
	payload, err := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"method":    "Page.lifecycleEvent",
		"params": map[string]any{
			"frameId":   "frame-1",
			"loaderId":  loaderID,
			"name":      "load",
			"timestamp": 1,
		},
	})
	if err != nil {
		return err
	}
	return f.writeServerMessage(conn, payload)
}

func (f *fakeRemoteCDP) recordCommand(command fakeRemoteCDPRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, command.Method)
	if command.Method == target.CommandCloseTarget {
		f.closeTargetCalls++
	}
	if command.Method == target.CommandDisposeBrowserContext {
		f.disposeContextCalls++
	}
}

func (f *fakeRemoteCDP) recordServerError(err error) {
	select {
	case f.serverErrors <- err:
	default:
	}
}

func (f *fakeRemoteCDP) cleanupSnapshot() (disposeOnDetach bool, closeTargetCalls, disposeContextCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disposeOnDetach, f.closeTargetCalls, f.disposeContextCalls
}

func (f *fakeRemoteCDP) diagnosticSnapshot() (sawURL, sawEvaluation bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawDiagnosticURL, f.sawEvaluation
}

func (f *fakeRemoteCDP) methodCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls int
	for _, got := range f.methods {
		if got == method {
			calls++
		}
	}
	return calls
}

func (f *fakeRemoteCDP) writeResult(conn net.Conn, command fakeRemoteCDPRequest, result any) error {
	response := map[string]any{"id": command.ID, "result": result}
	if command.SessionID != "" {
		response["sessionId"] = command.SessionID
	}
	if f.opts.mutateResultFrame != nil {
		f.opts.mutateResultFrame(command, response)
	}
	if f.opts.resultFramePayload != nil {
		if payload := f.opts.resultFramePayload(command, response); payload != nil {
			return f.writeServerMessage(conn, payload)
		}
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return f.writeServerMessage(conn, payload)
}

func (f *fakeRemoteCDP) writeError(conn net.Conn, command fakeRemoteCDPRequest, message string) error {
	response := map[string]any{
		"id": command.ID,
		"error": map[string]any{
			"code":    -32000,
			"message": message,
		},
	}
	if command.SessionID != "" {
		response["sessionId"] = command.SessionID
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return f.writeServerMessage(conn, payload)
}

func (f *fakeRemoteCDP) writeServerMessage(conn net.Conn, payload []byte) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return wsutil.WriteServerMessage(conn, ws.OpText, payload)
}

func (f *fakeRemoteCDP) writeJSONFrame(conn net.Conn, frame map[string]any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return f.writeServerMessage(conn, payload)
}

func newDiagnosticTestManager(t *testing.T, endpoint string) *Manager {
	t.Helper()
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = endpoint
	m.remoteDiscovery = remoteDiscoveryPolicy{
		maxAttempts:    3,
		initialBackoff: time.Millisecond,
		startupWindow:  time.Second,
	}
	return m
}

func requireRemoteCheckStage(t *testing.T, err error, want RemoteCheckStage) *RemoteCheckError {
	t.Helper()
	var checkErr *RemoteCheckError
	if !errors.As(err, &checkErr) {
		t.Fatalf("error = %v, want *RemoteCheckError", err)
	}
	if checkErr.Stage != want {
		t.Fatalf("error stage = %q, want %q: %v", checkErr.Stage, want, err)
	}
	return checkErr
}

func TestManagerCheckRemoteHealthyAfterTransientDiscovery(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		discoveryStatuses: []int{http.StatusServiceUnavailable},
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	if err != nil {
		t.Fatalf("CheckRemote() error = %v", err)
	}
	for _, stage := range []RemoteCheckStage{
		RemoteCheckDiscovery,
		RemoteCheckWebSocket,
		RemoteCheckTarget,
		RemoteCheckEvaluation,
		RemoteCheckCleanup,
	} {
		if !result.Passed(stage) {
			t.Errorf("stage %q did not pass: %+v", stage, result)
		}
	}
	if result.BrowserProduct != "Chrome/140.0.0.0" || result.ProtocolVersion != "1.3" {
		t.Fatalf("unexpected version result: %+v", result)
	}
	if got := fake.discoveryCalls.Load(); got != 2 {
		t.Fatalf("discovery calls = %d, want 2", got)
	}
	disposeOnDetach, closes, disposes := fake.cleanupSnapshot()
	if !disposeOnDetach || closes != 1 || disposes != 1 {
		t.Fatalf("cleanup = disposeOnDetach:%t close:%d dispose:%d, want true/1/1", disposeOnDetach, closes, disposes)
	}
	sawURL, sawEvaluation := fake.diagnosticSnapshot()
	if !sawURL || !sawEvaluation {
		t.Fatalf("diagnostic execution = URL:%t evaluation:%t, want both true", sawURL, sawEvaluation)
	}
}

func TestManagerRemoteDiscoverySurvivesMultiSecondRestartWindow(t *testing.T) {
	for _, transportErr := range []error{syscall.EHOSTUNREACH, syscall.ECONNREFUSED} {
		t.Run(transportErr.Error(), func(t *testing.T) {
			var virtualElapsed time.Duration
			var calls atomic.Int32
			m := newManager(t.TempDir(), false)
			m.RemoteDebugURL = "http://127.0.0.1:9222"
			m.remoteTransportConnect = stubRemoteTransportConnect
			m.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if virtualElapsed < 3*time.Second {
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: transportErr}
				}
				return remoteDiscoveryResponse(http.StatusOK, validRemoteVersionJSON), nil
			})}
			m.remoteDiscoveryWait = func(ctx context.Context, delay time.Duration) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if delay > remoteDiscoveryMaxBackoff {
					t.Fatalf("retry delay = %s, exceeds cap %s", delay, remoteDiscoveryMaxBackoff)
				}
				virtualElapsed += delay
				return nil
			}

			policy := m.normalizedRemoteDiscoveryPolicy()
			if policy.startupWindow != 20*time.Second {
				t.Fatalf("startup window = %s, want 20s", policy.startupWindow)
			}
			inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
			if err != nil {
				t.Fatalf("connectRemote() did not survive restart window: %v", err)
			}
			m.cleanupInstance(inst)
			if virtualElapsed < 3*time.Second {
				t.Fatalf("virtual retry coverage = %s, want at least 3s", virtualElapsed)
			}
			if got := calls.Load(); got <= 3 {
				t.Fatalf("discovery calls = %d, want more than legacy three attempts", got)
			}
		})
	}
}

func TestManagerCheckRemoteWebSocketRetrySurvivesMultiSecondRestartWindow(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unavailable port: %v", err)
	}
	unavailableURL := "ws://" + listener.Addr().String() + "/devtools/browser/restarting"
	if err := listener.Close(); err != nil {
		t.Fatalf("close unavailable port: %v", err)
	}

	var ready atomic.Bool
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		webSocketURL: func(defaultURL string) string {
			if ready.Load() {
				return defaultURL
			}
			return unavailableURL
		},
	})
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = fake.server.URL
	var virtualElapsed time.Duration
	m.remoteDiscoveryWait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if delay > remoteDiscoveryMaxBackoff {
			t.Fatalf("retry delay = %s, exceeds cap %s", delay, remoteDiscoveryMaxBackoff)
		}
		virtualElapsed += delay
		if virtualElapsed >= 3*time.Second {
			ready.Store(true)
		}
		return nil
	}

	result, err := m.CheckRemote(context.Background())
	if err != nil {
		t.Fatalf("CheckRemote() did not survive WebSocket restart window: %v", err)
	}
	if !result.Passed(RemoteCheckWebSocket) || !result.Passed(RemoteCheckEvaluation) {
		t.Fatalf("unexpected completed stages: %+v", result.Completed)
	}
	if virtualElapsed < 3*time.Second {
		t.Fatalf("virtual WebSocket retry coverage = %s, want at least 3s", virtualElapsed)
	}
	if got := fake.discoveryCalls.Load(); got <= 3 {
		t.Fatalf("rediscovery calls = %d, want more than legacy three attempts", got)
	}
}

func TestManagerConnectRemoteWebSocketRetrySurvivesMultiSecondRestartWindow(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve unavailable port: %v", err)
	}
	unavailableURL := "ws://" + listener.Addr().String() + "/devtools/browser/restarting"
	if err := listener.Close(); err != nil {
		t.Fatalf("close unavailable port: %v", err)
	}

	var ready atomic.Bool
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		webSocketURL: func(defaultURL string) string {
			if ready.Load() {
				return defaultURL
			}
			return unavailableURL
		},
	})
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = fake.server.URL
	var virtualElapsed time.Duration
	m.remoteDiscoveryWait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		virtualElapsed += delay
		if virtualElapsed >= 3*time.Second {
			ready.Store(true)
		}
		return nil
	}

	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote() did not survive WebSocket restart window: %v", err)
	}
	defer m.cleanupInstance(inst)
	chromedpCtx := chromedp.FromContext(inst.browserCtx)
	if chromedpCtx == nil || chromedpCtx.Browser == nil {
		t.Fatal("recovered runtime transport was not retained")
	}
	if virtualElapsed < 3*time.Second {
		t.Fatalf("virtual WebSocket retry coverage = %s, want at least 3s", virtualElapsed)
	}
	if got := fake.discoveryCalls.Load(); got <= 3 {
		t.Fatalf("rediscovery calls = %d, want more than legacy three attempts", got)
	}
	if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 1 {
		t.Fatalf("Browser.getVersion calls on connected transports = %d, want 1", got)
	}
	for _, method := range []string{
		target.CommandCreateBrowserContext,
		target.CommandCreateTarget,
		target.CommandAttachToTarget,
		page.CommandNavigate,
		runtime.CommandEvaluate,
	} {
		if got := fake.methodCalls(method); got != 0 {
			t.Fatalf("%s calls during retryable runtime setup = %d, want 0", method, got)
		}
	}
}

func TestManagerConnectRemoteRetriesDroppedHandshakeAndRetainsRecoveredSocket(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		closeOnMethod:    cdpbrowser.CommandGetVersion,
		closeMethodCalls: 1,
	})
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = fake.server.URL
	m.remoteDiscovery = remoteDiscoveryPolicy{
		maxAttempts:    3,
		initialBackoff: time.Millisecond,
		maxBackoff:     time.Millisecond,
		startupWindow:  time.Second,
	}

	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote() did not recover a dropped Browser.getVersion transport: %v", err)
	}
	defer m.cleanupInstance(inst)
	chromedpCtx := chromedp.FromContext(inst.browserCtx)
	if chromedpCtx == nil || chromedpCtx.Browser == nil {
		t.Fatal("recovered browser transport was not retained")
	}
	if got := fake.discoveryCalls.Load(); got != 2 {
		t.Fatalf("rediscovery calls = %d, want 2", got)
	}
	if got := fake.connectionCalls.Load(); got != 2 {
		t.Fatalf("WebSocket connections = %d, want one dropped and one retained", got)
	}
	if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 2 {
		t.Fatalf("Browser.getVersion calls = %d, want one per transport", got)
	}
	if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
		t.Fatalf("target creation calls during recovered setup = %d, want 0", got)
	}
}

func TestManagerConnectRemoteProtocolErrorFailsFastWithoutTargetDispatch(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{failMethod: cdpbrowser.CommandGetVersion})
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = fake.server.URL
	var waits atomic.Int32
	m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
		waits.Add(1)
		return nil
	}

	_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
	if got := fake.discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
	}
	if got := waits.Load(); got != 0 {
		t.Fatalf("retry waits = %d, want zero for protocol error", got)
	}
	if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
		t.Fatalf("target creation calls after browser-level protocol failure = %d, want 0", got)
	}
	select {
	case <-fake.disconnected:
	case <-time.After(time.Second):
		t.Fatal("failed runtime handshake did not close its WebSocket")
	}
}

func TestParseRemoteDiagnosticFrameShapeRejectsDuplicateFieldsRecursively(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		key      string
		pathPart string
	}{
		{name: "response ID", raw: `{"id":1,"id":2,"result":{}}`, key: "id", pathPart: "at $"},
		{name: "response session", raw: `{"id":1,"sessionId":"one","sessionId":"two","result":{}}`, key: "sessionId", pathPart: "at $"},
		{name: "event method", raw: `{"method":"Page.one","method":"Page.two","params":{}}`, key: "method", pathPart: "at $"},
		{name: "event params", raw: `{"method":"Page.one","params":{},"params":{}}`, key: "params", pathPart: "at $"},
		{name: "response result", raw: `{"id":1,"result":{},"result":{}}`, key: "result", pathPart: "at $"},
		{name: "response error", raw: `{"id":1,"error":{},"error":{}}`, key: "error", pathPart: "at $"},
		{name: "unknown field", raw: `{"id":1,"result":{},"future":1,"future":2}`, key: "future", pathPart: "at $"},
		{name: "nested result", raw: `{"id":1,"result":{"product":"one","product":"two"}}`, key: "product", pathPart: "at $.result"},
		{name: "object inside array", raw: `{"id":1,"result":{"future":[{"name":"one","name":"two"}]}}`, key: "name", pathPart: "at $.result.future[0]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRemoteDiagnosticFrameShape([]byte(tt.raw))
			if err == nil {
				t.Fatal("parseRemoteDiagnosticFrameShape() error = nil, want duplicate-field failure")
			}
			want := fmt.Sprintf("duplicate CDP JSON field %q", tt.key)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("parseRemoteDiagnosticFrameShape() error = %v, want %q", err, want)
			}
			if !strings.Contains(err.Error(), tt.pathPart) {
				t.Fatalf("parseRemoteDiagnosticFrameShape() error = %v, want path %q", err, tt.pathPart)
			}
		})
	}
}

func TestRemoteCDPRejectsDuplicateWireFieldsWithoutRetry(t *testing.T) {
	const versionResult = `{"protocolVersion":"1.3","product":"Chrome/140.0.0.0","revision":"test","userAgent":"fake","jsVersion":"test"}`
	tests := []struct {
		name     string
		field    string
		pathPart string
		frame    func(id int64) string
	}{
		{
			name:     "same response ID",
			field:    "id",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"id":%d,"result":%s}`, id, id, versionResult)
			},
		},
		{
			name:     "conflicting response ID",
			field:    "id",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"id":%d,"result":%s}`, id, id+1, versionResult)
			},
		},
		{
			name:     "method",
			field:    "method",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"method":"Browser.one","method":"Browser.two","result":%s}`, id, versionResult)
			},
		},
		{
			name:     "session ID",
			field:    "sessionId",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"sessionId":"one","sessionId":"two","result":%s}`, id, versionResult)
			},
		},
		{
			name:     "result",
			field:    "result",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"result":%s,"result":%s}`, id, versionResult, versionResult)
			},
		},
		{
			name:     "error",
			field:    "error",
			pathPart: "at $",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"error":{"code":-32000,"message":"one"},"error":{"code":-32001,"message":"two"}}`, id)
			},
		},
		{
			name:     "nested version product",
			field:    "product",
			pathPart: "at $.result",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"result":{"protocolVersion":"1.3","product":"Chrome/one","product":"Chrome/two","revision":"test","userAgent":"fake","jsVersion":"test"}}`, id)
			},
		},
		{
			name:     "object inside result array",
			field:    "name",
			pathPart: "at $.result.future[0]",
			frame: func(id int64) string {
				return fmt.Sprintf(`{"id":%d,"result":{"protocolVersion":"1.3","product":"Chrome/140.0.0.0","revision":"test","userAgent":"fake","jsVersion":"test","future":[{"name":"one","name":"two"}]}}`, id)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []string{"runtime", "diagnostic"} {
				t.Run(mode, func(t *testing.T) {
					fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
						resultFramePayload: func(command fakeRemoteCDPRequest, _ map[string]any) []byte {
							if command.Method != cdpbrowser.CommandGetVersion {
								return nil
							}
							return []byte(tt.frame(command.ID))
						},
					})
					m := newManager(t.TempDir(), false)
					m.RemoteDebugURL = fake.server.URL
					m.remoteDiscovery = remoteDiscoveryPolicy{
						maxAttempts:    3,
						initialBackoff: time.Millisecond,
						maxBackoff:     time.Millisecond,
						startupWindow:  2 * time.Second,
					}
					var waits atomic.Int32
					m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
						waits.Add(1)
						return nil
					}

					started := time.Now()
					var err error
					if mode == "runtime" {
						_, err = m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
					} else {
						var result RemoteCheckResult
						result, err = m.CheckRemote(context.Background())
						if !result.Passed(RemoteCheckDiscovery) || result.Passed(RemoteCheckWebSocket) {
							t.Fatalf("unexpected stages after duplicate frame: %+v", result.Completed)
						}
					}
					if err == nil {
						t.Fatal("remote check error = nil, want duplicate-field failure")
					}
					requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
					if errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("duplicate frame waited for deadline: %v", err)
					}
					want := fmt.Sprintf("duplicate CDP JSON field %q", tt.field)
					if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), tt.pathPart) {
						t.Fatalf("error = %v, want %q and %q", err, want, tt.pathPart)
					}
					if elapsed := time.Since(started); elapsed >= time.Second {
						t.Fatalf("duplicate frame failure took %s, want fail-fast", elapsed)
					}
					if got := fake.discoveryCalls.Load(); got != 1 {
						t.Fatalf("discovery calls = %d, want one", got)
					}
					if got := fake.connectionCalls.Load(); got != 1 {
						t.Fatalf("WebSocket connections = %d, want one", got)
					}
					if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 1 {
						t.Fatalf("Browser.getVersion calls = %d, want one", got)
					}
					if got := waits.Load(); got != 0 {
						t.Fatalf("retry waits = %d, want zero", got)
					}
					if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
						t.Fatalf("target creation calls = %d, want zero", got)
					}
					select {
					case <-fake.disconnected:
					case <-time.After(time.Second):
						t.Fatal("duplicate frame did not close its WebSocket")
					}
				})
			}
		})
	}
}

func TestManagerConnectRemoteRejectsInvalidHandshakeFramesWithoutRetry(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(command fakeRemoteCDPRequest, response map[string]any)
		wantDetail string
	}{
		{
			name: "wrong response ID",
			mutate: func(command fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = command.ID + 1
			},
			wantDetail: "Browser.getVersion response ID 2, want 1",
		},
		{
			name: "foreign response session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["sessionId"] = "foreign-session"
			},
			wantDetail: `Browser.getVersion response session ID "foreign-session", want empty`,
		},
		{
			name: "null response ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = nil
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "zero response ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = 0
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "null response session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["sessionId"] = nil
			},
			wantDetail: "CDP sessionId must be a non-empty JSON string",
		},
		{
			name: "empty response session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["sessionId"] = ""
			},
			wantDetail: "CDP sessionId must be a non-empty JSON string",
		},
		{
			name: "null response method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["method"] = nil
			},
			wantDetail: "CDP method must be a non-empty JSON string",
		},
		{
			name: "empty response method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["method"] = ""
			},
			wantDetail: "CDP method must be a non-empty JSON string",
		},
		{
			name: "mixed response and event",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["method"] = "Browser.downloadWillBegin"
			},
			wantDetail: "mixes response ID 1 with event method Browser.downloadWillBegin",
		},
		{
			name: "missing response ID and event method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
			},
			wantDetail: "neither response ID nor event method",
		},
		{
			name: "missing result and error",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "result")
			},
			wantDetail: "must contain exactly one of result or error",
		},
		{
			name: "null result",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["result"] = nil
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "scalar result",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["result"] = true
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "array result",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["result"] = []any{}
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "response params",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["params"] = map[string]any{}
			},
			wantDetail: "response frame contains command params",
		},
		{
			name: "result and error",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["error"] = map[string]any{"code": -32000, "message": "ambiguous"}
			},
			wantDetail: "must contain exactly one of result or error",
		},
		{
			name: "result and null error",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["error"] = nil
			},
			wantDetail: "must contain exactly one of result or error",
		},
		{
			name: "event with result",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
				response["method"] = "Browser.downloadWillBegin"
			},
			wantDetail: "event frame contains response result or error",
		},
		{
			name: "event with error",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["error"] = map[string]any{"code": -32000, "message": "invalid event"}
			},
			wantDetail: "event frame contains response result or error",
		},
		{
			name: "event with scalar params",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["params"] = "not-an-object"
			},
			wantDetail: "event params must be a JSON object",
		},
		{
			name: "event with null ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = nil
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["params"] = map[string]any{}
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "event with zero ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = 0
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["params"] = map[string]any{}
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "event with null method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = nil
				response["params"] = map[string]any{}
			},
			wantDetail: "CDP method must be a non-empty JSON string",
		},
		{
			name: "event with null session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["sessionId"] = nil
				response["params"] = map[string]any{}
			},
			wantDetail: "CDP sessionId must be a non-empty JSON string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
				mutateResultFrame: func(command fakeRemoteCDPRequest, response map[string]any) {
					if command.Method == cdpbrowser.CommandGetVersion {
						tt.mutate(command, response)
					}
				},
			})
			m := newManager(t.TempDir(), false)
			m.RemoteDebugURL = fake.server.URL
			m.remoteDiscovery = remoteDiscoveryPolicy{
				maxAttempts:    3,
				initialBackoff: time.Millisecond,
				maxBackoff:     time.Millisecond,
				startupWindow:  2 * time.Second,
			}
			var waits atomic.Int32
			m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
				waits.Add(1)
				return nil
			}

			started := time.Now()
			_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
			if err == nil {
				t.Fatal("connectRemote() error = nil, want protocol failure")
			}
			requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error waited for the startup deadline instead of failing on the frame: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tt.wantDetail)
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("protocol failure took %s, want less than half the 2s startup window", elapsed)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
			}
			if got := fake.connectionCalls.Load(); got != 1 {
				t.Fatalf("WebSocket connections = %d, want fail-fast single attempt", got)
			}
			if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 1 {
				t.Fatalf("Browser.getVersion calls = %d, want one", got)
			}
			if got := waits.Load(); got != 0 {
				t.Fatalf("retry waits = %d, want zero for protocol failure", got)
			}
			if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
				t.Fatalf("target creation calls after invalid handshake frame = %d, want 0", got)
			}
		})
	}
}

func TestManagerConnectRemoteAcceptsHandshakeEventAndRetainsValidatedBrowser(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		beforeResultFrame: func(command fakeRemoteCDPRequest) map[string]any {
			if command.Method != cdpbrowser.CommandGetVersion {
				return nil
			}
			return map[string]any{
				"sessionId": "foreign-session",
				"method":    "Page.lifecycleEvent",
				"params": map[string]any{
					"frameId":   "foreign-frame",
					"loaderId":  "foreign-loader",
					"name":      "load",
					"timestamp": 1,
				},
			}
		},
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote() rejected a valid async handshake event: %v", err)
	}
	defer m.cleanupInstance(inst)
	if inst.browserCtx.Err() != nil || inst.transportCtx.Err() != nil {
		t.Fatalf("retained browser contexts were canceled after a valid handshake: browser=%v transport=%v", inst.browserCtx.Err(), inst.transportCtx.Err())
	}
	chromedpCtx := chromedp.FromContext(inst.browserCtx)
	if chromedpCtx == nil || chromedpCtx.Browser == nil {
		t.Fatal("connectRemote() did not retain the validated Browser")
	}
	protocolVersion, product, _, _, _, err := cdpbrowser.GetVersion().Do(
		cdp.WithExecutor(context.Background(), chromedpCtx.Browser),
	)
	if err != nil {
		t.Fatalf("post-handshake command failed; validator may still be active: %v", err)
	}
	if protocolVersion != "1.3" || product != "Chrome/140.0.0.0" {
		t.Fatalf("unexpected retained Browser version: protocol=%q product=%q", protocolVersion, product)
	}
	if got := fake.discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want one retained transport", got)
	}
	if got := fake.connectionCalls.Load(); got != 1 {
		t.Fatalf("WebSocket connections = %d, want one retained transport", got)
	}
	if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 2 {
		t.Fatalf("Browser.getVersion calls = %d, want handshake plus retained lifecycle command", got)
	}
}

func TestManagerCheckRemoteRejectsInvalidProtocolFramesWithoutRetry(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(command fakeRemoteCDPRequest, response map[string]any)
		wantDetail string
	}{
		{
			name: "wrong response ID",
			mutate: func(command fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = command.ID + 1
			},
			wantDetail: "response ID 2, want 1",
		},
		{
			name: "foreign response session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["sessionId"] = "foreign-session"
			},
			wantDetail: `response session ID "foreign-session", want ""`,
		},
		{
			name: "null response ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = nil
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "null response session",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["sessionId"] = nil
			},
			wantDetail: "CDP sessionId must be a non-empty JSON string",
		},
		{
			name: "null response method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["method"] = nil
			},
			wantDetail: "CDP method must be a non-empty JSON string",
		},
		{
			name: "event with null ID",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["id"] = nil
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["params"] = map[string]any{}
			},
			wantDetail: "CDP id must be a positive JSON integer",
		},
		{
			name: "mixed response and event",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				response["method"] = "Browser.downloadWillBegin"
			},
			wantDetail: "mixes response ID 1 with event method Browser.downloadWillBegin",
		},
		{
			name: "missing response ID and event method",
			mutate: func(_ fakeRemoteCDPRequest, response map[string]any) {
				delete(response, "id")
			},
			wantDetail: "neither response ID nor event method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{mutateResultFrame: tt.mutate})
			m := newManager(t.TempDir(), false)
			m.RemoteDebugURL = fake.server.URL
			m.remoteDiscovery = remoteDiscoveryPolicy{
				maxAttempts:    3,
				initialBackoff: time.Millisecond,
				maxBackoff:     time.Millisecond,
				startupWindow:  2 * time.Second,
			}
			var waits atomic.Int32
			m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
				waits.Add(1)
				return nil
			}

			started := time.Now()
			result, err := m.CheckRemote(context.Background())
			if err == nil {
				t.Fatal("CheckRemote() error = nil, want protocol failure")
			}
			requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
			if !result.Passed(RemoteCheckDiscovery) || result.Passed(RemoteCheckWebSocket) {
				t.Fatalf("unexpected stages after invalid handshake frame: %+v", result.Completed)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error waited for the overall deadline instead of failing on the frame: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tt.wantDetail)
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("protocol failure took %s, want less than half the 2s startup window", elapsed)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
			}
			if got := fake.connectionCalls.Load(); got != 1 {
				t.Fatalf("WebSocket connections = %d, want fail-fast single attempt", got)
			}
			if got := fake.methodCalls(cdpbrowser.CommandGetVersion); got != 1 {
				t.Fatalf("Browser.getVersion calls = %d, want one", got)
			}
			if got := waits.Load(); got != 0 {
				t.Fatalf("retry waits = %d, want zero for protocol failure", got)
			}
			if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
				t.Fatalf("target creation calls after invalid handshake frame = %d, want 0", got)
			}
		})
	}
}

func TestManagerCheckRemoteBuffersForeignEventBeforeCorrectResponse(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		beforeResultFrame: func(command fakeRemoteCDPRequest) map[string]any {
			if command.Method != cdpbrowser.CommandGetVersion {
				return nil
			}
			return map[string]any{
				"sessionId": "foreign-session",
				"method":    "Page.lifecycleEvent",
				"params": map[string]any{
					"frameId":   "foreign-frame",
					"loaderId":  "foreign-loader",
					"name":      "load",
					"timestamp": 1,
				},
			}
		},
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	if err != nil {
		t.Fatalf("CheckRemote() rejected an unrelated valid event: %v", err)
	}
	if !result.Passed(RemoteCheckEvaluation) || !result.Passed(RemoteCheckCleanup) {
		t.Fatalf("valid foreign event blocked the diagnostic flow: %+v", result.Completed)
	}
}

func TestManagerCheckRemoteRejectsMalformedNoReturnResponses(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(response map[string]any)
		wantDetail string
	}{
		{
			name: "missing result",
			mutate: func(response map[string]any) {
				delete(response, "result")
			},
			wantDetail: "must contain exactly one of result or error",
		},
		{
			name: "null result",
			mutate: func(response map[string]any) {
				response["result"] = nil
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "scalar result",
			mutate: func(response map[string]any) {
				response["result"] = true
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "array result",
			mutate: func(response map[string]any) {
				response["result"] = []any{}
			},
			wantDetail: "response result must be a JSON object",
		},
		{
			name: "response params",
			mutate: func(response map[string]any) {
				response["params"] = map[string]any{}
			},
			wantDetail: "response frame contains command params",
		},
		{
			name: "result and error",
			mutate: func(response map[string]any) {
				response["error"] = map[string]any{"code": -32000, "message": "ambiguous"}
			},
			wantDetail: "must contain exactly one of result or error",
		},
		{
			name: "result and null error",
			mutate: func(response map[string]any) {
				response["error"] = nil
			},
			wantDetail: "must contain exactly one of result or error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
				mutateResultFrame: func(command fakeRemoteCDPRequest, response map[string]any) {
					if command.Method == page.CommandEnable {
						tt.mutate(response)
					}
				},
			})
			m := newDiagnosticTestManager(t, fake.server.URL)

			started := time.Now()
			result, err := m.CheckRemote(context.Background())
			if err == nil {
				t.Fatal("CheckRemote() error = nil, want malformed no-return response failure")
			}
			requireRemoteCheckStage(t, err, RemoteCheckEvaluation)
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error waited for the diagnostic deadline instead of failing on the response: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tt.wantDetail)
			}
			if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
				t.Fatalf("malformed no-return response took %s, want immediate failure", elapsed)
			}
			if !result.Passed(RemoteCheckTarget) || result.Passed(RemoteCheckEvaluation) {
				t.Fatalf("unexpected stages after malformed Page.enable response: %+v", result.Completed)
			}
			if got := fake.methodCalls(page.CommandEnable); got != 1 {
				t.Fatalf("Page.enable calls = %d, want one", got)
			}
			if got := fake.methodCalls(page.CommandSetLifecycleEventsEnabled); got != 0 {
				t.Fatalf("Page.setLifecycleEventsEnabled calls after malformed Page.enable response = %d, want 0", got)
			}
		})
	}
}

func TestManagerCheckRemoteRejectsMalformedEventShapes(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(response map[string]any)
		wantDetail string
	}{
		{
			name: "event with result",
			mutate: func(response map[string]any) {
				delete(response, "id")
				response["method"] = "Browser.downloadWillBegin"
			},
			wantDetail: "event frame contains response result or error",
		},
		{
			name: "event with error",
			mutate: func(response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["error"] = map[string]any{"code": -32000, "message": "invalid event"}
			},
			wantDetail: "event frame contains response result or error",
		},
		{
			name: "event with null params",
			mutate: func(response map[string]any) {
				delete(response, "id")
				delete(response, "result")
				response["method"] = "Browser.downloadWillBegin"
				response["params"] = nil
			},
			wantDetail: "event params must be a JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
				mutateResultFrame: func(command fakeRemoteCDPRequest, response map[string]any) {
					if command.Method == cdpbrowser.CommandGetVersion {
						tt.mutate(response)
					}
				},
			})
			m := newDiagnosticTestManager(t, fake.server.URL)

			result, err := m.CheckRemote(context.Background())
			if err == nil {
				t.Fatal("CheckRemote() error = nil, want malformed event failure")
			}
			requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tt.wantDetail)
			}
			if !result.Passed(RemoteCheckDiscovery) || result.Passed(RemoteCheckWebSocket) {
				t.Fatalf("unexpected stages after malformed handshake event: %+v", result.Completed)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
			}
		})
	}
}

func TestManagerCheckRemoteLifecycleWaitRejectsInvalidFrames(t *testing.T) {
	tests := []struct {
		name       string
		frame      func(sessionID string) map[string]any
		wantDetail string
	}{
		{
			name: "unexpected response ID",
			frame: func(sessionID string) map[string]any {
				return map[string]any{"id": 999, "sessionId": sessionID, "result": map[string]any{}}
			},
			wantDetail: "unexpected response frame ID 999",
		},
		{
			name: "mixed response and event",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"id":        999,
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params":    map[string]any{},
				}
			},
			wantDetail: "mixes response ID 999 with event method Page.lifecycleEvent",
		},
		{
			name: "missing response ID and event method",
			frame: func(sessionID string) map[string]any {
				return map[string]any{"sessionId": sessionID, "result": map[string]any{}}
			},
			wantDetail: "neither response ID nor event method",
		},
		{
			name: "lifecycle missing frame ID",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"loaderId":  "loader-1",
						"name":      "load",
						"timestamp": 1,
					},
				}
			},
			wantDetail: "missing required frameId",
		},
		{
			name: "lifecycle null loader ID",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"frameId":   "frame-1",
						"loaderId":  nil,
						"name":      "load",
						"timestamp": 1,
					},
				}
			},
			wantDetail: "Page.lifecycleEvent loaderId must be a JSON string",
		},
		{
			name: "lifecycle null name",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"frameId":   "frame-1",
						"loaderId":  "loader-1",
						"name":      nil,
						"timestamp": 1,
					},
				}
			},
			wantDetail: "Page.lifecycleEvent name must be a non-empty JSON string",
		},
		{
			name: "lifecycle missing timestamp",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"frameId":  "frame-1",
						"loaderId": "loader-1",
						"name":     "load",
					},
				}
			},
			wantDetail: "missing required timestamp",
		},
		{
			name: "lifecycle null timestamp",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"frameId":   "frame-1",
						"loaderId":  "loader-1",
						"name":      "load",
						"timestamp": nil,
					},
				}
			},
			wantDetail: "timestamp must be a non-negative JSON number",
		},
		{
			name: "lifecycle negative timestamp",
			frame: func(sessionID string) map[string]any {
				return map[string]any{
					"sessionId": sessionID,
					"method":    "Page.lifecycleEvent",
					"params": map[string]any{
						"frameId":   "frame-1",
						"loaderId":  "loader-1",
						"name":      "load",
						"timestamp": -1,
					},
				}
			},
			wantDetail: "timestamp must be a non-negative JSON number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{beforeLifecycleFrame: tt.frame})
			m := newDiagnosticTestManager(t, fake.server.URL)
			var waits atomic.Int32
			m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
				waits.Add(1)
				return nil
			}

			started := time.Now()
			result, err := m.CheckRemote(context.Background())
			if err == nil {
				t.Fatal("CheckRemote() error = nil, want lifecycle protocol failure")
			}
			requireRemoteCheckStage(t, err, RemoteCheckEvaluation)
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error waited for the overall deadline instead of failing on the lifecycle frame: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tt.wantDetail)
			}
			if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
				t.Fatalf("lifecycle protocol failure took %s, want immediate failure", elapsed)
			}
			if !result.Passed(RemoteCheckTarget) || result.Passed(RemoteCheckEvaluation) {
				t.Fatalf("unexpected stages after invalid lifecycle frame: %+v", result.Completed)
			}
			if got := fake.methodCalls(runtime.CommandEvaluate); got != 0 {
				t.Fatalf("Runtime.evaluate calls after invalid lifecycle frame = %d, want 0", got)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want no whole-check retry", got)
			}
			if got := waits.Load(); got != 0 {
				t.Fatalf("retry waits = %d, want zero", got)
			}
		})
	}
}

func TestManagerConnectRemoteMalformedVersionFailsFastWithoutRetry(t *testing.T) {
	tests := []struct {
		name          string
		versionResult any
	}{
		{
			name:          "wrong result shape",
			versionResult: []string{"not-a-version-object"},
		},
		{
			name: "missing required product",
			versionResult: map[string]any{
				"protocolVersion": "1.3",
				"product":         "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{versionResult: tt.versionResult})
			m := newManager(t.TempDir(), false)
			m.RemoteDebugURL = fake.server.URL
			var waits atomic.Int32
			m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
				waits.Add(1)
				return nil
			}

			_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
			requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
			}
			if got := fake.connectionCalls.Load(); got != 1 {
				t.Fatalf("WebSocket connections = %d, want fail-fast single attempt", got)
			}
			if got := waits.Load(); got != 0 {
				t.Fatalf("retry waits = %d, want zero for malformed version", got)
			}
			if got := fake.methodCalls(target.CommandCreateTarget); got != 0 {
				t.Fatalf("target creation calls after malformed version = %d, want 0", got)
			}
		})
	}
}

func TestManagerCheckRemoteProtocolErrorFailsFastWithoutRetry(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{failMethod: cdpbrowser.CommandGetVersion})
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = fake.server.URL
	var waits atomic.Int32
	m.remoteDiscoveryWait = func(context.Context, time.Duration) error {
		waits.Add(1)
		return nil
	}

	result, err := m.CheckRemote(context.Background())
	requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
	if !result.Passed(RemoteCheckDiscovery) || result.Passed(RemoteCheckWebSocket) {
		t.Fatalf("unexpected stages after protocol error: %+v", result.Completed)
	}
	if got := fake.discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
	}
	if got := waits.Load(); got != 0 {
		t.Fatalf("retry waits = %d, want zero for protocol error", got)
	}
}

func TestManagerCheckRemoteRejectsHTTPOnlyFalseGreen(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{closeOnMethod: cdpbrowser.CommandGetVersion})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	requireRemoteCheckStage(t, err, RemoteCheckWebSocket)
	if !result.Passed(RemoteCheckDiscovery) || result.Passed(RemoteCheckWebSocket) {
		t.Fatalf("unexpected stages after HTTP-only false green: %+v", result.Completed)
	}
	select {
	case <-fake.disconnected:
	case <-time.After(time.Second):
		t.Fatal("diagnostic WebSocket was not closed")
	}
}

func TestManagerCheckRemoteTargetFailureCleansCreatedResources(t *testing.T) {
	tests := []struct {
		name            string
		failMethod      string
		wantAutoDispose bool
		wantClose       int
		wantDispose     int
	}{
		{name: "create context", failMethod: target.CommandCreateBrowserContext},
		{name: "create target", failMethod: target.CommandCreateTarget, wantAutoDispose: true, wantDispose: 1},
		{name: "attach target", failMethod: target.CommandAttachToTarget, wantAutoDispose: true, wantClose: 1, wantDispose: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{failMethod: tt.failMethod})
			m := newDiagnosticTestManager(t, fake.server.URL)

			result, err := m.CheckRemote(context.Background())
			requireRemoteCheckStage(t, err, RemoteCheckTarget)
			if !result.Passed(RemoteCheckCleanup) {
				t.Fatalf("cleanup stage did not pass: %+v", result.Completed)
			}
			disposeOnDetach, closes, disposes := fake.cleanupSnapshot()
			if disposeOnDetach != tt.wantAutoDispose || closes != tt.wantClose || disposes != tt.wantDispose {
				t.Fatalf("cleanup = disposeOnDetach:%t close:%d dispose:%d, want %t/%d/%d", disposeOnDetach, closes, disposes, tt.wantAutoDispose, tt.wantClose, tt.wantDispose)
			}
			if calls := fake.methodCalls(tt.failMethod); calls != 1 {
				t.Fatalf("%s calls = %d, want single dispatch", tt.failMethod, calls)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls after target-stage failure = %d, want no whole-check retry", got)
			}
			if got := fake.connectionCalls.Load(); got != 1 {
				t.Fatalf("WebSocket connections after target-stage failure = %d, want no reconnect", got)
			}
			select {
			case <-fake.disconnected:
			case <-time.After(time.Second):
				t.Fatal("target-stage failure did not close the diagnostic WebSocket")
			}
		})
	}
}

func TestManagerCheckRemoteEvaluationFailureCleansTargetAndContext(t *testing.T) {
	for _, failMethod := range []string{page.CommandNavigate, runtime.CommandEvaluate} {
		t.Run(failMethod, func(t *testing.T) {
			fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{failMethod: failMethod})
			m := newDiagnosticTestManager(t, fake.server.URL)

			result, err := m.CheckRemote(context.Background())
			requireRemoteCheckStage(t, err, RemoteCheckEvaluation)
			if !result.Passed(RemoteCheckTarget) || !result.Passed(RemoteCheckCleanup) {
				t.Fatalf("unexpected stages after evaluation failure: %+v", result.Completed)
			}
			disposeOnDetach, closes, disposes := fake.cleanupSnapshot()
			if !disposeOnDetach || closes != 1 || disposes != 1 {
				t.Fatalf("cleanup = disposeOnDetach:%t close:%d dispose:%d, want true/1/1", disposeOnDetach, closes, disposes)
			}
			if calls := fake.methodCalls(failMethod); calls != 1 {
				t.Fatalf("%s calls = %d, want single dispatch", failMethod, calls)
			}
			if got := fake.discoveryCalls.Load(); got != 1 {
				t.Fatalf("discovery calls after evaluation-stage failure = %d, want no whole-check retry", got)
			}
			if got := fake.connectionCalls.Load(); got != 1 {
				t.Fatalf("WebSocket connections after evaluation-stage failure = %d, want no reconnect", got)
			}
		})
	}
}

func TestManagerCheckRemoteReportsFalseCloseTargetCleanup(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		mutateResultFrame: func(command fakeRemoteCDPRequest, response map[string]any) {
			if command.Method == target.CommandCloseTarget {
				response["result"] = map[string]any{"success": false}
			}
		},
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	if err == nil {
		t.Fatal("CheckRemote() error = nil, want failed close-target cleanup")
	}
	requireRemoteCheckStage(t, err, RemoteCheckCleanup)
	if !strings.Contains(err.Error(), "Target.closeTarget did not return success=true") {
		t.Fatalf("cleanup error = %v, want visible close-target success failure", err)
	}
	if !result.Passed(RemoteCheckEvaluation) || result.Passed(RemoteCheckCleanup) {
		t.Fatalf("unexpected stages after false close-target success: %+v", result.Completed)
	}
	disposeOnDetach, closes, disposes := fake.cleanupSnapshot()
	if !disposeOnDetach || closes != 1 || disposes != 1 {
		t.Fatalf("cleanup attempts = disposeOnDetach:%t close:%d dispose:%d, want true/1/1", disposeOnDetach, closes, disposes)
	}
}

func TestManagerCheckRemoteWaitsForMatchingNewDocumentLoadBeforeEvaluation(t *testing.T) {
	const loadDelay = 40 * time.Millisecond
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		lifecycleDelay:      loadDelay,
		staleLifecycleFirst: true,
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	started := time.Now()
	result, err := m.CheckRemote(context.Background())
	if err != nil {
		t.Fatalf("CheckRemote() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < loadDelay {
		t.Fatalf("diagnostic completed in %s before delayed new-document load at %s", elapsed, loadDelay)
	}
	if !result.Passed(RemoteCheckEvaluation) {
		t.Fatalf("evaluation stage did not pass: %+v", result.Completed)
	}
}

func TestManagerCheckRemoteAllowsSpecValidEmptyLoaderLifecycleInterleave(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{
		beforeLifecycleFrame: func(sessionID string) map[string]any {
			return map[string]any{
				"sessionId": sessionID,
				"method":    "Page.lifecycleEvent",
				"params": map[string]any{
					"frameId":   "worker-frame",
					"loaderId":  "",
					"name":      "DOMContentLoaded",
					"timestamp": 1,
				},
			}
		},
	})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	if err != nil {
		t.Fatalf("spec-valid empty-loader lifecycle event blocked the matching load: %v", err)
	}
	if !result.Passed(RemoteCheckEvaluation) || !result.Passed(RemoteCheckCleanup) {
		t.Fatalf("diagnostic stages = %+v, want evaluation and cleanup", result.Completed)
	}
	if got := fake.methodCalls(runtime.CommandEvaluate); got != 1 {
		t.Fatalf("Runtime.evaluate calls = %d, want exactly one after matching load", got)
	}
}

func TestManagerCheckRemoteDeadlineClosesTransportAndOwnsTargetTeardown(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{blockMethod: runtime.CommandEvaluate})
	m := newDiagnosticTestManager(t, fake.server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := m.CheckRemote(ctx)
	requireRemoteCheckStage(t, err, RemoteCheckEvaluation)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if result.Passed(RemoteCheckCleanup) {
		t.Fatalf("explicit cleanup unexpectedly passed after deadline: %+v", result.Completed)
	}
	select {
	case <-fake.disconnected:
	case <-time.After(time.Second):
		t.Fatal("deadline did not close the diagnostic WebSocket")
	}
	disposeOnDetach, closes, disposes := fake.cleanupSnapshot()
	if !disposeOnDetach {
		t.Fatal("browser context was not created with disposeOnDetach")
	}
	if closes != 0 || disposes != 0 {
		t.Fatalf("post-deadline protocol cleanup calls = close:%d dispose:%d, want 0/0", closes, disposes)
	}
}

func TestManagerCheckRemoteMalformedDiscoveryFailsFast(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{discoveryBody: `{"webSocketDebuggerUrl":`})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	requireRemoteCheckStage(t, err, RemoteCheckDiscovery)
	if len(result.Completed) != 0 {
		t.Fatalf("completed stages = %+v, want none", result.Completed)
	}
	if got := fake.discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls = %d, want fail-fast single attempt", got)
	}
}

func TestManagerCheckRemoteCleanupFailureIsRequired(t *testing.T) {
	fake := newFakeRemoteCDP(t, fakeRemoteCDPOptions{failMethod: target.CommandCloseTarget})
	m := newDiagnosticTestManager(t, fake.server.URL)

	result, err := m.CheckRemote(context.Background())
	requireRemoteCheckStage(t, err, RemoteCheckCleanup)
	if !result.Passed(RemoteCheckEvaluation) || result.Passed(RemoteCheckCleanup) {
		t.Fatalf("unexpected stages after cleanup failure: %+v", result.Completed)
	}
	_, closes, disposes := fake.cleanupSnapshot()
	if closes != 1 || disposes != 1 {
		t.Fatalf("cleanup calls = close:%d dispose:%d, want 1/1", closes, disposes)
	}
}
