package curate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrApprovalRequired is returned by Apply when the caller did not explicitly
// confirm. This makes "apply without confirmation" a hard error rather than a
// silent default.
var ErrApprovalRequired = errors.New("curate: explicit admin approval required to modify MEMORY.md")

// ErrAuditBlocked is returned by Apply when the safety audit reported errors
// that block promotion.
var ErrAuditBlocked = errors.New("curate: audit findings block apply")

// ErrEmptyDraft is returned by Apply when the draft has nothing worth
// promoting. Callers should treat this as a no-op rather than a failure.
var ErrEmptyDraft = errors.New("curate: draft has no candidates worth promoting")

// ErrAlreadyApplied is returned when a draft has already been applied; callers
// must explicitly create a fresh draft to promote new facts.
var ErrAlreadyApplied = errors.New("curate: draft already applied")

// ApplyOptions controls Apply behavior.
type ApplyOptions struct {
	// Approved must be true. This makes the approval explicit and is the
	// gate that prevents callers from accidentally promoting a draft.
	Approved bool
	// AdminLabel records who approved the change (e.g. telegram username
	// or "cli"). Stored on the draft for audit history.
	AdminLabel string
}

// Apply writes the draft's promotions into MEMORY.md after the safety audit
// passes and the caller has set Approved=true. It is the only function in the
// curate package that mutates MEMORY.md.
func Apply(soulPath string, store *DraftStore, draftID string, opts ApplyOptions) (*Draft, AuditReport, error) {
	if soulPath == "" {
		return nil, AuditReport{}, fmt.Errorf("curate apply: soul path is empty")
	}
	if store == nil {
		return nil, AuditReport{}, fmt.Errorf("curate apply: draft store is nil")
	}
	if draftID == "" {
		return nil, AuditReport{}, fmt.Errorf("curate apply: draft id is empty")
	}

	d, err := store.Load(draftID)
	if err != nil {
		return nil, AuditReport{}, err
	}
	if d.Status == StatusApplied {
		return d, AuditReport{}, ErrAlreadyApplied
	}

	audit := AuditDraft(d)
	if d.IsEmpty() {
		return d, audit, ErrEmptyDraft
	}
	if audit.HasErrors() {
		return d, audit, ErrAuditBlocked
	}
	if !opts.Approved {
		return d, audit, ErrApprovalRequired
	}

	memoryPath := filepath.Join(soulPath, "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		return d, audit, fmt.Errorf("ensure soul path: %w", err)
	}
	block := RenderApplyBlock(d)
	if err := appendToFile(memoryPath, block); err != nil {
		return d, audit, fmt.Errorf("append to MEMORY.md: %w", err)
	}

	d.Status = StatusApplied
	notes := "applied"
	if opts.AdminLabel != "" {
		notes = "applied by " + opts.AdminLabel
	}
	d.Notes = notes
	if err := store.Save(d); err != nil {
		return d, audit, fmt.Errorf("save updated draft: %w", err)
	}
	return d, audit, nil
}

// appendToFile creates the file if missing and appends the block. The file is
// always read-modify-rewrite so we can guarantee a leading newline.
func appendToFile(path, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	final := string(existing)
	if final != "" && !endsWithNewline(final) {
		final += "\n"
	}
	final += block
	return os.WriteFile(path, []byte(final), 0o644)
}

func endsWithNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}
