package browser

import (
	"errors"
	"strings"
	"testing"
)

func twoProfiles(t *testing.T) *AccountProfiles {
	t.Helper()
	set, err := NewAccountProfiles([]AccountProfile{
		{Name: "work", Account: "me@example.com", DebugURL: "http://cdp.example:9224"},
		{Name: "personal", Account: "Me.Personal@personal.example", DebugURL: "http://cdp.example:9221"},
	}, "work")
	if err != nil {
		t.Fatalf("NewAccountProfiles: %v", err)
	}
	return set
}

func TestAccountProfilesResolveByNameEmailAndDefault(t *testing.T) {
	set := twoProfiles(t)

	def, err := set.Resolve("")
	if err != nil || def.Name != "work" {
		t.Fatalf("Resolve(\"\") = %+v, %v; want work", def, err)
	}
	if !set.IsDefault("work") || set.IsDefault("personal") {
		t.Fatal("IsDefault does not follow default_profile")
	}
	for _, key := range []string{"personal", "PERSONAL", " me.personal@personal.example ", "ME.PERSONAL@PERSONAL.EXAMPLE"} {
		p, err := set.Resolve(key)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", key, err)
		}
		if p.Name != "personal" || p.DebugURL != "http://cdp.example:9221" {
			t.Fatalf("Resolve(%q) = %+v, want personal profile", key, p)
		}
	}
	all := set.All()
	if len(all) != 2 || all[0].Name != "personal" || all[1].Name != "work" {
		t.Fatalf("All() = %+v, want sorted by name", all)
	}
	if !set.UsesRemoteCDP() {
		t.Fatal("UsesRemoteCDP() = false for remote profiles")
	}
}

func TestAccountProfilesResolveUnknownListsKnownProfiles(t *testing.T) {
	set := twoProfiles(t)
	_, err := set.Resolve("nobody@example.com")
	var unknown *UnknownAccountError
	if !errors.As(err, &unknown) {
		t.Fatalf("Resolve(unknown) error = %v, want *UnknownAccountError", err)
	}
	msg := err.Error()
	for _, want := range []string{`"nobody@example.com"`, "personal (Me.Personal@personal.example)", "work (me@example.com)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestAccountProfilesValidation(t *testing.T) {
	good := AccountProfile{Name: "a", Account: "a@example.com", DebugURL: "http://cdp.example:1"}
	tests := []struct {
		name     string
		profiles []AccountProfile
		def      string
		wantErr  string
	}{
		{name: "empty set", def: "a", wantErr: "at least one profile"},
		{name: "missing default", profiles: []AccountProfile{good}, def: "", wantErr: "default_profile is required"},
		{name: "unknown default", profiles: []AccountProfile{good}, def: "zzz", wantErr: `default_profile "zzz" does not name`},
		{name: "default by account is rejected", profiles: []AccountProfile{good}, def: "a@example.com", wantErr: "does not name a configured profile"},
		{name: "empty debug_url", profiles: []AccountProfile{{Name: "a", Account: "a@example.com"}}, def: "a", wantErr: "debug_url must not be empty"},
		{name: "relative debug_url", profiles: []AccountProfile{{Name: "a", Account: "a@example.com", DebugURL: "cdp.example:1"}}, def: "a", wantErr: "absolute http(s) URL"},
		{name: "empty account", profiles: []AccountProfile{{Name: "a", DebugURL: "http://cdp.example:1"}}, def: "a", wantErr: "account must not be empty"},
		{name: "empty name", profiles: []AccountProfile{{Account: "a@example.com", DebugURL: "http://cdp.example:1"}}, def: "a", wantErr: "name must not be empty"},
		{name: "duplicate account case-insensitive", profiles: []AccountProfile{good, {Name: "b", Account: "A@Example.com", DebugURL: "http://cdp.example:2"}}, def: "a", wantErr: `account "A@Example.com" is already used by profile "a"`},
		{name: "duplicate name case-insensitive", profiles: []AccountProfile{good, {Name: "A", Account: "b@example.com", DebugURL: "http://cdp.example:2"}}, def: "a", wantErr: `name "A" is already used`},
		{name: "account collides with name", profiles: []AccountProfile{good, {Name: "b", Account: "a", DebugURL: "http://cdp.example:2"}}, def: "a", wantErr: `account "a" is already used by profile "a"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAccountProfiles(tt.profiles, tt.def)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLegacyAndNilAccountProfiles(t *testing.T) {
	legacy := LegacyAccountProfiles(" http://cdp.example:9222 ")
	p, err := legacy.Resolve("")
	if err != nil || p.Name != LegacyAccountProfileName || p.DebugURL != "http://cdp.example:9222" {
		t.Fatalf("legacy Resolve(\"\") = %+v, %v", p, err)
	}
	if p, err := legacy.Resolve("DEFAULT"); err != nil || p.Name != LegacyAccountProfileName {
		t.Fatalf("legacy Resolve(name) = %+v, %v", p, err)
	}
	if _, err := legacy.Resolve("someone@example.com"); err == nil {
		t.Fatal("legacy profile resolved an unknown account")
	}

	var nilSet *AccountProfiles
	p, err = nilSet.Resolve("")
	if err != nil || p.Name != LegacyAccountProfileName || p.DebugURL != "" {
		t.Fatalf("nil Resolve(\"\") = %+v, %v", p, err)
	}
	if nilSet.UsesRemoteCDP() || LegacyAccountProfiles("").UsesRemoteCDP() {
		t.Fatal("local profiles report remote CDP")
	}
	if len(nilSet.All()) != 1 {
		t.Fatalf("nil All() = %+v", nilSet.All())
	}
}
