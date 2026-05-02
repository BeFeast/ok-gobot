package bot

import (
	"fmt"
	"strings"

	"gopkg.in/telebot.v4"
)

type nativeSkill struct {
	Name        string
	Aliases     []string
	Command     string
	Usage       string
	Description string
}

func nativeSkills() []nativeSkill {
	return []nativeSkill{
		{
			Name:        "video-summary",
			Aliases:     []string{"video_summary", "video-summary"},
			Command:     "/video_summary",
			Usage:       "/skill video-summary <youtube_url>",
			Description: "YouTube transcript + summary into Obsidian Digests.",
		},
		{
			Name:        "karaoke",
			Aliases:     []string{"youtube-karaoke", "karaoke"},
			Command:     "/karaoke",
			Usage:       "/skill karaoke <youtube_url>",
			Description: "YouTube karaoke job with share, karaoke.mp3, vocals.mp3, lyrics.txt.",
		},
	}
}

func (b *Bot) handleSkillsCommand(c telebot.Context) error {
	return c.Send(formatNativeSkills())
}

func (b *Bot) handleSkillCommand(c telebot.Context) error {
	name, payload := parseSkillPayload(c.Message().Payload)
	if name == "" || name == "list" {
		return b.handleSkillsCommand(c)
	}

	skill, ok := resolveNativeSkill(name)
	if !ok {
		return c.Send(formatUnknownNativeSkill(name))
	}
	if strings.TrimSpace(payload) == "" {
		return c.Send(fmt.Sprintf("Usage: %s\nNative command: %s <youtube_url>", skill.Usage, skill.Command))
	}

	msg := c.Message()
	originalPayload := msg.Payload
	msg.Payload = payload
	defer func() { msg.Payload = originalPayload }()

	switch skill.Name {
	case "video-summary":
		return b.handleVideoSummaryCommand(c)
	case "karaoke":
		return b.handleKaraokeCommand(c)
	default:
		return c.Send(formatUnknownNativeSkill(name))
	}
}

func parseSkillPayload(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return "", ""
	}
	name := normalizeSkillName(parts[0])
	rest := strings.TrimSpace(strings.TrimPrefix(raw, parts[0]))
	return name, rest
}

func resolveNativeSkill(name string) (nativeSkill, bool) {
	name = normalizeSkillName(name)
	for _, skill := range nativeSkills() {
		if normalizeSkillName(skill.Name) == name {
			return skill, true
		}
		for _, alias := range skill.Aliases {
			if normalizeSkillName(alias) == name {
				return skill, true
			}
		}
	}
	return nativeSkill{}, false
}

func normalizeSkillName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimPrefix(name, "/")
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

func formatNativeSkills() string {
	var sb strings.Builder
	sb.WriteString("Native skills:\n\n")
	for _, skill := range nativeSkills() {
		sb.WriteString(fmt.Sprintf("• %s — %s\n", skill.Name, skill.Description))
		sb.WriteString(fmt.Sprintf("  Command: %s <youtube_url>\n", skill.Command))
		sb.WriteString(fmt.Sprintf("  Skill: %s\n", skill.Usage))
	}
	return strings.TrimSpace(sb.String())
}

func formatUnknownNativeSkill(name string) string {
	var names []string
	for _, skill := range nativeSkills() {
		names = append(names, skill.Name)
	}
	return fmt.Sprintf("Unknown native skill: %s\nAvailable: %s\nUsage: /skill <name> <args>", name, strings.Join(names, ", "))
}
