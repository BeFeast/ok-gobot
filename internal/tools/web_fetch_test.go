package tools

import (
	"net"
	"testing"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		expected bool
	}{
		// Public IPs - should return false
		{"Public IPv4", "8.8.8.8", false},
		{"Public IPv4 2", "1.1.1.1", false},
		{"Public IPv4 3", "93.184.216.34", false},

		// Loopback - should return true
		{"Loopback 127.0.0.1", "127.0.0.1", true},
		{"Loopback 127.1.1.1", "127.1.1.1", true},
		{"Loopback IPv6 ::1", "::1", true},

		// Private IPv4 ranges - should return true
		{"Private 10.0.0.1", "10.0.0.1", true},
		{"Private 10.255.255.255", "10.255.255.255", true},
		{"Private 172.16.0.1", "172.16.0.1", true},
		{"Private 172.31.255.255", "172.31.255.255", true},
		{"Private 192.168.0.1", "192.168.0.1", true},
		{"Private 192.168.1.1", "192.168.1.1", true},
		{"Private 192.168.255.255", "192.168.255.255", true},

		// Link-local - should return true
		{"Link-local 169.254.1.1", "169.254.1.1", true},
		{"Link-local IPv6 fe80::1", "fe80::1", true},

		// 0.0.0.0/8 - should return true
		{"Zero address 0.0.0.0", "0.0.0.0", true},

		// Private IPv6 ranges - should return true
		{"IPv6 unique local fc00::1", "fc00::1", true},
		{"IPv6 unique local fd00::1", "fd00::1", true},
		{"IPv6 multicast ff00::1", "ff00::1", true},

		// Edge cases for private ranges
		{"Edge of 10.0.0.0/8 - 9.255.255.255", "9.255.255.255", false},
		{"Edge of 10.0.0.0/8 - 11.0.0.0", "11.0.0.0", false},
		{"Edge of 172.16.0.0/12 - 172.15.255.255", "172.15.255.255", false},
		{"Edge of 172.16.0.0/12 - 172.32.0.0", "172.32.0.0", false},
		{"Edge of 192.168.0.0/16 - 192.167.255.255", "192.167.255.255", false},
		{"Edge of 192.168.0.0/16 - 192.169.0.0", "192.169.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", tt.ip)
			}

			result := isPrivateIP(ip)
			if result != tt.expected {
				t.Errorf("isPrivateIP(%s) = %v, expected %v", tt.ip, result, tt.expected)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		shouldErr bool
		errText   string
	}{
		// Valid public URLs
		{"Valid HTTP", "http://example.com", false, ""},
		{"Valid HTTPS", "https://example.com", false, ""},
		{"Valid with path", "https://example.com/path/to/page", false, ""},
		{"Valid with query", "https://example.com?query=value", false, ""},

		// Invalid schemes
		{"FTP scheme", "ftp://example.com", true, "only http/https"},
		{"File scheme", "file:///etc/passwd", true, "only http/https"},
		{"Data scheme", "data:text/plain,hello", true, "only http/https"},
		{"Javascript scheme", "javascript:alert(1)", true, "only http/https"},

		// Localhost variations
		{"Localhost HTTP", "http://localhost", true, "localhost"},
		{"Localhost HTTPS", "https://localhost", true, "localhost"},
		{"Localhost with port", "http://localhost:8080", true, "localhost"},
		{"0.0.0.0", "http://0.0.0.0", true, "localhost"},
		{"0.0.0.0 with port", "http://0.0.0.0:8080", true, "localhost"},

		// Loopback IPs
		{"127.0.0.1", "http://127.0.0.1", true, "private IP"},
		{"127.0.0.1 with port", "http://127.0.0.1:8080", true, "private IP"},
		{"127.1.1.1", "http://127.1.1.1", true, "private IP"},
		{"IPv6 loopback", "http://[::1]", true, "private IP"},
		{"IPv6 loopback with port", "http://[::1]:8080", true, "private IP"},

		// Private IPv4 ranges
		{"10.0.0.1", "http://10.0.0.1", true, "private IP"},
		{"10.255.255.255", "http://10.255.255.255", true, "private IP"},
		{"172.16.0.1", "http://172.16.0.1", true, "private IP"},
		{"172.31.255.255", "http://172.31.255.255", true, "private IP"},
		{"192.168.0.1", "http://192.168.0.1", true, "private IP"},
		{"192.168.1.1", "http://192.168.1.1", true, "private IP"},

		// Link-local
		{"169.254.1.1", "http://169.254.1.1", true, "private IP"},
		{"IPv6 link-local", "http://[fe80::1]", true, "private IP"},

		// Invalid URLs
		{"Empty URL", "", true, ""},
		{"Invalid format", "not-a-url", true, ""},
		{"No hostname", "http://", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)

			if tt.shouldErr {
				if err == nil {
					t.Errorf("validateURL(%s) expected error containing '%s', got nil", tt.url, tt.errText)
				} else if tt.errText != "" && !contains(err.Error(), tt.errText) {
					t.Errorf("validateURL(%s) error = '%s', expected to contain '%s'", tt.url, err.Error(), tt.errText)
				}
			} else {
				if err != nil {
					t.Errorf("validateURL(%s) unexpected error: %v", tt.url, err)
				}
			}
		})
	}
}

func TestValidateURL_DNSResolution(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		shouldErr bool
	}{
		// These tests rely on actual DNS resolution
		{"Valid domain - google.com", "https://google.com", false},
		{"Valid domain - example.com", "https://example.com", false},
		{"Invalid domain", "https://this-domain-should-not-exist-12345.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)

			if tt.shouldErr && err == nil {
				t.Errorf("validateURL(%s) expected error, got nil", tt.url)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("validateURL(%s) unexpected error: %v", tt.url, err)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Integration tests for web fetch with readability

func TestWebFetchTool_WithReadability(t *testing.T) {
	// This test requires actual HTTP server, skipping in unit tests
	// The readability integration is tested via the readability_test.go tests
	t.Skip("Integration test - requires HTTP server")
}

