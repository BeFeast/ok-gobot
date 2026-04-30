package memory

import "testing"

func TestRecallPolicyPrivateChatIsolation(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{
		UserID:     101,
		ChatID:     101,
		SessionKey: "dm:101",
		ChatType:   "private",
	})

	tests := []struct {
		source string
		want   bool
	}{
		{source: "memory/users/101/2026-04-30.md", want: true},
		{source: "memory/chats/101/2026-04-30.md", want: true},
		{source: "memory/sessions/dm_101/notes.md", want: true},
		{source: "memory/users/202/2026-04-30.md", want: false},
		{source: "memory/chats/202/2026-04-30.md", want: false},
		{source: "MEMORY.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := policy.AllowSource(tt.source); got != tt.want {
				t.Fatalf("AllowSource(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestRecallPolicyGroupChatIsOptIn(t *testing.T) {
	standby := NewRecallPolicy(RecallContext{
		ChatID:     -1001,
		SessionKey: "group:-1001",
		ChatType:   "group",
	})
	if standby.AllowSource("memory/chats/-1001/2026-04-30.md") {
		t.Fatal("standby group should not recall chat memory")
	}
	if standby.AllowSource("memory/users/101/2026-04-30.md") {
		t.Fatal("group should not recall private user memory")
	}

	active := NewRecallPolicy(RecallContext{
		ChatID:         -1001,
		SessionKey:     "group:-1001",
		ChatType:       "group",
		AllowGroupChat: true,
	})
	if !active.AllowSource("memory/chats/-1001/2026-04-30.md") {
		t.Fatal("active group should recall its own chat-scoped memory")
	}
	if active.AllowSource("memory/chats/-1002/2026-04-30.md") {
		t.Fatal("group should not recall another chat's memory")
	}
}

func TestRecallPolicyExternalPathsRequireLabel(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{UserID: 101, ChatID: 101, ChatType: "private"})
	decision := policy.Decide("external://team-vault/projects/notes.md")
	if decision.Allowed {
		t.Fatal("external memory should be denied by default")
	}
	if decision.Scope.Kind != ScopeExtraPath || decision.Scope.Label != "team-vault" {
		t.Fatalf("unexpected external scope: %+v", decision.Scope)
	}

	allowed := NewRecallPolicy(RecallContext{
		UserID:          101,
		ChatID:          101,
		ChatType:        "private",
		ExtraPathLabels: []string{"team-vault"},
	})
	if !allowed.AllowSource("external://team-vault/projects/notes.md") {
		t.Fatal("configured external label should be allowed")
	}
	if allowed.AllowSource("external://other-vault/projects/notes.md") {
		t.Fatal("unconfigured external label should remain denied")
	}
}

func TestRecallPolicyLegacyMemoryMarkdownFilesStayPrivate(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{
		UserID:             101,
		ChatID:             101,
		ChatType:           "private",
		AllowGlobalPrivate: true,
	})

	for _, source := range []string{"MEMORY.md", "memory/2026-04-30.md", "memory/notes.md", "memory/projects.md"} {
		t.Run(source, func(t *testing.T) {
			decision := policy.Decide(source)
			if decision.Scope.Kind != ScopeLegacyGlobal {
				t.Fatalf("scope=%s, want %s", decision.Scope.Kind, ScopeLegacyGlobal)
			}
			if !decision.Allowed {
				t.Fatalf("expected %q to be allowed for private legacy memory: %+v", source, decision)
			}
		})
	}

	otherUser := NewRecallPolicy(RecallContext{UserID: 202, ChatID: 202, ChatType: "private"})
	if otherUser.AllowSource("memory/notes.md") {
		t.Fatal("legacy memory should still require explicit private-global allowance")
	}
}

func TestRecallPolicyScopedMemoryPathsAreNotLegacy(t *testing.T) {
	userScoped := ClassifySource("memory/users/101/notes.md")
	if userScoped.Kind != ScopeUser || userScoped.ID != "101" {
		t.Fatalf("unexpected user scope: %+v", userScoped)
	}

	extraScoped := ClassifySource("extra:obsidian/memory/notes.md")
	if extraScoped.Kind != ScopeExtraPath || extraScoped.Label != "obsidian" {
		t.Fatalf("unexpected extra scope: %+v", extraScoped)
	}
}

func TestSanitizeSnippetRedactsBeforePromptUse(t *testing.T) {
	got := SanitizeSnippet("token: Bearer demo\x00")
	if got != "token: Bearer ***" {
		t.Fatalf("SanitizeSnippet() = %q", got)
	}
}
