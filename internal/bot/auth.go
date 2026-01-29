package bot

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

// AuthManager handles user authorization
type AuthManager struct {
	store        *storage.Store
	config       config.AuthConfig
	pairingCodes map[string]*PairingCode
	mu           sync.RWMutex
}

// PairingCode represents a temporary pairing code
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// NewAuthManager creates a new authorization manager
func NewAuthManager(store *storage.Store, cfg config.AuthConfig) *AuthManager {
	am := &AuthManager{
		store:        store,
		config:       cfg,
		pairingCodes: make(map[string]*PairingCode),
	}

	// Start cleanup goroutine for expired codes
	go am.cleanupExpiredCodes()

	return am
}

// CheckAccess checks if a user has access to the bot
func (am *AuthManager) CheckAccess(userID int64, chatID int64) bool {
	switch am.config.Mode {
	case "open":
		return true

	case "allowlist":
		// Check config allowed users
		for _, allowedID := range am.config.AllowedUsers {
			if allowedID == userID {
				return true
			}
		}
		// Check DB authorized users
		return am.store.IsUserAuthorized(userID)

	case "pairing":
		// Only check DB authorized users
		return am.store.IsUserAuthorized(userID)

	default:
		// Default to open mode
		return true
	}
}

// IsAdmin checks if a user is the admin
func (am *AuthManager) IsAdmin(userID int64) bool {
	return am.config.AdminID != 0 && am.config.AdminID == userID
}

// GeneratePairingCode generates a new 6-digit pairing code
func (am *AuthManager) GeneratePairingCode() string {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Generate random 6-digit code
	code := am.generateRandomCode()

	// Store with 5 minute expiry
	am.pairingCodes[code] = &PairingCode{
		Code:      code,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	return code
}

// ValidatePairingCode validates a pairing code and authorizes the user
func (am *AuthManager) ValidatePairingCode(code string, userID int64, username string) bool {
	am.mu.Lock()
	defer am.mu.Unlock()

	pairingCode, exists := am.pairingCodes[code]
	if !exists {
		return false
	}

	// Check if code is expired
	if time.Now().After(pairingCode.ExpiresAt) {
		delete(am.pairingCodes, code)
		return false
	}

	// Authorize user
	if err := am.store.AuthorizeUser(userID, username, "pairing_code"); err != nil {
		return false
	}

	// Remove used code
	delete(am.pairingCodes, code)

	return true
}

// AuthorizeUser manually authorizes a user (admin action)
func (am *AuthManager) AuthorizeUser(userID int64, username string) error {
	return am.store.AuthorizeUser(userID, username, "admin")
}

// DeauthorizeUser removes authorization from a user
func (am *AuthManager) DeauthorizeUser(userID int64) error {
	return am.store.DeauthorizeUser(userID)
}

// ListAuthorizedUsers returns all authorized users
func (am *AuthManager) ListAuthorizedUsers() ([]storage.AuthorizedUser, error) {
	return am.store.ListAuthorizedUsers()
}

// generateRandomCode generates a random 6-digit code
func (am *AuthManager) generateRandomCode() string {
	// Generate a random number between 100000 and 999999
	max := big.NewInt(900000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		// Fallback to timestamp-based code
		return fmt.Sprintf("%06d", time.Now().Unix()%1000000)
	}
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

// cleanupExpiredCodes periodically removes expired pairing codes
func (am *AuthManager) cleanupExpiredCodes() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		am.mu.Lock()
		now := time.Now()
		for code, pairingCode := range am.pairingCodes {
			if now.After(pairingCode.ExpiresAt) {
				delete(am.pairingCodes, code)
			}
		}
		am.mu.Unlock()
	}
}
