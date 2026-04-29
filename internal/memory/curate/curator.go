package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Curator scans daily notes under SoulPath/memory and builds curation drafts.
// It does not call any LLM — extraction is heuristic so the v1 promotion loop
// is fully reproducible from the local filesystem.
type Curator struct {
	// SoulPath is the soul root (the directory that contains memory/).
	SoulPath string
	// Now returns the wall-clock time. Tests inject a fixed clock.
	Now func() time.Time
}

// NewCurator returns a Curator rooted at soulPath.
func NewCurator(soulPath string) *Curator {
	return &Curator{
		SoulPath: soulPath,
		Now:      time.Now,
	}
}

// CurateRange reads daily notes between since and until (inclusive) and
// returns a draft with extracted candidate facts. If no useful candidates
// are found the draft is still returned with an empty candidate list so the
// caller can render a "nothing to promote" message.
func (c *Curator) CurateRange(since, until time.Time) (*Draft, error) {
	if c.SoulPath == "" {
		return nil, fmt.Errorf("curator: soul path is empty")
	}
	if since.After(until) {
		return nil, fmt.Errorf("curator: --since (%s) is after --until (%s)", formatDate(since), formatDate(until))
	}

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	current := now()

	memoryDir := filepath.Join(c.SoulPath, "memory")
	dates := datesBetween(since, until)

	draft := &Draft{
		ID:        generateDraftID(current),
		CreatedAt: current,
		Since:     formatDate(since),
		Until:     formatDate(until),
		Status:    StatusPending,
	}

	for _, date := range dates {
		path := filepath.Join(memoryDir, date+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read daily note %s: %w", path, err)
		}
		draft.SourceCount++

		relPath := filepath.ToSlash(filepath.Join("memory", date+".md"))
		extractCandidates(string(data), date, relPath, draft)
	}

	detectConflicts(draft.Candidates)
	return draft, nil
}

// extractCandidates parses a single daily-note body, appending recognized
// candidate facts to the draft.
func extractCandidates(body, date, relPath string, draft *Draft) {
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	for i, raw := range lines {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		// Skip headers — they are structure, not candidates.
		if strings.HasPrefix(text, "#") {
			continue
		}
		// Strip common bullet prefixes.
		stripped := strings.TrimSpace(strings.TrimLeft(text, "-*•+ \t"))
		if stripped == "" || stripped == "---" {
			continue
		}
		// Skip lines that are clearly headers / metadata (timestamps).
		if isTimestampHeader(stripped) {
			continue
		}
		// Drop overly long lines — they are usually prose, not facts.
		if len(stripped) > 300 {
			continue
		}

		section, conf := classifyLine(stripped)
		// Drop low-signal lines that didn't trigger any keyword.
		if section == SectionUncategoried || (section == SectionMisc && conf < 0.3) {
			continue
		}

		cand := Candidate{
			ID:         candidateID(date, i+1),
			Section:    section,
			Text:       cleanText(stripped),
			Confidence: conf,
			Sources: []Source{{
				Date: date,
				Path: relPath,
				Line: i + 1,
			}},
		}
		draft.Candidates = append(draft.Candidates, cand)
	}
}

// isTimestampHeader matches "12:34" or "Quick Note (12:34)" style markers that
// the rest of the bot writes via Memory.AppendQuickNoteToToday.
func isTimestampHeader(s string) bool {
	if len(s) >= 5 && s[2] == ':' && isAllDigits(s[:2]) && isAllDigits(s[3:5]) {
		// "12:34" possibly followed by trailing text.
		return true
	}
	if strings.HasPrefix(strings.ToLower(s), "quick note (") {
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cleanText normalizes whitespace and trailing punctuation so candidates render
// consistently in the draft markdown.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func candidateID(date string, line int) string {
	return fmt.Sprintf("%s-%04d", strings.ReplaceAll(date, "-", ""), line)
}

func generateDraftID(now time.Time) string {
	stamp := now.UTC().Format("20060102-150405")
	h := sha256.Sum256([]byte(stamp + fmt.Sprint(now.UnixNano())))
	return fmt.Sprintf("%s-%s", stamp, hex.EncodeToString(h[:3]))
}

func datesBetween(start, end time.Time) []string {
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	var out []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	sort.Strings(out)
	return out
}

func formatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

// ParseDate parses a YYYY-MM-DD string into a UTC time.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("date is empty")
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse date %q: %w", s, err)
	}
	return t, nil
}
