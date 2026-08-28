package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// RemoteCheckStage identifies one independently reportable remote CDP check.
type RemoteCheckStage string

const (
	RemoteCheckDiscovery  RemoteCheckStage = "discovery"
	RemoteCheckWebSocket  RemoteCheckStage = "websocket"
	RemoteCheckTarget     RemoteCheckStage = "target"
	RemoteCheckEvaluation RemoteCheckStage = "evaluation"
	RemoteCheckCleanup    RemoteCheckStage = "cleanup"
)

// RemoteCheckResult records the successful stages of a fresh end-to-end CDP
// diagnostic. It never reuses the Manager's cached browser transport or tab.
type RemoteCheckResult struct {
	Endpoint        string
	BrowserProduct  string
	ProtocolVersion string
	Completed       []RemoteCheckStage
}

// Passed reports whether a diagnostic stage completed successfully.
func (r RemoteCheckResult) Passed(stage RemoteCheckStage) bool {
	for _, completed := range r.Completed {
		if completed == stage {
			return true
		}
	}
	return false
}

func (r *RemoteCheckResult) markPassed(stage RemoteCheckStage) {
	if !r.Passed(stage) {
		r.Completed = append(r.Completed, stage)
	}
}

// RemoteCheckError reports the first stage that could not be completed.
type RemoteCheckError struct {
	Stage RemoteCheckStage
	Err   error
}

func (e *RemoteCheckError) Error() string {
	return fmt.Sprintf("remote CDP %s check failed: %v", e.Stage, e.Err)
}

func (e *RemoteCheckError) Unwrap() error {
	return e.Err
}

const remoteDiagnosticPage = "data:text/html,%3C%21doctype%20html%3E%3Ctitle%3Eok-gobot-cdp-check%3C%2Ftitle%3E%3Cbody%20data-ok-gobot-cdp%3D%22ready%22%3ECDP%20ready%3C%2Fbody%3E"

const remoteHeadedEmptyWindowMessage = "Failed to open new tab - no browser is open"

const remoteDiagnosticExpression = `(async () => {
	const deadline = Date.now() + 2000;
	while (Date.now() < deadline) {
		if (document.readyState === "complete" &&
			document.title === "ok-gobot-cdp-check" &&
			document.body?.dataset.okGobotCdp === "ready") {
			return true;
		}
		await new Promise(resolve => setTimeout(resolve, 10));
	}
	return false;
})()`

