// Package tessera implements the deterministic connector, independently of LLM providers.
package tessera

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

const Schema = "ai-brain/connector-v1"

type Workspace struct {
	BrainID    string `json:"brain_id" mapstructure:"brain_id" yaml:"brain_id"`
	Root       string `json:"root" mapstructure:"root" yaml:"root"`
	RecordsDir string `json:"records_dir" mapstructure:"records_dir" yaml:"records_dir"`
	Managed    bool   `json:"managed" mapstructure:"managed" yaml:"managed"`
}
type Route struct {
	ChatID  string  `json:"chat_id" mapstructure:"chat_id" yaml:"chat_id"`
	TopicID *string `json:"topic_id" mapstructure:"topic_id" yaml:"topic_id"`
}
type Config struct {
	Enabled     bool      `json:"-" mapstructure:"enabled" yaml:"enabled"`
	Endpoint    string    `json:"endpoint" mapstructure:"endpoint" yaml:"endpoint"`
	TokenFile   string    `json:"-" mapstructure:"token_file" yaml:"token_file"`
	ConnectorID string    `json:"connector_id" mapstructure:"connector_id" yaml:"connector_id"`
	Workspace   Workspace `json:"workspace" mapstructure:"workspace" yaml:"workspace"`
	InstanceID  string    `json:"instance_id" mapstructure:"instance_id" yaml:"instance_id"`
	AccountID   string    `json:"account_id" mapstructure:"account_id" yaml:"account_id"`
	ActorID     string    `json:"actor_id" mapstructure:"actor_id" yaml:"actor_id"`
	SenderID    string    `json:"sender_id" mapstructure:"sender_id" yaml:"sender_id"`
	Routes      []Route   `json:"routes" mapstructure:"routes" yaml:"routes"`
	PollSeconds int       `json:"-" mapstructure:"poll_seconds" yaml:"poll_seconds"`
}

