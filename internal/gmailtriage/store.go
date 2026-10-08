package gmailtriage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item statuses.
const (
	StatusNew      = "new"      // classified, not yet in a digest
	StatusReported = "reported" // delivered in a digest
	StatusSent     = "sent"     // draft sent after confirmation
	StatusSkipped  = "skipped"  // owner skipped the draft
	StatusTrashed  = "trashed"  // moved to Trash after confirmation
)

// Item is one triaged thread.
type Item struct {
	ID            int64
	Profile       string
	ThreadID      string
	LastMessageID string
	Sender        string // address
	SenderName    string
	ReplyTo       string
	Subject       string
	Snippet       string
	Bucket        string
	Why           string
	Uncertain     bool
	Draft         string
	Source        string // rule, prefilter, waiting, llm
	Status        string
	Archived      bool
	MessageTime   time.Time
	ChatMessageID int

	// inInbox is set during a run: the thread was in the inbox when triaged.
	// Only such threads count as archived by triage, so moving one out of
	// Bulk later never pulls mail that a Gmail filter skipped into the inbox.
	inInbox bool
}

// Rule is a standing correction by sender address or domain.
type Rule struct {
	ID      int64
	Profile string
	Scope   string // "sender" or "domain"
	Value   string
	Bucket  string
	Note    string
}

// Example is a few-shot sample learned from a correction or a reply note.
type Example struct {
	Sender  string
	Subject string
	Snippet string
	Bucket  string
	Note    string
	// ThreadID, MessageID and Kind identify the source; one example per
	// (thread, message, kind) is kept, so repeating a button does not pile up.
	ThreadID  string
	MessageID string
	Kind      string
}

// Example kinds.
const (
	ExampleConfirmed  = "confirmed"
	ExampleCorrection = "correction"
	ExampleNote       = "note"
)

// Action is a pending trash or send that a Telegram button must confirm.
type Action struct {
	ID        string
	Profile   string
	ItemID    int64
	Kind      string // "send" or "trash"
	ChatID    int64
	Status    string
	ExpiresAt time.Time
	// Fingerprint binds the button to what the user saw: newest message,
	// recipient and draft. A changed item no longer matches.
	Fingerprint string
}

// Action kinds.
const (
	ActionSend  = "send"
	ActionTrash = "trash"
)

// ActionTTL bounds how long a digest button stays usable.
const ActionTTL = 72 * time.Hour

// Store persists triage state in the bot database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore creates the tables when missing.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("gmail_triage: nil database")
	}
	s := &Store{db: db, now: time.Now}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS gmail_triage_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			profile TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			last_message_id TEXT NOT NULL DEFAULT '',
			sender TEXT NOT NULL DEFAULT '',
			sender_name TEXT NOT NULL DEFAULT '',
			reply_to TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			snippet TEXT NOT NULL DEFAULT '',
			bucket TEXT NOT NULL DEFAULT '',
			why TEXT NOT NULL DEFAULT '',
			uncertain INTEGER NOT NULL DEFAULT 0,
			draft TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'new',
			archived INTEGER NOT NULL DEFAULT 0,
			message_time INTEGER NOT NULL DEFAULT 0,
			chat_message_id INTEGER NOT NULL DEFAULT 0,
			digest_id INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(profile, thread_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_gmail_triage_items_status ON gmail_triage_items(profile, status);`,
		`CREATE INDEX IF NOT EXISTS idx_gmail_triage_items_chat_msg ON gmail_triage_items(profile, chat_message_id);`,
		`CREATE TABLE IF NOT EXISTS gmail_triage_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			profile TEXT NOT NULL,
			scope TEXT NOT NULL,
			value TEXT NOT NULL,
			bucket TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(profile, scope, value)
		);`,
		`CREATE TABLE IF NOT EXISTS gmail_triage_examples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			profile TEXT NOT NULL,
			sender TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			snippet TEXT NOT NULL DEFAULT '',
			bucket TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS gmail_triage_state (
			profile TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(profile, key)
		);`,
		`CREATE TABLE IF NOT EXISTS gmail_triage_actions (
			id TEXT PRIMARY KEY,
			profile TEXT NOT NULL,
			item_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			chat_id INTEGER NOT NULL,
			fingerprint TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0
		);`,
	}
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("gmail_triage schema: %w", err)
		}
	}
	// v0.21.1: examples are keyed by source so repeated feedback is upserted.
	for _, q := range []string{
		`ALTER TABLE gmail_triage_examples ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE gmail_triage_examples ADD COLUMN message_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE gmail_triage_examples ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("gmail_triage schema: %w", err)
		}
	}
	for _, q := range []string{
		// Collapse exact duplicates left by v0.21.0 (a repeated 👍 press).
		`DELETE FROM gmail_triage_examples WHERE id NOT IN (SELECT MIN(id) FROM gmail_triage_examples GROUP BY profile, sender, subject, snippet, bucket, note, thread_id, message_id, kind)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_gmail_triage_examples_source ON gmail_triage_examples(profile, thread_id, message_id, kind) WHERE thread_id != ''`,
	} {
		if _, err := db.Exec(q); err != nil {
			return nil, fmt.Errorf("gmail_triage schema: %w", err)
		}
	}
	return s, nil
}

