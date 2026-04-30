package memory

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ok-gobot/internal/redact"
)

// RecallScopeKind identifies the privacy boundary attached to a memory source.
type RecallScopeKind string

const (
	ScopeUnknown      RecallScopeKind = "unknown"
	ScopeUser         RecallScopeKind = "user"
	ScopeChat         RecallScopeKind = "chat"
	ScopeSession      RecallScopeKind = "session"
	ScopeRole         RecallScopeKind = "role"
	ScopeJob          RecallScopeKind = "job"
	ScopeExtraPath    RecallScopeKind = "extra_path"
	ScopeLegacyGlobal RecallScopeKind = "legacy_global"
)

var legacyDailySourceRegexp = regexp.MustCompile(`^memory/\d{4}-\d{2}-\d{2}\.md$`)

// RecallScope is the parsed scope of one memory source label.
type RecallScope struct {
	Kind  RecallScopeKind
	ID    string
	Label string
}

// RecallContext describes the current Telegram/job scope for active recall.
type RecallContext struct {
	UserID             int64
	ChatID             int64
	SessionKey         string
	ChatType           string
	RoleName           string
	JobID              string
	AllowGlobalPrivate bool
	AllowGroupChat     bool
	ExtraPathLabels    []string
}

// RecallDecision records why a memory source was allowed or denied.
type RecallDecision struct {
	Source  string
	Scope   RecallScope
	Allowed bool
	Reason  string
}

// RecallPolicy enforces memory source boundaries for one active run.
type RecallPolicy struct {
	Context RecallContext
}

// NewRecallPolicy returns a normalized recall policy for the provided context.
func NewRecallPolicy(ctx RecallContext) *RecallPolicy {
	ctx.ChatType = strings.ToLower(strings.TrimSpace(ctx.ChatType))
	ctx.SessionKey = strings.TrimSpace(ctx.SessionKey)
	ctx.RoleName = strings.TrimSpace(ctx.RoleName)
	ctx.JobID = strings.TrimSpace(ctx.JobID)
	ctx.ExtraPathLabels = compactLabels(ctx.ExtraPathLabels)
	return &RecallPolicy{Context: ctx}
}

// Decide evaluates whether a source label can be recalled in this context.
func (p *RecallPolicy) Decide(source string) RecallDecision {
	clean := cleanSourceLabel(source)
	scope := ClassifySource(clean)
	decision := RecallDecision{Source: clean, Scope: scope}

	if p == nil {
		decision.Allowed = true
		decision.Reason = "no recall policy configured"
		return decision
	}

	switch scope.Kind {
	case ScopeUser:
		want := idString(p.Context.UserID)
		decision.Allowed = want != "" && scope.ID == want
		decision.Reason = allowReason(decision.Allowed, "matches current Telegram user", "different Telegram user")
	case ScopeChat:
		want := idString(p.Context.ChatID)
		matches := want != "" && scope.ID == want
		if !matches {
			decision.Allowed = false
			decision.Reason = "different Telegram chat"
		} else if p.isGroupLike() && !p.Context.AllowGroupChat {
			decision.Allowed = false
			decision.Reason = "group chat memory recall is not enabled for this chat"
		} else {
			decision.Allowed = true
			decision.Reason = "matches current Telegram chat"
		}
	case ScopeSession:
		want := SafeScopeID(p.Context.SessionKey)
		decision.Allowed = want != "" && scope.ID == want
		decision.Reason = allowReason(decision.Allowed, "matches current session", "different session")
	case ScopeRole:
		want := SafeScopeID(p.Context.RoleName)
		decision.Allowed = want != "" && scope.ID == want
		decision.Reason = allowReason(decision.Allowed, "matches current role", "different role")
	case ScopeJob:
		want := SafeScopeID(p.Context.JobID)
		decision.Allowed = want != "" && scope.ID == want
		decision.Reason = allowReason(decision.Allowed, "matches current job", "different job")
	case ScopeExtraPath:
		decision.Allowed = containsLabel(p.Context.ExtraPathLabels, scope.Label)
		decision.Reason = allowReason(decision.Allowed, "configured extra memory path label", "external memory path is not allowed by policy")
	case ScopeLegacyGlobal:
		decision.Allowed = p.Context.AllowGlobalPrivate && !p.isGroupLike()
		decision.Reason = allowReason(decision.Allowed, "legacy private memory explicitly enabled", "legacy global memory is not allowed in this scope")
	default:
		decision.Allowed = false
		decision.Reason = "unrecognized memory source scope"
	}

	return decision
}

// AllowSource reports whether a source label can be recalled.
func (p *RecallPolicy) AllowSource(source string) bool {
	return p.Decide(source).Allowed
}

// AllowResult reports whether a search result can be recalled.
func (p *RecallPolicy) AllowResult(result MemoryResult) bool {
	return p.AllowSource(result.SourceFile)
}

// FilterResults drops denied results and returns one decision per inspected source.
func (p *RecallPolicy) FilterResults(results []MemoryResult) ([]MemoryResult, []RecallDecision) {
	if p == nil {
		return results, nil
	}
	filtered := make([]MemoryResult, 0, len(results))
	decisions := make([]RecallDecision, 0, len(results))
	for _, result := range results {
		decision := p.Decide(result.SourceFile)
		decisions = append(decisions, decision)
		if decision.Allowed {
			filtered = append(filtered, result)
		}
	}
	return filtered, decisions
}