func numeric(s string, positive bool) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return e == nil && strconv.FormatInt(n, 10) == s && (!positive || n > 0) && n != 0
}
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("Tessera connector endpoint must be a numeric loopback address")
	}
	if p, e := strconv.Atoi(port); e != nil || p < 1 || p > 65535 {
		return errors.New("invalid Tessera connector port")
	}
	if c.ConnectorID == "" || c.TokenFile == "" || c.InstanceID == "" || c.AccountID == "" || c.ActorID == "" || c.Workspace.BrainID == "" || c.Workspace.Root == "" || c.Workspace.RecordsDir == "" {
		return errors.New("Tessera connector identity, token file, and workspace are required")
	}
	if !numeric(c.SenderID, true) || len(c.Routes) == 0 {
		return errors.New("Tessera requires a configured sender and routes")
	}
	seen := map[string]bool{}
	for _, r := range c.Routes {
		if !numeric(r.ChatID, false) || (r.TopicID != nil && !numeric(*r.TopicID, true)) {
			return errors.New("invalid Tessera Telegram route")
		}
		key := routeKey(r)
		if seen[key] {
			return errors.New("duplicate Tessera route")
		}
		seen[key] = true
	}
	if c.PollSeconds < 0 {
		return errors.New("Tessera poll_seconds cannot be negative")
	}
	return nil
}
func routeKey(r Route) string { b, _ := json.Marshal(r); return string(b) }
func (c Config) Fingerprint() string {
	// A token rotation has no authority meaning. Public routing changes do.
	c.Routes = append([]Route(nil), c.Routes...)
	sort.Slice(c.Routes, func(i, j int) bool { return routeKey(c.Routes[i]) < routeKey(c.Routes[j]) })
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Telegram struct {
	SenderID  string  `json:"sender_id"`
	ChatID    string  `json:"chat_id"`
	TopicID   *string `json:"topic_id"`
	MessageID string  `json:"message_id,omitempty"`
	UpdateID  string  `json:"update_id,omitempty"`
}

func (c Config) Authorize(t Telegram, mutation bool) error {
	if !c.Enabled {
		return errors.New("Tessera connector is disabled")
	}
	if t.SenderID != c.SenderID {
		return errors.New("Tessera sender is not authorized")
	}
	ok := false
	for _, r := range c.Routes {
		if routeKey(r) == routeKey(Route{t.ChatID, t.TopicID}) {
			ok = true
			break
		}
	}
	if !ok {
		return errors.New("Tessera route is not authorized")
	}
	if mutation && (!numeric(t.MessageID, true) || strings.TrimSpace(t.UpdateID) == "" || len(t.UpdateID) > 256) {
		return errors.New("Tessera mutation requires an immutable Telegram update")
	}
	return nil
}

// Turn is captured at Telegram ingestion and copied into each run. It is never
// reconstructed from mutable session routing or tool arguments.
type Turn struct {
	Telegram Telegram
	Content  string
}
type turnKey struct{}

func WithTurn(ctx context.Context, t *Turn) context.Context {
	if t != nil {
		copy := *t
		copy.Telegram = cloneTelegram(t.Telegram)
		t = &copy
	}
	return context.WithValue(ctx, turnKey{}, t)
}
func TrustedTurn(ctx context.Context) (Turn, bool) {
	t, ok := ctx.Value(turnKey{}).(*Turn)
	if !ok || t == nil {
		return Turn{}, false
	}
	copy := *t
	copy.Telegram = cloneTelegram(t.Telegram)
	return copy, true
}

type Item struct {
	GoalID         string   `json:"goal_id"`
	AttentionID    string   `json:"attention_id"`
	Revision       string   `json:"revision"`
	GoalTitle      string   `json:"goal_title"`
	GoalStatus     string   `json:"goal_status"`
	StageID        *string  `json:"stage_id"`
	ResultID       *string  `json:"result_id"`
	Kind           string   `json:"kind"`
	Message        string   `json:"message"`
	AllowedActions []string `json:"allowed_actions"`
	Current        bool     `json:"current"`
	Seen           bool     `json:"seen"`
	SeenAt         *string  `json:"seen_at"`
	ActorID        string   `json:"actor_id"`
	Channel        string   `json:"channel"`
}

func (i Item) Allows(action, actor string) bool {
	if !i.Current || i.ActorID != actor || i.Channel != "telegram" {
		return false
	}
	for _, a := range i.AllowedActions {
		if a == action {
			return true
		}
	}
	return false
}

type AttentionPage struct {
	Items          []Item  `json:"items"`
	NextCursor     *string `json:"next_cursor"`
	Complete       bool    `json:"complete"`
	Generation     string  `json:"generation"`
	DeliveryCursor uint64  `json:"delivery_cursor"`
}
type InboxItem struct {
	CaptureID string `json:"capture_id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	Revision  string `json:"revision"`
}
type InboxPage struct {
	Items      []InboxItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
	Complete   bool        `json:"complete"`
	Generation string      `json:"generation"`
}
type Receipt struct {
	OperationID   string `json:"operation_id"`
	Status        string `json:"status"`
	RequestSHA256 string `json:"request_sha256"`
	Replayed      bool   `json:"replayed"`
}
type APIError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Current json.RawMessage `json:"current,omitempty"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Intent contains only immutable nonsecret command/context bytes; credentials
// and transport correlation are added after durable retention.
type Intent struct {
	Telegram Telegram       `json:"telegram"`
	Command  map[string]any `json:"command"`
}
type Response struct {
	Schema string          `json:"schema"`
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Data   json.RawMessage `json:"data"`
	Error  *APIError       `json:"error"`
}

func cloneTelegram(t Telegram) Telegram {
	if t.TopicID != nil {
		v := *t.TopicID
		t.TopicID = &v
	}
	return t
}
func cloneConfig(c Config) Config {
	c.Routes = append([]Route(nil), c.Routes...)
	for i, r := range c.Routes {
		if r.TopicID != nil {
			v := *r.TopicID
			c.Routes[i].TopicID = &v
		}
	}
	return c
}