const itemColumns = `id, profile, thread_id, last_message_id, sender, sender_name, reply_to, subject, snippet, bucket, why, uncertain, draft, source, status, archived, message_time, chat_message_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanItem(row rowScanner) (Item, error) {
	var it Item
	var uncertain, archived int
	var msgTime int64
	err := row.Scan(&it.ID, &it.Profile, &it.ThreadID, &it.LastMessageID, &it.Sender, &it.SenderName, &it.ReplyTo, &it.Subject, &it.Snippet, &it.Bucket, &it.Why, &uncertain, &it.Draft, &it.Source, &it.Status, &archived, &msgTime, &it.ChatMessageID)
	it.Uncertain = uncertain != 0
	it.Archived = archived != 0
	if msgTime > 0 {
		it.MessageTime = time.Unix(msgTime, 0)
	}
	return it, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LastMessageID returns the last triaged message of a thread ("" if unseen).
func (s *Store) LastMessageID(profile, threadID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT last_message_id FROM gmail_triage_items WHERE profile=? AND thread_id=?`, profile, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// UpsertItem records a fresh classification of a thread and resets it to new.
func (s *Store) UpsertItem(it Item) (Item, error) {
	_, err := s.db.Exec(`INSERT INTO gmail_triage_items (profile, thread_id, last_message_id, sender, sender_name, reply_to, subject, snippet, bucket, why, uncertain, draft, source, status, archived, message_time, chat_message_id, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)
		ON CONFLICT(profile, thread_id) DO UPDATE SET last_message_id=excluded.last_message_id, sender=excluded.sender, sender_name=excluded.sender_name,
			reply_to=excluded.reply_to, subject=excluded.subject, snippet=excluded.snippet, bucket=excluded.bucket, why=excluded.why, uncertain=excluded.uncertain,
			draft=excluded.draft, source=excluded.source, status=excluded.status, archived=excluded.archived, message_time=excluded.message_time,
			chat_message_id=0, digest_id=0, updated_at=excluded.updated_at`,
		it.Profile, it.ThreadID, it.LastMessageID, it.Sender, it.SenderName, it.ReplyTo, it.Subject, it.Snippet, it.Bucket, it.Why, boolInt(it.Uncertain), it.Draft, it.Source, StatusNew, boolInt(it.Archived), it.MessageTime.Unix(), s.now().Unix())
	if err != nil {
		return Item{}, err
	}
	return s.itemByThread(it.Profile, it.ThreadID)
}

