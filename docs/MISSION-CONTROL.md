# Mission Control v1

## Concept

Mission Control is the operator-facing view of ok-gobot's autonomous capabilities: agent profiles, role schedules, job runs, provider state, emergency-stop state, and system health in a single place.

The goal is not a full dashboard product. It is the minimum monitoring surface an operator needs to trust that scheduled roles are running correctly and to intervene when they are not.

## Current State (April 2026)

The Mission Control API exists on the authenticated HTTP API server. A dashboard UI is planned but not yet built.

### API Endpoints

Enable the HTTP API to serve these endpoints (default port 8080, loopback bind by default):

```yaml
api:
  enabled: true
  api_key: "a-strong-random-token"
```

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/mission/roles` | GET | Registered agent profiles with metadata (name, display name, model, tools) |
| `/api/mission/schedules` | GET | Scheduled jobs with cron expression, next run time, timeout |
| `/api/mission/runs` | GET | Recent job runs with status, summary, errors, attempt count |
| `/api/mission/stats` | GET | Daily and total statistics (tokens, messages, sessions) |
| `/api/mission/estop` | GET | Current emergency-stop state |
| `/api/mission/providers` | GET | Active AI provider and model |

Run query parameters: `limit` (1-200), `status` (filter by run status). Stats query parameters: `days` (1-90).

### What It Monitors

- **Profiles** -- which agent profiles are loaded and what tools/models they use
- **Schedules** -- when each role is next due to run
- **Runs** -- whether recent job runs succeeded or failed, with summaries
- **Stats** -- aggregate token usage and message counts
- **Controls** -- emergency-stop state and active provider/model

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
3. Set `max_tool_calls` and `max_duration` on scheduled roles.
4. Review role output regularly until token/cost budget enforcement and the central policy gateway are complete.
5. Use `/estop on` to immediately halt all dangerous tool execution if something goes wrong.

Token/cost budget enforcement and the central policy gateway are tracked in Phase 5 of the [Roadmap](ROADMAP.md).

## Planned (Phase 6)

- Dashboard page rendering profiles, schedules, runs, and stats in a single view
- Role health monitoring with alerts for repeated failures or exceeded enforced budgets
- One-click role enable/disable from dashboard and TUI
- Operator playbook system: composable multi-role workflows with dependency ordering
