package curate

import (
	"fmt"
	"strings"
)

// RenderDraftMarkdown produces a human-readable rendering of a draft and its
// audit findings. Used both for on-disk inspection files and Telegram replies.
func RenderDraftMarkdown(d *Draft, audit AuditReport) string {
	if d == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Curation Draft %s\n\n", d.ID)
	fmt.Fprintf(&sb, "- Status: `%s`\n", d.Status)
	fmt.Fprintf(&sb, "- Created: %s\n", d.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"))
	fmt.Fprintf(&sb, "- Range: %s → %s\n", d.Since, d.Until)
	fmt.Fprintf(&sb, "- Daily notes scanned: %d\n", d.SourceCount)
	fmt.Fprintf(&sb, "- Candidates: %d\n", len(d.Candidates))
	if d.Notes != "" {
		fmt.Fprintf(&sb, "- Reviewer notes: %s\n", d.Notes)
	}
	sb.WriteString("\n")

	if d.IsEmpty() {
		sb.WriteString("_No useful candidates were extracted from the daily notes in this range._\n")
	} else {
		grouped := d.CandidatesBySection()
		for _, section := range AllSections() {
			cands := grouped[section]
			if len(cands) == 0 && section != SectionStale && section != SectionTodo {
				continue
			}
			fmt.Fprintf(&sb, "## %s\n\n", SectionTitle(section))
			if len(cands) == 0 {
				sb.WriteString("_(none)_\n\n")
				continue
			}
			for _, c := range cands {
				fmt.Fprintf(&sb, "- **%s** (%s, conf %.2f)\n", c.Text, c.ID, c.Confidence)
				for _, src := range c.Sources {
					fmt.Fprintf(&sb, "  - source: `%s`\n", src.String())
				}
				if len(c.Conflicts) > 0 {
					fmt.Fprintf(&sb, "  - ⚠️ conflicts with: %s\n", strings.Join(c.Conflicts, ", "))
				}
			}
			sb.WriteString("\n")
		}
	}

	if len(audit.Findings) > 0 {
		sb.WriteString("## Audit findings\n\n")
		for _, f := range audit.Findings {
			fmt.Fprintf(&sb, "- %s\n", f.String())
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("Apply with `ok-gobot memory curate apply " + d.ID + " --yes` (admin) or reject with `ok-gobot memory curate reject " + d.ID + "`.\n")

	return sb.String()
}

// RenderDraftSummary produces a short summary suitable for Telegram messages
// and CLI list output.
func RenderDraftSummary(d *Draft) string {
	if d == nil {
		return ""
	}
	conflicts := 0
	for _, c := range d.Candidates {
		if len(c.Conflicts) > 0 {
			conflicts++
		}
	}
	return fmt.Sprintf("%s [%s] %s→%s — %d candidate(s), %d conflict(s)",
		d.ID, d.Status, d.Since, d.Until, len(d.Candidates), conflicts)
}

// RenderApplyBlock formats the markdown block that gets appended to MEMORY.md
// when a draft is applied. The block is bracketed with a unique header so it
// can be located later if the admin wants to revert the change manually.
func RenderApplyBlock(d *Draft) string {
	if d == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n## Curated promotion %s\n\n", d.ID)
	fmt.Fprintf(&sb, "_Promoted from daily notes %s → %s on %s._\n\n",
		d.Since, d.Until, d.CreatedAt.UTC().Format("2006-01-02"))
	grouped := d.CandidatesBySection()
	for _, section := range AllSections() {
		cands := grouped[section]
		if len(cands) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n", SectionTitle(section))
		for _, c := range cands {
			sources := make([]string, 0, len(c.Sources))
			for _, src := range c.Sources {
				sources = append(sources, src.String())
			}
			fmt.Fprintf(&sb, "- %s _(source: %s)_\n", c.Text, strings.Join(sources, ", "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