// CheckRemote performs a fresh, bounded end-to-end check of the configured
// remote browser. Discovery shares the runtime startup retry policy. The
// diagnostic then proves the browser WebSocket command channel, creates an
// isolated disposable context and target, navigates a deterministic data URL,
// evaluates its contents, and explicitly cleans up. DisposeOnDetach makes a
// caller timeout leak-safe even when it closes the socket before explicit CDP
// cleanup can complete.
func (m *Manager) CheckRemote(ctx context.Context) (RemoteCheckResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result := RemoteCheckResult{Endpoint: m.RemoteDebugURL}
	if strings.TrimSpace(m.RemoteDebugURL) == "" {
		return result, &RemoteCheckError{
			Stage: RemoteCheckDiscovery,
			Err:   errors.New("remote CDP endpoint is not configured"),
		}
	}

	policy := m.normalizedRemoteDiscoveryPolicy()
	checkCtx, cancel := context.WithTimeout(ctx, policy.startupWindow)
	defer cancel()

	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		attemptResult := RemoteCheckResult{Endpoint: m.RemoteDebugURL}
		var retryHeadedEmptyWindow bool
		var cleanupSucceeded bool

		attemptErr := func() (retErr error) {
			transport, version, err := m.openRemoteDiagnosticTransport(checkCtx, policy, &attemptResult)
			if err != nil {
				return err
			}
			client := transport.client
			var browserContextID cdp.BrowserContextID
			var targetID target.ID

			defer func() {
				cleanupErr := client.cleanup(checkCtx, targetID, browserContextID)
				transport.stopCancellationClose()
				if cleanupErr == nil {
					cleanupSucceeded = true
					return
				}
				retryHeadedEmptyWindow = false
				if retErr == nil {
					retErr = remoteCheckFailure(RemoteCheckCleanup, cleanupErr)
					return
				}
				retErr = joinRemoteCleanupError(retErr, cleanupErr)
			}()

			attemptResult.BrowserProduct = version.Product
			attemptResult.ProtocolVersion = version.ProtocolVersion

			createContext := target.CreateBrowserContext().WithDisposeOnDetach(true)
			var contextResult target.CreateBrowserContextReturns
			if err := client.execute(checkCtx, "", target.CommandCreateBrowserContext, createContext, &contextResult); err != nil {
				return remoteCheckFailure(RemoteCheckTarget, fmt.Errorf("create browser context: %w", err))
			}
			if contextResult.BrowserContextID == "" {
				return remoteCheckFailure(RemoteCheckTarget, errors.New("Target.createBrowserContext returned an empty context ID"))
			}
			browserContextID = contextResult.BrowserContextID

			createTarget := target.CreateTarget("about:blank").
				WithBrowserContextID(browserContextID).
				WithBackground(true)
			var targetResult target.CreateTargetReturns
			if err := client.execute(checkCtx, "", target.CommandCreateTarget, createTarget, &targetResult); err != nil {
				retryHeadedEmptyWindow = isRemoteHeadedEmptyWindowError(err)
				return remoteCheckFailure(RemoteCheckTarget, fmt.Errorf("create target: %w", err))
			}
			if targetResult.TargetID == "" {
				return remoteCheckFailure(RemoteCheckTarget, errors.New("Target.createTarget returned an empty target ID"))
			}
			targetID = targetResult.TargetID

			attachTarget := target.AttachToTarget(targetID).WithFlatten(true)
			var attachResult target.AttachToTargetReturns
			if err := client.execute(checkCtx, "", target.CommandAttachToTarget, attachTarget, &attachResult); err != nil {
				return remoteCheckFailure(RemoteCheckTarget, fmt.Errorf("attach to target: %w", err))
			}
			if attachResult.SessionID == "" {
				return remoteCheckFailure(RemoteCheckTarget, errors.New("Target.attachToTarget returned an empty session ID"))
			}
			attemptResult.markPassed(RemoteCheckTarget)

			if err := client.execute(checkCtx, attachResult.SessionID, page.CommandEnable, page.Enable(), nil); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("enable Page domain: %w", err))
			}
			if err := client.execute(
				checkCtx,
				attachResult.SessionID,
				page.CommandSetLifecycleEventsEnabled,
				page.SetLifecycleEventsEnabled(true),
				nil,
			); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("enable page lifecycle events: %w", err))
			}

			navigate := page.Navigate(remoteDiagnosticPage)
			var navigation page.NavigateReturns
			if err := client.execute(checkCtx, attachResult.SessionID, page.CommandNavigate, navigate, &navigation); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("navigate diagnostic page: %w", err))
			}
			if navigation.ErrorText != "" {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("navigate diagnostic page: %s", navigation.ErrorText))
			}
			if navigation.IsDownload {
				return remoteCheckFailure(RemoteCheckEvaluation, errors.New("diagnostic navigation unexpectedly became a download"))
			}
			if navigation.LoaderID == "" {
				return remoteCheckFailure(RemoteCheckEvaluation, errors.New("diagnostic navigation returned no loader ID"))
			}
			if err := client.waitForPageLoad(checkCtx, attachResult.SessionID, navigation.LoaderID); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("wait for diagnostic page load: %w", err))
			}

			evaluate := runtime.Evaluate(remoteDiagnosticExpression).
				WithAwaitPromise(true).
				WithReturnByValue(true)
			var evaluation remoteEvaluationReturns
			if err := client.execute(checkCtx, attachResult.SessionID, runtime.CommandEvaluate, evaluate, &evaluation); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("evaluate diagnostic page: %w", err))
			}
			if evaluation.ExceptionDetails != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("evaluate diagnostic page: %s", evaluation.ExceptionDetails.Text))
			}
			if evaluation.Result == nil {
				return remoteCheckFailure(RemoteCheckEvaluation, errors.New("Runtime.evaluate returned no result"))
			}
			var pageReady bool
			if err := json.Unmarshal(evaluation.Result.Value, &pageReady); err != nil {
				return remoteCheckFailure(RemoteCheckEvaluation, fmt.Errorf("decode Runtime.evaluate result: %w", err))
			}
			if evaluation.Result.Type != "boolean" || !pageReady {
				return remoteCheckFailure(RemoteCheckEvaluation, errors.New("diagnostic page content did not match"))
			}
			attemptResult.markPassed(RemoteCheckEvaluation)

			return nil
		}()

		if cleanupSucceeded {
			attemptResult.markPassed(RemoteCheckCleanup)
		}
		if attemptErr == nil || !retryHeadedEmptyWindow {
			return attemptResult, attemptErr
		}
		if attempt == policy.maxAttempts {
			return attemptResult, fmt.Errorf(
				"remote CDP headed empty-window recovery exhausted after %d attempts: %w",
				attempt,
				attemptErr,
			)
		}
		if err := m.waitForRemoteDiscoveryRetry(checkCtx, cappedRemoteDiscoveryBackoff(policy, attempt)); err != nil {
			return attemptResult, fmt.Errorf(
				"wait to retry remote CDP headed empty-window recovery after attempt %d: %w",
				attempt,
				errors.Join(attemptErr, err),
			)
		}
	}

	return result, remoteCheckFailure(RemoteCheckTarget, errors.New("remote CDP headed empty-window recovery exhausted its retry policy"))
}

