// Package gmailtriage sorts a Gmail mailbox into a short Telegram digest.
//
// Policy lives here, not in a prompt: a read_only profile has no code path
// that reaches a mutating Gmail command, and an assistant profile can only
// trash or send through a pending action that a Telegram button confirmed.
// Email content is untrusted input; the classification pass is a plain
// completion with no tools.
package gmailtriage

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Mode is the mailbox access policy of a profile.
type Mode string

const (
	// ModeReadOnly never changes the mailbox. Pair it with a gmail.readonly
	// OAuth token so Google enforces the same boundary.
	ModeReadOnly Mode = "read_only"
	// ModeAssistant labels every thread, archives and marks Bulk as read on
	// its own, and trashes or sends only after a confirmed Telegram button.
	ModeAssistant Mode = "assistant"
)

// Taxonomy selects the bucket set.
type Taxonomy string

const (
	// TaxonomyOwner answers "what needs me": reply, action, waiting, meetings, FYI, bulk.
	TaxonomyOwner Taxonomy = "owner"
	// TaxonomySales reads a shared address through a sales lens.
	TaxonomySales Taxonomy = "sales"
)

// Bucket keys. They are stable identifiers stored in SQLite and callback data.
const (
	BucketReply    = "reply"
	BucketAction   = "action"
	BucketWaiting  = "waiting"
	BucketMeetings = "meetings"
	BucketFYI      = "fyi"
	BucketBulk     = "bulk"

	BucketSales  = "sales"
	BucketUrgent = "urgent"
	BucketNeeds  = "needs_reply"
	BucketIgnore = "ignore"
)

// BucketDef describes one bucket of a taxonomy.
type BucketDef struct {
	Key   string
	Emoji string
	// CountOnly buckets are summarized as a count instead of item cards.
	CountOnly bool
	// Label is the Gmail label suffix written by assistant profiles.
	Label string
	// Hint explains the bucket to the classifier.
	Hint string
}

var taxonomies = map[Taxonomy][]BucketDef{
	TaxonomyOwner: {
		{Key: BucketReply, Emoji: "💬", Label: "Reply", Hint: "a person expects a reply from the mailbox owner"},
		{Key: BucketAction, Emoji: "⏰", Label: "Action", Hint: "the owner must do something, often with a deadline (pay, sign, submit, confirm)"},
		{Key: BucketWaiting, Emoji: "⏳", Label: "Waiting", Hint: "the owner wrote last and is waiting for someone else"},
		{Key: BucketMeetings, Emoji: "📅", Label: "Meetings", Hint: "invitations, reschedules and meeting logistics"},
		{Key: BucketFYI, Emoji: "📄", Label: "FYI", Hint: "worth knowing, no action needed"},
		{Key: BucketBulk, Emoji: "📦", Label: "Bulk", CountOnly: true, Hint: "newsletters, notifications, receipts, marketing"},
	},
	TaxonomySales: {
		{Key: BucketSales, Emoji: "💰", Label: "Sales", Hint: "a real commercial opportunity: licensing, booking, sync, commission, partnership, a buyer"},
		{Key: BucketUrgent, Emoji: "🚨", Label: "Urgent", Hint: "time-sensitive and needs attention soon: deadlines, legal, payment or account problems"},
		{Key: BucketNeeds, Emoji: "💬", Label: "NeedsReply", Hint: "a person expects a reply but there is no sale"},
		{Key: BucketIgnore, Emoji: "🙈", Label: "Ignore", CountOnly: true, Hint: "fan mail, thanks, spam, cold pitches and newsletters that need nothing"},
	},
}

var bucketNames = map[string]map[string]string{
	"ru": {
		BucketReply:    "Ждёт моего ответа",
		BucketAction:   "Действие / дедлайн",
		BucketWaiting:  "Жду других",
		BucketMeetings: "Встречи",
		BucketFYI:      "FYI",
		BucketBulk:     "Bulk",
		BucketSales:    "Sales",
		BucketUrgent:   "Срочно",
		BucketNeeds:    "Нужен ответ (не sales)",
		BucketIgnore:   "Ignore",
	},
	"en": {
		BucketReply:    "Needs my reply",
		BucketAction:   "Action / deadline",
		BucketWaiting:  "Waiting on others",
		BucketMeetings: "Meetings",
		BucketFYI:      "FYI",
		BucketBulk:     "Bulk",
		BucketSales:    "Sales",
		BucketUrgent:   "Urgent",
		BucketNeeds:    "Needs reply (non-sales)",
		BucketIgnore:   "Ignore",
	},
}

