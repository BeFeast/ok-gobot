package tessera

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"ok-gobot/internal/storage"
)

type Coordinator struct {
	config      Config
	store       *storage.Store
	transport   Transport
	fingerprint string
}

func NewCoordinator(config Config, store *storage.Store, transport Transport) (*Coordinator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if store == nil || transport == nil {
		return nil, errors.New("Tessera requires durable storage and transport")
	}
	// Reuse the client's deep copy so callers cannot mutate route authority later.
	client, err := NewClient(config)
	if err != nil {
		return nil, err
	}
	return &Coordinator{config: client.config, store: store, transport: transport, fingerprint: config.Fingerprint()}, nil
}
func (c *Coordinator) Fingerprint() string { return c.fingerprint }
func (c *Coordinator) Config() Config {
	return cloneConfig(c.config)
}
func (c *Coordinator) Read(ctx context.Context, t Telegram, command map[string]any) (json.RawMessage, error) {
	op, err := validateCommand(command)
	if err != nil {
		return nil, err
	}
	if mutationCommand(op) {
		return nil, errors.New("mutations require durable Tessera delivery")
	}
	if err = c.config.Authorize(t, false); err != nil {
		return nil, err
	}
	return c.transport.Call(ctx, Intent{Telegram: t, Command: command})
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func upstreamKey(t Telegram, invocation string) string {
	return hash([]any{t.SenderID, t.ChatID, t.TopicID, t.MessageID, t.UpdateID, invocation})
}
func (c *Coordinator) Mutate(ctx context.Context, t Telegram, invocation string, command map[string]any) (json.RawMessage, error) {
	op, err := validateCommand(command)
	if err != nil {
		return nil, err
	}
	if !mutationCommand(op) {
		return nil, errors.New("not a Tessera mutation")
	}
	if err = c.config.Authorize(t, true); err != nil {
		return nil, err
	}
	if invocation == "" || len(invocation) > 80 {
		return nil, errors.New("invalid durable Tessera invocation")
	}
	if _, exists := command["operation_id"]; exists {
		return nil, errors.New("Tessera operation identity is assigned by the coordinator")
	}
	if op == "inbox_capture" || op == "attention_reply" {
		body, ok := command["text"].(string)
		if !ok || strings.TrimSpace(body) == "" || len(body) > 65536 {
			return nil, errors.New("enter 1–65,536 UTF-8 bytes of text")
		}
	}
	key := upstreamKey(t, invocation)
	// Invocation names are fixed by command/tool code, never by the model. The
	// same upstream update cannot become a second action after regenerated output.
	t.UpdateID += ":" + invocation
	if len(t.UpdateID) > 256 {
		return nil, errors.New("Tessera update identity exceeds its bound")
	}
	payload := Intent{Telegram: t, Command: command}
	digest := hash(payload)
	copyCommand := make(map[string]any, len(command)+1)
	for k, v := range command {
		copyCommand[k] = v
	}
	operation := uuid.NewString()
	copyCommand["operation_id"] = operation
	payload.Command = copyCommand
	bytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	retained, err := c.store.RetainTesseraIntent(storage.TesseraIntent{Key: key, Fingerprint: c.fingerprint, Digest: digest, OperationID: operation, Payload: string(bytes)})
	if err != nil {
		return nil, err
	}
	return c.deliver(ctx, retained)
}
func (c *Coordinator) deliver(ctx context.Context, v storage.TesseraIntent) (json.RawMessage, error) {
	if v.Fingerprint != c.fingerprint {
		return nil, errors.New("retained Tessera delivery belongs to another connector configuration")
	}
	if v.State == "committed" {
		return json.RawMessage(v.Receipt), nil
	}
	if v.State == "rejected" {
		var api APIError
		if json.Unmarshal([]byte(v.LastError), &api) == nil && api.Code != "" {
			return nil, &api
		}
		return nil, errors.New("Tessera rejected this exact target; review a current item before a new action")
	}
	var payload Intent
	if json.Unmarshal([]byte(v.Payload), &payload) != nil {
		return nil, errors.New("retained Tessera payload is invalid")
	}
	if payload.Command["operation_id"] != v.OperationID {
		return nil, errors.New("retained Tessera operation identity changed")
	}
	if err := c.config.Authorize(payload.Telegram, true); err != nil {
		return nil, err
	}
	result, err := c.transport.Call(ctx, payload)
	if err != nil {
		var api *APIError
		rejected := errors.As(err, &api) && api.Code == "attention_stale"
		message := err.Error()
		if rejected {
			b, _ := json.Marshal(api)
			message = string(b)
		}
		if saveErr := c.store.FailTesseraIntent(v.Key, v.OperationID, message, rejected); saveErr != nil {
			return nil, fmt.Errorf("Tessera delivery and failure recording are uncertain: %w", saveErr)
		}
		return nil, err
	}
	var ack struct {
		Receipt        Receipt `json:"receipt"`
		CaptureID      string  `json:"capture_id"`
		DecisionID     string  `json:"decision_id"`
		AcknowledgedAt string  `json:"acknowledged_at"`
	}
	if json.Unmarshal(result, &ack) != nil || ack.Receipt.Status != "committed" || ack.Receipt.OperationID != v.OperationID {
		return nil, errors.New("Tessera receipt does not match the retained request")
	}
	op, _ := payload.Command["op"].(string)
	if (op == "inbox_capture" && ack.CaptureID == "") || (op == "attention_reply" && ack.DecisionID == "") || (op == "attention_ack" && ack.AcknowledgedAt == "") {
		return nil, errors.New("Tessera receipt omits the committed result")
	}
	if err = c.store.FinishTesseraIntent(v.Key, v.OperationID, string(result)); err != nil {
		return nil, err
	}
	return result, nil
}
func (c *Coordinator) RetryPending(ctx context.Context, t Telegram) ([]json.RawMessage, error) {
	if err := c.config.Authorize(t, false); err != nil {
		return nil, err
	}
	pending, err := c.store.PendingTesseraIntents(c.fingerprint)
	if err != nil {
		return nil, err
	}
	var results []json.RawMessage
	for _, v := range pending {
		var p Intent
		if json.Unmarshal([]byte(v.Payload), &p) != nil {
			return nil, errors.New("invalid retained Tessera payload")
		}
		if p.Telegram.SenderID != t.SenderID || routeKey(Route{p.Telegram.ChatID, p.Telegram.TopicID}) != routeKey(Route{t.ChatID, t.TopicID}) {
			continue
		}
		r, e := c.deliver(ctx, v)
		if e != nil {
			return results, e
		}
		results = append(results, r)
	}
	return results, nil
}

// DeliveryMetadata travels through the existing outbox; it never becomes native
// backend authority. The configured fingerprint is checked again before sending.
type DeliveryMetadata struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	TopicID     int64  `json:"topic_id"`
	ForceReply  bool   `json:"force_reply"`
}

