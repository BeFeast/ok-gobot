package redact_test

import (
	"fmt"
	"ok-gobot/internal/redact"
)

// Example demonstrates basic usage of the Redact function
func Example() {
	// Example log message with sensitive data
	logMessage := "Connecting to API with key sk-test123456789012345678901234567890"

	// Redact sensitive information
	safe := redact.Redact(logMessage)

	fmt.Println(safe)
	// Output: Connecting to API with key sk-test12***
}

// Example_multipleSecrets shows redacting multiple secrets in one string
func Example_multipleSecrets() {
	logMessage := "Auth: sk-abc123def456789012 Bot: 123456789:ABCdefGHIjklMNOpqrsTUVwxyz"

	safe := redact.Redact(logMessage)

	fmt.Println(safe)
	// Output: Auth: sk-abc123*** Bot: 123456***
}

// Example_bearerToken shows redacting Bearer tokens
func Example_bearerToken() {
	logMessage := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"

	safe := redact.Redact(logMessage)

	fmt.Println(safe)
	// Output: Authorization: Bearer ***
}
