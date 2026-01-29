# Daemon Management

ok-gobot supports running as a system service/daemon on macOS and Linux.

## Platform Support

- **macOS**: Uses launchd with plist files in `~/Library/LaunchAgents/`
- **Linux**: Uses systemd user services in `~/.config/systemd/user/`

## Commands

### Install Service

Generates and installs the service file for automatic startup:

```bash
ok-gobot daemon install
```

This will:
- Create the service file in the appropriate location
- Set up automatic restart on failure
- Configure logging paths
- Use the current binary path

### Start Service

Start the ok-gobot service:

```bash
ok-gobot daemon start
```

### Stop Service

Stop the running service:

```bash
ok-gobot daemon stop
```

### Check Status

Check if the service is running:

```bash
ok-gobot daemon status
```

### View Logs

Display recent service logs:

```bash
ok-gobot daemon logs
```

**macOS**: Shows last 50 lines from `~/Library/Logs/ok-gobot.log` and `~/Library/Logs/ok-gobot-error.log`

**Linux**: Shows last 50 lines from journalctl

### Uninstall Service

Stop and remove the service:

```bash
ok-gobot daemon uninstall
```

## Service Configuration

### macOS (launchd)

Service file location: `~/Library/LaunchAgents/com.ok-gobot.plist`

Features:
- Auto-restart on failure
- Runs at user login
- Logs to `~/Library/Logs/ok-gobot.log`
- Preserves environment variables (HOME, PATH)

### Linux (systemd)

Service file location: `~/.config/systemd/user/ok-gobot.service`

Features:
- Auto-restart on failure (10s delay)
- Starts after network is available
- Logs to systemd journal
- Preserves environment variables (HOME, PATH)

## Usage Example

```bash
# Install and start the service
ok-gobot daemon install
ok-gobot daemon start

# Check if it's running
ok-gobot daemon status

# View recent logs
ok-gobot daemon logs

# Stop the service
ok-gobot daemon stop

# Remove the service
ok-gobot daemon uninstall
```

## Troubleshooting

### Service won't start

1. Check if the service is installed:
   ```bash
   ok-gobot daemon status
   ```

2. View logs for error messages:
   ```bash
   ok-gobot daemon logs
   ```

3. Verify configuration:
   ```bash
   ok-gobot config show
   ok-gobot doctor
   ```

### Permission issues

Make sure the service files have correct permissions:

**macOS**:
```bash
chmod 644 ~/Library/LaunchAgents/com.ok-gobot.plist
```

**Linux**:
```bash
chmod 644 ~/.config/systemd/user/ok-gobot.service
systemctl --user daemon-reload
```

### Binary path changed

If you move the ok-gobot binary, reinstall the service:

```bash
ok-gobot daemon uninstall
ok-gobot daemon install
ok-gobot daemon start
```

## Notes

- The service runs with your user permissions
- Configuration is read from `~/.ok-gobot/config.yaml`
- The service automatically restarts on failure
- Logs are rotated by the system (launchd/systemd)
