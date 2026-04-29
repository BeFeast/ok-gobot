package curate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// draftIDRegexp matches the exact format produced by generateDraftID:
// "YYYYMMDD-HHMMSS-<6 hex chars>". Unsanitized IDs would otherwise be joined
// straight into filesystem paths, so any caller-supplied value must be
// validated before being used to build a draft path.
var draftIDRegexp = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{6}$`)

// ValidateDraftID returns nil iff id matches the canonical draft ID format.
// Exposed so handlers can fail fast (and uniformly) before calling store ops
// that would otherwise turn a bad id into a path traversal.
func ValidateDraftID(id string) error {
	if id == "" {
		return fmt.Errorf("draft store: id is empty")
	}
	if !draftIDRegexp.MatchString(id) {
		return fmt.Errorf("draft store: invalid draft id %q", id)
	}
	return nil
}

// DraftStore persists drafts to disk under SoulPath/memory/drafts/.
// Each draft lives in two side-by-side files:
//
//   - <id>.md   — human-readable rendering for inspection
//   - <id>.json — structured form used by the CLI/bot to load and apply
//
// Rejected drafts are kept on disk by default so admins can re-read them; they
// must be deleted explicitly via DeleteDraft.
type DraftStore struct {
	// SoulPath is the soul root (the directory that contains memory/).
	SoulPath string
}

// NewDraftStore returns a DraftStore rooted at soulPath.
func NewDraftStore(soulPath string) *DraftStore {
	return &DraftStore{SoulPath: soulPath}
}

func (s *DraftStore) draftsDir() string {
	return filepath.Join(s.SoulPath, "memory", "drafts")
}

func (s *DraftStore) jsonPath(id string) string {
	return filepath.Join(s.draftsDir(), id+".json")
}

func (s *DraftStore) markdownPath(id string) string {
	return filepath.Join(s.draftsDir(), id+".md")
}

// Save writes a draft to disk in both JSON and markdown form. Existing files
// are overwritten so callers can update status by re-saving.
func (s *DraftStore) Save(d *Draft) error {
	if s.SoulPath == "" {
		return fmt.Errorf("draft store: soul path is empty")
	}
	if d == nil {
		return fmt.Errorf("draft store: draft missing id")
	}
	if err := ValidateDraftID(d.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.draftsDir(), 0o755); err != nil {
		return fmt.Errorf("create drafts dir: %w", err)
	}
	jsonBytes, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal draft: %w", err)
	}
	if err := os.WriteFile(s.jsonPath(d.ID), jsonBytes, 0o644); err != nil {
		return fmt.Errorf("write draft json: %w", err)
	}
	rendered := RenderDraftMarkdown(d, AuditDraft(d))
	if err := os.WriteFile(s.markdownPath(d.ID), []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write draft markdown: %w", err)
	}
	return nil
}

// Load returns the draft with the given id.
func (s *DraftStore) Load(id string) (*Draft, error) {
	if err := ValidateDraftID(id); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.jsonPath(id))
	if err != nil {
		return nil, fmt.Errorf("read draft %s: %w", id, err)
	}
	var d Draft
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse draft %s: %w", id, err)
	}
	return &d, nil
}

// DraftSummary is a lightweight description used by CLI/bot listings.
type DraftSummary struct {
	ID         string
	Status     Status
	Since      string
	Until      string
	Candidates int
	Conflicts  int
	CreatedAt  time.Time
}

// List returns drafts on disk sorted newest-first.
func (s *DraftStore) List() ([]DraftSummary, error) {
	dir := s.draftsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read drafts dir: %w", err)
	}

	var summaries []DraftSummary
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		d, err := s.Load(id)
		if err != nil {
			continue
		}
		conflicts := 0
		for _, c := range d.Candidates {
			if len(c.Conflicts) > 0 {
				conflicts++
			}
		}
		summaries = append(summaries, DraftSummary{
			ID:         d.ID,
			Status:     d.Status,
			Since:      d.Since,
			Until:      d.Until,
			Candidates: len(d.Candidates),
			Conflicts:  conflicts,
			CreatedAt:  d.CreatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return summaries, nil
}

// Delete removes a draft from disk. Callers must opt in explicitly; rejected
// drafts are kept by default so admins can re-read them.
func (s *DraftStore) Delete(id string) error {
	if err := ValidateDraftID(id); err != nil {
		return err
	}
	for _, p := range []string{s.jsonPath(id), s.markdownPath(id)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", p, err)
		}
	}
	return nil
}

// SetStatus updates a draft's status and re-saves it.
func (s *DraftStore) SetStatus(id string, status Status, notes string) (*Draft, error) {
	d, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	d.Status = status
	if notes != "" {
		d.Notes = notes
	}
	if err := s.Save(d); err != nil {
		return nil, err
	}
	return d, nil
}
