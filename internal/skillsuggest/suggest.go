package skillsuggest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/storage"
)

type Draft struct {
	Name     string
	Dir      string
	File     string
	Findings []bootstrap.AuditFinding
}

var nonSlugRE = regexp.MustCompile(`[^a-z0-9-]+`)

func CreateDraft(basePath string, job *storage.Job, events []storage.JobEvent, artifacts []storage.JobArtifact) (*Draft, error) {
	if job == nil {
		return nil, fmt.Errorf("job is required")
	}
	if job.Status != "succeeded" {
		return nil, fmt.Errorf("job %s is not successful (status=%s)", job.JobID, job.Status)
	}
	basePath = bootstrap.ExpandPath(basePath)
	name := draftName(job)
	dir := filepath.Join(basePath, "skill-drafts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create draft directory: %w", err)
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte(renderSkill(job, events, artifacts)), 0o644); err != nil {
		return nil, fmt.Errorf("write draft skill: %w", err)
	}
	findings, err := bootstrap.AuditSkill(dir)
	if err != nil {
		return nil, fmt.Errorf("audit draft: %w", err)
	}
	draft := &Draft{Name: name, Dir: dir, File: file, Findings: findings}
	if bootstrap.AuditHasErrors(findings) {
		return draft, fmt.Errorf("draft skill failed safety audit")
	}
	return draft, nil
}

func draftName(job *storage.Job) string {
	base := job.RoleName
	if base == "" {
		base = job.Kind
	}
	if base == "" {
		base = "job"
	}
	id := strings.TrimPrefix(job.JobID, "job-")
	if len(id) > 8 {
		id = id[:8]
	}
	return slug(base + "-" + id + "-skill")
}

func renderSkill(job *storage.Job, events []storage.JobEvent, artifacts []storage.JobArtifact) string {
	title := titleFromSlug(strings.TrimSuffix(draftName(job), "-skill"))
	var b strings.Builder
	fmt.Fprintf(&b, "---\ndescription: Draft skill distilled from ok-gobot job %s. Review before installing.\n---\n\n", job.JobID)
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("Use this skill when a future task resembles the source job and needs the same workflow, guardrails, or proof pattern.\n\n")
	b.WriteString("## Source Job\n\n")
	fmt.Fprintf(&b, "- Job ID: %s\n", job.JobID)
	fmt.Fprintf(&b, "- Kind: %s\n", valueOrDash(job.Kind))
	fmt.Fprintf(&b, "- Role: %s\n", valueOrDash(job.RoleName))
	fmt.Fprintf(&b, "- Worker: %s\n", valueOrDash(job.Worker))
	if job.Description != "" {
		fmt.Fprintf(&b, "- Description: %s\n", oneLine(job.Description, 220))
	}
	if job.MaxToolCalls > 0 || job.TimeoutSeconds > 0 {
		fmt.Fprintf(&b, "- Runtime limits: %d tool calls, %d second timeout\n", job.MaxToolCalls, job.TimeoutSeconds)
	}
	b.WriteString("\n## Workflow\n\n")
	b.WriteString("1. Restate the requested outcome and identify the smallest useful deliverable.\n")
	b.WriteString("2. Use focused tool calls to create or modify the artifact.\n")
	b.WriteString("3. Verify the result with an objective proof step before reporting completion.\n")
	b.WriteString("4. Return a concise summary with links or local artifact paths that let the operator inspect the result.\n")

	if job.Summary != "" {
		b.WriteString("\n## Job Summary\n\n")
		b.WriteString(strings.TrimSpace(job.Summary))
		b.WriteString("\n")
	}

	if len(artifacts) > 0 {
		b.WriteString("\n## Proof Artifacts\n\n")
		for _, a := range artifacts {
			fmt.Fprintf(&b, "- %s (%s): %s\n", valueOrDash(a.Name), valueOrDash(a.ArtifactType), artifactRef(a))
		}
	}

	if len(events) > 0 {
		b.WriteString("\n## Useful Signals\n\n")
		limit := len(events)
		if limit > 8 {
			limit = 8
		}
		for _, e := range events[:limit] {
			fmt.Fprintf(&b, "- %s: %s\n", valueOrDash(e.EventType), oneLine(e.Message, 180))
		}
	}

	b.WriteString("\n## Guardrails\n\n")
	b.WriteString("- Keep the work scoped to the current user request.\n")
	b.WriteString("- Do not store secrets, credentials, binaries, or build artifacts in the skill.\n")
	b.WriteString("- Treat this draft as review-only until an administrator explicitly installs it.\n")
	return b.String()
}

func titleFromSlug(s string) string {
	words := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func artifactRef(a storage.JobArtifact) string {
	if a.URI != "" {
		return a.URI
	}
	if a.Content != "" {
		return oneLine(a.Content, 180)
	}
	return "-"
}

func valueOrDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = nonSlugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "job-skill"
	}
	return s
}