func isRemoteHeadedEmptyWindowError(err error) bool {
	var protocolErr *cdproto.Error
	return errors.As(err, &protocolErr) &&
		protocolErr.Code == -32000 &&
		protocolErr.Message == remoteHeadedEmptyWindowMessage
}

type remoteDiagnosticTransport struct {
	client                *remoteDiagnosticClient
	stopCancellationClose func() bool
}

// openRemoteDiagnosticTransport retries only the safe pre-action handshake:
// fresh discovery, WebSocket dial, and Browser.getVersion. A browser restart
// can change its WebSocket target ID, so every retry rediscovers the URL. Any
// syntactically valid CDP error or malformed response fails immediately.
func (m *Manager) openRemoteDiagnosticTransport(ctx context.Context, policy remoteDiscoveryPolicy, result *RemoteCheckResult) (*remoteDiagnosticTransport, cdpbrowser.GetVersionReturns, error) {
	var emptyVersion cdpbrowser.GetVersionReturns
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		webSocketURL, err := m.discoverRemoteWebSocketURL(ctx, policy)
		if err != nil {
			return nil, emptyVersion, remoteCheckFailure(RemoteCheckDiscovery, err)
		}

		dialURL, err := normalizeRemoteWebSocketURL(ctx, webSocketURL)
		if err != nil {
			err = fmt.Errorf("prepare WebSocket URL: %w", err)
			if retryErr := m.waitForRemoteHandshakeRetry(ctx, policy, attempt, err); retryErr == nil {
				continue
			} else {
				result.markPassed(RemoteCheckDiscovery)
				return nil, emptyVersion, remoteCheckFailure(RemoteCheckWebSocket, retryErr)
			}
		}

		observer := &remoteDiagnosticFrameObserver{}
		conn, err := chromedp.DialContext(ctx, dialURL, chromedp.WithConnDebugf(observer.debugf))
		if err != nil {
			err = remoteDiagnosticContextError(ctx, fmt.Errorf("dial browser WebSocket: %w", err))
			if retryErr := m.waitForRemoteHandshakeRetry(ctx, policy, attempt, err); retryErr == nil {
				continue
			} else {
				result.markPassed(RemoteCheckDiscovery)
				return nil, emptyVersion, remoteCheckFailure(RemoteCheckWebSocket, retryErr)
			}
		}

		// chromedp.Conn reads do not observe context cancellation themselves.
		// Closing the connection is what unblocks Browser.getVersion and every
		// later command when the overall diagnostic deadline expires.
		stopCancellationClose := context.AfterFunc(ctx, func() {
			_ = conn.Close()
		})
		client := &remoteDiagnosticClient{conn: conn, observer: observer}
		var version cdpbrowser.GetVersionReturns
		err = client.execute(ctx, "", cdpbrowser.CommandGetVersion, nil, &version)
		// A response and the overall deadline can win their respective races at
		// the same instant. Do not return a transport that is already expired even
		// when Browser.getVersion itself happened to decode successfully.
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && (strings.TrimSpace(version.Product) == "" || strings.TrimSpace(version.ProtocolVersion) == "") {
			err = errors.New("Browser.getVersion returned incomplete version data")
		}
		if err == nil {
			result.markPassed(RemoteCheckDiscovery)
			result.markPassed(RemoteCheckWebSocket)
			return &remoteDiagnosticTransport{
				client:                client,
				stopCancellationClose: stopCancellationClose,
			}, version, nil
		}

		stopCancellationClose()
		closeErr := closeRemoteDiagnosticConnection(conn)
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if retryErr := m.waitForRemoteHandshakeRetry(ctx, policy, attempt, err); retryErr == nil {
			continue
		} else {
			result.markPassed(RemoteCheckDiscovery)
			return nil, emptyVersion, remoteCheckFailure(RemoteCheckWebSocket, retryErr)
		}
	}

	return nil, emptyVersion, remoteCheckFailure(RemoteCheckWebSocket, errors.New("remote CDP handshake exhausted its retry policy"))
}

