package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTesseraConfigRoundtripAndEnv(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	content := `telegram:
  token: synthetic
ai:
  api_key: synthetic
  provider: openrouter
  model: synthetic
tessera:
  enabled: true
  endpoint: '127.0.0.1:24172'
  token_file: '~/.fixture/tessera-token'
  connector_id: fixture
  workspace:
    brain_id: '00000000-0000-4000-8000-000000000134'
    root: /example/brain
    records_dir: records
    managed: true
  instance_id: instance
  account_id: bot
  actor_id: operator
  sender_id: '123'
  routes:
    - chat_id: '123'
      topic_id: null
    - chat_id: '-456'
      topic_id: '7'
  poll_seconds: 0
`
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFrom(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tessera.Validate(); err != nil {
		t.Fatal(err)
	}
	before := c.Tessera.Fingerprint()
	if !filepath.IsAbs(c.Tessera.TokenFile) {
		t.Fatal("path not expanded")
	}
	if err = c.Save(); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadFrom(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Tessera.Validate(); err != nil {
		t.Fatal(err)
	}
	if restored.Tessera.Fingerprint() != before {
		t.Fatalf("config save changed authority: %#v", restored.Tessera)
	}
	t.Setenv("OKGOBOT_TESSERA_ENDPOINT", "127.0.0.1:24500")
	t.Setenv("OKGOBOT_TESSERA_ENABLED", "false")
	restored, err = LoadFrom(file)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Tessera.Enabled || restored.Tessera.Endpoint != "127.0.0.1:24500" {
		t.Fatal("env mapping failed")
	}
}
