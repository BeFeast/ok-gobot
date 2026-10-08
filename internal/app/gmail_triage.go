package app

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/bot"
	"ok-gobot/internal/gmailtriage"
)

// setupGmailTriage wires the email digest when gmail_triage is enabled. A
// broken setup is logged and leaves the rest of the bot running.
func (a *App) setupGmailTriage(b *bot.Bot) {
	cfg := a.config.GmailTriage
	if !cfg.Enabled {
		return
	}
	if a.ai == nil {
		log.Printf("⚠️ [gmail_triage] enabled but no AI client is configured; triage disabled")
		return
	}
	store, err := gmailtriage.NewStore(a.store.DB())
	if err != nil {
		log.Printf("⚠️ [gmail_triage] storage: %v; triage disabled", err)
		return
	}
	binary := cfg.GogBinary
	if binary == "" {
		binary = gmailtriage.DefaultGogBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		log.Printf("⚠️ [gmail_triage] gog binary %q not found (%v); digests will report the error", binary, err)
	}
	client := a.ai
	svc, err := gmailtriage.New(cfg, gmailtriage.Options{
		Store: store,
		Completer: gmailtriage.CompleterFunc(func(ctx context.Context, system, user string) (string, error) {
			return client.Complete(ctx, []ai.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
		}),
		Mailboxes: gmailtriage.GogMailboxFactory(binary, nil),
		SkillText: a.gmailTriageSkillText,
	})
	if err != nil {
		log.Printf("⚠️ [gmail_triage] %v; triage disabled", err)
		return
	}
	b.SetGmailTriage(svc)
	if err := svc.StartSchedules(a.scheduler, b.FireGmailTriage); err != nil {
		log.Printf("⚠️ [gmail_triage] schedule: %v", err)
	}
	for _, p := range svc.Profiles() {
		sched, overridden, _ := svc.Schedule(p)
		paused, _ := svc.Paused(p)
		log.Printf("📬 Gmail triage profile %s: mode=%s taxonomy=%s schedule=%s override=%v paused=%v", p.Name, p.Mode, p.Taxonomy, sched.Describe("en"), overridden, paused)
	}
}

// gmailTriageSkillText reads the configured skill's SKILL.md body on every
// run, so edits to the skill apply without a restart.
func (a *App) gmailTriageSkillText() string {
	name := a.config.GmailTriage.Skill
	if name == "" {
		name = gmailtriage.DefaultSkill
	}
	if a.personality == nil || a.personality.Loader() == nil {
		return ""
	}
	for _, s := range a.personality.Loader().Skills {
		if s.Name != name {
			continue
		}
		data, err := os.ReadFile(s.Path)
		if err != nil {
			log.Printf("[gmail_triage] read skill %s: %v", s.Path, err)
			return ""
		}
		return stripFrontmatter(string(data))
	}
	return ""
}

func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---") {
		return strings.TrimSpace(s)
	}
	rest := s[3:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		rest = rest[i+4:]
		return strings.TrimSpace(strings.TrimPrefix(rest, "\n"))
	}
	return strings.TrimSpace(s)
}