// Summary returns a compact operator-readable description of the current policy.
func (p *RecallPolicy) Summary() string {
	if p == nil {
		return "memory recall policy: unrestricted"
	}

	parts := []string{"memory recall policy: scoped"}
	if p.Context.UserID != 0 {
		parts = append(parts, fmt.Sprintf("user:%d", p.Context.UserID))
	}
	if p.Context.ChatID != 0 {
		parts = append(parts, fmt.Sprintf("chat:%d", p.Context.ChatID))
	}
	if p.Context.SessionKey != "" {
		parts = append(parts, "session:"+SafeScopeID(p.Context.SessionKey))
	}
	if p.Context.RoleName != "" {
		parts = append(parts, "role:"+SafeScopeID(p.Context.RoleName))
	}
	if p.Context.JobID != "" {
		parts = append(parts, "job:"+SafeScopeID(p.Context.JobID))
	}
	if p.isGroupLike() {
		parts = append(parts, fmt.Sprintf("group_recall:%t", p.Context.AllowGroupChat))
	}
	parts = append(parts, fmt.Sprintf("legacy_global:%t", p.Context.AllowGlobalPrivate && !p.isGroupLike()))
	if len(p.Context.ExtraPathLabels) > 0 {
		labels := append([]string(nil), p.Context.ExtraPathLabels...)
		sort.Strings(labels)
		parts = append(parts, "extra_paths:"+strings.Join(labels, ","))
	} else {
		parts = append(parts, "extra_paths:deny")
	}
	return strings.Join(parts, " ")
}

// ClassifySource parses a memory source label into a recall scope.
func ClassifySource(source string) RecallScope {
	clean := cleanSourceLabel(source)
	if clean == "" {
		return RecallScope{Kind: ScopeUnknown}
	}
	if clean == "MEMORY.md" || legacyDailySourceRegexp.MatchString(clean) {
		return RecallScope{Kind: ScopeLegacyGlobal, ID: "private", Label: "legacy"}
	}

	if collection, _, ok := ParseExtraSourceLabel(clean); ok {
		label := SafeScopeID(collection)
		return RecallScope{Kind: ScopeExtraPath, ID: label, Label: label}
	}

	if strings.HasPrefix(clean, "external://") {
		rest := strings.TrimPrefix(clean, "external://")
		label := firstPathSegment(rest)
		return RecallScope{Kind: ScopeExtraPath, ID: label, Label: label}
	}

	parts := strings.Split(clean, "/")
	if len(parts) >= 3 && parts[0] == "memory" {
		if scope := classifyScopedParts(parts[1], parts[2]); scope.Kind != ScopeUnknown {
			return scope
		}
	}
	if len(parts) >= 2 {
		if scope := classifyScopedParts(parts[0], parts[1]); scope.Kind != ScopeUnknown {
			return scope
		}
	}

	if filepath.IsAbs(clean) || strings.Contains(clean, ":") || strings.Contains(clean, "/") {
		label := firstPathSegment(clean)
		if filepath.IsAbs(clean) {
			label = "absolute"
		}
		return RecallScope{Kind: ScopeExtraPath, ID: label, Label: label}
	}

	return RecallScope{Kind: ScopeUnknown, Label: clean}
}

func classifyScopedParts(kind, id string) RecallScope {
	id = SafeScopeID(id)
	switch kind {
	case "users", "user":
		return RecallScope{Kind: ScopeUser, ID: id, Label: id}
	case "chats", "chat", "groups", "group":
		return RecallScope{Kind: ScopeChat, ID: id, Label: id}
	case "sessions", "session":
		return RecallScope{Kind: ScopeSession, ID: id, Label: id}
	case "roles", "role":
		return RecallScope{Kind: ScopeRole, ID: id, Label: id}
	case "jobs", "job":
		return RecallScope{Kind: ScopeJob, ID: id, Label: id}
	case "external", "extra", "extra_paths":
		return RecallScope{Kind: ScopeExtraPath, ID: id, Label: id}
	default:
		return RecallScope{Kind: ScopeUnknown}
	}
}

// SanitizeSnippet redacts sensitive values and strips unsafe control characters.
func SanitizeSnippet(input string) string {
	redacted := redact.Redact(input)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, redacted)
}

// SafeScopeID turns a scope identifier into a stable path-safe label.
func SafeScopeID(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range input {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func cleanSourceLabel(source string) string {
	clean := strings.TrimSpace(source)
	clean = filepath.ToSlash(clean)
	clean = strings.TrimPrefix(clean, "./")
	return clean
}

func (p *RecallPolicy) isGroupLike() bool {
	if p.Context.ChatType == "" && p.Context.ChatID < 0 {
		return true
	}
	switch p.Context.ChatType {
	case "group", "supergroup", "channel":
		return true
	default:
		return false
	}
}

func idString(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func allowReason(allowed bool, yes, no string) string {
	if allowed {
		return yes
	}
	return no
}

func firstPathSegment(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "/"); idx >= 0 {
		path = path[:idx]
	}
	return SafeScopeID(path)
}

func compactLabels(labels []string) []string {
	seen := make(map[string]struct{}, len(labels))
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = SafeScopeID(label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	return out
}

func containsLabel(labels []string, want string) bool {
	want = SafeScopeID(want)
	if want == "" {
		return false
	}
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}
