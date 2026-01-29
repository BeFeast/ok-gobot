package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
)

const (
	launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.ok-gobot</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinaryPath}}</string>
		<string>start</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}/ok-gobot.log</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}/ok-gobot-error.log</string>
	<key>WorkingDirectory</key>
	<string>{{.HomeDir}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>{{.HomeDir}}</string>
		<key>PATH</key>
		<string>{{.Path}}</string>
	</dict>
</dict>
</plist>
`

	systemdServiceTemplate = `[Unit]
Description=ok-gobot Telegram Bot
After=network.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} start
Restart=on-failure
RestartSec=10
WorkingDirectory={{.HomeDir}}
Environment="HOME={{.HomeDir}}"
Environment="PATH={{.Path}}"

[Install]
WantedBy=default.target
`
)

type serviceConfig struct {
	BinaryPath string
	HomeDir    string
	LogPath    string
	Path       string
}

func newDaemonCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage ok-gobot daemon service",
		Long:  `Install, uninstall, start, stop, and check status of ok-gobot as a system service.`,
	}

	cmd.AddCommand(newDaemonInstallCommand(cfg))
	cmd.AddCommand(newDaemonUninstallCommand(cfg))
	cmd.AddCommand(newDaemonStartCommand(cfg))
	cmd.AddCommand(newDaemonStopCommand(cfg))
	cmd.AddCommand(newDaemonStatusCommand(cfg))
	cmd.AddCommand(newDaemonLogsCommand(cfg))

	return cmd
}

func newDaemonInstallCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install ok-gobot as a system service",
		Long:  `Generate and install service file for automatic startup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installDaemon()
		},
	}
}

func newDaemonUninstallCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall ok-gobot system service",
		Long:  `Remove service file and stop the service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return uninstallDaemon()
		},
	}
}

func newDaemonStartCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start ok-gobot service",
		Long:  `Start the ok-gobot daemon service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return startDaemon()
		},
	}
}

func newDaemonStopCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop ok-gobot service",
		Long:  `Stop the running ok-gobot daemon service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopDaemon()
		},
	}
}

func newDaemonStatusCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check ok-gobot service status",
		Long:  `Check if ok-gobot daemon service is running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return statusDaemon()
		},
	}
}

func newDaemonLogsCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Show ok-gobot service logs",
		Long:  `Display recent logs from ok-gobot daemon service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return logsDaemon()
		},
	}
}

// getServiceConfig returns the configuration needed for service templates
func getServiceConfig() (*serviceConfig, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	logPath := filepath.Join(homeDir, "Library", "Logs")
	if runtime.GOOS == "linux" {
		logPath = filepath.Join(homeDir, ".local", "state", "ok-gobot")
	}

	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/local/bin:/usr/bin:/bin"
	}

	return &serviceConfig{
		BinaryPath: binaryPath,
		HomeDir:    homeDir,
		LogPath:    logPath,
		Path:       pathEnv,
	}, nil
}

// getServicePath returns the path to the service file based on OS
func getServicePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "LaunchAgents", "com.ok-gobot.plist"), nil
	case "linux":
		return filepath.Join(homeDir, ".config", "systemd", "user", "ok-gobot.service"), nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

// installDaemon generates and installs the service file
func installDaemon() error {
	servicePath, err := getServicePath()
	if err != nil {
		return err
	}

	// Check if already installed
	if _, err := os.Stat(servicePath); err == nil {
		return fmt.Errorf("service already installed at %s\nRun 'ok-gobot daemon uninstall' first to reinstall", servicePath)
	}

	// Create service directory if it doesn't exist
	serviceDir := filepath.Dir(servicePath)
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return fmt.Errorf("failed to create service directory: %w", err)
	}

	// Get service configuration
	cfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	// Create log directory for macOS
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(cfg.LogPath, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	} else if runtime.GOOS == "linux" {
		// Create state directory for Linux
		if err := os.MkdirAll(cfg.LogPath, 0755); err != nil {
			return fmt.Errorf("failed to create state directory: %w", err)
		}
	}

	// Generate service file content
	var tmpl *template.Template
	var content bytes.Buffer

	switch runtime.GOOS {
	case "darwin":
		tmpl = template.Must(template.New("launchd").Parse(launchdPlistTemplate))
	case "linux":
		tmpl = template.Must(template.New("systemd").Parse(systemdServiceTemplate))
	}

	if err := tmpl.Execute(&content, cfg); err != nil {
		return fmt.Errorf("failed to generate service file: %w", err)
	}

	// Write service file
	if err := os.WriteFile(servicePath, content.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	fmt.Printf("✓ Service file installed at: %s\n", servicePath)
	fmt.Printf("✓ Binary path: %s\n", cfg.BinaryPath)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. Run 'ok-gobot daemon start' to start the service\n")
	fmt.Printf("  2. Run 'ok-gobot daemon status' to check if it's running\n")
	fmt.Printf("  3. Run 'ok-gobot daemon logs' to view logs\n")

	return nil
}

// uninstallDaemon stops and removes the service
func uninstallDaemon() error {
	servicePath, err := getServicePath()
	if err != nil {
		return err
	}

	// Check if service exists
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed")
	}

	// Try to stop the service first (ignore errors)
	_ = stopDaemon()

	// Remove service file
	if err := os.Remove(servicePath); err != nil {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	fmt.Printf("✓ Service uninstalled: %s\n", servicePath)
	return nil
}

// startDaemon starts the service
func startDaemon() error {
	servicePath, err := getServicePath()
	if err != nil {
		return err
	}

	// Check if service is installed
	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed. Run 'ok-gobot daemon install' first")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("launchctl", "load", servicePath)
	case "linux":
		cmd = exec.Command("systemctl", "--user", "start", "ok-gobot")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, string(output))
	}

	fmt.Println("✓ Service started successfully")
	return nil
}

// stopDaemon stops the service
func stopDaemon() error {
	servicePath, err := getServicePath()
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("launchctl", "unload", servicePath)
	case "linux":
		cmd = exec.Command("systemctl", "--user", "stop", "ok-gobot")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if service is already stopped
		if strings.Contains(string(output), "Could not find specified service") ||
			strings.Contains(string(output), "not loaded") {
			fmt.Println("Service is not running")
			return nil
		}
		return fmt.Errorf("failed to stop service: %w\nOutput: %s", err, string(output))
	}

	fmt.Println("✓ Service stopped successfully")
	return nil
}

// statusDaemon checks if the service is running
func statusDaemon() error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("launchctl", "list")
	case "linux":
		cmd = exec.Command("systemctl", "--user", "status", "ok-gobot")
	}

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	switch runtime.GOOS {
	case "darwin":
		if strings.Contains(outputStr, "com.ok-gobot") {
			fmt.Println("✓ Service is running")
			// Extract PID and status
			lines := strings.Split(outputStr, "\n")
			for _, line := range lines {
				if strings.Contains(line, "com.ok-gobot") {
					fmt.Printf("  %s\n", strings.TrimSpace(line))
				}
			}
			return nil
		}
		fmt.Println("✗ Service is not running")
		return nil

	case "linux":
		if err == nil {
			fmt.Println("✓ Service is running")
		} else {
			fmt.Println("✗ Service is not running")
		}
		fmt.Println(outputStr)
		return nil
	}

	return nil
}

// logsDaemon displays recent logs
func logsDaemon() error {
	switch runtime.GOOS {
	case "darwin":
		cfg, err := getServiceConfig()
		if err != nil {
			return err
		}

		logFile := filepath.Join(cfg.LogPath, "ok-gobot.log")
		errorLogFile := filepath.Join(cfg.LogPath, "ok-gobot-error.log")

		fmt.Println("=== Standard Output ===")
		if content, err := os.ReadFile(logFile); err == nil {
			lines := strings.Split(string(content), "\n")
			start := len(lines) - 51
			if start < 0 {
				start = 0
			}
			fmt.Println(strings.Join(lines[start:], "\n"))
		} else {
			fmt.Printf("No logs found at %s\n", logFile)
		}

		fmt.Println("\n=== Error Output ===")
		if content, err := os.ReadFile(errorLogFile); err == nil {
			lines := strings.Split(string(content), "\n")
			start := len(lines) - 51
			if start < 0 {
				start = 0
			}
			fmt.Println(strings.Join(lines[start:], "\n"))
		} else {
			fmt.Printf("No error logs found at %s\n", errorLogFile)
		}

	case "linux":
		cmd := exec.Command("journalctl", "--user", "-u", "ok-gobot", "--no-pager", "-n", "50")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to read logs: %w\nOutput: %s", err, string(output))
		}
		fmt.Println(string(output))
	}

	return nil
}
