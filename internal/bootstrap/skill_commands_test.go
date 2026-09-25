package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDefaultSkillCommand(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"media-request":       "media_request",
		"Add-Knowledge":       "add_knowledge",
		"transcript_summary":  "transcript_summary",
		" obsidian-markdown ": "obsidian_markdown",
	}
	for in, want := range cases {
		if got := DefaultSkillCommand(in); got != want {
			t.Errorf("DefaultSkillCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSkillFrontmatter_CommandFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want skillFrontmatter
	}{
		{
			name: "no command key",
			in:   "---\nname: x\ndescription: Do things\n---\n",
			want: skillFrontmatter{Description: "Do things"},
		},
		{
			name: "override bare",
			in:   "---\ndescription: Do things\ncommand: plex\n---\n",
			want: skillFrontmatter{Description: "Do things", Command: "plex", CommandSet: true},
		},
		{
			name: "override quoted with menu description",
			in:   "---\ndescription: 'Do: things'\ncommand: \"plex\"\ncommand_description: \"Ask Plex\"\n---\n",
			want: skillFrontmatter{Description: "Do: things", Command: "plex", CommandSet: true, CommandDescription: "Ask Plex"},
		},
		{
			name: "explicit opt-out",
			in:   "---\ndescription: Do things\ncommand: \"\"\n---\n",
			want: skillFrontmatter{Description: "Do things", Command: "", CommandSet: true},
		},
		{
			name: "body fallback description",
			in:   "# Title\nBody line.\n",
			want: skillFrontmatter{Description: "Body line."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSkillFrontmatter(tt.in); got != tt.want {
				t.Fatalf("parseSkillFrontmatter() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestListSkills_PopulatesCommandFields(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	write := func(name, content string) {
		dir := filepath.Join(base, "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("media-request", "---\nname: media-request\ndescription: Download a movie\n---\n")
	write("plex-tools", "---\ndescription: Plex helpers\ncommand: plex\ncommand_description: Ask Plex\n---\n")
	write("hidden-skill", "---\ndescription: Hidden\ncommand: \"\"\n---\n")

	skills, err := ListSkills(base)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SkillEntry{}
	for _, s := range skills {
		got[s.Name] = s
	}
	if s := got["media-request"]; s.Command != "media_request" || s.CommandDescription != "Download a movie" {
		t.Errorf("media-request = %+v", s)
	}
	if s := got["plex-tools"]; s.Command != "plex" || s.CommandDescription != "Ask Plex" {
		t.Errorf("plex-tools = %+v", s)
	}
	if s := got["hidden-skill"]; s.Command != "" {
		t.Errorf("hidden-skill should opt out, got command %q", s.Command)
	}
}

func TestValidSkillCommandName(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "media_request", "x9", strings.Repeat("a", 32)}
	invalid := []string{"", "Media", "media-request", "медиа", "with space", strings.Repeat("a", 33)}
	for _, v := range valid {
		if !ValidSkillCommandName(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range invalid {
		if ValidSkillCommandName(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

func TestTruncateCommandDescription(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("download movies and series with subtitles ", 12)
	got := TruncateCommandDescription(long, "media-request")
	if n := utf8.RuneCountInString(got); n > maxCommandDescriptionRunes {
		t.Fatalf("truncated description has %d runes, want <= %d", n, maxCommandDescriptionRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if strings.Contains(got, "  ") || strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Fatalf("expected squeezed whitespace and clean cut, got %q", got)
	}
	if got := TruncateCommandDescription("  short\n text ", "x"); got != "short text" {
		t.Fatalf("short text = %q", got)
	}
	if got := TruncateCommandDescription("", "media-request"); got != "Run skill media-request" {
		t.Fatalf("empty fallback = %q", got)
	}
	if got := TruncateCommandDescription("No description available", "x"); got != "Run skill x" {
		t.Fatalf("placeholder fallback = %q", got)
	}
}

func TestResolveSkillCommands(t *testing.T) {
	t.Parallel()
	skills := []SkillEntry{
		{Name: "media-request", Command: "media_request", CommandDescription: "Download", Path: "/s/media-request/SKILL.md"},
		{Name: "help-writer", Command: "help", CommandDescription: "collides with builtin"},
		{Name: "plex-a", Command: "plex", CommandDescription: "first"},
		{Name: "plex-b", Command: "plex", CommandDescription: "second"},
		{Name: "opted-out", Command: "", CommandDescription: "x"},
		{Name: "bad-name", Command: "Bad-Name", CommandDescription: "x"},
		{Name: "blocked", Command: "blocked", Compatibility: SkillCompatibilityBlocked, CompatibilityReason: "audit"},
		{Name: "trusted", Command: "trusted", Compatibility: SkillCompatibilityTrustedWorkspace, CommandDescription: "ok"},
	}
	cmds, skips := ResolveSkillCommands(skills, []string{"help", "status"}, 10)

	gotCmds := map[string]SkillCommand{}
	for _, c := range cmds {
		gotCmds[c.Command] = c
	}
	if len(cmds) != 3 {
		t.Fatalf("commands = %+v, want media_request, plex, trusted", cmds)
	}
	if c := gotCmds["media_request"]; c.SkillName != "media-request" || c.SkillPath != "/s/media-request/SKILL.md" || c.Description != "Download" {
		t.Errorf("media_request = %+v", c)
	}
	if c := gotCmds["plex"]; c.SkillName != "plex-a" {
		t.Errorf("plex should resolve to plex-a (name order), got %+v", c)
	}
	if _, ok := gotCmds["trusted"]; !ok {
		t.Errorf("trusted_workspace skill should be included")
	}

	reasons := map[string]string{}
	for _, s := range skips {
		reasons[s.SkillName] = s.Reason
	}
	for name, want := range map[string]string{
		"help-writer": "built-in",
		"plex-b":      "collides with skill plex-a",
		"opted-out":   "opted out",
		"bad-name":    "invalid command name",
		"blocked":     "blocked",
	} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("skip reason for %s = %q, want containing %q", name, reasons[name], want)
		}
	}
	if len(skips) != 5 {
		t.Errorf("skips = %d, want 5: %+v", len(skips), skips)
	}
}

func TestResolveSkillCommands_RespectsBudget(t *testing.T) {
	t.Parallel()
	const builtin = 40
	var skills []SkillEntry
	for i := 0; i < 70; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		skills = append(skills, SkillEntry{Name: name, Command: DefaultSkillCommand(name), CommandDescription: "d"})
	}
	cmds, skips := ResolveSkillCommands(skills, nil, MaxTelegramCommands-builtin)
	if len(cmds) != MaxTelegramCommands-builtin {
		t.Fatalf("commands = %d, want %d", len(cmds), MaxTelegramCommands-builtin)
	}
	if len(skips) != 10 {
		t.Fatalf("skips = %d, want 10", len(skips))
	}
	for _, s := range skips {
		if !strings.Contains(s.Reason, "limit of 100") {
			t.Fatalf("unexpected skip reason %q", s.Reason)
		}
	}
	if cmds[0].Command != "skill_000" || cmds[len(cmds)-1].Command != "skill_059" {
		t.Fatalf("expected name-ordered fill, got first=%s last=%s", cmds[0].Command, cmds[len(cmds)-1].Command)
	}
}