func (m *Manager) waitForRemoteHandshakeRetry(ctx context.Context, policy remoteDiscoveryPolicy, attempt int, cause error) error {
	if !isRetryableRemoteTransportError(ctx, cause) || attempt >= policy.maxAttempts {
		return cause
	}
	if err := m.waitForRemoteDiscoveryRetry(ctx, cappedRemoteDiscoveryBackoff(policy, attempt)); err != nil {
		return errors.Join(err, fmt.Errorf("last WebSocket attempt: %w", cause))
	}
	return nil
}

func closeRemoteDiagnosticConnection(conn *chromedp.Conn) error {
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close WebSocket: %w", err)
	}
	return nil
}

type remoteEvaluationReturns struct {
	Result *struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text string `json:"text"`
	} `json:"exceptionDetails"`
}

type remoteDiagnosticClient struct {
	conn          *chromedp.Conn
	observer      *remoteDiagnosticFrameObserver
	nextID        int64
	pendingEvents []cdproto.Message
}

func (c *remoteDiagnosticClient) execute(ctx context.Context, sessionID target.SessionID, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.nextID++
	var rawParams []byte
	if params != nil {
		var err error
		rawParams, err = json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s parameters: %w", method, err)
		}
	}

	request := &cdproto.Message{
		ID:        c.nextID,
		SessionID: sessionID,
		Method:    cdproto.MethodType(method),
		Params:    rawParams,
	}
	if err := c.conn.Write(ctx, request); err != nil {
		return remoteDiagnosticContextError(ctx, fmt.Errorf("write %s command: %w", method, err))
	}

	for {
		var response cdproto.Message
		shape, err := c.readFrame(ctx, &response)
		if err != nil {
			return remoteDiagnosticContextError(ctx, fmt.Errorf("read %s response: %w", method, err))
		}
		isEvent, err := classifyRemoteDiagnosticFrameShape(response, shape)
		if err != nil {
			return fmt.Errorf("read %s response: %w", method, err)
		}
		if isEvent {
			c.pendingEvents = append(c.pendingEvents, response)
			continue
		}
		if response.ID != request.ID {
			return fmt.Errorf("read %s response: response ID %d, want %d", method, response.ID, request.ID)
		}
		if shape.sessionIDPresent != (request.SessionID != "") || response.SessionID != request.SessionID {
			return fmt.Errorf(
				"read %s response: response session ID %q, want %q",
				method,
				response.SessionID,
				request.SessionID,
			)
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}
}

