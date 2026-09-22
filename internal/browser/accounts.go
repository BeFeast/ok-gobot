package browser

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// LegacyAccountProfileName is the profile synthesized from the legacy
// browser.debug_url setting when browser.profiles is empty.
const LegacyAccountProfileName = "default"

// AccountProfile is one named remote CDP endpoint bound to an account.
// Account is the email (or alias) the model passes to select the profile;
// DebugURL is the remote debugging base URL of the browser that is signed in
// to that account. An empty DebugURL means "launch Chrome locally".
type AccountProfile struct {
	Name     string
	Account  string
	DebugURL string
}

// String renders the profile for log lines and error messages.
func (p AccountProfile) String() string {
	if p.Account == "" {
		return p.Name
	}
	return fmt.Sprintf("%s (%s)", p.Name, p.Account)
}

// AccountProfiles resolves an account (email or profile name) to the browser
// profile that should serve it. The zero value and a nil pointer both behave
// like a single local-Chrome profile so callers never need a nil check.
type AccountProfiles struct {
	ordered     []AccountProfile
	defaultName string
}

// NewAccountProfiles validates and indexes a set of named profiles.
//
// Rules: at least one profile; every profile has a non-empty name, account
// and debug_url; debug_url parses as an absolute http(s) URL; names and
// accounts are unique case-insensitively and no account collides with
// another profile's name (both are resolvable keys); defaultName is required
// and must name one of the profiles.
func NewAccountProfiles(profiles []AccountProfile, defaultName string) (*AccountProfiles, error) {
	if len(profiles) == 0 {
		return nil, errors.New("at least one profile is required")
	}

	ordered := make([]AccountProfile, 0, len(profiles))
	keys := make(map[string]string, len(profiles)*2) // normalized key -> owning profile name
	claim := func(key, owner, what string) error {
		normalized := normalizeAccountKey(key)
		if normalized == "" {
			return fmt.Errorf("profile %q: %s must not be empty", owner, what)
		}
		if prev, taken := keys[normalized]; taken {
			return fmt.Errorf("profile %q: %s %q is already used by profile %q", owner, what, key, prev)
		}
		keys[normalized] = owner
		return nil
	}

	for _, p := range profiles {
		p.Name = strings.TrimSpace(p.Name)
		p.Account = strings.TrimSpace(p.Account)
		p.DebugURL = strings.TrimSpace(p.DebugURL)
		if p.Name == "" {
			return nil, errors.New("profile name must not be empty")
		}
		if err := claim(p.Name, p.Name, "name"); err != nil {
			return nil, err
		}
		if err := claim(p.Account, p.Name, "account"); err != nil {
			return nil, err
		}
		if p.DebugURL == "" {
			return nil, fmt.Errorf("profile %q: debug_url must not be empty", p.Name)
		}
		parsed, err := url.Parse(p.DebugURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("profile %q: debug_url %q must be an absolute http(s) URL", p.Name, p.DebugURL)
		}
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	defaultName = strings.TrimSpace(defaultName)
	if defaultName == "" {
		return nil, errors.New("default_profile is required when profiles are configured")
	}
	set := &AccountProfiles{ordered: ordered}
	def, ok := set.lookup(defaultName)
	if !ok || !strings.EqualFold(def.Name, defaultName) {
		return nil, fmt.Errorf("default_profile %q does not name a configured profile (known: %s)", defaultName, set.knownList())
	}
	set.defaultName = def.Name
	return set, nil
}

// LegacyAccountProfiles wraps the legacy single browser.debug_url setting as
// one profile named "default" that is also the default. An empty URL yields
// a profile that launches Chrome locally.
func LegacyAccountProfiles(debugURL string) *AccountProfiles {
	return &AccountProfiles{
		ordered:     []AccountProfile{{Name: LegacyAccountProfileName, DebugURL: strings.TrimSpace(debugURL)}},
		defaultName: LegacyAccountProfileName,
	}
}

// Default returns the profile used when no account is requested.
func (s *AccountProfiles) Default() AccountProfile {
	if s == nil || len(s.ordered) == 0 {
		return AccountProfile{Name: LegacyAccountProfileName}
	}
	for _, p := range s.ordered {
		if p.Name == s.defaultName {
			return p
		}
	}
	return s.ordered[0]
}

// All returns every profile sorted by name.
func (s *AccountProfiles) All() []AccountProfile {
	if s == nil || len(s.ordered) == 0 {
		return []AccountProfile{{Name: LegacyAccountProfileName}}
	}
	return append([]AccountProfile(nil), s.ordered...)
}

// IsDefault reports whether name is the default profile.
func (s *AccountProfiles) IsDefault(name string) bool {
	return s.Default().Name == name
}

// UsesRemoteCDP reports whether any profile points at a remote endpoint.
func (s *AccountProfiles) UsesRemoteCDP() bool {
	for _, p := range s.All() {
		if p.DebugURL != "" {
			return true
		}
	}
	return false
}

// Resolve maps a requested account to a profile. An empty account selects
// the default. Matching is case-insensitive on both profile name and
// account. Unknown accounts are an error that lists the known profiles; the
// request is never silently redirected to the default.
func (s *AccountProfiles) Resolve(account string) (AccountProfile, error) {
	if strings.TrimSpace(account) == "" {
		return s.Default(), nil
	}
	if p, ok := s.lookup(account); ok {
		return p, nil
	}
	return AccountProfile{}, &UnknownAccountError{Account: strings.TrimSpace(account), Known: s.All()}
}

func (s *AccountProfiles) lookup(key string) (AccountProfile, bool) {
	normalized := normalizeAccountKey(key)
	if normalized == "" {
		return AccountProfile{}, false
	}
	for _, p := range s.All() {
		if normalizeAccountKey(p.Name) == normalized || (p.Account != "" && normalizeAccountKey(p.Account) == normalized) {
			return p, true
		}
	}
	return AccountProfile{}, false
}

func (s *AccountProfiles) knownList() string {
	names := make([]string, 0, len(s.All()))
	for _, p := range s.All() {
		names = append(names, p.String())
	}
	return strings.Join(names, ", ")
}

// UnknownAccountError reports an account that matches no configured profile.
type UnknownAccountError struct {
	Account string
	Known   []AccountProfile
}

func (e *UnknownAccountError) Error() string {
	known := make([]string, 0, len(e.Known))
	for _, p := range e.Known {
		known = append(known, p.String())
	}
	return fmt.Sprintf("unknown browser account %q; known profiles: %s", e.Account, strings.Join(known, ", "))
}

func normalizeAccountKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}
