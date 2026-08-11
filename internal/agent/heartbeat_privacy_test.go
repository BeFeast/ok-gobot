package agent

import "testing"

func TestGmailAccountUsesGenericDefault(t *testing.T) {
	t.Setenv(gmailAccountEnv, "")

	if got := gmailAccount(); got != defaultGmailAccount {
		t.Fatalf("gmailAccount() = %q, want %q", got, defaultGmailAccount)
	}
	if defaultGmailAccount != "default" {
		t.Fatalf("default gmail account = %q, want generic default", defaultGmailAccount)
	}
}

func TestGmailAccountUsesConfiguredEnvironmentValue(t *testing.T) {
	t.Setenv(gmailAccountEnv, "  team-inbox  ")

	if got := gmailAccount(); got != "team-inbox" {
		t.Fatalf("gmailAccount() = %q, want configured account", got)
	}
}
