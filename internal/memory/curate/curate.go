// Package curate turns raw daily memory notes into auditable durable-memory
// promotion drafts. Drafts never modify MEMORY.md on their own; they are
// inspected, audited, and only applied with explicit admin approval.
package curate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Section identifies the bucket a candidate fact belongs to.
type Section string

const (
	SectionPreference   Section = "preferences"
	SectionDecision     Section = "decisions"
	SectionInfra        Section = "infra"
	SectionTodo         Section = "todos"
	SectionStale        Section = "stale"
	SectionMisc         Section = "misc"
	SectionUncategoried Section = "uncategorized"
)

// SectionTitle returns a human-readable title for the section.
func SectionTitle(s Section) string {
	switch s {
	case SectionPreference:
		return "Durable user preferences"
	case SectionDecision:
		return "Project decisions"
	case SectionInfra:
		return "Infrastructure facts"
	case SectionTodo:
		return "Todos / follow-ups"
	case SectionStale:
		return "Stale or conflicting facts"
	case SectionMisc:
		return "Miscellaneous candidates"
	default:
		return "Uncategorized"
	}
}

// AllSections lists sections in the canonical render order. SectionStale and
// SectionTodo always render even if empty so reviewers see a complete picture.
func AllSections() []Section {
	return []Section{
		SectionPreference,
		SectionDecision,
		SectionInfra,
		SectionTodo,
		SectionStale,
		SectionMisc,
	}
}

// Source records where a candidate originated within the daily notes.
type Source struct {
	Date string // YYYY-MM-DD of the daily note file
	Path string // relative path under soul, e.g. memory/2026-04-15.md
	Line int    // line number in the note (1-based)
}

// String renders a source reference suitable for the draft markdown.
func (s Source) String() string {
	if s.Line > 0 {
		return fmt.Sprintf("%s:L%d", s.Path, s.Line)
	}
	return s.Path
}

// Candidate is a single fact extracted from daily notes that may be promoted.
type Candidate struct {
	ID         string  // stable id within a draft
	Section    Section // categorized bucket
	Text       string  // the extracted fact
	Sources    []Source
	Conflicts  []string // ids of other candidates that disagree
	Confidence float64  // 0.0..1.0; lower means more uncertain
}

// Status is the lifecycle state of a draft.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusApplied  Status = "applied"
)

// Draft is a single curation pass over a date range.
type Draft struct {
	ID          string
	CreatedAt   time.Time
	Since       string // YYYY-MM-DD
	Until       string // YYYY-MM-DD inclusive
	SourceCount int    // number of daily notes scanned
	Candidates  []Candidate
	Status      Status
	Notes       string // free-form notes about why a status was set
}

// IsEmpty reports whether the draft has no candidates worth promoting.
func (d *Draft) IsEmpty() bool {
	if d == nil {
		return true
	}
	for _, c := range d.Candidates {
		if strings.TrimSpace(c.Text) != "" {
			return false
		}
	}
	return true
}

// CandidatesBySection groups candidates by section in canonical render order.
func (d *Draft) CandidatesBySection() map[Section][]Candidate {
	out := make(map[Section][]Candidate)
	if d == nil {
		return out
	}
	for _, c := range d.Candidates {
		out[c.Section] = append(out[c.Section], c)
	}
	for s := range out {
		sort.SliceStable(out[s], func(i, j int) bool {
			return out[s][i].ID < out[s][j].ID
		})
	}
	return out
}

// HasConflicts reports whether any candidate is marked as conflicting.
func (d *Draft) HasConflicts() bool {
	if d == nil {
		return false
	}
	for _, c := range d.Candidates {
		if len(c.Conflicts) > 0 || c.Section == SectionStale {
			return true
		}
	}
	return false
}

