package bot

import (
	"strings"
	"testing"
)

func TestParseSkillPayload(t *testing.T) {
	name, payload := parseSkillPayload(" video_summary   https://youtu.be/x ")
	if name != "video-summary" {
		t.Fatalf("name=%q, want video-summary", name)
	}
	if payload != "https://youtu.be/x" {
		t.Fatalf("payload=%q, want URL", payload)
	}
}

func TestResolveNativeSkillAliases(t *testing.T) {
	for _, alias := range []string{"video-summary", "video_summary", "/video_summary"} {
		skill, ok := resolveNativeSkill(alias)
		if !ok {
			t.Fatalf("expected alias %q to resolve", alias)
		}
		if skill.Name != "video-summary" {
			t.Fatalf("alias %q resolved to %q", alias, skill.Name)
		}
	}

	for _, alias := range []string{"karaoke", "youtube-karaoke", "youtube_karaoke"} {
		skill, ok := resolveNativeSkill(alias)
		if !ok {
			t.Fatalf("expected alias %q to resolve", alias)
		}
		if skill.Name != "karaoke" {
			t.Fatalf("alias %q resolved to %q", alias, skill.Name)
		}
	}
}

func TestFormatNativeSkillsShowsFallbackCommands(t *testing.T) {
	out := formatNativeSkills()
	for _, want := range []string{
		"video-summary",
		"/video_summary <youtube_url>",
		"/skill video-summary <youtube_url>",
		"karaoke",
		"/karaoke <youtube_url>",
		"/skill karaoke <youtube_url>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestFormatUnknownNativeSkillIncludesAvailableSkills(t *testing.T) {
	out := formatUnknownNativeSkill("missing")
	for _, want := range []string{"Unknown native skill: missing", "video-summary", "karaoke", "/skill <name> <args>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}
