package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"ok-gobot/internal/supervisor"
)

const supervisorStatusStateKey = "supervisor_status"

// GetSupervisorStatus returns the latest supervisor decision snapshot.
// Missing state is treated as clear so Mission Control can render a stable shape.
func (s *Store) GetSupervisorStatus() (supervisor.Status, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, supervisorStatusStateKey).Scan(&raw)
	if err == sql.ErrNoRows {
		clear := supervisor.Decision{State: supervisor.StateClear, Reason: "supervisor has not run yet"}
		return supervisor.Status{CurrentDecision: &clear}, nil
	}
	if err != nil {
		return supervisor.Status{}, err
	}
	if strings.TrimSpace(raw) == "" {
		clear := supervisor.Decision{State: supervisor.StateClear, Reason: "supervisor has not run yet"}
		return supervisor.Status{CurrentDecision: &clear}, nil
	}

	var status supervisor.Status
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return supervisor.Status{}, fmt.Errorf("decode supervisor status: %w", err)
	}
	return status, nil
}

// SetSupervisorStatus persists the latest supervisor decision and action ledger.
func (s *Store) SetSupervisorStatus(status supervisor.Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode supervisor status: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO app_state (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, supervisorStatusStateKey, string(data))
	return err
}