// classifyLine inspects a candidate text and returns the best-fit section plus
// a confidence score. Heuristic-only — no AI is required.
func classifyLine(text string) (Section, float64) {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return SectionUncategoried, 0
	}

	// Highest-priority explicit prefixes.
	switch {
	case strings.HasPrefix(lower, "preference:") || strings.HasPrefix(lower, "pref:"):
		return SectionPreference, 0.9
	case strings.HasPrefix(lower, "decision:") || strings.HasPrefix(lower, "decided:"):
		return SectionDecision, 0.9
	case strings.HasPrefix(lower, "infra:") || strings.HasPrefix(lower, "infrastructure:"):
		return SectionInfra, 0.9
	case strings.HasPrefix(lower, "todo:") || strings.HasPrefix(lower, "todo "):
		return SectionTodo, 0.9
	case strings.HasPrefix(lower, "follow-up:") || strings.HasPrefix(lower, "followup:"):
		return SectionTodo, 0.85
	}

	// Keyword-based fallback (lower confidence).
	switch {
	case containsAny(lower, "i prefer ", "i like ", "i don't like ", "i don't want ", "preferred "):
		return SectionPreference, 0.65
	case containsAny(lower, "decided ", "we agreed", "we will use", "we'll use", "going to use", "switching to "):
		return SectionDecision, 0.6
	case containsAny(lower, "deploy", "kubernetes", "k8s", "docker ", "postgres", "redis", "host:", "endpoint", "port "):
		return SectionInfra, 0.55
	case containsAny(lower, "todo ", "todo:", "fixme", "follow up", "follow-up", "remember to "):
		return SectionTodo, 0.6
	}

	return SectionMisc, 0.3
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// candidateKeyRegexp captures the leading "<key>:" portion of a candidate so we
// can detect when the same key appears with different values in different
// notes.
var candidateKeyRegexp = regexp.MustCompile(`^\s*([\w][\w \-]{0,40}):\s*(.+)$`)

// candidateIsRegexp captures "X is Y" / "X = Y" / "X are Y" patterns where the
// subject acts as the conflict key.
var candidateIsRegexp = regexp.MustCompile(`(?i)^\s*([\w][\w \-]{1,60}?)\s+(?:is|are|=)\s+(.+?)\s*$`)

// classificationPrefixes are recognized leading tags that classify a note;
// they are not the conflict key themselves. Stripped before key extraction.
var classificationPrefixes = []string{
	"preference",
	"pref",
	"decision",
	"decided",
	"infra",
	"infrastructure",
	"todo",
	"follow-up",
	"followup",
}

// stripClassificationPrefix removes a leading "preference: ", "decision: ",
// etc. so conflict detection can compare the underlying fact.
func stripClassificationPrefix(text string) string {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	for _, p := range classificationPrefixes {
		if strings.HasPrefix(lower, p+":") {
			return strings.TrimSpace(trimmed[len(p)+1:])
		}
	}
	return trimmed
}

// candidateKey extracts a normalized "key" from a candidate when the text uses
// a "key: value" or "key is value" shape. Returns empty when no key is
// recognizable. Classification prefixes are stripped first so two notes
// labeled "decision: X is Y" / "decision: X is Z" share the same key X.
func candidateKey(text string) (key, value string) {
	stripped := stripClassificationPrefix(text)

	if m := candidateKeyRegexp.FindStringSubmatch(stripped); len(m) == 3 {
		rawKey := strings.ToLower(strings.TrimSpace(m[1]))
		rawVal := strings.TrimSpace(m[2])
		// Skip if the "key" turned out to be a leftover classification tag.
		for _, p := range classificationPrefixes {
			if rawKey == p {
				return "", ""
			}
		}
		return rawKey, rawVal
	}
	if m := candidateIsRegexp.FindStringSubmatch(stripped); len(m) == 3 {
		return strings.ToLower(strings.TrimSpace(m[1])), strings.TrimSpace(m[2])
	}
	return "", ""
}

// detectConflicts marks candidates that share a key but disagree on the value.
// Updates the candidates in place.
func detectConflicts(cands []Candidate) {
	type entry struct {
		idx   int
		value string
	}
	groups := map[string][]entry{}
	for i, c := range cands {
		k, v := candidateKey(c.Text)
		if k == "" || v == "" {
			continue
		}
		groups[k] = append(groups[k], entry{idx: i, value: strings.ToLower(v)})
	}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		// All-same value → not a conflict.
		first := group[0].value
		allSame := true
		for _, e := range group[1:] {
			if e.value != first {
				allSame = false
				break
			}
		}
		if allSame {
			continue
		}
		ids := make([]string, 0, len(group))
		for _, e := range group {
			ids = append(ids, cands[e.idx].ID)
		}
		for _, e := range group {
			peers := make([]string, 0, len(ids)-1)
			for _, id := range ids {
				if id == cands[e.idx].ID {
					continue
				}
				peers = append(peers, id)
			}
			cands[e.idx].Conflicts = append(cands[e.idx].Conflicts, peers...)
			cands[e.idx].Section = SectionStale
		}
	}
}
