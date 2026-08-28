package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const validRemoteVersionJSON = `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/test"}`

type roundTripFunc func(*http.Request) (*http.Response, error)

type remoteGenerationTestKey struct{}

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeNotifyBody struct {
	io.Reader
	once   sync.Once
	closed chan struct{}
}

func (b *closeNotifyBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func remoteDiscoveryResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newRemoteDiscoveryTestManager(t *testing.T, transport http.RoundTripper) *Manager {
	t.Helper()

	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://127.0.0.1:9222"
	m.httpClient = &http.Client{Transport: transport}
	m.remoteDiscovery = remoteDiscoveryPolicy{
		maxAttempts:    3,
		initialBackoff: time.Millisecond,
		startupWindow:  time.Second,
	}
	// These tests isolate HTTP discovery and shared-launch coordination. CDP
	// transport validation has dedicated deterministic tests below.
	m.remoteLiveness = func(context.Context) error { return nil }
	return m
}

func waitForRemoteLaunchWaiters(t *testing.T, m *Manager, profile string, want int) *remoteProfileLaunch {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		launch := m.remoteLaunches[profile]
		got := 0
		if launch != nil {
			got = launch.waiters
		}
		m.mu.Unlock()
		if launch != nil && got == want {
			return launch
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("remote launch waiters did not reach %d", want)
	return nil
}

func TestManagerConnectRemoteRetriesServiceUnavailable(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/test"}`))
	}))
	defer server.Close()

	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = server.URL

	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote failed after transient 503: %v", err)
	}
	m.cleanupInstance(inst)

	if got := calls.Load(); got != 2 {
		t.Fatalf("discovery attempts = %d, want 2", got)
	}
}

func TestManagerValidatedWebSocketURLDoesNotTriggerSecondDiscovery(t *testing.T) {
	var discoveryCalls atomic.Int32
	var webSocketCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/json/version":
			discoveryCalls.Add(1)
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"
			_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":%q}`, wsURL)
		case "/devtools/browser/test":
			webSocketCalls.Add(1)
			http.Error(w, "test endpoint does not implement WebSocket", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = server.URL
	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote failed: %v", err)
	}
	defer m.cleanupInstance(inst)

	runCtx, cancel := context.WithTimeout(inst.browserCtx, time.Second)
	err = chromedp.Run(runCtx)
	cancel()
	if err == nil {
		t.Fatal("chromedp.Run unexpectedly connected to the test WebSocket endpoint")
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery requests = %d, want exactly 1", got)
	}
	if got := webSocketCalls.Load(); got != 1 {
		t.Fatalf("WebSocket attach requests = %d, want exactly 1", got)
	}
}

func TestManagerRemoteDiscoveryRetriesTransientTransportError(t *testing.T) {
	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("discovery method = %s, want GET", req.Method)
		}
		if got, want := req.URL.String(), "http://127.0.0.1:9222/json/version"; got != want {
			t.Fatalf("discovery URL = %s, want %s", got, want)
		}
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("dial remote browser: %w", syscall.EHOSTUNREACH)
		}
		return remoteDiscoveryResponse(http.StatusCreated, validRemoteVersionJSON), nil
	}))

	inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err != nil {
		t.Fatalf("connectRemote failed after transient transport error: %v", err)
	}
	m.cleanupInstance(inst)

	if got := calls.Load(); got != 2 {
		t.Fatalf("discovery attempts = %d, want 2", got)
	}
}

func TestManagerRemoteDiscoveryRetriesGatewayStatuses(t *testing.T) {
	for _, status := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					return remoteDiscoveryResponse(status, "temporarily unavailable"), nil
				}
				return remoteDiscoveryResponse(http.StatusOK, validRemoteVersionJSON), nil
			}))

			inst, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
			if err != nil {
				t.Fatalf("connectRemote failed after transient %d: %v", status, err)
			}
			m.cleanupInstance(inst)

			if got := calls.Load(); got != 2 {
				t.Fatalf("discovery attempts = %d, want 2", got)
			}
		})
	}
}

