package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
)

type checkResult struct {
	name     string
	passed   bool
	required bool
	warning  bool // true = non-fatal issue (⚠️ instead of ❌)
	message  string
}

func newDoctorCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics to check system health",
		Long:  `Verify that all required dependencies and configurations are properly set up.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🦞 ok-gobot diagnostics")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			var results []checkResult
			var hasFailures bool

			// Run all checks
			results = append(results, checkConfigFile(cfg))
			results = append(results, checkTelegramToken(cfg))
			results = append(results, checkProvider(cfg)...)
			results = append(results, checkStoragePath(cfg))
			results = append(results, checkPDFToText())
			results = append(results, checkWhisper())
			results = append(results, checkFFmpeg())
			results = append(results, checkChrome())

			// Print results
			for _, result := range results {
				printResult(result)
				if result.required && !result.passed && !result.warning {
					hasFailures = true
				}
			}

			fmt.Println()
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			if hasFailures {
				fmt.Printf("%s✗ Some required checks failed%s\n", colorRed, colorReset)
				return fmt.Errorf("diagnostics failed")
			}

			fmt.Printf("%s✓ All required checks passed%s\n", colorGreen, colorReset)
			return nil
		},
	}
}

func printResult(result checkResult) {
	var symbol, color, typeLabel string

	switch {
	case result.passed:
		symbol = "✓"
		color = colorGreen
	case result.warning:
		symbol = "⚠"
		color = colorYellow
	default:
		symbol = "✗"
		if result.required {
			color = colorRed
		} else {
			color = colorYellow
		}
	}

	if result.required {
		typeLabel = ""
	} else {
		typeLabel = fmt.Sprintf(" %s[optional]%s", colorCyan, colorReset)
	}

	fmt.Printf("%s%s%s %s%s", color, symbol, colorReset, result.name, typeLabel)

	if result.message != "" {
		fmt.Printf("\n  %s%s%s", color, result.message, colorReset)
	}

	fmt.Println()
}

func checkConfigFile(cfg *config.Config) checkResult {
	result := checkResult{
		name:     "Config file exists",
		required: true,
	}

	if cfg.ConfigPath == "" {
		result.passed = false
		result.message = "No config file found. Run 'ok-gobot config init' to create one."
		return result
	}

	// Check if file exists
	if _, err := os.Stat(cfg.ConfigPath); os.IsNotExist(err) {
		result.passed = false
		result.message = fmt.Sprintf("Config file not found: %s", cfg.ConfigPath)
		return result
	}

	// Validate YAML
	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		result.passed = false
		result.message = fmt.Sprintf("Failed to read config file: %v", err)
		return result
	}

	var yamlData map[string]interface{}
	if err := yaml.Unmarshal(data, &yamlData); err != nil {
		result.passed = false
		result.message = fmt.Sprintf("Invalid YAML syntax: %v", err)
		return result
	}

	result.passed = true
	result.message = fmt.Sprintf("Found: %s", cfg.ConfigPath)
	return result
}

func checkTelegramToken(cfg *config.Config) checkResult {
	result := checkResult{
		name:     "Telegram bot token",
		required: true,
	}

	if cfg.Telegram.Token == "" {
		result.passed = false
		result.message = "Not set. Get token from @BotFather and run: ok-gobot config set telegram.token <token>"
		return result
	}

	// Basic validation: Telegram tokens are typically in format 123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
	if len(cfg.Telegram.Token) < 30 {
		result.passed = false
		result.message = "Token appears invalid (too short)"
		return result
	}

	result.passed = true
	result.message = "Set"
	return result
}

// checkProvider probes the configured AI provider, returning one or more
// checkResults that distinguish auth, endpoint, and model failures.
func checkProvider(cfg *config.Config) []checkResult {
	provider := cfg.AI.Provider
	if provider == "" {
		provider = "openrouter"
	}

	label := fmt.Sprintf("Provider %s", provider)

	// Build ProviderConfig from the app config.
	pcfg := ai.ProviderConfig{
		Name:    provider,
		APIKey:  cfg.AI.APIKey,
		BaseURL: cfg.AI.BaseURL,
		Model:   cfg.AI.Model,
	}

	// Quick pre-check: API key must be set (unless Anthropic OAuth).
	if pcfg.APIKey == "" && provider != "anthropic" && provider != "droid" {
		return []checkResult{{
			name:     label,
			required: true,
			message:  "API key not set. Configure with `ok-gobot config set ai.api_key <key>`",
		}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	droidCfg := ai.DroidConfig{
		BinaryPath: cfg.AI.Droid.BinaryPath,
	}
	probe := ai.ProbeProvider(ctx, pcfg, droidCfg)

	switch probe.Status {
	case ai.ProbeOK:
		return []checkResult{{
			name:     label,
			required: true,
			passed:   true,
			message:  fmt.Sprintf("✅ %s: ok (model %s, latency %dms)", provider, probe.Model, probe.Latency.Milliseconds()),
		}}

	case ai.ProbeAuthFailed:
		msg := fmt.Sprintf("❌ %s: authentication failed (check API key)", provider)
		return []checkResult{{
			name:     label,
			required: true,
			message:  msg,
		}}

	case ai.ProbeEndpointUnreachable:
		msg := fmt.Sprintf("❌ %s: endpoint unreachable (check base_url)", provider)
		if probe.Detail != "" {
			msg += "\n  " + probe.Detail
		}
		return []checkResult{{
			name:     label,
			required: true,
			message:  msg,
		}}

	case ai.ProbeModelNotFound:
		available := ""
		if len(probe.AvailableModels) > 0 {
			shown := probe.AvailableModels
			if len(shown) > 5 {
				shown = shown[:5]
			}
			available = strings.Join(shown, ", ")
			if len(probe.AvailableModels) > 5 {
				available += fmt.Sprintf(" … and %d more", len(probe.AvailableModels)-5)
			}
		}
		msg := fmt.Sprintf("⚠️ %s: model %s not found", provider, probe.Model)
		if available != "" {
			msg += fmt.Sprintf(" (available: %s)", available)
		}
		return []checkResult{{
			name:     label,
			required: true,
			warning:  true,
			message:  msg,
		}}

	case ai.ProbeSkipped:
		return []checkResult{{
			name:     label,
			required: true,
			passed:   true,
			message:  fmt.Sprintf("Skipped (%s)", probe.Detail),
		}}

	default:
		return []checkResult{{
			name:     label,
			required: true,
			message:  probe.Detail,
		}}
	}
}

func checkStoragePath(cfg *config.Config) checkResult {
	result := checkResult{
		name:     "Storage path",
		required: true,
	}

	if cfg.StoragePath == "" {
		result.passed = false
		result.message = "Storage path not configured"
		return result
	}

	// Get directory
	dir := filepath.Dir(cfg.StoragePath)

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// Try to create it
		if err := os.MkdirAll(dir, 0755); err != nil {
			result.passed = false
			result.message = fmt.Sprintf("Directory doesn't exist and cannot be created: %s", dir)
			return result
		}
		result.passed = true
		result.message = fmt.Sprintf("Created directory: %s", dir)
		return result
	}

	// Check if writable
	testFile := filepath.Join(dir, ".ok-gobot-write-test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		result.passed = false
		result.message = fmt.Sprintf("Directory not writable: %s", dir)
		return result
	}
	os.Remove(testFile)

	result.passed = true
	result.message = fmt.Sprintf("Exists and writable: %s", dir)
	return result
}

func checkPDFToText() checkResult {
	result := checkResult{
		name:     "pdftotext binary",
		required: false,
	}

	path, err := exec.LookPath("pdftotext")
	if err != nil {
		result.passed = false
		result.message = "Not found. Install poppler-utils for PDF support."
		return result
	}

	result.passed = true
	result.message = fmt.Sprintf("Found: %s", path)
	return result
}

func checkWhisper() checkResult {
	result := checkResult{
		name:     "whisper binary",
		required: false,
	}

	path, err := exec.LookPath("whisper")
	if err != nil {
		result.passed = false
		result.message = "Not found. Install openai-whisper for audio transcription."
		return result
	}

	result.passed = true
	result.message = fmt.Sprintf("Found: %s", path)
	return result
}

func checkFFmpeg() checkResult {
	result := checkResult{
		name:     "ffmpeg binary",
		required: false,
	}

	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		result.passed = false
		result.message = "Not found. Install ffmpeg for media processing."
		return result
	}

	result.passed = true
	result.message = fmt.Sprintf("Found: %s", path)
	return result
}

func checkChrome() checkResult {
	result := checkResult{
		name:     "Chrome/Chromium browser",
		required: false,
	}

	// Common Chrome/Chromium locations
	chromePaths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	}

	// Also check PATH
	if path, err := exec.LookPath("google-chrome"); err == nil {
		result.passed = true
		result.message = fmt.Sprintf("Found: %s", path)
		return result
	}

	if path, err := exec.LookPath("chromium"); err == nil {
		result.passed = true
		result.message = fmt.Sprintf("Found: %s", path)
		return result
	}

	if path, err := exec.LookPath("chromium-browser"); err == nil {
		result.passed = true
		result.message = fmt.Sprintf("Found: %s", path)
		return result
	}

	// Check known paths
	for _, chromePath := range chromePaths {
		if _, err := os.Stat(chromePath); err == nil {
			result.passed = true
			result.message = fmt.Sprintf("Found: %s", chromePath)
			return result
		}
	}

	result.passed = false
	result.message = "Not found. Install Chrome or Chromium for browser automation."
	return result
}
