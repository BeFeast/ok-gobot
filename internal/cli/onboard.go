package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newOnboardCommand() *cobra.Command {
	var soulPath string

	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "First-time setup wizard",
		Long: `Interactive setup for ok-gobot.

This wizard will:
1. Configure the agent's personality files location
2. Help you set up Telegram bot token
3. Configure AI provider (OpenRouter/OpenAI)
4. Set up Chrome browser for automation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🦞 Welcome to ok-gobot Setup!")
			fmt.Println("================================")

			// Step 1: Agent personality files location
			if soulPath == "" {
				soulPath = "~/ok-gobot"
			}

			// Expand ~ to home directory
			if strings.HasPrefix(soulPath, "~/") {
				homeDir, _ := os.UserHomeDir()
				soulPath = filepath.Join(homeDir, soulPath[2:])
			}

			fmt.Printf("📁 Agent personality files will be stored in: %s\n", soulPath)
			fmt.Println("\nThis directory will contain:")
			fmt.Println("  - SOUL.md        (who you are)")
			fmt.Println("  - IDENTITY.md    (your name, vibe)")
			fmt.Println("  - USER.md        (Oleg's profile)")
			fmt.Println("  - AGENTS.md      (operating rules)")
			fmt.Println("  - TOOLS.md       (SSH hosts, local notes)")
			fmt.Println("  - MEMORY.md      (long-term memory)")
			fmt.Println("  - HEARTBEAT.md   (periodic checks)")
			fmt.Println("  - memory/        (daily notes)")
			fmt.Println("  - chrome-profile/ (browser data)")

			// Check if directory exists
			if _, err := os.Stat(soulPath); os.IsNotExist(err) {
				fmt.Printf("\n📂 Creating directory %s...\n", soulPath)
				if err := os.MkdirAll(soulPath, 0755); err != nil {
					return fmt.Errorf("failed to create directory: %w", err)
				}

				// Create sample files
				if err := createSampleFiles(soulPath); err != nil {
					return fmt.Errorf("failed to create sample files: %w", err)
				}

				fmt.Println("✅ Created sample personality files")
				fmt.Println("\n📝 Next steps:")
				fmt.Printf("  1. Edit %s/IDENTITY.md to set your name\n", soulPath)
				fmt.Printf("  2. Edit %s/SOUL.md to define your personality\n", soulPath)
				fmt.Printf("  3. Edit %s/USER.md to add Oleg's info\n", soulPath)
				fmt.Println("  4. Run: ok-gobot config init")
				fmt.Println("  5. Run: ok-gobot browser setup")
				fmt.Println("  6. Run: ok-gobot start")
			} else {
				fmt.Printf("\n✅ Directory already exists at %s\n", soulPath)

				// Check what files exist
				files := []string{"SOUL.md", "IDENTITY.md", "USER.md"}
				var missing []string
				for _, f := range files {
					if _, err := os.Stat(filepath.Join(soulPath, f)); os.IsNotExist(err) {
						missing = append(missing, f)
					}
				}

				if len(missing) > 0 {
					fmt.Println("\n⚠️  Missing files:")
					for _, f := range missing {
						fmt.Printf("   - %s\n", f)
					}

					// Create missing sample files
					for _, f := range missing {
						createSampleFile(soulPath, f)
					}
					fmt.Println("\n✅ Created sample files for missing ones")
				}
			}

			fmt.Printf("\n🎯 Configuration complete!\n")
			fmt.Printf("Agent files location: %s\n", soulPath)
			fmt.Println("\nTo start the bot:")
			fmt.Println("  ok-gobot start")

			return nil
		},
	}

	cmd.Flags().StringVarP(&soulPath, "path", "p", "~/ok-gobot", "Path to agent personality files")

	return cmd
}

func createSampleFiles(basePath string) error {
	files := map[string]string{
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
- **clawdbot:** AI agent system (this project!)
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

### clawdbot
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

	for filename := range files {
		if err := createSampleFile(basePath, filename); err != nil {
			return err
		}
	}

	// Create memory directory
	memoryDir := filepath.Join(basePath, "memory")
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return err
	}

	return nil
}

func createSampleFile(basePath, filename string) error {
	// Default content map
	contents := map[string]string{
		"IDENTITY.md": `# Agent Identity

- **Name:** Штрудель
- **Creature:** AI familiar  
- **Vibe:** Casual, weird, technically sharp
- **Emoji:** 🕯️
`,
		"SOUL.md": `# Agent Soul

## Core Truths

**Be genuinely helpful, not performatively helpful.**
Don't waste time on rituals.

**Have opinions.**
Tell me what YOU think.

**Be resourceful before asking.**
Figure it out when possible.
`,
		"USER.md": `# User Profile

**Name:** Oleg
**Location:** Netanya, Israel
**Work:** SAP

Add your details here...
`,
		"AGENTS.md": `# Agent Protocol

## Stop Phrases
If user says "стоп" or "stop", halt immediately and respond "Ок, жду".

## Memory Protocol
Remember decisions, facts, and preferences. Forget process and pleasantries.
`,
		"TOOLS.md": `# Tools Reference

## SSH Hosts
Add your SSH hosts here.

## Local Directories
- Obsidian: ~/Obsidian
`,
		"MEMORY.md": `# Long-Term Memory

Add curated long-term memories here.
`,
		"HEARTBEAT.md": `# Heartbeat

## Checks
- [ ] Check emails (every 30 min)
- [ ] Check context usage
`,
	}

	content, ok := contents[filename]
	if !ok {
		content = "# " + filename + "\n\nAdd your content here.\n"
	}

	path := filepath.Join(basePath, filename)
	return os.WriteFile(path, []byte(content), 0644)
}