func (c *Coordinator) EnqueueAttention(t Telegram, item Item) (int64, error) {
	if err := c.config.Authorize(t, false); err != nil {
		return 0, err
	}
	if !item.Current || item.ActorID != c.config.ActorID || item.Channel != "telegram" || !strings.HasPrefix(item.Revision, "sha256:") {
		return 0, errors.New("Tessera attention identity does not match this connector")
	}
	if item.Kind != "blocker" && item.Kind != "decision" && item.Kind != "final" {
		return 0, nil
	}
	chat, _ := strconv.ParseInt(t.ChatID, 10, 64)
	sender, _ := strconv.ParseInt(t.SenderID, 10, 64)
	var topic int64
	if t.TopicID != nil {
		topic, _ = strconv.ParseInt(*t.TopicID, 10, 64)
	}
	// Event identity excludes mutable seen state and observation timestamps.
	target := struct {
		GoalID         string   `json:"goal_id"`
		AttentionID    string   `json:"attention_id"`
		Revision       string   `json:"revision"`
		StageID        *string  `json:"stage_id"`
		Kind           string   `json:"kind"`
		ActorID        string   `json:"actor_id"`
		Channel        string   `json:"channel"`
		AllowedActions []string `json:"allowed_actions"`
	}{item.GoalID, item.AttentionID, item.Revision, item.StageID, item.Kind, item.ActorID, item.Channel, item.AllowedActions}
	bytes, _ := json.Marshal(target)
	metadata, _ := json.Marshal(DeliveryMetadata{Kind: "tessera-attention", Fingerprint: c.fingerprint, TopicID: topic, ForceReply: item.Kind != "final"})
	text := fmt.Sprintf("Tessera · Attention\n%s\n\n%s", preview(item.GoalTitle, 150), preview(item.Message, 1300))
	if item.Kind == "final" {
		text += "\n\nReply /seen to mark this version seen."
	} else {
		text += "\n\nReply with a decision, or /seen to mark this version seen. A reply is saved as unverified knowledge."
	}
	text += "\n\nDetails: /attention " + item.GoalID + " " + item.AttentionID + " " + item.Revision
	event := hash([]any{c.config.Workspace.BrainID, c.config.ActorID, t.ChatID, t.TopicID, item.GoalID, item.AttentionID, item.Revision})
	return c.store.EnqueueTesseraDelivery(storage.TesseraDelivery{EventKey: event, Fingerprint: c.fingerprint, ChatID: chat, TopicID: topic, SenderID: sender, Target: string(bytes), Metadata: string(metadata), Text: text})
}
func (c *Coordinator) BoundReply(ctx context.Context, t Telegram, repliedMessageID int64, text string, seen bool) (json.RawMessage, error) {
	if err := c.config.Authorize(t, true); err != nil {
		return nil, err
	}
	chat, _ := strconv.ParseInt(t.ChatID, 10, 64)
	sender, _ := strconv.ParseInt(t.SenderID, 10, 64)
	var topic int64
	if t.TopicID != nil {
		topic, _ = strconv.ParseInt(*t.TopicID, 10, 64)
	}
	binding, err := c.store.TesseraReplyBinding(c.fingerprint, chat, topic, sender, repliedMessageID)
	if err != nil {
		return nil, err
	}
	var item Item
	if json.Unmarshal([]byte(binding.Target), &item) != nil {
		return nil, errors.New("retained attention target is invalid")
	}
	item.Current = true
	action := "save_decision"
	op := "attention_reply"
	if seen {
		action = "ack_seen"
		op = "attention_ack"
	}
	if !item.Allows(action, c.config.ActorID) {
		return nil, errors.New("this retained attention item does not allow that action")
	}
	command := map[string]any{"op": op, "goal_id": item.GoalID, "attention_id": item.AttentionID, "expected_revision": item.Revision, "stage_id": item.StageID}
	if !seen {
		command["text"] = text
	}
	return c.Mutate(ctx, t, "bound-"+action, command)
}
func IsUnbound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func preview(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "… [preview truncated; use /attention for details]"
}

// ResumeTools resolves a replay before inference can regenerate different calls.
// No retained action means the ordinary provider path may proceed.
func (c *Coordinator) ResumeTools(ctx context.Context, t Telegram) (bool, error) {
	if err := c.config.Authorize(t, true); err != nil {
		return false, nil
	}
	found := false
	for _, name := range []string{"tool-inbox_capture", "tool-attention_reply", "tool-attention_ack"} {
		v, err := c.store.TesseraIntent(upstreamKey(t, name))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return found, err
		}
		found = true
		if _, err = c.deliver(ctx, v); err != nil {
			return true, err
		}
	}
	return found, nil
}

func (c *Coordinator) RetryNotifications(t Telegram) (int64, error) {
	if err := c.config.Authorize(t, false); err != nil {
		return 0, err
	}
	chat, _ := strconv.ParseInt(t.ChatID, 10, 64)
	sender, _ := strconv.ParseInt(t.SenderID, 10, 64)
	var topic int64
	if t.TopicID != nil {
		topic, _ = strconv.ParseInt(*t.TopicID, 10, 64)
	}
	return c.store.RetryTesseraOutbox(c.fingerprint, chat, topic, sender)
}
