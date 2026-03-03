package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardScaffoldsBootstrapAndConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	bootstrapPath := filepath.Join(homeDir, "my-bootstrap")
	cmd := newOnboardCommand()
	cmd.SetArgs([]string{"--path", bootstrapPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, filename := range []string{"IDENTITY.md", "SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "MEMORY.md", "HEARTBEAT.md"} {
		if _, err := os.Stat(filepath.Join(bootstrapPath, filename)); err != nil {
			t.Fatalf("missing bootstrap file %s: %v", filename, err)
		}
	}

	configPath := filepath.Join(homeDir, ".ok-gobot", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", configPath, err)
	}

	if !strings.Contains(string(data), `soul_path: `+`"`+bootstrapPath+`"`) {
		t.Fatalf("config does not reference bootstrap path:\n%s", string(data))
	}
}