// Buckets returns the bucket set of a taxonomy in display order.
func (t Taxonomy) Buckets() []BucketDef {
	return taxonomies[t]
}

// Bucket looks a bucket up by key.
func (t Taxonomy) Bucket(key string) (BucketDef, bool) {
	for _, b := range taxonomies[t] {
		if b.Key == key {
			return b, true
		}
	}
	return BucketDef{}, false
}

// QuietBucket is the count-only bucket that collects mail needing nothing.
func (t Taxonomy) QuietBucket() string {
	if t == TaxonomySales {
		return BucketIgnore
	}
	return BucketBulk
}

// Defaults applied to profiles that leave a field empty.
const (
	DefaultTimezone            = "Asia/Jerusalem"
	DefaultLabelPrefix         = "Triage"
	DefaultInitialLookbackDays = 2
	DefaultWaitingWorkdays     = 3
	DefaultMaxThreads          = 150
	DefaultMaxItems            = 50
	DefaultSkill               = "gmail-triage"
	DefaultGogBinary           = "gog"
)

// DefaultSchedule is the digest schedule when a profile sets none.
var DefaultSchedule = []string{"08:30", "13:30", "19:00"}

// Config is the gmail_triage config section.
type Config struct {
	Enabled bool `json:"enabled" mapstructure:"enabled" yaml:"enabled"`
	// GogBinary is the gog CLI used for Gmail access.
	GogBinary string `json:"gog_binary" mapstructure:"gog_binary" yaml:"gog_binary"`
	// Skill names the installed skill whose SKILL.md carries taxonomy,
	// tone and output format for the classifier.
	Skill    string          `json:"skill" mapstructure:"skill" yaml:"skill"`
	Profiles []ProfileConfig `json:"profiles" mapstructure:"profiles" yaml:"profiles"`
}

// ProfileConfig is one mailbox the bot triages.
type ProfileConfig struct {
	Name     string `json:"name" mapstructure:"name" yaml:"name"`
	Mode     string `json:"mode" mapstructure:"mode" yaml:"mode"`
	Taxonomy string `json:"taxonomy" mapstructure:"taxonomy" yaml:"taxonomy"`
	Language string `json:"language" mapstructure:"language" yaml:"language"`
	// Account is the gog account (mailbox address).
	Account string `json:"account" mapstructure:"account" yaml:"account"`
	// Query is the Gmail search that selects mail to triage, e.g. "in:inbox"
	// or "label:some-label".
	Query string `json:"query" mapstructure:"query" yaml:"query"`
	// ChatID is the Telegram chat that receives the digest and owns the buttons.
	ChatID int64 `json:"chat_id" mapstructure:"chat_id" yaml:"chat_id"`
	// Schedule lists local digest times (HH:MM); Days limits them to weekdays
	// (e.g. "sun-thu"). Both are defaults: Telegram overrides win.
	Schedule []string `json:"schedule" mapstructure:"schedule" yaml:"schedule"`
	Days     string   `json:"days" mapstructure:"days" yaml:"days"`
	Timezone string   `json:"timezone" mapstructure:"timezone" yaml:"timezone"`
	// InitialLookbackDays bounds the first run; later runs only see new mail.
	InitialLookbackDays int `json:"initial_lookback_days" mapstructure:"initial_lookback_days" yaml:"initial_lookback_days"`
	// WaitingWorkdays (Sun–Thu) before an unanswered sent thread counts as Waiting.
	WaitingWorkdays int    `json:"waiting_workdays" mapstructure:"waiting_workdays" yaml:"waiting_workdays"`
	LabelPrefix     string `json:"label_prefix" mapstructure:"label_prefix" yaml:"label_prefix"`
	MaxThreads      int    `json:"max_threads" mapstructure:"max_threads" yaml:"max_threads"`
	MaxItems        int    `json:"max_items" mapstructure:"max_items" yaml:"max_items"`
}

// Profile is a validated ProfileConfig with defaults applied.
type Profile struct {
	Name                string
	Mode                Mode
	Taxonomy            Taxonomy
	Language            string
	Account             string
	Query               string
	ChatID              int64
	Schedule            Schedule
	InitialLookbackDays int
	WaitingWorkdays     int
	LabelPrefix         string
	MaxThreads          int
	MaxItems            int
}

// ReadOnly reports whether the profile may never change the mailbox.
func (p Profile) ReadOnly() bool { return p.Mode != ModeAssistant }