func (s *Store) itemByThread(profile, threadID string) (Item, error) {
	return scanItem(s.db.QueryRow(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND thread_id=?`, profile, threadID))
}

// Item loads an item of a profile by ID.
func (s *Store) Item(profile string, id int64) (Item, error) {
	return scanItem(s.db.QueryRow(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND id=?`, profile, id))
}

// ItemByChatMessage finds the item whose card is the given Telegram message.
func (s *Store) ItemByChatMessage(profile string, messageID int) (Item, error) {
	return scanItem(s.db.QueryRow(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND chat_message_id=? AND chat_message_id != 0`, profile, messageID))
}

// NewItems lists items not yet delivered, oldest message first.
func (s *Store) NewItems(profile string) ([]Item, error) {
	rows, err := s.db.Query(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND status=? ORDER BY message_time, id`, profile, StatusNew)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ItemsByIDs loads items of a profile in the given order.
func (s *Store) ItemsByIDs(profile string, ids []int64) ([]Item, error) {
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		it, err := s.Item(profile, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// QuietItems lists the count-only items delivered in one digest.
func (s *Store) QuietItems(profile, bucket string, digestID int64, offset, limit int) ([]Item, error) {
	rows, err := s.db.Query(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND bucket=? AND digest_id=? ORDER BY message_time DESC, id LIMIT ? OFFSET ?`, profile, bucket, digestID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkReported records delivery in a digest; chatMessageID is the card
// message (0 for list-only items).
func (s *Store) MarkReported(id int64, chatMessageID int, digestID int64) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_items SET status=?, chat_message_id=CASE WHEN ? != 0 THEN ? ELSE chat_message_id END, digest_id=?, updated_at=? WHERE id=? AND status=?`,
		StatusReported, chatMessageID, chatMessageID, digestID, s.now().Unix(), id, StatusNew)
	return err
}

// SetCardMessage binds an item to the Telegram message showing it.
func (s *Store) SetCardMessage(id int64, chatMessageID int) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_items SET chat_message_id=?, updated_at=? WHERE id=?`, chatMessageID, s.now().Unix(), id)
	return err
}

// SetStatus changes an item status.
func (s *Store) SetStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_items SET status=?, updated_at=? WHERE id=?`, status, s.now().Unix(), id)
	return err
}

// SetBucket records a corrected bucket and archive state.
func (s *Store) SetBucket(id int64, bucket, why string, archived bool) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_items SET bucket=?, why=?, uncertain=0, archived=?, source='user', updated_at=? WHERE id=?`, bucket, why, boolInt(archived), s.now().Unix(), id)
	return err
}

// SetDraft replaces an item draft.
func (s *Store) SetDraft(id int64, draft string) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_items SET draft=?, updated_at=? WHERE id=?`, draft, s.now().Unix(), id)
	return err
}

