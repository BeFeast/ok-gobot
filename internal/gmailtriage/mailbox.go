package gmailtriage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Message is the part of a Gmail message triage needs.
type Message struct {
	ID              string
	ThreadID        string
	From            string
	To              string
	Cc              string
	ReplyTo         string
	Subject         string
	ListUnsubscribe string
	ListID          string
	Precedence      string
	AutoSubmitted   string
	Labels          []string // Gmail label IDs (system labels are names like INBOX)
	Time            time.Time
	Snippet         string
	Body            string // plain text, bounded
}

// HasLabel reports whether the message carries a label ID.
func (m Message) HasLabel(label string) bool {
	for _, l := range m.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// FromAddress is the lower-case sender address.
func (m Message) FromAddress() string { return addressOf(m.From) }

// FromName is the display name, falling back to the address.
func (m Message) FromName() string {
	if a, err := mail.ParseAddress(m.From); err == nil && strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	if addr := m.FromAddress(); addr != "" {
		return addr
	}
	return strings.TrimSpace(m.From)
}

// Thread is a Gmail thread with messages oldest first.
type Thread struct {
	ID       string
	Messages []Message
}

// Last returns the newest message.
func (t Thread) Last() Message {
	if len(t.Messages) == 0 {
		return Message{}
	}
	return t.Messages[len(t.Messages)-1]
}

// Subject is the thread subject taken from its first message.
func (t Thread) Subject() string {
	for _, m := range t.Messages {
		if s := strings.TrimSpace(m.Subject); s != "" {
			return s
		}
	}
	return ""
}

// Reply is an outgoing answer within a thread.
type Reply struct {
	ThreadID         string
	ReplyToMessageID string
	To               string
	Subject          string
	Body             string
}

// Reader is the read-only part of a mailbox.
type Reader interface {
	Search(ctx context.Context, query string, max int) ([]string, error)
	Thread(ctx context.Context, threadID string) (Thread, error)
}

// Writer changes a mailbox. Only assistant profiles ever hold one, and Trash
// and Send are reachable only through a confirmed pending action (policy.go).
type Writer interface {
	EnsureLabels(ctx context.Context, names []string) error
	ModifyThread(ctx context.Context, threadID string, add, remove []string) error
	TrashThread(ctx context.Context, threadID string) error
	SendReply(ctx context.Context, r Reply) error
}

// ErrForbidden is returned when a profile's policy rejects an operation.
var ErrForbidden = errors.New("gmail_triage: operation forbidden by profile policy")

// Runner executes the gog binary. stdin may be empty.
type Runner func(ctx context.Context, binary string, args []string, env []string, stdin string) ([]byte, error)

// ExecRunner runs gog as a subprocess without a shell.
func ExecRunner(ctx context.Context, binary string, args []string, env []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return nil, fmt.Errorf("gog %s: %w: %s", strings.Join(commandPath(args), " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// gog command paths triage uses. Anything else is rejected before exec.
var (
	readCommands = map[string]bool{
		"gmail search":      true,
		"gmail thread get":  true,
		"gmail labels list": true,
	}
	writeCommands = map[string]bool{
		"gmail thread modify": true,
		"gmail labels create": true,
		"gmail send":          true,
	}
)

// commandPath is the leading non-flag tokens of a gog argv (the subcommand).
func commandPath(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(out) == 3 {
			break
		}
		out = append(out, a)
	}
	return out
}

// checkCommand is the last line of defence: a read-only client refuses to exec
// any gog command outside the read allowlist, whatever the caller asked for.
func checkCommand(readOnly bool, args []string) error {
	path := commandPath(args)
	for n := len(path); n >= 2; n-- {
		key := strings.Join(path[:n], " ")
		if readCommands[key] {
			return nil
		}
		if writeCommands[key] {
			if readOnly {
				return fmt.Errorf("%w: %q on a read-only mailbox", ErrForbidden, key)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: gog command %q is not allowed", ErrForbidden, strings.Join(path, " "))
}

// GogClient talks to Gmail through the gog CLI.
type GogClient struct {
	Binary   string
	Account  string
	Env      []string
	ReadOnly bool
	Run      Runner
}

func (g *GogClient) exec(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	if err := checkCommand(g.ReadOnly, args); err != nil {
		return nil, err
	}
	run := g.Run
	if run == nil {
		run = ExecRunner
	}
	binary := g.Binary
	if binary == "" {
		binary = DefaultGogBinary
	}
	// Global flags go before a "--" separator so they are never read as positionals.
	split := len(args)
	for i, a := range args {
		if a == "--" {
			split = i
			break
		}
	}
	full := make([]string, 0, len(args)+4)
	full = append(full, args[:split]...)
	full = append(full, "--json", "--no-input", "--account", g.Account)
	full = append(full, args[split:]...)
	return run(ctx, binary, full, g.Env, stdin)
}

// gmailPageMax is the Gmail API limit for one threads.list page.
const gmailPageMax = 500

// Search returns up to max thread IDs matching a Gmail query, newest first,
// following page tokens.
func (g *GogClient) Search(ctx context.Context, query string, max int) ([]string, error) {
	if max <= 0 {
		max = DefaultMaxThreads
	}
	var ids []string
	page := ""
	for len(ids) < max {
		args := []string{"gmail", "search", "--max=" + strconv.Itoa(min(max-len(ids), gmailPageMax))}
		if page != "" {
			args = append(args, "--page="+page)
		}
		// "--" ends flag parsing so a query starting with "-" stays a query.
		args = append(args, "--", query)
		out, err := g.exec(ctx, "", args...)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Threads []struct {
				ID string `json:"id"`
			} `json:"threads"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, fmt.Errorf("parse gog search: %w", err)
		}
		for _, t := range resp.Threads {
			if validID(t.ID) == nil {
				ids = append(ids, t.ID)
			}
		}
		if resp.NextPageToken == "" || len(resp.Threads) == 0 {
			break
		}
		page = resp.NextPageToken
	}
	return ids, nil
}

// Thread fetches a full thread.
func (g *GogClient) Thread(ctx context.Context, threadID string) (Thread, error) {
	if err := validID(threadID); err != nil {
		return Thread{}, err
	}
	out, err := g.exec(ctx, "", "gmail", "thread", "get", threadID)
	if err != nil {
		return Thread{}, err
	}
	var resp struct {
		Thread apiThread `json:"thread"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return Thread{}, fmt.Errorf("parse gog thread: %w", err)
	}
	return resp.Thread.convert(), nil
}

// labelNames lists existing user label names (lower-cased).
func (g *GogClient) labelNames(ctx context.Context) (map[string]bool, error) {
	out, err := g.exec(ctx, "", "gmail", "labels", "list")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse gog labels: %w", err)
	}
	names := make(map[string]bool, len(resp.Labels))
	for _, l := range resp.Labels {
		names[strings.ToLower(l.Name)] = true
	}
	return names, nil
}

// EnsureLabels creates missing labels.
func (g *GogClient) EnsureLabels(ctx context.Context, names []string) error {
	existing, err := g.labelNames(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if existing[strings.ToLower(name)] {
			continue
		}
		if _, err := g.exec(ctx, "", "gmail", "labels", "create", "--", name); err != nil {
			return err
		}
		existing[strings.ToLower(name)] = true
	}
	return nil
}

// ModifyThread adds and removes labels (names or system IDs) on a thread.
func (g *GogClient) ModifyThread(ctx context.Context, threadID string, add, remove []string) error {
	if err := validID(threadID); err != nil {
		return err
	}
	args := []string{"gmail", "thread", "modify", threadID}
	if len(add) > 0 {
		args = append(args, "--add="+strings.Join(add, ","))
	}
	if len(remove) > 0 {
		args = append(args, "--remove="+strings.Join(remove, ","))
	}
	_, err := g.exec(ctx, "", args...)
	return err
}

// TrashThread moves a thread to Trash (recoverable for 30 days).
func (g *GogClient) TrashThread(ctx context.Context, threadID string) error {
	return g.ModifyThread(ctx, threadID, []string{"TRASH"}, []string{"INBOX"})
}

// SendReply sends a plain-text reply in a thread. The body goes through stdin.
func (g *GogClient) SendReply(ctx context.Context, r Reply) error {
	if err := validID(r.ReplyToMessageID); err != nil {
		return err
	}
	if addressOf(r.To) == "" {
		return errors.New("reply has no valid recipient")
	}
	if strings.TrimSpace(r.Body) == "" {
		return errors.New("reply body is empty")
	}
	// "--flag=value" keeps a value that starts with "-" from parsing as a flag.
	_, err := g.exec(ctx, r.Body, "gmail", "send",
		"--reply-to-message-id="+r.ReplyToMessageID,
		"--to="+addressOf(r.To),
		"--subject="+r.Subject,
		"--body-file=-")
	return err
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid Gmail id %q", id)
	}
	return nil
}

// apiThread mirrors the Gmail API Thread JSON that `gog gmail thread get --json` prints.
type apiThread struct {
	ID       string       `json:"id"`
	Messages []apiMessage `json:"messages"`
}

type apiMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	Snippet      string   `json:"snippet"`
	InternalDate string   `json:"internalDate"`
	Payload      apiPart  `json:"payload"`
}

type apiPart struct {
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []apiPart `json:"parts"`
}

const maxBodyChars = 4000

func (t apiThread) convert() Thread {
	out := Thread{ID: t.ID}
	for _, m := range t.Messages {
		// An unsent Gmail draft is not part of the conversation.
		if hasString(m.LabelIDs, "DRAFT") {
			continue
		}
		msg := Message{
			ID:       m.ID,
			ThreadID: m.ThreadID,
			Labels:   m.LabelIDs,
			Snippet:  html.UnescapeString(m.Snippet),
		}
		if ms, err := strconv.ParseInt(m.InternalDate, 10, 64); err == nil {
			msg.Time = time.UnixMilli(ms)
		}
		for _, h := range m.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "from":
				msg.From = h.Value
			case "to":
				msg.To = h.Value
			case "cc":
				msg.Cc = h.Value
			case "reply-to":
				msg.ReplyTo = h.Value
			case "subject":
				msg.Subject = h.Value
			case "list-unsubscribe":
				msg.ListUnsubscribe = h.Value
			case "list-id":
				msg.ListID = h.Value
			case "precedence":
				msg.Precedence = h.Value
			case "auto-submitted":
				msg.AutoSubmitted = h.Value
			}
		}
		msg.Body = truncate(bodyText(m.Payload), maxBodyChars)
		if msg.ThreadID == "" {
			msg.ThreadID = t.ID
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}

// bodyText prefers text/plain and falls back to tag-stripped text/html.
func bodyText(p apiPart) string {
	if plain := findPart(p, "text/plain"); plain != "" {
		return plain
	}
	if h := findPart(p, "text/html"); h != "" {
		return stripHTML(h)
	}
	return ""
}

func findPart(p apiPart, mime string) string {
	if strings.EqualFold(p.MimeType, mime) && p.Body.Data != "" {
		if data, err := decodeBase64URL(p.Body.Data); err == nil {
			return string(data)
		}
	}
	for _, child := range p.Parts {
		if s := findPart(child, mime); s != "" {
			return s
		}
	}
	return ""
}

func decodeBase64URL(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

var (
	tagPattern   = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	spacePattern = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankLines   = regexp.MustCompile(`\n{3,}`)
)

func stripHTML(s string) string {
	s = tagPattern.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spacePattern.ReplaceAllString(s, " ")
	return blankLines.ReplaceAllString(s, "\n\n")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func hasString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// plainAddress accepts only ordinary addresses. RFC 5322 allows a quoted
// local part with spaces and punctuation, which would let an attacker smuggle
// text into prompts, rules and argv; such senders are treated as unknown.
var plainAddress = regexp.MustCompile(`^[a-z0-9._%+'][a-z0-9._%+'-]{0,63}@[a-z0-9-]+(\.[a-z0-9-]+)+$`)

// addressOf extracts the lower-case address from an RFC 5322 address header,
// or "" when it is not a plain address.
func addressOf(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	addr := ""
	if a, err := mail.ParseAddress(header); err == nil {
		addr = a.Address
	} else if list, err := mail.ParseAddressList(header); err == nil && len(list) > 0 {
		addr = list[0].Address
	} else if strings.Contains(header, "@") && !strings.ContainsAny(header, " <>") {
		addr = header
	}
	addr = strings.ToLower(addr)
	if len(addr) > 254 || !plainAddress.MatchString(addr) {
		return ""
	}
	return addr
}

// domainOf returns the domain of an address.
func domainOf(addr string) string {
	_, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(domain)
}