func TestManagerRemoteDiscoveryDoesNotFollowRedirects(t *testing.T) {
	var discoveryCalls atomic.Int32
	var redirectedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/json/version":
			discoveryCalls.Add(1)
			http.Redirect(w, req, "/redirected", http.StatusFound)
		case "/redirected":
			redirectedCalls.Add(1)
			_, _ = w.Write([]byte(validRemoteVersionJSON))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = server.URL
	m.remoteDiscovery = remoteDiscoveryPolicy{
		maxAttempts:    3,
		initialBackoff: time.Millisecond,
		startupWindow:  time.Second,
	}

	_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	if err == nil {
		t.Fatal("connectRemote followed a redirect and unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "302 Found") {
		t.Fatalf("connectRemote error = %v, want 302 Found", err)
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery requests = %d, want 1", got)
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("followed redirect requests = %d, want 0", got)
	}
	var discoveryErr *remoteDiscoveryError
	if !errors.As(err, &discoveryErr) || discoveryErr.attempts != 1 {
		t.Fatalf("terminal discovery error = %#v, want 1 attempt", discoveryErr)
	}
}

func TestManagerRemoteDiscoveryStopsAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("dial remote browser: %w", syscall.EHOSTUNREACH)
	}))

	started := time.Now()
	_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("connectRemote succeeded after permanent transport failure")
	}

	var discoveryErr *remoteDiscoveryError
	if !errors.As(err, &discoveryErr) {
		t.Fatalf("error type = %T, want *remoteDiscoveryError: %v", err, err)
	}
	if discoveryErr.attempts != 3 {
		t.Fatalf("reported attempts = %d, want 3", discoveryErr.attempts)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("actual attempts = %d, want 3", got)
	}
	if !errors.Is(err, syscall.EHOSTUNREACH) {
		t.Fatalf("error does not preserve EHOSTUNREACH: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("bounded retry took %s, want no more than 500ms", elapsed)
	}
}

func TestManagerRemoteDiscoveryFailsFast(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "bad request", status: http.StatusBadRequest, body: "bad request", want: "400 Bad Request"},
		{name: "too many requests", status: http.StatusTooManyRequests, body: "slow down", want: "429 Too Many Requests"},
		{name: "non-retryable server error", status: http.StatusInternalServerError, body: "failed", want: "500 Internal Server Error"},
		{name: "malformed JSON", status: http.StatusOK, body: `{"webSocketDebuggerUrl":`, want: "failed to parse remote"},
		{name: "trailing malformed JSON", status: http.StatusOK, body: validRemoteVersionJSON + " trailing", want: "failed to parse remote"},
		{name: "oversized JSON", status: http.StatusOK, body: validRemoteVersionJSON + strings.Repeat(" ", remoteDiscoveryMaxBodySize), want: "exceeds"},
		{name: "missing WebSocket URL", status: http.StatusOK, body: `{}`, want: "no webSocketDebuggerUrl"},
		{name: "invalid WebSocket scheme", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"http://127.0.0.1:9222/devtools/browser/test"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "missing WebSocket hostname", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://:9222/devtools/browser/test"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "invalid WebSocket port", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://127.0.0.1:99999/devtools/browser/test"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "WebSocket URL fragment", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/test#fragment"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "missing browser path", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "missing browser ID", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/"}`, want: "invalid webSocketDebuggerUrl"},
		{name: "malformed WebSocket URL", status: http.StatusOK, body: `{"webSocketDebuggerUrl":"ws://[::1"}`, want: "invalid webSocketDebuggerUrl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls.Add(1)
				return remoteDiscoveryResponse(tt.status, tt.body), nil
			}))

			_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
			if err == nil {
				t.Fatal("connectRemote unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("connectRemote error = %v, want it to contain %q", err, tt.want)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("discovery attempts = %d, want fail-fast single attempt", got)
			}

			var discoveryErr *remoteDiscoveryError
			if !errors.As(err, &discoveryErr) {
				t.Fatalf("error type = %T, want *remoteDiscoveryError: %v", err, err)
			}
			if discoveryErr.attempts != 1 {
				t.Fatalf("reported attempts = %d, want 1", discoveryErr.attempts)
			}
		})
	}
}

