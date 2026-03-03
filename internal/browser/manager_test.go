package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func skipIfNoChrome(t *testing.T) {
	t.Helper()
	m := NewManager("")
	if !m.IsChromeInstalled() {
		t.Skip("Chrome not installed, skipping integration test")
	}
}

func TestNewManager_Defaults(t *testing.T) {
	m := NewManager("")

	homeDir, _ := os.UserHomeDir()
	expectedProfile := filepath.Join(homeDir, ".ok-gobot", "chrome-profile")
	expectedScreenshots := filepath.Join(homeDir, ".ok-gobot", "screenshots")

	if m.ProfilePath != expectedProfile {
		t.Errorf("ProfilePath = %q, want %q", m.ProfilePath, expectedProfile)
	}
	if m.ScreenshotDir != expectedScreenshots {
		t.Errorf("ScreenshotDir = %q, want %q", m.ScreenshotDir, expectedScreenshots)
	}
	if m.Headless != false {
		t.Error("Headless should default to false")
	}
	if m.IsRunning() {
		t.Error("should not be running before Start()")
	}
	if m.TabCount() != 0 {
		t.Errorf("TabCount() = %d, want 0", m.TabCount())
	}
}

func TestNewManager_CustomPath(t *testing.T) {
	m := NewManager("/tmp/custom-profile")
	if m.ProfilePath != "/tmp/custom-profile" {
		t.Errorf("ProfilePath = %q, want /tmp/custom-profile", m.ProfilePath)
	}
}

func TestActiveTabCtx_NotStarted(t *testing.T) {
	m := NewManager("")
	_, err := m.ActiveTabCtx()
	if err == nil {
		t.Error("ActiveTabCtx should fail when browser not started")
	}
}

func TestActiveTabID_NotStarted(t *testing.T) {
	m := NewManager("")
	if id := m.ActiveTabID(); id != "" {
		t.Errorf("ActiveTabID = %q, want empty", id)
	}
}

func TestFocusTab_NotFound(t *testing.T) {
	m := NewManager("")
	if err := m.FocusTab("nonexistent"); err == nil {
		t.Error("FocusTab should fail for nonexistent tab")
	}
}

func TestCloseTab_NotFound(t *testing.T) {
	m := NewManager("")
	if err := m.CloseTab("nonexistent"); err == nil {
		t.Error("CloseTab should fail for nonexistent tab")
	}
}

func TestOpenTab_NotStarted(t *testing.T) {
	m := NewManager("")
	_, err := m.OpenTab()
	if err == nil {
		t.Error("OpenTab should fail when browser not started")
	}
}

func TestListTabs_Empty(t *testing.T) {
	m := NewManager("")
	tabs := m.ListTabs()
	if len(tabs) != 0 {
		t.Errorf("ListTabs = %d tabs, want 0", len(tabs))
	}
}

func TestNavigate_NotStarted(t *testing.T) {
	m := NewManager("")
	if err := m.Navigate("https://example.com"); err == nil {
		t.Error("Navigate should fail when browser not started")
	}
}

func TestScreenshot_NotStarted(t *testing.T) {
	m := NewManager("")
	_, err := m.Screenshot()
	if err == nil {
		t.Error("Screenshot should fail when browser not started")
	}
}

func TestClick_NotStarted(t *testing.T) {
	m := NewManager("")
	if err := m.Click("body"); err == nil {
		t.Error("Click should fail when browser not started")
	}
}

func TestStopIdempotent(t *testing.T) {
	m := NewManager("")
	m.Stop() // should not panic
	m.Stop() // should not panic
}

// --- Integration tests (require Chrome) ---

func TestStartStop(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "profile"))
	m.ScreenshotDir = filepath.Join(tmpDir, "screenshots")
	m.Headless = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	if !m.IsRunning() {
		t.Error("should be running after Start()")
	}
	if m.TabCount() != 1 {
		t.Errorf("TabCount() = %d, want 1 after Start()", m.TabCount())
	}
	if m.ActiveTabID() == "" {
		t.Error("ActiveTabID should not be empty after Start()")
	}
}

func TestStartIdempotent(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "profile"))
	m.Headless = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	// Second Start should be a no-op.
	if err := m.Start(); err != nil {
		t.Fatalf("second Start failed: %v", err)
	}
	if m.TabCount() != 1 {
		t.Errorf("TabCount() = %d, want 1 after double Start()", m.TabCount())
	}
}

func TestNavigateAndScreenshot(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "profile"))
	m.ScreenshotDir = filepath.Join(tmpDir, "screenshots")
	m.Headless = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	if err := m.Navigate("https://example.com"); err != nil {
		t.Fatalf("Navigate failed: %v", err)
	}

	path, err := m.Screenshot()
	if err != nil {
		t.Fatalf("Screenshot failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("screenshot file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("screenshot file is empty")
	}
}

func TestMultiTab(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "profile"))
	m.Headless = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	firstTab := m.ActiveTabID()

	// Open second tab.
	secondTab, err := m.OpenTab()
	if err != nil {
		t.Fatalf("OpenTab failed: %v", err)
	}
	if m.TabCount() != 2 {
		t.Errorf("TabCount() = %d, want 2", m.TabCount())
	}
	if m.ActiveTabID() != secondTab {
		t.Errorf("ActiveTabID = %q, want %q (newly opened)", m.ActiveTabID(), secondTab)
	}

	// Focus back to first tab.
	if err := m.FocusTab(firstTab); err != nil {
		t.Fatalf("FocusTab failed: %v", err)
	}
	if m.ActiveTabID() != firstTab {
		t.Errorf("ActiveTabID = %q, want %q after focus", m.ActiveTabID(), firstTab)
	}

	// Close second tab.
	if err := m.CloseTab(secondTab); err != nil {
		t.Fatalf("CloseTab failed: %v", err)
	}
	if m.TabCount() != 1 {
		t.Errorf("TabCount() = %d, want 1 after close", m.TabCount())
	}

	// Cannot close last tab.
	if err := m.CloseTab(firstTab); err == nil {
		t.Error("CloseTab should fail for last tab")
	}
}

func TestListTabs_WithContent(t *testing.T) {
	skipIfNoChrome(t)

	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "profile"))
	m.Headless = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	if err := m.Navigate("https://example.com"); err != nil {
		t.Fatalf("Navigate failed: %v", err)
	}

	tabs := m.ListTabs()
	if len(tabs) != 1 {
		t.Fatalf("ListTabs = %d tabs, want 1", len(tabs))
	}

	tab := tabs[0]
	if tab.URL == "" || tab.URL == "about:blank" {
		t.Errorf("tab URL = %q, want real URL", tab.URL)
	}
	if !tab.Active {
		t.Error("single tab should be active")
	}
}
