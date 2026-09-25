package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
)

// builtinCommandOrder is the user-facing menu contract. Append-only.
var builtinCommandOrder = []string{
	"help", "commands", "status", "whoami", "new", "note", "clear", "stop", "abort",
	"memory", "memory_status", "video_summary", "youtube_karaoke", "memory_curate", "qmd",
	"tools", "model", "agent", "usage", "context", "compact", "think", "verbose",
	"active_memory", "queue", "steer", "tts", "estop", "task", "btw", "activate",
	"standby", "pair", "auth", "reload", "restart",
}

func TestBuiltinCommandOrderUnchanged(t *testing.T) {
	b := &Bot{}
	got := b.builtinCommands()
	if len(got) != len(builtinCommandOrder) {
		t.Fatalf("built-in menu has %d commands, want %d", len(got), len(builtinCommandOrder))
	}
	for i, cmd := range got {
		if cmd.Text != builtinCommandOrder[i] {
			t.Fatalf("built-in command %d = %q, want %q", i, cmd.Text, builtinCommandOrder[i])
		}
	}
}

// Every slash command with a telebot handler must be reserved so a skill can
// never register the same name in the menu (telebot routes handled commands
// first, so the skill entry would be dead).
func TestReservedCommandsCoverAllHandlers(t *testing.T) {
	handlePattern := regexp.MustCompile(`Handle\("/([a-z0-9_]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	reserved := map[string]bool{}
	for _, name := range (&Bot{}).reservedCommandNames() {
		reserved[name] = true
	}
	// Tessera commands are only registered when the coordinator is configured.
	for _, name := range []string{"capture", "inbox", "attention", "tessera_retry"} {
		reserved[name] = true
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range handlePattern.FindAllStringSubmatch(string(src), -1) {
			if !reserved[m[1]] {
				t.Errorf("%s registers /%s but it is not in the reserved command set", file, m[1])
			}
		}
	}
}

func writeTestSkill(t *testing.T, soul, name, frontmatter string) string {
	t.Helper()
	dir := filepath.Join(soul, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\n"+frontmatter+"---\n# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSkillCommands_FromPersonalitySkipsCollisionsAndOptOuts(t *testing.T) {
	soul := t.TempDir()
	mediaPath := writeTestSkill(t, soul, "media-request", "description: Download a movie via Radarr\n")
	writeTestSkill(t, soul, "help-writer", "description: Writes help\ncommand: help\n")
	writeTestSkill(t, soul, "hidden-skill", "description: Hidden\ncommand: \"\"\n")
	writeTestSkill(t, soul, "jobs-helper", "description: shadows hidden handler\ncommand: jobs\n")

	personality, err := agent.NewPersonality(soul)
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{personality: personality}

	logs := captureLogs(t, func() {
		b.setSkillCommands(b.resolveSkillCommands())
	})

	cmd, ok := b.lookupSkillCommand("media_request")
	if !ok {
		t.Fatalf("media_request not registered; list=%+v", b.skillCommandList())
	}
	if cmd.SkillName != "media-request" || cmd.SkillPath != mediaPath || cmd.Description != "Download a movie via Radarr" {
		t.Fatalf("media_request = %+v", cmd)
	}
	for _, name := range []string{"help", "hidden_skill", "jobs"} {
		if _, ok := b.lookupSkillCommand(name); ok {
			t.Errorf("%s must not be registered as a skill command", name)
		}
	}
	if len(b.skillCommandList()) != 1 {
		t.Fatalf("expected exactly one skill command, got %+v", b.skillCommandList())
	}
	for _, want := range []string{"skill=help-writer", "skill=hidden-skill", "skill=jobs-helper", "collides with a built-in"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		text       string
		name, args string
		ok         bool
	}{
		{"/media_request Dune Part Two 2024", "media_request", "Dune Part Two 2024", true},
		{"/media_request@okgobot Dune", "media_request", "Dune", true},
		{"/media_request@OKGOBOT", "media_request", "", true},
		{"/media_request@otherbot Dune", "", "", false},
		{"/media_request\nмногострочный запрос", "media_request", "многострочный запрос", true},
		{"  /status  ", "status", "", true},
		{"plain text", "", "", false},
		{"/", "", "", false},
	}
	for _, tc := range cases {
		name, args, ok := parseSlashCommand(tc.text, "okgobot")
		if name != tc.name || args != tc.args || ok != tc.ok {
			t.Errorf("parseSlashCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.text, name, args, ok, tc.name, tc.args, tc.ok)
		}
	}
}

func newSkillCommandTestBot(t *testing.T, tg *fakeTelegramAPI) (*Bot, *policyAIClient, string) {
	t.Helper()
	b, aiClient := newChatPolicyTestBot(t, tg, "standby")
	soul := b.personality.BasePath
	skillPath := writeTestSkill(t, soul, "media-request", "description: Download a movie via Radarr or a TV show via Sonarr\n")
	if err := b.personality.Reload(); err != nil {
		t.Fatal(err)
	}
	b.RefreshCommands()
	if _, ok := b.lookupSkillCommand("media_request"); !ok {
		t.Fatalf("media_request not registered: %+v", b.skillCommandList())
	}
	return b, aiClient, skillPath
}

func dmContext(id int, text string) *fakeContext {
	return &fakeContext{msg: &telebot.Message{
		ID:     id,
		Text:   text,
		Chat:   &telebot.Chat{ID: 5150, Type: telebot.ChatPrivate},
		Sender: &telebot.User{ID: 7, Username: "oleg"},
	}}
}

func TestRefreshCommands_RegistersSkillCommandsAfterBuiltins(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _, _ := newSkillCommandTestBot(t, tg)
	_ = b

	var registered []telebot.Command
	for _, req := range tg.snapshotRequests() {
		if req.Method == "setMyCommands" {
			registered = nil
			if err := json.Unmarshal([]byte(req.Commands), &registered); err != nil {
				t.Fatalf("decode setMyCommands payload: %v (%s)", err, req.Commands)
			}
		}
	}
	if len(registered) != len(builtinCommandOrder)+1 {
		t.Fatalf("registered %d commands, want %d built-in + 1 skill", len(registered), len(builtinCommandOrder))
	}
	for i, name := range builtinCommandOrder {
		if registered[i].Text != name {
			t.Fatalf("command %d = %q, want built-in %q", i, registered[i].Text, name)
		}
	}
	last := registered[len(registered)-1]
	if last.Text != "media_request" || !strings.Contains(last.Description, "Download a movie") {
		t.Fatalf("last command = %+v, want media_request", last)
	}
}

func TestHandleMessage_SkillCommandRunsAgentTurnWithSelectedSkill(t *testing.T) {
	for _, text := range []string{
		"/media_request Dune Part Two 2024",
		"/media_request@okgobot Dune Part Two 2024",
	} {
		t.Run(text, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, aiClient, skillPath := newSkillCommandTestBot(t, tg)

			if err := b.handleMessage(context.Background(), dmContext(100, text)); err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}
			tg.waitForText(t, policyAnswer, 5*time.Second)
			waitForChatIdle(t, b, 5150, 5*time.Second)

			for _, want := range []string{"skill `media-request`", skillPath, "User request:\nDune Part Two 2024", "/media_request"} {
				if !aiClient.sawUserText(want) {
					t.Errorf("agent turn missing %q; seen=%q", want, aiClient.seen)
				}
			}
		})
	}
}

func TestHandleMessage_UnknownCommandStaysSilent(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, aiClient, _ := newSkillCommandTestBot(t, tg)

	if err := b.handleMessage(context.Background(), dmContext(100, "/nonexistent Dune")); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if aiClient.calls() != 0 {
		t.Fatalf("unknown command must not start an agent turn; seen=%q", aiClient.seen)
	}
}

func TestHandleMessage_SkillCommandWithoutArgsPromptsThenRuns(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, aiClient, skillPath := newSkillCommandTestBot(t, tg)

	first := dmContext(100, "/media_request")
	if err := b.handleMessage(context.Background(), first); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	if len(first.sent) != 1 || !strings.Contains(first.sent[0], "What should the media-request skill do?") {
		t.Fatalf("expected guided prompt, got %#v", first.sent)
	}
	if len(first.sentOpts) != 1 || len(first.sentOpts[0]) != 1 {
		t.Fatalf("expected ForceReply send options, got %#v", first.sentOpts)
	}
	opts, ok := first.sentOpts[0][0].(*telebot.SendOptions)
	if !ok || opts.ReplyMarkup == nil || !opts.ReplyMarkup.ForceReply || !opts.ReplyMarkup.Selective {
		t.Fatalf("expected selective ForceReply markup, got %#v", first.sentOpts[0][0])
	}
	if aiClient.calls() != 0 {
		t.Fatalf("empty invocation must not start an agent turn")
	}

	if err := b.handleMessage(context.Background(), dmContext(101, "Dune Part Two 2024")); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	tg.waitForText(t, policyAnswer, 5*time.Second)
	waitForChatIdle(t, b, 5150, 5*time.Second)
	for _, want := range []string{skillPath, "User request:\nDune Part Two 2024"} {
		if !aiClient.sawUserText(want) {
			t.Errorf("agent turn missing %q; seen=%q", want, aiClient.seen)
		}
	}
}

func TestCommandsCommand_ListsSkillCommandsAfterBuiltins(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _, _ := newSkillCommandTestBot(t, tg)

	ctx := dmContext(100, "/commands")
	if err := b.handleCommandsCommand(ctx); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(ctx.sent, "\n")
	builtinIdx := strings.Index(out, "/restart —")
	skillIdx := strings.Index(out, "/media_request —")
	if builtinIdx < 0 || skillIdx < 0 || skillIdx < builtinIdx {
		t.Fatalf("expected skill commands after built-ins:\n%s", out)
	}
}

func TestReloadCommand_ReregistersSkillCommands(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _, _ := newSkillCommandTestBot(t, tg)
	b.authManager = &AuthManager{config: config.AuthConfig{AdminID: 7}}

	writeTestSkill(t, b.personality.BasePath, "stem-separation", "description: Split stems\n")

	ctx := dmContext(100, "/reload")
	if err := b.handleReloadCommand(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "✅ Reloaded: bootstrap") {
		t.Fatalf("unexpected reply %#v", ctx.sent)
	}
	if _, ok := b.lookupSkillCommand("stem_separation"); !ok {
		t.Fatalf("stem_separation not registered after /reload: %+v", b.skillCommandList())
	}
	if !strings.Contains(ctx.sent[0], "38 commands registered") {
		t.Fatalf("expected 36 built-in + 2 skill commands in reply, got %q", ctx.sent[0])
	}

	nonAdmin := dmContext(101, "/reload")
	nonAdmin.msg.Sender.ID = 8
	if err := b.handleReloadCommand(nonAdmin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(nonAdmin.sent, ""), "administrators") {
		t.Fatalf("non-admin should be refused, got %#v", nonAdmin.sent)
	}
}

func TestSkillCommandPrompt(t *testing.T) {
	cmd := bootstrap.SkillCommand{Command: "media_request", SkillName: "media-request", SkillPath: "/soul/skills/media-request/SKILL.md"}
	got := skillCommandPrompt(cmd, "  Dune Part Two 2024 ")
	for _, want := range []string{"/media_request", "skill `media-request`", "/soul/skills/media-request/SKILL.md", "`{baseDir}` with /soul/skills/media-request", "User request:\nDune Part Two 2024"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}
