package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"moltbot/internal/session"
)

// Heartbeat performs periodic proactive checks
type Heartbeat struct {
	BasePath string
	State    *session.HeartbeatState
}

// NewHeartbeat creates a new heartbeat manager
func NewHeartbeat(basePath string) (*Heartbeat, error) {
	state, err := session.LoadHeartbeatState(basePath)
	if err != nil {
		return nil, err
	}

	return &Heartbeat{
		BasePath: basePath,
		State:    state,
	}, nil
}

// Check performs all heartbeat checks and returns results
func (h *Heartbeat) Check(ctx context.Context) (*HeartbeatResult, error) {
	result := &HeartbeatResult{
		Timestamp: time.Now(),
		Checks:    make(map[string]CheckResult),
	}

	// Check 1: Context usage
	if result.ContextWarning = h.checkContextUsage(); result.ContextWarning != "" {
		result.Checks["context"] = CheckResult{Status: "warning", Message: result.ContextWarning}
	}

	// Check 2: Gmail (every 30 minutes)
	if h.State.ShouldCheck("email", 30) {
		if emails, err := h.checkEmails(); err == nil && len(emails) > 0 {
			result.Emails = emails
			result.Checks["email"] = CheckResult{Status: "info", Message: fmt.Sprintf("%d new emails", len(emails))}
		}
		h.State.MarkChecked("email")
	}

	// Save state
	h.State.Save(h.BasePath)

	return result, nil
}

// CheckResult represents the result of a single check
type CheckResult struct {
	Status  string // ok, warning, info, error
	Message string
}

// HeartbeatResult aggregates all check results
type HeartbeatResult struct {
	Timestamp      time.Time
	Checks         map[string]CheckResult
	ContextWarning string
	Emails         []EmailInfo
}

// ShouldNotify returns true if there's something worth notifying about
func (r *HeartbeatResult) ShouldNotify() bool {
	if r.ContextWarning != "" {
		return true
	}
	if len(r.Emails) > 0 {
		return true
	}
	for _, check := range r.Checks {
		if check.Status == "warning" || check.Status == "error" {
			return true
		}
	}
	return false
}

// FormatNotification formats the heartbeat result for sending
func (r *HeartbeatResult) FormatNotification() string {
	var parts []string

	if r.ContextWarning != "" {
		parts = append(parts, r.ContextWarning)
	}

	if len(r.Emails) > 0 {
		parts = append(parts, fmt.Sprintf("📬 %d new important emails", len(r.Emails)))
		for _, email := range r.Emails[:min(3, len(r.Emails))] {
			parts = append(parts, fmt.Sprintf("  - %s: %s", email.From, email.Subject))
		}
		if len(r.Emails) > 3 {
			parts = append(parts, fmt.Sprintf("  ... and %d more", len(r.Emails)-3))
		}
	}

	if len(parts) == 0 {
		return "HEARTBEAT_OK"
	}

	return strings.Join(parts, "\n")
}

// checkContextUsage checks if context is getting full
func (h *Heartbeat) checkContextUsage() string {
	// This would integrate with actual context monitoring
	// For now, return empty (no warning)
	return ""
}

// EmailInfo represents email information
type EmailInfo struct {
	From    string
	Subject string
	Date    time.Time
}

// checkEmails checks for new important emails
func (h *Heartbeat) checkEmails() ([]EmailInfo, error) {
	// Check if gmail script exists
	scriptPath := filepath.Join(h.BasePath, "scripts", "gmail.py")
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		// Try alternative path
		scriptPath = filepath.Join(h.BasePath, "scripts", "gmail-check.sh")
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("gmail script not found")
		}
	}

	// Run script
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", scriptPath, "check", "kossoy")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gmail check failed: %w", err)
	}

	// Parse output (simplified)
	return parseEmailOutput(string(output)), nil
}

// parseEmailOutput parses gmail script output
func parseEmailOutput(output string) []EmailInfo {
	var emails []EmailInfo
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		// Look for lines with email format
		if strings.Contains(line, "From:") && strings.Contains(line, "Subject:") {
			// Simple parsing - in practice, use structured output
			emails = append(emails, EmailInfo{
				From:    extractEmailField(line, "From:"),
				Subject: extractEmailField(line, "Subject:"),
			})
		}
	}

	return emails
}

func extractEmailField(line, field string) string {
	parts := strings.Split(line, field)
	if len(parts) > 1 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
