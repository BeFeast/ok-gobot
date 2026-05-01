package bootstrap

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"ok-gobot/internal/evidence"
	"ok-gobot/internal/redact"
	"ok-gobot/internal/storage"
)

// ErrSkillSuggestionUnsafe is returned when a generated draft fails skill audit.
// The draft is still left on disk for admin review and editing.
var ErrSkillSuggestionUnsafe = errors.New("skill draft failed safety audit")

// SkillSuggestionJobSource is the durable job data needed to distill a skill.
type SkillSuggestionJobSource interface {
	GetJob(jobID string) (*storage.Job, error)
	ListJobEvents(jobID string, limit int) ([]storage.JobEvent, error)
	ListJobArtifacts(jobID string, limit int) ([]storage.JobArtifact, error)
	ListEvidenceEventsForJob(jobID string, limit int) ([]evidence.Event, error)
}

// SkillSuggestion describes a generated, review-only skill draft.
type SkillSuggestion struct {
	JobID         string
	DraftID       string
	SkillName     string
	DraftDir      string
	SkillFile     string
	AuditFindings []AuditFinding
	Unsafe        bool
}

// SuggestSkillFromJob creates a reviewable SKILL.md draft from a successful job.
// It never installs or rewrites installed skills; admins must explicitly audit,
// review, and install the draft directory themselves.
func SuggestSkillFromJob(basePath string, source SkillSuggestionJobSource, jobID string) (*SkillSuggestion, error) {
	basePath = strings.TrimSpace(ExpandPath(basePath))
	if basePath == "" {
		return nil, fmt.Errorf("soul path is empty")
	}
	if source == nil {
		return nil, fmt.Errorf("job storage is required")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job ID is required")
	}

	job, err := source.GetJob(jobID)
	if err != nil {
		return nil, fmt.Errorf("load job %q: %w", jobID, err)
	}
	if job == nil {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	if strings.TrimSpace(job.Status) != "succeeded" {
		return nil, fmt.Errorf("job %q is %s; skill suggestions require succeeded jobs", job.JobID, job.Status)
	}

	events, err := source.ListJobEvents(job.JobID, 80)
	if err != nil {
		return nil, fmt.Errorf("load job events for %q: %w", job.JobID, err)
	}
	artifacts, err := source.ListJobArtifacts(job.JobID, 80)
	if err != nil {
		return nil, fmt.Errorf("load job artifacts for %q: %w", job.JobID, err)
	}
	evidenceEvents, err := source.ListEvidenceEventsForJob(job.JobID, 80)
	if err != nil {
		return nil, fmt.Errorf("load job evidence for %q: %w", job.JobID, err)
	}

	skillName := skillSuggestionName(job)
	draftID := uniqueSkillDraftID(skillName, job.JobID)
	draftDir := filepath.Join(basePath, "skill-drafts", draftID, skillName)
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		return nil, fmt.Errorf("create skill draft directory: %w", err)
	}

	skillFile := filepath.Join(draftDir, "SKILL.md")
	content := renderSkillSuggestionMarkdown(job, events, evidenceEvents, artifacts, skillName)
	if err := os.WriteFile(skillFile, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write skill draft: %w", err)
	}

	findings, err := AuditSkill(draftDir)
	if err != nil {
		return nil, fmt.Errorf("audit generated skill draft: %w", err)
	}

	result := &SkillSuggestion{
		JobID:         job.JobID,
		DraftID:       draftID,
		SkillName:     skillName,
		DraftDir:      draftDir,
		SkillFile:     skillFile,
		AuditFindings: findings,
		Unsafe:        AuditHasErrors(findings),
	}
	if result.Unsafe {
		return result, fmt.Errorf("%w: %d error(s)", ErrSkillSuggestionUnsafe, countAuditErrors(findings))
	}
	return result, nil
}