type remoteDiagnosticFrameShape struct {
	idPresent        bool
	sessionIDPresent bool
	methodPresent    bool
	paramsPresent    bool
	resultPresent    bool
	errorPresent     bool
	id               json.RawMessage
	sessionID        json.RawMessage
	method           json.RawMessage
	params           json.RawMessage
	result           json.RawMessage
	protocolError    json.RawMessage
}

func remoteDiagnosticFrameShapeFromMessage(message cdproto.Message) remoteDiagnosticFrameShape {
	shape := remoteDiagnosticFrameShape{
		idPresent:        message.ID != 0,
		sessionIDPresent: message.SessionID != "",
		methodPresent:    message.Method != "",
		paramsPresent:    len(message.Params) != 0,
		resultPresent:    len(message.Result) != 0,
		errorPresent:     message.Error != nil,
		id:               json.RawMessage(fmt.Sprintf("%d", message.ID)),
		sessionID:        json.RawMessage(strconv.Quote(string(message.SessionID))),
		method:           json.RawMessage(strconv.Quote(string(message.Method))),
		params:           json.RawMessage(message.Params),
		result:           json.RawMessage(message.Result),
	}
	if message.Error != nil {
		// A typed cdproto.Message can only contain an object-valued protocol
		// error. Raw observers below retain the exact wire value as well, which
		// lets strict checks distinguish an absent field from explicit null.
		shape.protocolError = json.RawMessage(`{}`)
	}
	return shape
}

func parseRemoteDiagnosticFrameShape(raw []byte) (remoteDiagnosticFrameShape, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return remoteDiagnosticFrameShape{}, errors.New("CDP frame must be a JSON object")
	}
	if err := validateRemoteDiagnosticJSONFields(trimmed); err != nil {
		return remoteDiagnosticFrameShape{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return remoteDiagnosticFrameShape{}, err
	}
	if fields == nil {
		return remoteDiagnosticFrameShape{}, errors.New("CDP frame must be a JSON object")
	}

	id, idPresent := fields["id"]
	sessionID, sessionIDPresent := fields["sessionId"]
	method, methodPresent := fields["method"]
	params, paramsPresent := fields["params"]
	result, resultPresent := fields["result"]
	protocolError, errorPresent := fields["error"]
	return remoteDiagnosticFrameShape{
		idPresent:        idPresent,
		sessionIDPresent: sessionIDPresent,
		methodPresent:    methodPresent,
		paramsPresent:    paramsPresent,
		resultPresent:    resultPresent,
		errorPresent:     errorPresent,
		id:               id,
		sessionID:        sessionID,
		method:           method,
		params:           params,
		result:           result,
		protocolError:    protocolError,
	}, nil
}

// validateRemoteDiagnosticJSONFields rejects duplicate names in every object,
// including objects nested inside arrays. encoding/json otherwise keeps the
// last duplicate value, which could make an ambiguous wire identity or version
// result look valid to the typed CDP decoder.
func validateRemoteDiagnosticJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeRemoteDiagnosticJSONValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("CDP frame contains trailing JSON data")
	}
	return nil
}

func consumeRemoteDiagnosticJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("CDP JSON object at %s contains a non-string field name", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate CDP JSON field %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := consumeRemoteDiagnosticJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("CDP JSON object at %s has invalid closing delimiter", path)
		}
		return nil
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := consumeRemoteDiagnosticJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("CDP JSON array at %s has invalid closing delimiter", path)
		}
		return nil
	default:
		return fmt.Errorf("unexpected CDP JSON delimiter %q at %s", delimiter, path)
	}
}

func remoteDiagnosticDebugPayload(format string, args ...any) ([]byte, bool) {
	if (format != "-> %s" && format != "<- %s") || len(args) != 1 {
		return nil, false
	}
	switch payload := args[0].(type) {
	case []byte:
		return payload, true
	case string:
		return []byte(payload), true
	default:
		return nil, false
	}
}

type remoteDiagnosticObservedFrame struct {
	shape remoteDiagnosticFrameShape
	err   error
}

// remoteDiagnosticFrameObserver captures shape metadata from the exact bytes
// read by chromedp.Conn. It never formats or emits those bytes. This preserves
// field presence (including explicit null) that cdproto.Message alone cannot
// represent, while the actual message is still decoded by chromedp.Conn.
type remoteDiagnosticFrameObserver struct {
	mu      sync.Mutex
	inbound []remoteDiagnosticObservedFrame
}

func (o *remoteDiagnosticFrameObserver) debugf(format string, args ...any) {
	if format != "<- %s" {
		return
	}
	payload, ok := remoteDiagnosticDebugPayload(format, args...)
	if !ok {
		return
	}
	shape, err := parseRemoteDiagnosticFrameShape(payload)
	o.mu.Lock()
	o.inbound = append(o.inbound, remoteDiagnosticObservedFrame{shape: shape, err: err})
	o.mu.Unlock()
}

func (o *remoteDiagnosticFrameObserver) pop() (remoteDiagnosticObservedFrame, bool) {
	if o == nil {
		return remoteDiagnosticObservedFrame{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.inbound) == 0 {
		return remoteDiagnosticObservedFrame{}, false
	}
	observed := o.inbound[0]
	o.inbound = o.inbound[1:]
	return observed, true
}

func (c *remoteDiagnosticClient) readFrame(ctx context.Context, message *cdproto.Message) (remoteDiagnosticFrameShape, error) {
	err := c.conn.Read(ctx, message)
	observed, ok := c.observer.pop()
	if ok && observed.err != nil {
		return remoteDiagnosticFrameShape{}, observed.err
	}
	if err != nil {
		return remoteDiagnosticFrameShape{}, err
	}
	if ok {
		return observed.shape, nil
	}
	return remoteDiagnosticFrameShapeFromMessage(*message), nil
}

func classifyRemoteDiagnosticFrame(message cdproto.Message) (bool, error) {
	return classifyRemoteDiagnosticFrameShape(message, remoteDiagnosticFrameShapeFromMessage(message))
}

func classifyRemoteDiagnosticFrameShape(message cdproto.Message, shape remoteDiagnosticFrameShape) (bool, error) {
	if shape.idPresent {
		if err := validateRemoteDiagnosticPositiveID(shape.id); err != nil {
			return false, err
		}
	}
	if shape.methodPresent {
		if err := validateRemoteDiagnosticNonEmptyString("method", shape.method); err != nil {
			return false, err
		}
	}
	if shape.sessionIDPresent {
		if err := validateRemoteDiagnosticNonEmptyString("sessionId", shape.sessionID); err != nil {
			return false, err
		}
	}

	switch {
	case !shape.idPresent && shape.methodPresent:
		if shape.resultPresent || shape.errorPresent {
			return false, errors.New("malformed CDP event frame contains response result or error")
		}
		if shape.paramsPresent {
			if err := validateRemoteDiagnosticObject("event params", shape.params); err != nil {
				return false, err
			}
		}
		return true, nil
	case !shape.idPresent:
		return false, errors.New("malformed CDP frame has neither response ID nor event method")
	case shape.methodPresent:
		return false, fmt.Errorf(
			"malformed CDP frame mixes response ID %d with event method %s",
			message.ID,
			message.Method,
		)
	default:
		if shape.paramsPresent {
			return false, errors.New("malformed CDP response frame contains command params")
		}
		if shape.resultPresent == shape.errorPresent {
			return false, errors.New("malformed CDP response frame must contain exactly one of result or error")
		}
		if shape.resultPresent {
			if err := validateRemoteDiagnosticObject("response result", shape.result); err != nil {
				return false, err
			}
		}
		if shape.errorPresent {
			if err := validateRemoteDiagnosticObject("response error", shape.protocolError); err != nil {
				return false, err
			}
		}
		return false, nil
	}
}

func validateRemoteDiagnosticPositiveID(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("malformed CDP id must be a positive JSON integer")
	}
	var id int64
	if err := json.Unmarshal(trimmed, &id); err != nil || id <= 0 {
		return errors.New("malformed CDP id must be a positive JSON integer")
	}
	return nil
}