func TestManagerRemoteDiscoveryStartupWindowIsBounded(t *testing.T) {
	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteDiscoveryResponse(http.StatusServiceUnavailable, "temporarily unavailable"), nil
	}))
	m.remoteDiscovery.initialBackoff = time.Hour
	m.remoteDiscovery.startupWindow = 25 * time.Millisecond

	started := time.Now()
	_, err := m.connectRemote(context.Background(), profileConfig{name: ProfileOpenclaw}, 19000)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connectRemote error = %v, want context.DeadlineExceeded", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("discovery attempts = %d, want 1 before startup window expires", got)
	}
	var discoveryErr *remoteDiscoveryError
	if !errors.As(err, &discoveryErr) {
		t.Fatalf("error type = %T, want *remoteDiscoveryError: %v", err, err)
	}
	if discoveryErr.attempts != 1 {
		t.Fatalf("reported attempts = %d, want 1", discoveryErr.attempts)
	}
	if discoveryErr.lastAttemptCause == nil || !strings.Contains(discoveryErr.lastAttemptCause.Error(), "503 Service Unavailable") {
		t.Fatalf("last attempt cause = %v, want transient 503", discoveryErr.lastAttemptCause)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("startup window returned after %s, want no more than 500ms", elapsed)
	}
}

func TestManagerStartContextCancelsRemoteDiscovery(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		close(requestCanceled)
		return nil, req.Context().Err()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- m.StartContext(ctx)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("remote discovery request did not start")
	}
	launch := waitForRemoteLaunchWaiters(t, m, ProfileOpenclaw, 1)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartContext error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("StartContext did not return promptly after caller cancellation")
	}
	select {
	case <-requestCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("underlying discovery request was not cancelled after its only caller left")
	}
	select {
	case <-launch.done:
		if !errors.Is(launch.err, context.Canceled) {
			t.Fatalf("shared launch error = %v, want context.Canceled", launch.err)
		}
		var discoveryErr *remoteDiscoveryError
		if !errors.As(launch.err, &discoveryErr) || discoveryErr.attempts != 1 {
			t.Fatalf("shared launch error = %#v, want one-attempt *remoteDiscoveryError", launch.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled shared launch did not tear down promptly")
	}
}

func TestManagerLeaderCancellationKeepsSharedLaunchForLiveWaiter(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	requestCanceled := make(chan struct{})
	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(requestStarted)
		select {
		case <-req.Context().Done():
			close(requestCanceled)
			return nil, req.Context().Err()
		case <-releaseResponse:
			return remoteDiscoveryResponse(http.StatusOK, validRemoteVersionJSON), nil
		}
	}))
	defer m.Stop()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- m.StartContext(leaderCtx)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("leader did not start remote discovery")
	}

	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- m.StartContext(context.Background())
	}()
	waitForRemoteLaunchWaiters(t, m, ProfileOpenclaw, 2)
	cancelLeader()

	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("leader StartContext error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("leader did not return promptly after cancellation")
	}
	select {
	case <-requestCanceled:
		t.Fatal("leader cancellation cancelled discovery while a live waiter remained")
	default:
	}
	close(releaseResponse)

	select {
	case err := <-waiterDone:
		if err != nil {
			t.Fatalf("live waiter failed after leader cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter did not receive shared launch success")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("remote discovery requests = %d, want one shared request", got)
	}
}

func TestManagerStartWaitsForStoppedLaunchTeardownThenStartsFresh(t *testing.T) {
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseFirstTeardown := make(chan struct{})
	secondStarted := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() {
		releaseOnce.Do(func() { close(releaseFirstTeardown) })
	}
	defer releaseFirst()

	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-req.Context().Done()
			close(firstCanceled)
			<-releaseFirstTeardown
			return nil, req.Context().Err()
		case 2:
			close(secondStarted)
			return remoteDiscoveryResponse(http.StatusOK, validRemoteVersionJSON), nil
		default:
			return nil, errors.New("unexpected extra remote discovery request")
		}
	}))
	defer m.Stop()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- m.StartContext(context.Background())
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first remote discovery did not start")
	}
	m.StopProfile(ProfileOpenclaw)
	select {
	case <-firstCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("StopProfile did not cancel the in-flight discovery request")
	}

	newCtx, cancelNew := context.WithTimeout(context.Background(), time.Second)
	defer cancelNew()
	newDone := make(chan error, 1)
	go func() {
		newDone <- m.StartContext(newCtx)
	}()
	select {
	case <-secondStarted:
		t.Fatal("new StartContext began before stopped launch teardown completed")
	case <-time.After(25 * time.Millisecond):
	}
	releaseFirst()

	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped launch error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stopped launch did not finish after teardown release")
	}
	select {
	case err := <-newDone:
		if err != nil {
			t.Fatalf("fresh StartContext failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh StartContext did not finish")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("remote discovery requests = %d, want stopped generation plus one fresh generation", got)
	}
}