// Rules lists the profile rules.
func (s *Store) Rules(profile string) ([]Rule, error) {
	rows, err := s.db.Query(`SELECT id, profile, scope, value, bucket, note FROM gmail_triage_rules WHERE profile=? ORDER BY id`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Profile, &r.Scope, &r.Value, &r.Bucket, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutRule inserts or replaces the rule for a sender or domain.
func (s *Store) PutRule(r Rule) (Rule, error) {
	_, err := s.db.Exec(`INSERT INTO gmail_triage_rules (profile, scope, value, bucket, note, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(profile, scope, value) DO UPDATE SET bucket=excluded.bucket, note=excluded.note, created_at=excluded.created_at`,
		r.Profile, r.Scope, r.Value, r.Bucket, r.Note, s.now().Unix())
	if err != nil {
		return Rule{}, err
	}
	err = s.db.QueryRow(`SELECT id FROM gmail_triage_rules WHERE profile=? AND scope=? AND value=?`, r.Profile, r.Scope, r.Value).Scan(&r.ID)
	return r, err
}

// DeleteRule removes a rule; it reports whether one existed.
func (s *Store) DeleteRule(profile string, id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM gmail_triage_rules WHERE profile=? AND id=?`, profile, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AddExample stores a few-shot example. An example with a source thread
// replaces the earlier one of the same kind for that message.
func (s *Store) AddExample(profile string, ex Example) error {
	_, err := s.db.Exec(`INSERT INTO gmail_triage_examples (profile, sender, subject, snippet, bucket, note, thread_id, message_id, kind, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(profile, thread_id, message_id, kind) WHERE thread_id != '' DO UPDATE SET sender=excluded.sender, subject=excluded.subject,
			snippet=excluded.snippet, bucket=excluded.bucket, note=excluded.note, created_at=excluded.created_at`,
		profile, ex.Sender, truncate(ex.Subject, 200), truncate(ex.Snippet, 300), ex.Bucket, truncate(ex.Note, 500), ex.ThreadID, ex.MessageID, ex.Kind, s.now().Unix())
	return err
}

// RetargetExamples moves a thread's notes and corrections to the bucket the
// user chose, and drops "confirmed" examples that now contradict it.
func (s *Store) RetargetExamples(profile, threadID, bucket string) error {
	if threadID == "" {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE gmail_triage_examples SET bucket=? WHERE profile=? AND thread_id=? AND kind IN (?, ?)`, bucket, profile, threadID, ExampleNote, ExampleCorrection); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM gmail_triage_examples WHERE profile=? AND thread_id=? AND kind=? AND bucket != ?`, profile, threadID, ExampleConfirmed, bucket)
	return err
}

// OpenItems lists items still shown to the user (not sent, trashed or skipped).
func (s *Store) OpenItems(profile string, limit int) ([]Item, error) {
	rows, err := s.db.Query(`SELECT `+itemColumns+` FROM gmail_triage_items WHERE profile=? AND status IN (?, ?) ORDER BY message_time DESC LIMIT ?`, profile, StatusNew, StatusReported, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Examples returns the newest examples, newest first.
func (s *Store) Examples(profile string, limit int) ([]Example, error) {
	rows, err := s.db.Query(`SELECT sender, subject, snippet, bucket, note FROM gmail_triage_examples WHERE profile=? ORDER BY created_at DESC, id DESC LIMIT ?`, profile, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Example
	for rows.Next() {
		var ex Example
		if err := rows.Scan(&ex.Sender, &ex.Subject, &ex.Snippet, &ex.Bucket, &ex.Note); err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}

// State keys.
const (
	stateLastRun  = "last_run_unix"
	stateSchedule = "schedule" // JSON {times, days, timezone}; one key so writes are atomic
	statePaused   = "paused"
	stateRetry    = "retry_threads"
)

// GetState reads a profile state value ("" when unset).
func (s *Store) GetState(profile, key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM gmail_triage_state WHERE profile=? AND key=?`, profile, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetState writes a profile state value; an empty value deletes it.
func (s *Store) SetState(profile, key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM gmail_triage_state WHERE profile=? AND key=?`, profile, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO gmail_triage_state (profile, key, value) VALUES (?,?,?) ON CONFLICT(profile, key) DO UPDATE SET value=excluded.value`, profile, key, value)
	return err
}

// LastRun returns the time of the last completed fetch (zero before the first run).
func (s *Store) LastRun(profile string) (time.Time, error) {
	v, err := s.GetState(profile, stateLastRun)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, nil
	}
	return time.Unix(n, 0), nil
}

// SetLastRun records a completed fetch.
func (s *Store) SetLastRun(profile string, t time.Time) error {
	return s.SetState(profile, stateLastRun, strconv.FormatInt(t.Unix(), 10))
}

// CreateAction mints a pending action for a digest button.
func (s *Store) CreateAction(profile string, itemID int64, kind string, chatID int64, fingerprint string) (Action, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return Action{}, err
	}
	a := Action{ID: hex.EncodeToString(buf), Profile: profile, ItemID: itemID, Kind: kind, ChatID: chatID, Status: "pending", ExpiresAt: s.now().Add(ActionTTL), Fingerprint: fingerprint}
	// A new button supersedes older pending ones of the same kind for the item.
	if _, err := s.db.Exec(`UPDATE gmail_triage_actions SET status='superseded' WHERE profile=? AND item_id=? AND kind=? AND status='pending'`, profile, itemID, kind); err != nil {
		return Action{}, err
	}
	_, err := s.db.Exec(`INSERT INTO gmail_triage_actions (id, profile, item_id, kind, chat_id, fingerprint, status, expires_at, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Profile, a.ItemID, a.Kind, a.ChatID, a.Fingerprint, a.Status, a.ExpiresAt.Unix(), s.now().Unix())
	return a, err
}

// consumeAction atomically marks a pending, unexpired action of the chat as used.
func (s *Store) consumeAction(id string, chatID int64) (Action, error) {
	res, err := s.db.Exec(`UPDATE gmail_triage_actions SET status='used' WHERE id=? AND chat_id=? AND status='pending' AND expires_at > ?`, id, chatID, s.now().Unix())
	if err != nil {
		return Action{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Action{}, ErrActionUnavailable
	}
	var a Action
	var exp int64
	err = s.db.QueryRow(`SELECT id, profile, item_id, kind, chat_id, fingerprint, status, expires_at FROM gmail_triage_actions WHERE id=?`, id).Scan(&a.ID, &a.Profile, &a.ItemID, &a.Kind, &a.ChatID, &a.Fingerprint, &a.Status, &exp)
	a.ExpiresAt = time.Unix(exp, 0)
	return a, err
}

// ErrActionUnavailable means the button was already used, superseded or expired.
var ErrActionUnavailable = errors.New("gmail_triage: action already used, superseded or expired")

// CancelActions retires pending actions of an item.
func (s *Store) CancelActions(profile string, itemID int64) error {
	_, err := s.db.Exec(`UPDATE gmail_triage_actions SET status='cancelled' WHERE profile=? AND item_id=? AND status='pending'`, profile, itemID)
	return err
}

func normalizeScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "sender", "from", "address", "email":
		return ScopeSender, nil
	case "domain":
		return ScopeDomain, nil
	case "local", "localpart", "local_part", "pattern", "name":
		return ScopeLocal, nil
	}
	return "", fmt.Errorf("unknown rule scope %q (sender|domain|local)", scope)
}

// Pattern renders what a rule matches: an address, *@domain or local@*.
func (r Rule) Pattern() string {
	switch r.Scope {
	case ScopeDomain:
		return "*@" + r.Value
	case ScopeLocal:
		return r.Value + "@*"
	}
	return r.Value
}

// Covers explains a rule's reach in plain words.
func (r Rule) Covers() string {
	switch r.Scope {
	case ScopeDomain:
		return "every sender at " + r.Value + " and its subdomains"
	case ScopeLocal:
		return "every sender whose address is " + r.Value + "@<any domain>"
	}
	return "only the address " + r.Value
}

// RetryThreads returns threads that failed earlier runs with their failure count.
func (s *Store) RetryThreads(profile string) (map[string]int, error) {
	v, err := s.GetState(profile, stateRetry)
	out := map[string]int{}
	if err != nil || v == "" {
		return out, err
	}
	for _, part := range strings.Split(v, ",") {
		id, n, ok := strings.Cut(part, ":")
		count, err := strconv.Atoi(n)
		if ok && err == nil && validID(id) == nil {
			out[id] = count
		}
	}
	return out, nil
}

// SetRetryThreads replaces the retry list.
func (s *Store) SetRetryThreads(profile string, retry map[string]int) error {
	parts := make([]string, 0, len(retry))
	for id, n := range retry {
		parts = append(parts, id+":"+strconv.Itoa(n))
	}
	sort.Strings(parts)
	return s.SetState(profile, stateRetry, strings.Join(parts, ","))
}
