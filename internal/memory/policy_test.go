package memory

import (
	"strings"
	"testing"
)

func TestRecallPolicy_PrivateChatOnlyAllowsMatchingUserScope(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{
		UserID:     1001,
		ChatID:     1001,
		ChatType:   "private",
		SessionKey: "dm:1001",
	})

	if decision := policy.DecisionForSource("telegram/users/1001/memory/2026-04-30.md"); !decision.Allowed {
		t.Fatalf("expected matching user source allowed, got %#v", decision)
	}
	if decision := policy.DecisionForSource("telegram/users/2002/memory/2026-04-30.md"); decision.Allowed {
		t.Fatalf("expected other user source denied, got %#v", decision)
	}
	if decision := policy.DecisionForSource("MEMORY.md"); decision.Allowed {
		t.Fatalf("expected legacy global memory denied by default, got %#v", decision)
	}
}

func TestRecallPolicy_GroupRecallDisabledByDefault(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{
		ChatID:     -100,
		ChatType:   "group",
		SessionKey: "group:-100",
	})

	if decision := policy.DecisionForSource("telegram/chats/-100/memory/2026-04-30.md"); decision.Allowed {
		t.Fatalf("expected group chat source denied by default, got %#v", decision)
	}

	optIn := NewRecallPolicy(RecallContext{
		ChatID:           -100,
		ChatType:         "group",
		SessionKey:       "group:-100",
		AllowGroupRecall: true,
	})
	if decision := optIn.DecisionForSource("telegram/chats/-100/memory/2026-04-30.md"); !decision.Allowed {
		t.Fatalf("expected matching group source allowed after opt-in, got %#v", decision)
	}
	if decision := optIn.DecisionForSource("telegram/users/1001/memory/2026-04-30.md"); decision.Allowed {
		t.Fatalf("expected private user source denied in group, got %#v", decision)
	}
}

func TestRecallPolicy_ExternalPathRequiresConfiguredLabel(t *testing.T) {
	policy := NewRecallPolicy(RecallContext{
		UserID:   1001,
		ChatID:   1001,
		ChatType: "private",
		ExtraPaths: []ExtraPathPolicy{
			{Label: "project", Path: "/tmp/project", AllowPrivate: true},
		},
	})

	if decision := policy.DecisionForSource("external/project/notes.md"); !decision.Allowed {
		t.Fatalf("expected configured external label allowed, got %#v", decision)
	}
	if decision := policy.DecisionForSource("external/other/notes.md"); decision.Allowed {
		t.Fatalf("expected unconfigured external label denied, got %#v", decision)
	}
	root, rel, ok := policy.ResolveExternalSource("external/project/notes.md")
	if !ok || root != "/tmp/project" || rel != "notes.md" {
		t.Fatalf("unexpected external source resolution: root=%q rel=%q ok=%v", root, rel, ok)
	}
}

func TestRedactMemorySnippet(t *testing.T) {
	input := "token: 123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabc and Authorization: Bearer sk-testsecret1234567890"
	got := RedactMemorySnippet(input)
	if strings.Contains(got, "ABCDEFGHIJKLMNOPQRSTUVWXYZabc") || strings.Contains(got, "sk-testsecret") {
		t.Fatalf("expected secrets redacted, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}