func TestManagerConcurrentRemoteStartupWaiterHonorsContext(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseRequest) })
	}
	// This fallback makes the test terminate on the pre-fix implementation,
	// where the second caller blocks on m.mu instead of observing its context.
	fallbackRelease := time.AfterFunc(750*time.Millisecond, release)
	defer fallbackRelease.Stop()
	defer release()

	var calls atomic.Int32
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(requestStarted)
		}
		<-releaseRequest
		return remoteDiscoveryResponse(http.StatusOK, validRemoteVersionJSON), nil
	}))
	defer m.Stop()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- m.StartContext(context.Background())
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("first remote discovery request did not start")
	}

	waiterCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	started := time.Now()
	waiterErr := m.StartContext(waiterCtx)
	elapsed := time.Since(started)
	cancel()
	release()

	firstErr := <-firstDone
	if firstErr != nil {
		t.Fatalf("first StartContext failed: %v", firstErr)
	}
	if !errors.Is(waiterErr, context.DeadlineExceeded) {
		t.Fatalf("concurrent StartContext error = %v, want context.DeadlineExceeded", waiterErr)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("concurrent StartContext returned after %s, want prompt caller-bound cancellation", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("remote discovery requests = %d, want one serialized launch", got)
	}
}

func TestManagerRemoteDiscoveryCancellationPreservesLastAttempt(t *testing.T) {
	responseClosed := make(chan struct{})
	m := newRemoteDiscoveryTestManager(t, roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		resp := remoteDiscoveryResponse(http.StatusServiceUnavailable, "temporarily unavailable")
		resp.Body = &closeNotifyBody{Reader: strings.NewReader("temporarily unavailable"), closed: responseClosed}
		return resp, nil
	}))
	m.remoteDiscovery.initialBackoff = time.Hour
	m.remoteDiscovery.startupWindow = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.connectRemote(ctx, profileConfig{name: ProfileOpenclaw}, 19000)
		done <- err
	}()

	select {
	case <-responseClosed:
	case <-time.After(time.Second):
		t.Fatal("transient response was not processed")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("connectRemote error = %v, want context.Canceled", err)
		}
		var discoveryErr *remoteDiscoveryError
		if !errors.As(err, &discoveryErr) {
			t.Fatalf("error type = %T, want *remoteDiscoveryError: %v", err, err)
		}
		if discoveryErr.attempts != 1 {
			t.Fatalf("reported attempts = %d, want 1", discoveryErr.attempts)
		}
		if discoveryErr.lastAttemptCause == nil || !strings.Contains(discoveryErr.lastAttemptCause.Error(), "503 Service Unavailable") {
			t.Fatalf("last attempt cause = %v, want transient 503", discoveryErr.lastAttemptCause)
		}
		if !strings.Contains(err.Error(), "503 Service Unavailable") {
			t.Fatalf("terminal error does not report last attempt: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("connectRemote did not return promptly after cancellation during backoff")
	}
}

func stubInstance(cfg profileConfig, userDataDir string, port int) *profileInstance {
	return &profileInstance{
		name:          cfg.name,
		persistent:    cfg.persistent,
		userDataDir:   userDataDir,
		debugPort:     port,
		allocCtx:      context.Background(),
		allocCancel:   func() {},
		browserCtx:    context.Background(),
		browserCancel: func() {},
	}
}

func TestManagerLazyLaunchPerProfile(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	launches := map[string]int{
		ProfileOpenclaw:  0,
		ProfileEphemeral: 0,
	}

	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches[cfg.name]++
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	if m.IsRunning() {
		t.Fatalf("expected manager to be idle before first use")
	}

	_, cancel, err := m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	if launches[ProfileOpenclaw] != 1 {
		t.Fatalf("expected 1 openclaw launch, got %d", launches[ProfileOpenclaw])
	}

	_, cancel, err = m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("NewTabForProfile(openclaw) second call failed: %v", err)
	}
	cancel()

	if launches[ProfileOpenclaw] != 1 {
		t.Fatalf("expected openclaw to remain on 1 launch, got %d", launches[ProfileOpenclaw])
	}

	_, cancel, err = m.NewTabForProfile(ProfileEphemeral)
	if err != nil {
		t.Fatalf("NewTabForProfile(ephemeral) failed: %v", err)
	}
	cancel()

	if launches[ProfileEphemeral] != 1 {
		t.Fatalf("expected 1 ephemeral launch, got %d", launches[ProfileEphemeral])
	}
}

