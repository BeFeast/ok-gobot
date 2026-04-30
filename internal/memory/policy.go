package memory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RecallScope names the policy boundary attached to a memory source label.
type RecallScope string

const (
	ScopeUser         RecallScope = "user"
	ScopeChat         RecallScope = "chat"
	ScopeSession      RecallScope = "session"
	ScopeRole         RecallScope = "role"
	ScopeJob          RecallScope = "job"
	ScopeExternalPath RecallScope = "external_path"
	ScopeLegacyGlobal RecallScope = "legacy_global"
	ScopeUnknown      RecallScope = "unknown"
)

// ExtraPathPolicy describes an operator-configured external memory source.
// The source label is matched against external/<label>/... and extra/<label>/...
// index source labels. External paths are denied unless explicitly listed here.
type ExtraPathPolicy struct {
	Label        string
	Path         string
	AllowPrivate bool
	AllowGroups  bool
}

// RecallContext describes the chat/session currently requesting memory recall.
type RecallContext struct {
	UserID     int64
	ChatID     int64
	ChatType   string
	SessionKey string
	Role       string
	JobID      string

	AllowGroupRecall     bool
	IncludeLegacyPrivate bool
	ExtraPaths           []ExtraPathPolicy
}

// RecallPolicy makes source-level allow/deny decisions for active memory recall.
type RecallPolicy struct {
	ctx    RecallContext
	extra  map[string]ExtraPathPolicy
	labels []string
}

// RecallDecision records how a source label was classified and whether it was allowed.
type RecallDecision struct {
	Source  string      `json:"source"`
	Scope   RecallScope `json:"scope"`
	Label   string      `json:"label"`
	Allowed bool        `json:"allowed"`
	Reason  string      `json:"reason"`
}

// NewRecallPolicy returns a conservative policy for the current chat context.
func NewRecallPolicy(ctx RecallContext) *RecallPolicy {
	p := &RecallPolicy{ctx: ctx, extra: map[string]ExtraPathPolicy{}}
	for _, extra := range ctx.ExtraPaths {
		label := sanitizeScopeID(extra.Label)
		if label == "" {
			continue
		}
		extra.Label = label
		p.extra[label] = extra
		p.labels = append(p.labels, label)
	}
	sort.Strings(p.labels)
	return p
}

// Context returns the policy input used to make decisions.
func (p *RecallPolicy) Context() RecallContext {
	if p == nil {
		return RecallContext{}
	}
	return p.ctx
}