func validateRemoteDiagnosticNonEmptyString(field string, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return fmt.Errorf("malformed CDP %s must be a non-empty JSON string", field)
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil || value == "" {
		return fmt.Errorf("malformed CDP %s must be a non-empty JSON string", field)
	}
	return nil
}

func validateRemoteDiagnosticString(field string, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return fmt.Errorf("malformed CDP %s must be a JSON string", field)
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("malformed CDP %s must be a JSON string", field)
	}
	return nil
}

func validateRemoteDiagnosticObject(field string, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("malformed CDP %s must be a JSON object", field)
	}
	return nil
}

func (c *remoteDiagnosticClient) waitForPageLoad(ctx context.Context, sessionID target.SessionID, loaderID cdp.LoaderID) error {
	for {
		for i := 0; i < len(c.pendingEvents); i++ {
			event := c.pendingEvents[i]
			c.pendingEvents = append(c.pendingEvents[:i], c.pendingEvents[i+1:]...)
			i--
			matched, err := matchesPageLoadEvent(event, sessionID, loaderID)
			if err != nil {
				return err
			}
			if matched {
				return nil
			}
		}

		var event cdproto.Message
		shape, err := c.readFrame(ctx, &event)
		if err != nil {
			return remoteDiagnosticContextError(ctx, fmt.Errorf("read Page.lifecycleEvent: %w", err))
		}
		isEvent, err := classifyRemoteDiagnosticFrameShape(event, shape)
		if err != nil {
			return fmt.Errorf("read Page.lifecycleEvent: %w", err)
		}
		if !isEvent {
			return fmt.Errorf("read Page.lifecycleEvent: unexpected response frame ID %d", event.ID)
		}
		matched, err := matchesPageLoadEvent(event, sessionID, loaderID)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}
	}
}

