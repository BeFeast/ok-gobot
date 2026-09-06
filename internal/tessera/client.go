package tessera

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PolicyFingerprint is shared with Tessera's Rust listener. Values are framed by
// UTF-8 byte length rather than JSON escaping; null topics remain explicit.
func (c Config) PolicyFingerprint() string {
	var b strings.Builder
	field := func(v string) { fmt.Fprintf(&b, "s%d:%s;", len(v), v) }
	for _, v := range []string{"ai-brain/connector-policy/v1", c.ConnectorID, c.Workspace.BrainID, c.Workspace.Root, c.Workspace.RecordsDir, strconv.FormatBool(c.Workspace.Managed), c.InstanceID, c.AccountID, c.ActorID, c.SenderID, strconv.Itoa(len(c.Routes))} {
		field(v)
	}
	routes := append([]Route(nil), c.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		a, z := routes[i], routes[j]
		if a.ChatID != z.ChatID {
			return a.ChatID < z.ChatID
		}
		if a.TopicID == nil {
			return z.TopicID != nil
		}
		if z.TopicID == nil {
			return false
		}
		return *a.TopicID < *z.TopicID
	})
	for _, r := range routes {
		field(r.ChatID)
		if r.TopicID == nil {
			b.WriteString("n;")
		} else {
			field(*r.TopicID)
		}
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

type Transport interface {
	Call(context.Context, Intent) (json.RawMessage, error)
}
type Client struct{ config Config }

func NewClient(config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.Routes = append([]Route(nil), config.Routes...)
	for i, r := range config.Routes {
		if r.TopicID != nil {
			v := *r.TopicID
			config.Routes[i].TopicID = &v
		}
	}
	return &Client{config: config}, nil
}

var allowedCommands = map[string][]string{
	"capabilities": {}, "inbox_list": {"limit", "cursor"}, "inbox_get": {"capture_id"},
	"inbox_capture": {"operation_id", "text"}, "attention_list": {"limit", "cursor"},
	"attention_get":   {"goal_id", "attention_id", "revision"},
	"attention_reply": {"operation_id", "goal_id", "attention_id", "expected_revision", "stage_id", "text"},
	"attention_ack":   {"operation_id", "goal_id", "attention_id", "expected_revision", "stage_id"},
}

func mutationCommand(op string) bool {
	return op == "inbox_capture" || op == "attention_reply" || op == "attention_ack"
}
func validateCommand(command map[string]any) (string, error) {
	op, _ := command["op"].(string)
	fields, ok := allowedCommands[op]
	if !ok {
		return "", errors.New("Tessera command is not permitted")
	}
	allowed := map[string]bool{"op": true}
	for _, f := range fields {
		allowed[f] = true
	}
	for f := range command {
		if !allowed[f] {
			return "", errors.New("Tessera command contains an unsupported field")
		}
	}
	if op == "attention_reply" || op == "attention_ack" {
		if _, ok := command["stage_id"]; !ok {
			return "", errors.New("Tessera attention action requires explicit nullable stage_id")
		}
	}
	return op, nil
}
func (c *Client) Call(ctx context.Context, intent Intent) (json.RawMessage, error) {
	op, err := validateCommand(intent.Command)
	if err != nil {
		return nil, err
	}
	if err = c.config.Authorize(intent.Telegram, mutationCommand(op)); err != nil {
		return nil, err
	}
	secret, err := os.ReadFile(c.config.TokenFile)
	if err != nil {
		return nil, errors.New("cannot read Tessera connector credential")
	}
	token := strings.TrimSpace(string(secret))
	if token == "" {
		return nil, errors.New("Tessera connector credential is empty")
	}
	id := uuid.NewString()
	envelope := struct {
		Schema    string    `json:"schema"`
		ID        string    `json:"id"`
		Workspace Workspace `json:"expected_workspace"`
		Connector struct {
			ID                string `json:"id"`
			Token             string `json:"token"`
			PolicyFingerprint string `json:"policy_fingerprint"`
		} `json:"connector"`
		Telegram Telegram       `json:"telegram"`
		Command  map[string]any `json:"command"`
	}{Schema: Schema, ID: id, Workspace: c.config.Workspace, Telegram: intent.Telegram, Command: intent.Command}
	envelope.Connector.ID = c.config.ConnectorID
	envelope.Connector.Token = token
	envelope.Connector.PolicyFingerprint = c.config.PolicyFingerprint()
	request, err := json.Marshal(envelope)
	if err != nil {
		return nil, errors.New("cannot encode Tessera request")
	}
	if len(request) > 1024*1024 {
		return nil, errors.New("Tessera request exceeds transport limit")
	}
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", c.config.Endpoint)
	if err != nil {
		return nil, errors.New("cannot connect to Tessera connector; retained delivery is unchanged")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, errors.New("cannot configure Tessera connection")
	}
	if _, err = io.Copy(conn, bytes.NewReader(append(request, '\n'))); err != nil {
		return nil, errors.New("Tessera delivery is uncertain; recover the retained request")
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 16*1024*1024+1)).ReadBytes('\n')
	if err != nil || len(line) > 16*1024*1024 {
		return nil, errors.New("Tessera reply is incomplete; recover the retained request")
	}
	var reply Response
	if json.Unmarshal(line, &reply) != nil || reply.Schema != Schema || reply.ID != id {
		return nil, errors.New("Tessera reply does not match the request")
	}
	if !reply.OK {
		if reply.Error == nil {
			return nil, errors.New("Tessera rejected the request without a receipt")
		}
		return nil, reply.Error
	}
	if len(reply.Data) == 0 || string(reply.Data) == "null" {
		return nil, errors.New("Tessera returned no result")
	}
	return reply.Data, nil
}