// DecisionForSource classifies source and returns the current policy decision.
func (p *RecallPolicy) DecisionForSource(source string) RecallDecision {
	source = normalizeSourceLabel(source)
	if p == nil {
		return RecallDecision{Source: source, Scope: ScopeUnknown, Allowed: true, Reason: "no recall policy configured"}
	}
	if source == "" {
		return RecallDecision{Source: source, Scope: ScopeUnknown, Allowed: false, Reason: "empty source label"}
	}

	scope, label := classifySource(source)
	decision := RecallDecision{Source: source, Scope: scope, Label: label}

	private := p.isPrivateChat()
	group := p.isGroupChat()
	userLabel := strconv.FormatInt(p.ctx.UserID, 10)
	chatLabel := strconv.FormatInt(p.ctx.ChatID, 10)
	sessionLabel := sanitizeScopeID(p.ctx.SessionKey)
	roleLabel := sanitizeScopeID(p.ctx.Role)
	jobLabel := sanitizeScopeID(p.ctx.JobID)

	switch scope {
	case ScopeUser:
		if !private {
			decision.Reason = "user-scoped memory is private-chat only"
			return decision
		}
		if label == "" || label != userLabel {
			decision.Reason = "user scope does not match current Telegram user"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched current Telegram user scope"
	case ScopeChat:
		if label == "" || label != chatLabel {
			decision.Reason = "chat scope does not match current Telegram chat"
			return decision
		}
		if group && !p.ctx.AllowGroupRecall {
			decision.Reason = "group chat recall is disabled by policy"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched current Telegram chat scope"
	case ScopeSession:
		if label == "" || label != sessionLabel {
			decision.Reason = "session scope does not match current session"
			return decision
		}
		if group && !p.ctx.AllowGroupRecall {
			decision.Reason = "group session recall is disabled by policy"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched current session scope"
	case ScopeRole:
		if label == "" || label != roleLabel {
			decision.Reason = "role scope does not match active role"
			return decision
		}
		if group && !p.ctx.AllowGroupRecall {
			decision.Reason = "group role recall is disabled by policy"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched active role scope"
	case ScopeJob:
		if label == "" || label != jobLabel {
			decision.Reason = "job scope does not match current job"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched current job scope"
	case ScopeExternalPath:
		extra, ok := p.extra[label]
		if !ok {
			decision.Reason = "external path label is not configured"
			return decision
		}
		if group && !extra.AllowGroups {
			decision.Reason = "external path is not allowed for group chats"
			return decision
		}
		if private && !extra.AllowPrivate {
			decision.Reason = "external path is not allowed for private chats"
			return decision
		}
		if !private && !group && !extra.AllowPrivate {
			decision.Reason = "external path is not allowed for this chat type"
			return decision
		}
		decision.Allowed = true
		decision.Reason = "matched configured external path label"
	case ScopeLegacyGlobal:
		if private && p.ctx.IncludeLegacyPrivate {
			decision.Allowed = true
			decision.Reason = "legacy global private memory explicitly enabled"
			return decision
		}
		decision.Reason = "legacy global memory is disabled by scoped recall policy"
	default:
		decision.Reason = "source label does not declare a supported memory scope"
	}

	return decision
}

// AllowsSource reports whether source may be recalled in the current context.
func (p *RecallPolicy) AllowsSource(source string) bool {
	return p.DecisionForSource(source).Allowed
}

// ResolveExternalSource maps external/<label>/... source labels to their configured root path.
func (p *RecallPolicy) ResolveExternalSource(source string) (rootPath, relativePath string, ok bool) {
	if p == nil {
		return "", "", false
	}
	source = normalizeSourceLabel(source)
	if label, rel, parsed := ParseExtraSourceLabel(source); parsed {
		extra, exists := p.extra[sanitizeScopeID(label)]
		if !exists || strings.TrimSpace(extra.Path) == "" {
			return "", "", false
		}
		return strings.TrimSpace(extra.Path), rel, true
	}
	parts := strings.Split(source, "/")
	if len(parts) < 3 || (parts[0] != "external" && parts[0] != "extra") {
		return "", "", false
	}
	label := sanitizeScopeID(parts[1])
	extra, exists := p.extra[label]
	if !exists || strings.TrimSpace(extra.Path) == "" {
		return "", "", false
	}
	return strings.TrimSpace(extra.Path), strings.Join(parts[2:], "/"), true
}

// Describe returns a compact status/debug string for the policy.
func (p *RecallPolicy) Describe() string {
	if p == nil {
		return "memory recall policy: none"
	}

	chatType := strings.TrimSpace(p.ctx.ChatType)
	if chatType == "" {
		chatType = "unknown"
	}

	allowed := []string{}
	if p.isPrivateChat() && p.ctx.UserID != 0 {
		allowed = append(allowed, "user:"+strconv.FormatInt(p.ctx.UserID, 10))
	}
	if p.ctx.ChatID != 0 {
		if !p.isGroupChat() || p.ctx.AllowGroupRecall {
			allowed = append(allowed, "chat:"+strconv.FormatInt(p.ctx.ChatID, 10))
		}
	}
	if p.ctx.SessionKey != "" && (!p.isGroupChat() || p.ctx.AllowGroupRecall) {
		allowed = append(allowed, "session:"+sanitizeScopeID(p.ctx.SessionKey))
	}
	if p.ctx.Role != "" && (!p.isGroupChat() || p.ctx.AllowGroupRecall) {
		allowed = append(allowed, "role:"+sanitizeScopeID(p.ctx.Role))
	}
	if p.ctx.JobID != "" {
		allowed = append(allowed, "job:"+sanitizeScopeID(p.ctx.JobID))
	}
	for _, label := range p.labels {
		extra := p.extra[label]
		if (p.isPrivateChat() && extra.AllowPrivate) || (p.isGroupChat() && extra.AllowGroups) {
			allowed = append(allowed, "external:"+label)
		}
	}
	if p.ctx.IncludeLegacyPrivate && p.isPrivateChat() {
		allowed = append(allowed, "legacy_global")
	}
	if len(allowed) == 0 {
		allowed = append(allowed, "none")
	}

	return fmt.Sprintf("memory recall policy: chat_type=%s user=%d chat=%d group_recall=%t legacy_private=%t allowed=%s",
		chatType,
		p.ctx.UserID,
		p.ctx.ChatID,
		p.ctx.AllowGroupRecall,
		p.ctx.IncludeLegacyPrivate,
		strings.Join(allowed, ","),
	)
}

func (p *RecallPolicy) isPrivateChat() bool {
	if p == nil {
		return false
	}
	chatType := strings.ToLower(strings.TrimSpace(p.ctx.ChatType))
	return chatType == "private" || chatType == "direct" || (chatType == "" && p.ctx.ChatID > 0)
}

func (p *RecallPolicy) isGroupChat() bool {
	if p == nil {
		return false
	}
	chatType := strings.ToLower(strings.TrimSpace(p.ctx.ChatType))
	return chatType == "group" || chatType == "supergroup" || chatType == "channel" || p.ctx.ChatID < 0
}

func classifySource(source string) (RecallScope, string) {
	source = normalizeSourceLabel(source)
	if label, _, ok := ParseExtraSourceLabel(source); ok {
		return ScopeExternalPath, sanitizeScopeID(label)
	}
	parts := strings.Split(source, "/")
	if source == "MEMORY.md" || strings.HasPrefix(source, "memory/") {
		return ScopeLegacyGlobal, ""
	}
	if len(parts) >= 3 {
		switch {
		case parts[0] == "telegram" && parts[1] == "users":
			return ScopeUser, sanitizeScopeID(parts[2])
		case parts[0] == "telegram" && parts[1] == "chats":
			return ScopeChat, sanitizeScopeID(parts[2])
		case parts[0] == "sessions":
			return ScopeSession, sanitizeScopeID(parts[1])
		case parts[0] == "roles":
			return ScopeRole, sanitizeScopeID(parts[1])
		case parts[0] == "jobs":
			return ScopeJob, sanitizeScopeID(parts[1])
		case parts[0] == "external" || parts[0] == "extra":
			return ScopeExternalPath, sanitizeScopeID(parts[1])
		}
	}
	if len(parts) >= 2 {
		switch parts[0] {
		case "user", "users":
			return ScopeUser, sanitizeScopeID(parts[1])
		case "chat", "chats", "dm", "group", "groups":
			return ScopeChat, sanitizeScopeID(parts[1])
		case "session":
			return ScopeSession, sanitizeScopeID(parts[1])
		case "role":
			return ScopeRole, sanitizeScopeID(parts[1])
		case "job":
			return ScopeJob, sanitizeScopeID(parts[1])
		}
	}
	return ScopeUnknown, ""
}

func normalizeSourceLabel(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	source = filepath.ToSlash(filepath.Clean(source))
	source = strings.TrimPrefix(source, "./")
	return source
}

// TelegramDailySource returns the scoped daily memory source label for a Telegram turn.
func TelegramDailySource(chatType string, chatID, userID int64, date time.Time) string {
	stamp := date.Format("2006-01-02")
	if strings.EqualFold(strings.TrimSpace(chatType), "private") {
		if userID == 0 {
			userID = chatID
		}
		return fmt.Sprintf("telegram/users/%d/memory/%s.md", userID, stamp)
	}
	return fmt.Sprintf("telegram/chats/%d/memory/%s.md", chatID, stamp)
}

// RedactMemorySnippet removes common secret shapes before memory text enters prompts or output.
func RedactMemorySnippet(text string) string {
	redacted := text
	for _, rule := range redactionRules {
		redacted = rule.re.ReplaceAllString(redacted, rule.repl)
	}
	return redacted
}

type redactionRule struct {
	re   *regexp.Regexp
	repl string
}

var redactionRules = []redactionRule{
	{regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{25,}\b`), "[REDACTED_TELEGRAM_TOKEN]"},
	{regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,})\b`), "[REDACTED_SECRET]"},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|authorization)\s*[:=]\s*[^\s]+`), "$1=[REDACTED]"},
}

func sanitizeScopeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var out strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if valid {
			out.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			out.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(out.String(), "_")
}
