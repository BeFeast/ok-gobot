# Mission Control v1

## Concept

Mission Control is the operator-facing view of ok-gobot's autonomous capabilities: roles, schedules, job runs, and system health in a single place.

The goal is not a full dashboard product. It is the minimum monitoring surface an operator needs to trust that scheduled roles are running correctly and to intervene when they are not.

## Current State (April 2026)

The Mission Control API exists and serves data. A dashboard UI is planned but not yet built.

### API Endpoints

All endpoints are served by the control server (default port 8787, loopback only).

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/mission/roles` | GET | All configured roles with metadata (name, display name, model, tools) |
| `/api/mission/schedules` | GET | Scheduled jobs with cron expression, next run time, timeout |
| `/api/mission/runs` | GET | Recent job runs with status, summary, errors, attempt count |
| `/api/mission/stats` | GET | Daily and total statistics (tokens, messages, sessions) |

Query parameters: `limit` (1-200), `status` (filter by run status).

### What It Monitors

- **Roles** -- which roles are loaded and what tools/models they use
- **Schedules** -- when each role is next due to run
- **Runs** -- whether recent job runs succeeded or failed, with summaries
- **Stats** -- aggregate token usage and message counts

## Architecture

Mission Control is a read-only API layer over existing runtime data. It does not introduce a separate data store or orchestration system.

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│  Roles       │    │  Cron        │    │  Runtime     │
│  (manifests) │    │  (scheduler) │    │  (jobs)      │
└──────┬───────┘    └──────┬───────┘    └──────┬───────┘
       │                   │                   │
       └───────────┬───────┘───────────────────┘
                   │
            ┌──────┴───────┐
            │ Mission API  │
            │ /api/mission │
            └──────┬───────┘
                   │
            ┌──────┴───────┐
            │  Dashboard   │
            │  (planned)   │
            └──────────────┘
```

## Safety Notes

Mission Control is read-only today. It does not modify roles, start jobs, or change configuration.

Scheduled roles execute with whatever tools and capabilities their manifest declares. Operators should:

1. Set explicit `tools` allowlists in role manifests (do not leave empty/all-tools).
2. Use per-agent `capabilities` to restrict shell, network, and filesystem access.
3. Review role output regularly until per-task budget enforcement is complete.
4. Use `/estop on` to immediately halt all dangerous tool execution if something goes wrong.

Full per-task budget caps (tool call count, model cost) are planned for Phase 5 of the [Roadmap](ROADMAP.md).

## Planned (Phase 6)

- Dashboard page rendering roles, schedules, runs, and stats in a single view
- Role health monitoring with alerts for repeated failures or exceeded token budgets
- One-click role enable/disable from dashboard and TUI
- Operator playbook system: composable multi-role workflows with dependency ordering
