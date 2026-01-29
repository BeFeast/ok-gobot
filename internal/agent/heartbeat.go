package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sessionpkg "ok-gobot/internal/session"
)

// HeartbeatChecker is a function that performs a specific check
type HeartbeatChecker func(ctx context.Context) (CheckResult, error)

// Heartbeat performs periodic proactive checks
type Heartbeat struct {
	BasePath string
	State    *sessionpkg.HeartbeatState
	checkers map[string]HeartbeatChecker
	imapCfg  *IMAPConfig
}

// NewHeartbeat creates a new heartbeat manager
func NewHeartbeat(basePath string) (*Heartbeat, error) {
	state, err := sessionpkg.LoadHeartbeatState(basePath)
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

// IMAPConfig holds IMAP connection settings
type IMAPConfig struct {
	Server   string
	Port     int
	Username string
	Password string
	UseTLS   bool
}

// ConfigureIMAP sets up IMAP email checking
func (h *Heartbeat) ConfigureIMAP(cfg *IMAPConfig) {
	h.imapCfg = cfg
}

// RegisterChecker adds a custom heartbeat checker
func (h *Heartbeat) RegisterChecker(name string, checker HeartbeatChecker) {
	if h.checkers == nil {
		h.checkers = make(map[string]HeartbeatChecker)
	}
	h.checkers[name] = checker
}

// checkEmailsIMAP checks emails via IMAP (alternative to script)
func (h *Heartbeat) checkEmailsIMAP(ctx context.Context) ([]EmailInfo, error) {
	if h.imapCfg == nil {
		return nil, fmt.Errorf("IMAP not configured")
	}

	// Use net.JoinHostPort for proper IPv6 support
	addr := net.JoinHostPort(h.imapCfg.Server, fmt.Sprintf("%d", h.imapCfg.Port))

	var conn net.Conn
	var err error

	if h.imapCfg.UseTLS {
		conn, err = tls.Dial("tcp", addr, &tls.Config{
			ServerName: h.imapCfg.Server,
		})
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Simple IMAP implementation
	// In production, use a proper IMAP library like go-imap
	buf := make([]byte, 1024)
	conn.Read(buf) // Read greeting

	// Login
	fmt.Fprintf(conn, "a001 LOGIN %s %s\r\n", h.imapCfg.Username, h.imapCfg.Password)
	conn.Read(buf)

	// Select INBOX
	fmt.Fprintf(conn, "a002 SELECT INBOX\r\n")
	conn.Read(buf)

	// Search for unseen messages
	fmt.Fprintf(conn, "a003 SEARCH UNSEEN\r\n")
	n, _ := conn.Read(buf)
	response := string(buf[:n])

	// Logout
	fmt.Fprintf(conn, "a004 LOGOUT\r\n")

	// Parse unseen count (very simplified)
	var emails []EmailInfo
	if strings.Contains(response, "SEARCH") {
		parts := strings.Split(response, "\r\n")
		for _, part := range parts {
			if strings.HasPrefix(part, "* SEARCH") {
				ids := strings.Fields(strings.TrimPrefix(part, "* SEARCH"))
				for range ids {
					emails = append(emails, EmailInfo{
						From:    "Unknown",
						Subject: "New email",
					})
				}
			}
		}
	}

	return emails, nil
}

// RunCustomCheckers executes all registered custom checkers
func (h *Heartbeat) RunCustomCheckers(ctx context.Context, result *HeartbeatResult) {
	for name, checker := range h.checkers {
		checkResult, err := checker(ctx)
		if err != nil {
			result.Checks[name] = CheckResult{
				Status:  "error",
				Message: err.Error(),
			}
		} else {
			result.Checks[name] = checkResult
		}
	}
}