func TestManagerAutoRestartWhenHealthCheckFails(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	launches := 0
	nextPort := 19000
	firstPort := 0
	failFirstPort := false

	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches++
		assignedPort := nextPort
		nextPort++
		if launches == 1 {
			firstPort = assignedPort
		}
		return stubInstance(cfg, userDataDir, assignedPort), nil
	}
	m.healthFn = func(port int) error {
		if failFirstPort && port == firstPort {
			return errors.New("cdp endpoint is down")
		}
		return nil
	}

	_, cancel, err := m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("first NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	failFirstPort = true

	_, cancel, err = m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("second NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	if launches != 2 {
		t.Fatalf("expected openclaw restart to relaunch browser once; got launches=%d", launches)
	}
}

func TestManagerEphemeralProfileRemovesDataDirOnStop(t *testing.T) {
	m := newManager(t.TempDir(), false)

	var ephemeralDir string
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		if cfg.name == ProfileEphemeral {
			ephemeralDir = userDataDir
		}
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	if err := m.StartProfile(ProfileEphemeral); err != nil {
		t.Fatalf("StartProfile(ephemeral) failed: %v", err)
	}
	if ephemeralDir == "" {
		t.Fatalf("expected ephemeral userDataDir to be captured")
	}
	if _, err := os.Stat(ephemeralDir); err != nil {
		t.Fatalf("expected ephemeral directory to exist before stop: %v", err)
	}

	m.StopProfile(ProfileEphemeral)

	if _, err := os.Stat(ephemeralDir); !os.IsNotExist(err) {
		t.Fatalf("expected ephemeral directory to be removed on stop, got err=%v", err)
	}
}

func TestManagerStartProfileRejectsUnknownProfile(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	if err := m.StartProfile("unknown"); err == nil {
		t.Fatalf("expected unknown profile error")
	}
}

func TestListTabsReturnsProfileNotRunning(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	_, err := m.ListTabs(ProfileOpenclaw)
	if err == nil {
		t.Fatal("expected error when profile not running")
	}
}

func TestManagerListTabsRecoversApparentlyLiveStaleRemoteContext(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()

	staleCtx := context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale")
	if err := staleCtx.Err(); err != nil {
		t.Fatalf("stale browser context is visibly cancelled before the regression: %v", err)
	}
	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		browserCtx:    staleCtx,
		browserCancel: func() {},
		allocCancel:   func() {},
	}

	var launches atomic.Int32
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches.Add(1)
		inst := stubInstance(cfg, userDataDir, debugPort)
		inst.browserCtx = context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh")
		return inst, nil
	}
	m.listTargets = func(ctx context.Context) ([]*target.Info, error) {
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation == "stale" {
			return nil, errors.New("invalid context")
		}
		return []*target.Info{{
			TargetID: "fresh-tab",
			Type:     "page",
			Title:    "Recovered",
			URL:      "https://example.com/recovered",
		}}, nil
	}

	tabs, err := m.ListTabs(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("ListTabs should recover the stale remote generation before dispatch: %v", err)
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want 1", got)
	}
	if len(tabs) != 1 || tabs[0].TargetID != "fresh-tab" {
		t.Fatalf("tabs after recovery = %+v, want the fresh generation", tabs)
	}
}

func TestManagerRemotePreflightCoordinatesSingleReplacement(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()

	const callers = 32
	m.nextRemoteGeneration = 17
	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    17,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}

	var staleProbes atomic.Int32
	var freshProbes atomic.Int32
	allStaleProbesStarted := make(chan struct{})
	releaseStaleProbes := make(chan struct{})
	freshProbeStarted := make(chan struct{})
	releaseFreshProbe := make(chan struct{})
	var allStartedOnce sync.Once
	var freshStartedOnce sync.Once
	m.remoteLiveness = func(ctx context.Context) error {
		switch generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation {
		case "stale":
			if staleProbes.Add(1) == callers {
				allStartedOnce.Do(func() { close(allStaleProbesStarted) })
			}
			<-releaseStaleProbes
			return errors.New("transport closed")
		case "fresh":
			freshProbes.Add(1)
			freshStartedOnce.Do(func() { close(freshProbeStarted) })
			<-releaseFreshProbe
			return nil
		default:
			return fmt.Errorf("unexpected test generation %q", generation)
		}
	}

	var launches atomic.Int32
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches.Add(1)
		inst := stubInstance(cfg, userDataDir, debugPort)
		inst.browserCtx = context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh")
		return inst, nil
	}

	errs := make(chan error, callers)
	for range callers {
		go func() {
			errs <- m.StartProfileContext(context.Background(), ProfileOpenclaw)
		}()
	}
	select {
	case <-allStaleProbesStarted:
	case <-time.After(time.Second):
		t.Fatalf("stale probes started = %d, want %d concurrent callers", staleProbes.Load(), callers)
	}
	close(releaseStaleProbes)
	select {
	case <-freshProbeStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement validation did not start")
	}
	waitForRemoteLaunchWaiters(t, m, ProfileOpenclaw, callers)
	close(releaseFreshProbe)

	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent preflight failed: %v", err)
		}
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want one coordinated replacement", got)
	}
	if got := freshProbes.Load(); got != 1 {
		t.Fatalf("replacement validation probes = %d, want one", got)
	}
	if got := m.ProfileGeneration(ProfileOpenclaw); got != 18 {
		t.Fatalf("installed generation = %d, want 18", got)
	}
}