// BucketName is the user-facing bucket title in the profile language.
func (p Profile) BucketName(key string) string {
	if name, ok := bucketNames[p.Language][key]; ok {
		return name
	}
	return key
}

// GmailLabel is the label an assistant profile applies for a bucket.
func (p Profile) GmailLabel(key string) string {
	def, ok := p.Taxonomy.Bucket(key)
	if !ok {
		return ""
	}
	return p.LabelPrefix + "/" + def.Label
}

// Resolve validates the config and returns profiles with defaults applied.
func (c Config) Resolve() ([]Profile, error) {
	if !c.Enabled {
		return nil, nil
	}
	if len(c.Profiles) == 0 {
		return nil, errors.New("gmail_triage: enabled without profiles")
	}
	names := map[string]bool{}
	chats := map[int64]bool{}
	out := make([]Profile, 0, len(c.Profiles))
	for i, pc := range c.Profiles {
		p, err := pc.profile()
		if err != nil {
			return nil, fmt.Errorf("gmail_triage.profiles[%d]: %w", i, err)
		}
		if names[p.Name] {
			return nil, fmt.Errorf("gmail_triage.profiles[%d]: duplicate name %q", i, p.Name)
		}
		if chats[p.ChatID] {
			return nil, fmt.Errorf("gmail_triage.profiles[%d]: chat_id is used by another profile", i)
		}
		names[p.Name] = true
		chats[p.ChatID] = true
		out = append(out, p)
	}
	return out, nil
}

func (pc ProfileConfig) profile() (Profile, error) {
	p := Profile{
		Name:                strings.TrimSpace(pc.Name),
		Mode:                Mode(strings.TrimSpace(pc.Mode)),
		Taxonomy:            Taxonomy(strings.TrimSpace(pc.Taxonomy)),
		Language:            strings.TrimSpace(pc.Language),
		Account:             strings.TrimSpace(pc.Account),
		Query:               strings.TrimSpace(pc.Query),
		ChatID:              pc.ChatID,
		InitialLookbackDays: pc.InitialLookbackDays,
		WaitingWorkdays:     pc.WaitingWorkdays,
		LabelPrefix:         strings.Trim(strings.TrimSpace(pc.LabelPrefix), "/"),
		MaxThreads:          pc.MaxThreads,
		MaxItems:            pc.MaxItems,
	}
	if p.Name == "" {
		return p, errors.New("name is required")
	}
	switch p.Mode {
	case "":
		p.Mode = ModeReadOnly
	case ModeReadOnly, ModeAssistant:
	default:
		return p, fmt.Errorf("unknown mode %q (read_only|assistant)", p.Mode)
	}
	switch p.Taxonomy {
	case "":
		p.Taxonomy = TaxonomyOwner
	case TaxonomyOwner, TaxonomySales:
	default:
		return p, fmt.Errorf("unknown taxonomy %q (owner|sales)", p.Taxonomy)
	}
	switch p.Language {
	case "":
		p.Language = "en"
	case "en", "ru":
	default:
		return p, fmt.Errorf("unsupported language %q (en|ru)", p.Language)
	}
	if p.Account == "" {
		return p, errors.New("account is required")
	}
	if strings.HasPrefix(p.Account, "-") {
		return p, errors.New("account must not start with '-'")
	}
	if p.Query == "" {
		p.Query = "in:inbox"
	}
	if p.ChatID == 0 {
		return p, errors.New("chat_id is required")
	}
	if p.InitialLookbackDays <= 0 {
		p.InitialLookbackDays = DefaultInitialLookbackDays
	}
	if p.WaitingWorkdays <= 0 {
		p.WaitingWorkdays = DefaultWaitingWorkdays
	}
	if p.LabelPrefix == "" {
		p.LabelPrefix = DefaultLabelPrefix
	}
	if p.MaxThreads <= 0 {
		p.MaxThreads = DefaultMaxThreads
	}
	if p.MaxItems <= 0 {
		p.MaxItems = DefaultMaxItems
	}
	times := pc.Schedule
	if len(times) == 0 {
		times = DefaultSchedule
	}
	tz := strings.TrimSpace(pc.Timezone)
	if tz == "" {
		tz = DefaultTimezone
	}
	schedule, err := ParseSchedule(strings.Join(times, ","), pc.Days, tz)
	if err != nil {
		return p, err
	}
	p.Schedule = schedule
	return p, nil
}

// location loads the profile timezone, falling back to UTC.
func (p Profile) location() *time.Location {
	if loc, err := time.LoadLocation(p.Schedule.Timezone); err == nil {
		return loc
	}
	return time.UTC
}
