# Mission Control v1

## Concept

Mission Control is ok-gobot's operator visibility layer for roles, jobs, and schedules. It exposes the autonomous runtime state through a set of REST API endpoints served by the control server.

The goal is to give operators a single place to understand what ok-gobot is doing autonomously: which roles are loaded, what schedules are active, which jobs have run, and aggregate execution stats.

## Current State

Mission Control v1 ships as HTTP API endpoints on the control server (default port 8787, disabled by default). A web-based operator dashboard is planned but not yet built.

### Prerequisites

Enable the control server in config:

```yaml
control:
  enabled: true
  port: 8787
```

The control server binds to `127.0.0.1` only. External access requires a reverse proxy.

## API Endpoints

### GET /api/mission/roles

Lists all loaded roles with metadata.

Response:
```json
[
  {
    "name": "researcher",
    "display_name": "Researcher",
    "emoji": "",
    "model": "",
    "allowed_tools": ["web_fetch", "search"]
  }
]
```

### GET /api/mission/schedules

Lists active cron schedules registered by roles.

### GET /api/mission/runs

Lists recent job execution history with status and timing.

Query parameters:
- `status` -- filter by job status (pending, running, succeeded, failed, cancelled, timed_out)
- `limit` -- max results (default 50)

### GET /api/mission/stats

Returns aggregate execution metrics.

## Architecture

Mission Control is implemented in `internal/control/mission.go`. It bridges the agent registry, cron scheduler, and storage layer through a `MissionProvider` interface:

```
Telegram/TUI/API  -->  Control Server  -->  Mission Control endpoints
                                              |
                                              +-- AgentRegistry (roles)
                                              +-- Scheduler (cron jobs)
                                              +-- Store (job records, events)
```

Roles are loaded from `roles_path` by `internal/role/loader.go`. Each role with a `schedule` field registers a cron job on startup. Job execution records and events are persisted to SQLite.

## Safety

Mission Control exposes read-only data. It does not allow starting, stopping, or modifying roles or jobs through the API (those operations go through Telegram commands or direct config changes).

Scheduled roles execute tool calls autonomously. Per-role budget caps (tool calls, duration, tokens) are not yet enforced. Until budget enforcement ships, operators should:

- Review role manifests before enabling them.
- Use capability policies to restrict tool access per agent.
- Monitor execution via `/api/mission/runs` and `/api/mission/stats`.
- Use `/estop on` to kill dangerous tools if needed.

## Roadmap

- Operator web dashboard for role/job/schedule visibility
- Per-role and per-job cost caps (tool calls, duration, tokens)
- Role enable/disable via API (currently config-only)
- Alerting on job failures or budget threshold breaches