func renderSkillSuggestionMarkdown(job *storage.Job, events []storage.JobEvent, evidenceEvents []evidence.Event, artifacts []storage.JobArtifact, skillName string) string {
	title := skillTitle(skillName)
	description := skillDescription(job)
	finalReport := finalTextReport(job, artifacts)

	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ndescription: %s\n---\n\n", description)
	fmt.Fprintf(&sb, "# %s\n\n", title)
	sb.WriteString("This is a generated draft distilled from a successful role job. Review and edit it before installing.\n\n")

	sb.WriteString("## When To Use\n\n")
	fmt.Fprintf(&sb, "Use this skill when a future task resembles job `%s`", sanitizeInline(job.JobID))
	if roleName := sanitizeInline(job.RoleName); roleName != "" {
		fmt.Fprintf(&sb, " for role `%s`", roleName)
	}
	sb.WriteString(" and should reuse the same successful working pattern.\n\n")

	sb.WriteString("## Successful Pattern\n\n")
	sb.WriteString("1. Confirm the operator's objective, constraints, and required proof artifacts.\n")
	sb.WriteString("2. Reconstruct relevant context before acting; prefer the same role and worker tier when available.\n")
	sb.WriteString("3. Use the observed events, tools, and artifacts below as a checklist, not as commands to run blindly.\n")
	sb.WriteString("4. Produce a concise final report with the outcome, evidence, and any follow-up action.\n\n")

	sb.WriteString("## Source Job\n\n")
	fmt.Fprintf(&sb, "- Job ID: `%s`\n", sanitizeInline(job.JobID))
	fmt.Fprintf(&sb, "- Final outcome: `%s`\n", sanitizeInline(job.Status))
	if job.Kind != "" {
		fmt.Fprintf(&sb, "- Kind: `%s`\n", sanitizeInline(job.Kind))
	}
	if job.RoleName != "" {
		fmt.Fprintf(&sb, "- Role: `%s`\n", sanitizeInline(job.RoleName))
	}
	if job.Worker != "" {
		fmt.Fprintf(&sb, "- Worker tier: `%s`\n", sanitizeInline(job.Worker))
	}
	if job.ModelTier != "" {
		fmt.Fprintf(&sb, "- Model tier: `%s`\n", sanitizeInline(job.ModelTier))
	}
	if job.Description != "" {
		fmt.Fprintf(&sb, "- Original description: %s\n", oneLine(job.Description, 240))
	}
	if job.Summary != "" {
		fmt.Fprintf(&sb, "- Job summary: %s\n", oneLine(job.Summary, 600))
	}
	if job.ToolCallCount > 0 || job.MaxToolCalls > 0 {
		fmt.Fprintf(&sb, "- Tool calls: %d", job.ToolCallCount)
		if job.MaxToolCalls > 0 {
			fmt.Fprintf(&sb, " of max %d", job.MaxToolCalls)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	if finalReport != "" {
		sb.WriteString("## Final Text Report\n\n")
		sb.WriteString(finalReport)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Observed Events And Tools\n\n")
	writeEvidenceEventBullets(&sb, evidenceEvents, 10)
	writeJobEventBullets(&sb, events, 14)
	sb.WriteString("\n")

	sb.WriteString("## Artifacts\n\n")
	writeArtifactBullets(&sb, artifacts)
	sb.WriteString("\n")

	sb.WriteString("## Review Checklist\n\n")
	sb.WriteString("- Remove any job-specific, stale, or private details before installation.\n")
	sb.WriteString("- Confirm the procedure is generally reusable and not only a one-off transcript.\n")
	sb.WriteString("- Run `ok-gobot skills audit <draft-dir>` after edits.\n")
	sb.WriteString("- Install only after explicit admin approval with `ok-gobot skills install <draft-dir>`.\n")

	return sb.String()
}

func writeEvidenceEventBullets(sb *strings.Builder, events []evidence.Event, limit int) {
	if len(events) == 0 {
		sb.WriteString("- No structured evidence events recorded.\n")
		return
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	for i := 0; i < limit; i++ {
		event := evidence.SanitizeEvent(events[i])
		summary := event.Summary
		if summary == "" {
			summary = "recorded"
		}
		fmt.Fprintf(sb, "- Evidence `%s`", sanitizeInline(event.Type))
		if event.Status != "" {
			fmt.Fprintf(sb, " [%s]", sanitizeInline(event.Status))
		}
		fmt.Fprintf(sb, ": %s\n", oneLine(summary, 220))
	}
	if len(events) > limit {
		fmt.Fprintf(sb, "- ... %d more evidence event(s) omitted.\n", len(events)-limit)
	}
}

func writeJobEventBullets(sb *strings.Builder, events []storage.JobEvent, limit int) {
	if len(events) == 0 {
		return
	}
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	for i := 0; i < limit; i++ {
		event := events[i]
		message := oneLine(event.Message, 220)
		if message == "" {
			message = "recorded"
		}
		fmt.Fprintf(sb, "- Job event `%s`: %s\n", sanitizeInline(event.EventType), message)
	}
	if len(events) > limit {
		fmt.Fprintf(sb, "- ... %d more job event(s) omitted.\n", len(events)-limit)
	}
}

func writeArtifactBullets(sb *strings.Builder, artifacts []storage.JobArtifact) {
	if len(artifacts) == 0 {
		sb.WriteString("- No durable artifacts recorded.\n")
		return
	}
	for _, artifact := range artifacts {
		name := sanitizeInline(artifact.Name)
		if name == "" {
			name = "artifact"
		}
		fmt.Fprintf(sb, "- `%s` (%s)", name, sanitizeInline(artifact.ArtifactType))
		if value := artifactReviewValue(artifact); value != "" {
			fmt.Fprintf(sb, ": %s", value)
		}
		sb.WriteString("\n")
	}
}

func finalTextReport(job *storage.Job, artifacts []storage.JobArtifact) string {
	var parts []string
	if job != nil && strings.TrimSpace(job.Summary) != "" {
		parts = append(parts, sanitizeBlock(job.Summary, 4000))
	}
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Content) == "" {
			continue
		}
		artifactType := strings.ToLower(strings.TrimSpace(artifact.ArtifactType))
		mimeType := strings.ToLower(strings.TrimSpace(artifact.MimeType))
		if artifactType != "text_report" && artifactType != "report" && !strings.HasPrefix(mimeType, "text/") {
			continue
		}
		content := sanitizeBlock(artifact.Content, 4000)
		if content != "" && !containsString(parts, content) {
			parts = append(parts, content)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n---\n\n"))
}

func skillSuggestionName(job *storage.Job) string {
	if job == nil {
		return "job-skill"
	}
	for _, candidate := range []string{
		job.RoleName,
		strings.TrimPrefix(strings.TrimSpace(job.Description), "role:"),
		job.Description,
		job.Kind,
	} {
		if name := slugifySkillPart(candidate); name != "" {
			if strings.HasSuffix(name, "-skill") {
				return name
			}
			return name + "-skill"
		}
	}
	return "job-skill"
}

func uniqueSkillDraftID(skillName, jobID string) string {
	base := slugifySkillPart(skillName)
	if base == "" {
		base = "job-skill"
	}
	jobPart := slugifySkillPart(shortJobID(jobID))
	if jobPart == "" {
		jobPart = "job"
	}
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%s-%s-%09d", base, jobPart, now.Format("20060102-150405"), now.Nanosecond())
}

func skillTitle(skillName string) string {
	skillName = strings.TrimSuffix(strings.TrimSpace(skillName), "-skill")
	if skillName == "" {
		return "Job Skill"
	}
	parts := strings.FieldsFunc(skillName, func(r rune) bool { return r == '-' || r == '_' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ") + " Skill"
}

func skillDescription(job *storage.Job) string {
	if job == nil {
		return "Draft skill distilled from a successful job."
	}
	base := strings.TrimSpace(job.Summary)
	if base == "" {
		base = strings.TrimSpace(job.Description)
	}
	if base == "" && strings.TrimSpace(job.RoleName) != "" {
		base = "Reuse the successful " + strings.TrimSpace(job.RoleName) + " role-job pattern."
	}
	if base == "" {
		base = "Draft skill distilled from a successful job."
	}
	return oneLine(base, 140)
}

func slugifySkillPart(s string) string {
	s = strings.ToLower(redact.Redact(strings.TrimSpace(s)))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortJobID(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ""
	}
	parts := strings.Split(jobID, "-")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	if len(jobID) > 12 {
		return jobID[len(jobID)-12:]
	}
	return jobID
}

func artifactReviewValue(artifact storage.JobArtifact) string {
	uri := strings.TrimSpace(artifact.URI)
	if uri == "" {
		if strings.TrimSpace(artifact.Content) != "" {
			return fmt.Sprintf("inline content (%d bytes)", len(artifact.Content))
		}
		return ""
	}
	if u, err := url.Parse(uri); err == nil && u != nil {
		scheme := strings.ToLower(u.Scheme)
		if (scheme == "http" || scheme == "https") && u.Host != "" {
			return sanitizeInline(uri)
		}
		if scheme == "file" && u.Path != "" {
			return "local artifact " + sanitizeInline(filepath.Base(u.Path))
		}
	}
	if filepath.IsAbs(uri) {
		return "local artifact " + sanitizeInline(filepath.Base(uri))
	}
	return sanitizeInline(uri)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(redact.Redact(strings.TrimSpace(s))), " ")
	return truncateRunes(s, limit)
}

func sanitizeInline(s string) string {
	return oneLine(s, 240)
}

func sanitizeBlock(s string, limit int) string {
	s = strings.TrimSpace(redact.Redact(s))
	return truncateRunes(s, limit)
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countAuditErrors(findings []AuditFinding) int {
	n := 0
	for _, finding := range findings {
		if finding.Severity == SeverityError {
			n++
		}
	}
	return n
}