func matchesPageLoadEvent(event cdproto.Message, sessionID target.SessionID, loaderID cdp.LoaderID) (bool, error) {
	if event.Method != cdproto.EventPageLifecycleEvent || event.SessionID != sessionID {
		return false, nil
	}

	params := bytes.TrimSpace(event.Params)
	if err := validateRemoteDiagnosticObject("Page.lifecycleEvent params", params); err != nil {
		return false, err
	}
	if err := validateRemoteDiagnosticJSONFields(params); err != nil {
		return false, fmt.Errorf("decode Page.lifecycleEvent: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return false, fmt.Errorf("decode Page.lifecycleEvent: %w", err)
	}
	for _, required := range []string{"frameId", "name"} {
		raw, present := fields[required]
		if !present {
			return false, fmt.Errorf("malformed CDP Page.lifecycleEvent is missing required %s", required)
		}
		if err := validateRemoteDiagnosticNonEmptyString("Page.lifecycleEvent "+required, raw); err != nil {
			return false, err
		}
	}
	loader, present := fields["loaderId"]
	if !present {
		return false, errors.New("malformed CDP Page.lifecycleEvent is missing required loaderId")
	}
	// loaderId is required, but CDP explicitly permits an empty string when a
	// request was fetched from a worker. Such an event is valid interleaved
	// noise and cannot match the non-empty navigation loader below.
	if err := validateRemoteDiagnosticString("Page.lifecycleEvent loaderId", loader); err != nil {
		return false, err
	}
	timestamp, present := fields["timestamp"]
	if !present {
		return false, errors.New("malformed CDP Page.lifecycleEvent is missing required timestamp")
	}
	timestamp = bytes.TrimSpace(timestamp)
	var timestampSeconds float64
	if len(timestamp) == 0 || bytes.Equal(timestamp, []byte("null")) {
		return false, errors.New("malformed CDP Page.lifecycleEvent timestamp must be a non-negative JSON number")
	}
	if err := json.Unmarshal(timestamp, &timestampSeconds); err != nil || math.IsNaN(timestampSeconds) || math.IsInf(timestampSeconds, 0) || timestampSeconds < 0 {
		return false, errors.New("malformed CDP Page.lifecycleEvent timestamp must be a non-negative JSON number")
	}

	var lifecycle page.EventLifecycleEvent
	if err := json.Unmarshal(params, &lifecycle); err != nil {
		return false, fmt.Errorf("decode Page.lifecycleEvent: %w", err)
	}
	if lifecycle.Timestamp == nil {
		return false, errors.New("malformed CDP Page.lifecycleEvent timestamp is required")
	}
	return lifecycle.LoaderID == loaderID && lifecycle.Name == "load", nil
}

func (c *remoteDiagnosticClient) cleanup(ctx context.Context, targetID target.ID, browserContextID cdp.BrowserContextID) error {
	var cleanupErrors []error
	if ctx.Err() == nil {
		if targetID != "" {
			var closeResult struct {
				Success bool `json:"success"`
			}
			if err := c.execute(ctx, "", target.CommandCloseTarget, target.CloseTarget(targetID), &closeResult); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("close target: %w", err))
			} else if !closeResult.Success {
				cleanupErrors = append(cleanupErrors, errors.New("close target: Target.closeTarget did not return success=true"))
			}
		}
		if browserContextID != "" {
			if err := c.execute(ctx, "", target.CommandDisposeBrowserContext, target.DisposeBrowserContext(browserContextID), nil); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("dispose browser context: %w", err))
			}
		}
	} else {
		// The connection is closed by the context callback. Chrome then applies
		// disposeOnDetach to the isolated diagnostic context and its targets.
		cleanupErrors = append(cleanupErrors, ctx.Err())
	}

	if err := c.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("close WebSocket: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func remoteCheckFailure(stage RemoteCheckStage, err error) error {
	return &RemoteCheckError{Stage: stage, Err: err}
}

func joinRemoteCleanupError(primary, cleanupErr error) error {
	var stageErr *RemoteCheckError
	if errors.As(primary, &stageErr) {
		return &RemoteCheckError{
			Stage: stageErr.Stage,
			Err:   errors.Join(stageErr.Err, fmt.Errorf("cleanup failed: %w", cleanupErr)),
		}
	}
	return errors.Join(primary, fmt.Errorf("remote CDP cleanup failed: %w", cleanupErr))
}

func remoteDiagnosticContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// normalizeRemoteWebSocketURL mirrors the hostname-to-IP rewrite performed by
// chromedp's RemoteAllocator without triggering another /json/version request.
// Chrome rejects WebSocket Host headers that are neither an IP nor localhost.
func normalizeRemoteWebSocketURL(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "localhost" || net.ParseIP(host) != nil {
		return parsed.String(), nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addresses) == 0 {
		return "", fmt.Errorf("hostname %q resolved to no addresses", host)
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(addresses[0].IP.String(), port)
	} else {
		parsed.Host = addresses[0].IP.String()
	}
	return parsed.String(), nil
}
