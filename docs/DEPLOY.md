# Deploy & Restart

Quick reference for building, deploying, and restarting ok-gobot.
For full installation from scratch, see [INSTALL.md](INSTALL.md).

---

## Quick Deploy (macOS launchd)

The standard deployment: build from source, restart the launchd service.

```bash
cd /path/to/ok-gobot

# 1. Build
make build            # → bin/ok-gobot
# or: make build-small  (stripped, smaller binary)

# 2. Restart the service (kills old process, starts new one)
launchctl kickstart -k gui/$(id -u)/com.befeast.ok-gobot

# 3. Verify it's running
sleep 5 && ps aux | grep ok-gobot | grep -v grep

# 4. Check logs
tail -20 ~/.ok-gobot/logs/ok-gobot.err.log
```

The plist (`~/Library/LaunchAgents/com.befeast.ok-gobot.plist`) has `KeepAlive: true`
and `ThrottleInterval: 10`, so launchd will restart automatically on crash after
a 10-second cooldown.

### One-liner

```bash
cd /path/to/ok-gobot && make build && launchctl kickstart -k gui/$(id -u)/com.befeast.ok-gobot
```

---

## Quick Deploy (Linux systemd)

```bash
cd /path/to/ok-gobot

# 1. Build
make build

# 2. Copy binary (if installed to system path)
sudo cp bin/ok-gobot /usr/local/bin/ok-gobot

# 3. Restart
sudo systemctl restart ok-gobot

# 4. Check status
sudo systemctl status ok-gobot
journalctl -u ok-gobot -f
```

---

## Verify Deployment

After restart, send `/status` in Telegram. Check that:

1. **Commit hash** matches your latest commit (`git log --oneline -1`)
2. **Uptime** is recent (seconds/minutes, not hours/days)
3. **Model** is correct

Example `/status` output:
```
🦞 Шрага (Shraga) или Штрудель (Sh-true-Dell) 0.1.0 (c8642b7)
🧠 Model: claude-sonnet-4-6 · 🔑 oauth:...QwAA (anthropic)
📚 Context: 0/8.2k (0%) · 🧹 Compactions: 0
🧵 Session: default · updated 2026-03-12T00:51:37Z
⚙️ Think: medium · 🪢 Queue: interrupt (depth 0)

🟢 Running for 0m 5s
```

If the commit hash is old, the new binary didn't get picked up — rebuild and
restart again.

---

## Daemon Management

ok-gobot has a built-in daemon manager:

```bash
# Install as system service (creates plist/unit file)
ok-gobot daemon install

# Control
ok-gobot daemon start
ok-gobot daemon stop
ok-gobot daemon status
ok-gobot daemon logs        # tail -f the log file

# Remove
ok-gobot daemon uninstall
```

---

## Development Workflow

```bash
# Run in foreground (no daemon)
make run
# or: ok-gobot start

# Run without building
make dev
# or: go run ./cmd/ok-gobot start

# Run diagnostics
ok-gobot doctor
```

---

## Log Files

| File | Content |
|------|---------|
| `~/.ok-gobot/logs/ok-gobot.log` | stdout — startup info, session processing |
| `~/.ok-gobot/logs/ok-gobot.err.log` | stderr — errors, warnings, debug output |

```bash
# Live tail
tail -f ~/.ok-gobot/logs/ok-gobot.err.log

# Last 50 lines
tail -50 ~/.ok-gobot/logs/ok-gobot.err.log

# Search for errors
grep -i error ~/.ok-gobot/logs/ok-gobot.err.log | tail -20
```

---

## Troubleshooting

### Bot doesn't start after restart

```bash
# Check launchd status (exit code -9 = still throttled, wait 10s)
launchctl list | grep gobot

# Check for crash logs
tail -30 ~/.ok-gobot/logs/ok-gobot.err.log

# Run manually to see errors
./bin/ok-gobot start
```

### Bot starts but doesn't respond in Telegram

```bash
# Check the process is running
ps aux | grep ok-gobot | grep -v grep

# Verify config has correct telegram token
ok-gobot doctor

# Check if BotFather commands are registered
# (look for "Registered N commands with BotFather" in logs)
grep "Registered" ~/.ok-gobot/logs/ok-gobot.err.log | tail -1
```

### Old code still running after deploy

```bash
# Verify the binary timestamp matches your build
ls -la bin/ok-gobot

# Force kill and let launchd restart
launchctl kill SIGTERM gui/$(id -u)/com.befeast.ok-gobot
sleep 12  # wait for throttle interval
ps aux | grep ok-gobot | grep -v grep
```