func TestManagerCallerCancellationDuringRemoteProbeDoesNotEvict(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	m.remoteProbeTimeout = time.Hour
	defer m.Stop()

	inst := &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    1,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	m.instances[ProfileOpenclaw] = inst

	probeStarted := make(chan struct{})
	var probeOnce sync.Once
	m.remoteLiveness = func(ctx context.Context) error {
		probeOnce.Do(func() { close(probeStarted) })
		<-ctx.Done()
		return ctx.Err()
	}
	var launches atomic.Int32
	m.launchFn = func(context.Context, profileConfig, string, int) (*profileInstance, error) {
		launches.Add(1)
		return nil, errors.New("replacement must not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- m.StartProfileContext(ctx, ProfileOpenclaw)
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("remote liveness probe did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartProfileContext error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("caller cancellation did not stop the remote probe promptly")
	}
	if got := launches.Load(); got != 0 {
		t.Fatalf("replacement launches = %d, want zero after caller cancellation", got)
	}
	m.mu.Lock()
	cached := m.instances[ProfileOpenclaw]
	m.mu.Unlock()
	if cached != inst {
		t.Fatal("caller cancellation evicted the cached generation")
	}
}

func TestManagerTimedOutRemoteProbeRebuildsBeforeReturn(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	m.remoteProbeTimeout = 10 * time.Millisecond
	defer m.Stop()

	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    1,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	probeExited := make(chan struct{})
	var exitedOnce sync.Once
	m.remoteLiveness = func(ctx context.Context) error {
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation == "stale" {
			<-ctx.Done()
			exitedOnce.Do(func() { close(probeExited) })
			return ctx.Err()
		}
		return nil
	}
	var launches atomic.Int32
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches.Add(1)
		inst := stubInstance(cfg, userDataDir, debugPort)
		inst.browserCtx = context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh")
		return inst, nil
	}

	started := time.Now()
	if err := m.StartProfileContext(context.Background(), ProfileOpenclaw); err != nil {
		t.Fatalf("timed-out stale probe did not recover: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded stale probe and rebuild took %s, want under 500ms", elapsed)
	}
	select {
	case <-probeExited:
	default:
		t.Fatal("timed-out stale probe did not exit")
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want one", got)
	}
}

func TestManagerCallerCancellationStopsFirstRemoteMaterialization(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	m.remoteProbeTimeout = time.Hour
	defer m.Stop()

	m.launchFn = func(_ context.Context, cfg profileConfig, _ string, debugPort int) (*profileInstance, error) {
		allocCtx, allocCancel := chromedp.NewRemoteAllocator(
			context.Background(),
			"ws://127.0.0.1:9222/devtools/browser/test",
			chromedp.NoModifyURL,
		)
		browserCtx, browserCancel := chromedp.NewContext(allocCtx)
		transportCtx, transportCancel := context.WithCancel(browserCtx)
		return &profileInstance{
			name:            cfg.name,
			persistent:      cfg.persistent,
			debugPort:       debugPort,
			allocCtx:        allocCtx,
			allocCancel:     allocCancel,
			browserCtx:      browserCtx,
			browserCancel:   browserCancel,
			transportCtx:    transportCtx,
			transportCancel: transportCancel,
		}, nil
	}
	probeStarted := make(chan struct{})
	probeExited := make(chan struct{})
	var startedOnce sync.Once
	var exitedOnce sync.Once
	m.remoteLiveness = func(ctx context.Context) error {
		startedOnce.Do(func() { close(probeStarted) })
		<-ctx.Done()
		exitedOnce.Do(func() { close(probeExited) })
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- m.StartProfileContext(ctx, ProfileOpenclaw)
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("first remote materialization did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartProfileContext error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("caller cancellation did not return promptly")
	}
	select {
	case <-probeExited:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first remote materialization leaked after caller cancellation")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		m.mu.Lock()
		inst := m.instances[ProfileOpenclaw]
		launch := m.remoteLaunches[ProfileOpenclaw]
		m.mu.Unlock()
		if inst == nil && launch == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled first materialization left cached state: instance=%p launch=%p", inst, launch)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerStopProfileDuringStaleRebuildPreventsInstall(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()

	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    1,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	m.remoteLiveness = func(ctx context.Context) error {
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation == "stale" {
			return errors.New("transport closed")
		}
		return nil
	}

	launchStarted := make(chan struct{})
	var launchOnce sync.Once
	m.launchFn = func(ctx context.Context, _ profileConfig, _ string, _ int) (*profileInstance, error) {
		launchOnce.Do(func() { close(launchStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		done <- m.StartProfileContext(context.Background(), ProfileOpenclaw)
	}()
	select {
	case <-launchStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement launch did not start")
	}
	m.StopProfile(ProfileOpenclaw)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped rebuild error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("StopProfile did not stop the stale rebuild promptly")
	}
	m.mu.Lock()
	inst := m.instances[ProfileOpenclaw]
	launch := m.remoteLaunches[ProfileOpenclaw]
	m.mu.Unlock()
	if inst != nil || launch != nil {
		t.Fatalf("StopProfile left remote state installed: instance=%p launch=%p", inst, launch)
	}
}

func TestManagerLocalProfileNeverUsesRemoteLiveness(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	inst := stubInstance(profileConfig{name: ProfileOpenclaw, persistent: true}, m.ProfilePath, 19000)
	m.instances[ProfileOpenclaw] = inst
	m.healthFn = func(int) error { return nil }
	var probes atomic.Int32
	m.remoteLiveness = func(context.Context) error {
		probes.Add(1)
		return errors.New("remote probe must not run for local Chrome")
	}

	if err := m.StartProfileContext(context.Background(), ProfileOpenclaw); err != nil {
		t.Fatalf("local cached profile failed: %v", err)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("remote liveness probes for local profile = %d, want zero", got)
	}
	m.mu.Lock()
	cached := m.instances[ProfileOpenclaw]
	m.mu.Unlock()
	if cached != inst {
		t.Fatal("local cached profile was replaced")
	}
}

func TestManagerRemoteActionFailureIsNotRetriedAfterPreflight(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()

	m.nextRemoteGeneration = 1
	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    1,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	var probes atomic.Int32
	m.remoteLiveness = func(context.Context) error {
		probes.Add(1)
		return nil
	}
	dispatchErr := errors.New("activate failed after dispatch")
	var dispatches atomic.Int32
	m.activateTarget = func(context.Context, target.ID) error {
		dispatches.Add(1)
		return dispatchErr
	}
	var launches atomic.Int32
	m.launchFn = func(context.Context, profileConfig, string, int) (*profileInstance, error) {
		launches.Add(1)
		return nil, errors.New("unexpected replacement")
	}

	err := m.FocusTabContext(context.Background(), ProfileOpenclaw, "target-1")
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("FocusTabContext error = %v, want dispatched action error", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("preflight probes = %d, want one", got)
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("activate dispatches = %d, want exactly one", got)
	}
	if got := launches.Load(); got != 0 {
		t.Fatalf("replacement launches after action failure = %d, want zero", got)
	}
}

func TestManagerFocusTabRecoversBeforeFirstAction(t *testing.T) {
	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()

	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	m.remoteLiveness = func(ctx context.Context) error {
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation == "stale" {
			return errors.New("transport closed")
		}
		return nil
	}
	var launches atomic.Int32
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches.Add(1)
		inst := stubInstance(cfg, userDataDir, debugPort)
		inst.browserCtx = context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh")
		return inst, nil
	}
	var dispatches atomic.Int32
	m.activateTarget = func(ctx context.Context, _ target.ID) error {
		dispatches.Add(1)
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation != "fresh" {
			return fmt.Errorf("action received generation %q, want fresh", generation)
		}
		return nil
	}

	if err := m.FocusTabContext(context.Background(), ProfileOpenclaw, "target-1"); err != nil {
		t.Fatalf("first action after injected disconnect failed: %v", err)
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("replacement launches = %d, want one", got)
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("action dispatches = %d, want one", got)
	}
}

func TestManagerRemoteRecoveryEmitsStructuredGenerationLogs(t *testing.T) {
	var logs bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()

	m := newManager(t.TempDir(), false)
	m.RemoteDebugURL = "http://remote.invalid:9222"
	defer m.Stop()
	m.nextRemoteGeneration = 7
	m.instances[ProfileOpenclaw] = &profileInstance{
		name:          ProfileOpenclaw,
		persistent:    true,
		generation:    7,
		browserCtx:    context.WithValue(context.Background(), remoteGenerationTestKey{}, "stale"),
		browserCancel: func() {},
		allocCancel:   func() {},
	}
	m.remoteLiveness = func(ctx context.Context) error {
		if generation, _ := ctx.Value(remoteGenerationTestKey{}).(string); generation == "stale" {
			return errors.New("socket gone")
		}
		return nil
	}
	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		inst := stubInstance(cfg, userDataDir, debugPort)
		inst.browserCtx = context.WithValue(context.Background(), remoteGenerationTestKey{}, "fresh")
		return inst, nil
	}

	if err := m.StartProfileContext(context.Background(), ProfileOpenclaw); err != nil {
		t.Fatalf("remote recovery failed: %v", err)
	}
	for _, want := range []string{
		`event=stale_detected profile="openclaw" generation=7 error="socket gone"`,
		`event=rebuild_started profile="openclaw" stale_generation=7 generation=8`,
		`event=rebuild_completed profile="openclaw" stale_generation=7 generation=8 success=true`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("structured logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestListTabsFiltersPageTargets(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	// Override listTargets to return mock data.
	origList := m.listTargets
	_ = origList
	m.listTargets = func(ctx context.Context) ([]*target.Info, error) {
		return []*target.Info{
			{TargetID: "t1", Type: "page", Title: "Page 1", URL: "https://example.com"},
			{TargetID: "t2", Type: "background_page", Title: "Extension BG", URL: "chrome-extension://abc"},
			{TargetID: "t3", Type: "page", Title: "Page 2", URL: "https://example.org"},
		}, nil
	}

	if err := m.StartProfile(ProfileOpenclaw); err != nil {
		t.Fatalf("StartProfile failed: %v", err)
	}

	tabs, err := m.ListTabs(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("ListTabs failed: %v", err)
	}

	if len(tabs) != 2 {
		t.Fatalf("expected 2 page tabs, got %d", len(tabs))
	}
	if tabs[0].TargetID != "t1" || tabs[1].TargetID != "t3" {
		t.Fatalf("unexpected tabs: %+v", tabs)
	}
}

func TestFocusTabReturnsErrorWhenNotRunning(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	if err := m.FocusTab(ProfileOpenclaw, "t1"); err == nil {
		t.Fatal("expected error when profile not running")
	}
}

func TestFocusTabActivatesTarget(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	var activated target.ID
	m.activateTarget = func(_ context.Context, id target.ID) error {
		activated = id
		return nil
	}

	if err := m.StartProfile(ProfileOpenclaw); err != nil {
		t.Fatalf("StartProfile failed: %v", err)
	}

	if err := m.FocusTab(ProfileOpenclaw, "t1"); err != nil {
		t.Fatalf("FocusTab failed: %v", err)
	}

	if activated != "t1" {
		t.Fatalf("expected activated target t1, got %s", activated)
	}
}

func TestCloseTabReturnsErrorWhenNotRunning(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	if err := m.CloseTab(ProfileOpenclaw, "t1"); err == nil {
		t.Fatal("expected error when profile not running")
	}
}

func TestCloseTabClosesTarget(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	m.launchFn = func(_ context.Context, cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	var closed target.ID
	m.closeTarget = func(_ context.Context, id target.ID) error {
		closed = id
		return nil
	}

	if err := m.StartProfile(ProfileOpenclaw); err != nil {
		t.Fatalf("StartProfile failed: %v", err)
	}

	if err := m.CloseTab(ProfileOpenclaw, "t1"); err != nil {
		t.Fatalf("CloseTab failed: %v", err)
	}

	if closed != "t1" {
		t.Fatalf("expected closed target t1, got %s", closed)
	}
}
