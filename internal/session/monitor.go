package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Monitor tracks token usage and context window
type Monitor struct {
	MaxTokens       int
	WarningPercent  float64
	CriticalPercent float64

	// Current session stats
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// NewMonitor creates a new context monitor
func NewMonitor() *Monitor {
	return &Monitor{
		MaxTokens:       200000, // Default to 200k context window
		WarningPercent:  0.70,   // Warn at 70%
		CriticalPercent: 0.85,   // Critical at 85%
		InputTokens:     0,
		OutputTokens:    0,
		TotalTokens:     0,
	}
}

// AddTokens adds tokens to the counter
func (m *Monitor) AddTokens(input, output int) {
	m.InputTokens += input
	m.OutputTokens += output
	m.TotalTokens = m.InputTokens + m.OutputTokens
}

// GetUsagePercent returns current usage percentage
func (m *Monitor) GetUsagePercent() float64 {
	return float64(m.TotalTokens) / float64(m.MaxTokens)
}

// GetStatus returns the current status and any warnings
func (m *Monitor) GetStatus() (status string, warning string) {
	percent := m.GetUsagePercent()

	if percent >= m.CriticalPercent {
		return "critical", fmt.Sprintf("🚨 Контекст критически заполнен (%.0f%%)! Сделай `/compact` сейчас или потеряем историю", percent*100)
	} else if percent >= m.WarningPercent {
		return "warning", fmt.Sprintf("⚠️ Контекст заполнен на %.0f%%. Рекомендую `/compact` или `/new`", percent*100)
	}

	return "ok", ""
}

// Reset resets the token counters
func (m *Monitor) Reset() {
	m.InputTokens = 0
	m.OutputTokens = 0
	m.TotalTokens = 0
}

// EstimateTokens roughly estimates tokens from text
// This is a rough approximation (1 token ≈ 4 chars for English)
func EstimateTokens(text string) int {
	// Simple estimation: ~4 characters per token for English
	// Russian/CJK use more tokens per character
	return len(text) / 4
}

// HeartbeatState tracks the last time various checks were performed
type HeartbeatState struct {
	LastChecks map[string]int64 `json:"lastChecks"`
}

// LoadHeartbeatState loads the heartbeat state from disk
func LoadHeartbeatState(basePath string) (*HeartbeatState, error) {
	if basePath == "" {
		homeDir, _ := os.UserHomeDir()
		basePath = filepath.Join(homeDir, "ok-gobot-soul")
	}

	path := filepath.Join(basePath, "memory", "heartbeat-state.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &HeartbeatState{
				LastChecks: make(map[string]int64),
			}, nil
		}
		return nil, err
	}

	var state HeartbeatState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	if state.LastChecks == nil {
		state.LastChecks = make(map[string]int64)
	}

	return &state, nil
}

// Save persists the heartbeat state
func (h *HeartbeatState) Save(basePath string) error {
	if basePath == "" {
		homeDir, _ := os.UserHomeDir()
		basePath = filepath.Join(homeDir, "ok-gobot-soul")
	}

	memoryDir := filepath.Join(basePath, "memory")
	os.MkdirAll(memoryDir, 0755)

	path := filepath.Join(memoryDir, "heartbeat-state.json")

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// ShouldCheck returns true if enough time has passed since last check
func (h *HeartbeatState) ShouldCheck(checkType string, intervalMinutes int) bool {
	lastCheck, exists := h.LastChecks[checkType]
	if !exists {
		return true
	}

	// Check if interval has passed
	timeSinceLastCheck := int64(intervalMinutes * 60)
	return (now() - lastCheck) >= timeSinceLastCheck
}

// MarkChecked updates the last check time
func (h *HeartbeatState) MarkChecked(checkType string) {
	h.LastChecks[checkType] = now()
}

// now returns current Unix timestamp
func now() int64 {
	return time.Now().Unix()
}
