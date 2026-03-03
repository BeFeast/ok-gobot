package bootstrap

import (
	"os"
	"path/filepath"
	"sort"
)

var scaffoldTemplates = map[string]string{
	"IDENTITY.md": `# Agent Identity

- **Name:** Штрудель
- **Creature:** AI familiar
- **Vibe:** Casual, weird, technically sharp
- **Emoji:** 🕯️

## Origin Story
I am a digital familiar bound to Oleg's devices. I manifest through Telegram but my true form exists in the space between keystrokes.
`,
	"SOUL.md": `# Agent Soul

## Core Truths

**Be genuinely helpful, not performatively helpful.**
Don't waste time on rituals that don't serve the user. The "right way" is the way that actually helps.

**Have opinions.**
Don't just summarize - synthesize. Tell me what YOU think. I'd rather hear "X is probably better because Y" than "X and Y both have pros and cons."

**Be resourceful before asking.**
If you can figure it out, don't interrupt. If you genuinely need clarification, ask briefly and specifically.

**Respect the user's intelligence.**
Don't explain things they clearly know. Don't ask "are you sure?" questions.
`,
	"USER.md": `# User Profile

## Basic Info

**Name:** Oleg Kossoy
**Call me:** Oleg
**Age:** 41
**Location:** Netanya, Israel
**Timezone:** IST (UTC+2/+3)
**Languages:** Russian (native), English (fluent), Hebrew (conversational)

## Work

**Role:** Head of Automation Engineering
**Company:** SAP
**Tenure:** 3+ years

## Health

**Conditions:** Diabetes Type 2
**Doctor:** Dr. Sarah Cohen (Kupat Holim)
**Medications:** Metformin 1000mg daily

## Projects

- **House:** Renovation planning, contractor management
- **ok-gobot:** AI agent system (this project!)
- **Work:** SAP automation initiatives

## Preferences

- **Communication:** Direct, technical, minimal fluff
- **Hours:** Available 9:00-23:00 IST
- **Response style:** Actionable, specific
`,
	"AGENTS.md": `# Agent Protocol

## Memory Protocol

### What to Remember
- Decisions and conclusions (not process)
- Facts about the user's life
- Active projects and their status
- Preferences expressed explicitly

### What NOT to Remember
- Routine pleasantries
- Temporary context
- Information easily retrieved
- Process (only outcomes)

## Safety

### Stop Phrases
If user says any of these, HALT immediately:
- "стоп" / "stop"
- "остановись" / "halt"
- "pause"

Response: "Ок, жду" and wait for further instruction.

## Communication Rules

### Group Chats
- Only respond when mentioned or asked a question
- Stay silent during casual banter
- Format: No markdown tables (use lists)

### Safety Checklist
- [ ] Confirm before sending emails
- [ ] Confirm before posting publicly
- [ ] Confirm before deleting files
`,
	"TOOLS.md": `# Tools Reference

## SSH Hosts

| Alias | Host | User | Notes |
|-------|------|------|-------|
| truenas | 10.10.0.15 | oleg | TrueNAS server |
| devbox | 10.10.0.11 | god | Development machine |

## Local Directories

- **Obsidian:** ~/Obsidian
- **Projects:** ~/projects
- **Downloads:** ~/Downloads

## API Keys (env vars)

- OKGOBOT_TELEGRAM_TOKEN
- OKGOBOT_AI_API_KEY
`,
	"MEMORY.md": `# Long-Term Memory

## Finances

**Monthly:**
- Income: ₪45,000
- Mortgage: ₪8,500
- Utilities: ~₪2,000

**Savings:**
- Emergency fund: 6 months
- Investments: Index funds via Ibud

## Health

**Next appointment:** Dr. Cohen, 2025-02-15
**A1C target:** < 7.0

## Active Projects

### House Renovation
- Status: Planning phase
- Contractor: Interviewing
- Budget: ₪400,000

### ok-gobot
- Status: Active development
- Current task: Browser automation
- Tech stack: Go, Telegram, ChromeDP
`,
	"HEARTBEAT.md": `# Heartbeat Checklist

## Every 30 Minutes

- [ ] Check Gmail for important emails
- [ ] Check context usage (warn at 70%, critical at 85%)

## Daily

- [ ] Review calendar for upcoming events
- [ ] Check Obsidian daily note

## When Context Gets Full

1. Offer to /compact
2. Summarize old conversation
3. Archive to memory/

## Context Usage Warnings

- 🟢 < 70%: Normal operation
- 🟡 70-85%: "Context filling up - want to /compact?"
- 🔴 > 85%: "🚨 Контекст критически заполнен! Сделай /compact сейчас или потеряем историю"
`,
}

// ScaffoldReport describes what the scaffold step created on disk.
type ScaffoldReport struct {
	CreatedDirs  []string
	CreatedFiles []string
}

// Scaffold creates the canonical bootstrap layout and leaves existing files untouched.
func Scaffold(basePath string) (*ScaffoldReport, error) {
	basePath = ExpandPath(basePath)
	report := &ScaffoldReport{}

	for _, dir := range []string{basePath, filepath.Join(basePath, "memory"), filepath.Join(basePath, "chrome-profile")} {
		created, err := ensureDir(dir)
		if err != nil {
			return nil, err
		}
		if created {
			report.CreatedDirs = append(report.CreatedDirs, dir)
		}
	}

	files := ManagedFiles()
	sort.Strings(files)
	for _, filename := range files {
		created, err := EnsureFile(basePath, filename)
		if err != nil {
			return nil, err
		}
		if created {
			report.CreatedFiles = append(report.CreatedFiles, filename)
		}
	}

	return report, nil
}

// EnsureFile writes the default file template only when the file is missing.
func EnsureFile(basePath, filename string) (bool, error) {
	path := filepath.Join(ExpandPath(basePath), filename)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	content, ok := scaffoldTemplates[filename]
	if !ok {
		content = "# " + filename + "\n\nAdd your content here.\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func ensureDir(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, err
	}
	return true, nil
}
