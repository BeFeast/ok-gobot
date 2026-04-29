package memory

import (
	"strings"
)

// SourceType identifies which retrieval bucket a memory chunk belongs to.
// Source types let the searcher filter results to a specific subset
// (workspace, daily, sessions, extra) without changing the underlying
// chunk storage.
type SourceType string

const (
	// SourceWorkspace is the durable curated MEMORY.md file at the soul root.
	SourceWorkspace SourceType = "workspace"
	// SourceDaily covers daily notes under memory/<date>.md.
	SourceDaily SourceType = "daily"
	// SourceSession covers indexed transcript chunks from past sessions.
	SourceSession SourceType = "session"
	// SourceExtra covers configured extra paths outside the canonical layout.
	SourceExtra SourceType = "extra"
)

// SessionSourceFilePrefix is the source_file prefix used for indexed session
// transcript chunks. The remainder of the source_file is the canonical
// session key as produced by the session package.
const SessionSourceFilePrefix = "session://"

// SessionSourceFile returns the canonical source_file value for a transcript
// chunk belonging to sessionKey.
func SessionSourceFile(sessionKey string) string {
	return SessionSourceFilePrefix + sessionKey
}

// SessionKeyFromSourceFile extracts the session key embedded in a session
// transcript source_file. Returns ("", false) when sourceFile does not
// represent a session.
func SessionKeyFromSourceFile(sourceFile string) (string, bool) {
	if !strings.HasPrefix(sourceFile, SessionSourceFilePrefix) {
		return "", false
	}
	return strings.TrimPrefix(sourceFile, SessionSourceFilePrefix), true
}

// DeriveSourceType returns the canonical SourceType for a stored chunk's
// source_file value. The derivation is intentionally string-based so that
// the underlying memory_chunks schema does not need a separate column for
// source classification.
func DeriveSourceType(sourceFile string) SourceType {
	sourceFile = strings.TrimSpace(sourceFile)
	if sourceFile == "" {
		return SourceExtra
	}
	if strings.HasPrefix(sourceFile, SessionSourceFilePrefix) {
		return SourceSession
	}
	if sourceFile == "MEMORY.md" {
		return SourceWorkspace
	}
	if strings.HasPrefix(sourceFile, "memory/") && strings.Count(sourceFile, "/") == 1 {
		return SourceDaily
	}
	return SourceExtra
}

// NormalizeSourceTypes returns a deduplicated, lower-cased list of source
// types. Unknown values are dropped so callers can pass user input directly.
// An empty input returns nil, which the searcher treats as "all sources".
func NormalizeSourceTypes(values []string) []SourceType {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[SourceType]struct{}, len(values))
	out := make([]SourceType, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(strings.ToLower(raw))
		if v == "" {
			continue
		}
		var st SourceType
		switch v {
		case string(SourceWorkspace), "memory":
			st = SourceWorkspace
		case string(SourceDaily), "notes":
			st = SourceDaily
		case string(SourceSession), "sessions", "transcript", "transcripts":
			st = SourceSession
		case string(SourceExtra), "extras", "external":
			st = SourceExtra
		default:
			continue
		}
		if _, ok := seen[st]; ok {
			continue
		}
		seen[st] = struct{}{}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AllSourceTypes returns the full list of supported source types. The order
// is stable so callers can rely on it for help text and CLI flags.
func AllSourceTypes() []SourceType {
	return []SourceType{SourceWorkspace, SourceDaily, SourceSession, SourceExtra}
}
