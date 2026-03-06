package bot

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

// AuthManager handles user authorization
type AuthManager struct {
	store           *storage.Store
	config          config.AuthConfig
	pairingCodes    map[string]*PairingCode
	pairingAttempts map[int64]*pairingAttempt
	mu              sync.RWMutex
}

// PairingCode represents a temporary pairing code
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// pairingAttempt tracks brute-force attempts per user
type pairingAttempt struct {
	Count    int
	LockedAt time.Time
}

const (
	maxPairingAttempts = 5
	pairingLockout     = 15 * time.Minute
)

// NewAuthManager creates a new authorization manager
func NewAuthManager(store *storage.Store, cfg config.AuthConfig) *AuthManager {
	am := &AuthManager{
		store:           store,
		config:          cfg,
		pairingCodes:    make(map[string]*PairingCode),
		pairingAttempts: make(map[int64]*pairingAttempt),
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
		log.Printf("[auth] unknown auth mode %q — denying access (fail-closed)", am.config.Mode)
		return false
	}
}

// CheckDirectMessageAccess checks DM access by sender ID only.
func (am *AuthManager) CheckDirectMessageAccess(userID int64) bool {
	return am.CheckAccess(userID, userID)
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

// ValidatePairingCode validates a pairing code and authorizes the user.
// Returns false and enforces lockout after maxPairingAttempts failed attempts.
func (am *AuthManager) ValidatePairingCode(code string, userID int64, username string) bool {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Check lockout
	if attempt, ok := am.pairingAttempts[userID]; ok {
		if attempt.Count >= maxPairingAttempts && time.Since(attempt.LockedAt) < pairingLockout {
			log.Printf("[auth] pairing attempt rejected for user %d — locked out until %s",
				userID, attempt.LockedAt.Add(pairingLockout).Format(time.RFC3339))
			return false
		}
		if time.Since(attempt.LockedAt) >= pairingLockout {
			delete(am.pairingAttempts, userID)
		}
	}

	pairingCode, exists := am.pairingCodes[code]
	if !exists {
		am.recordFailedAttempt(userID)
		return false
	}

	if time.Now().After(pairingCode.ExpiresAt) {
		delete(am.pairingCodes, code)
		am.recordFailedAttempt(userID)
		return false
	}

	if err := am.store.AuthorizeUser(userID, username, "pairing_code"); err != nil {
		return false
	}

	delete(am.pairingCodes, code)
	delete(am.pairingAttempts, userID) // reset on success
	return true
}

// recordFailedAttempt increments the failed attempt counter for a user.
// Must be called with am.mu held.
func (am *AuthManager) recordFailedAttempt(userID int64) {
	attempt, ok := am.pairingAttempts[userID]
	if !ok {
		attempt = &pairingAttempt{}
		am.pairingAttempts[userID] = attempt
	}
	attempt.Count++
	if attempt.Count >= maxPairingAttempts {
		attempt.LockedAt = time.Now()
		log.Printf("[auth] user %d locked out from pairing after %d failed attempts", userID, attempt.Count)
	}
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
