# Mission Control v1

Last updated: April 29, 2026.

## Concept

Mission Control is the operator-facing layer that ties together ok-gobot's runtime, roles, jobs, and monitoring into a coherent experience. Instead of managing individual cron jobs, sub-agents, and tool policies separately, Mission Control presents them as a unified operations view.

## Current State

The following pieces are shipped and functional:

### Runtime
- **Chat/jobs mailbox runtime** (`internal/runtime`): session-keyed workers with bounded queues, parallel execution across sessions, FIFO within each session.
- **Chat routing**: rules-first classification routes lightweight replies inline, promotes heavy work to background jobs, and asks for clarification on ambiguous requests.
- **Sub-agent spawning**: background jobs run as isolated sub-agents with parent-child result delivery and cost tier selection.

### Roles
- **Declarative role manifests**: markdown files with YAML frontmatter define prompt, tools, schedule, approval mode, worker tier, and report template.
- **Prebuilt roles**: `researcher`, `monitor`, `release-watch` ship as bundled examples.
- **Cron integration**: roles with a `schedule` field are registered as cron jobs on startup. Reports delivered to the admin chat.

### Monitoring API
- `GET /api/mission/roles` -- list configured roles with metadata
- `GET /api/mission/schedules` -- list cron jobs with next-run times
- `GET /api/mission/runs` -- recent job run history
- `GET /api/mission/stats` -- aggregate statistics

Loopback-only with CORS restriction.

### Safety Controls
- **Capability policy**: per-agent tool restrictions (shell, network, cron, memory_write, spawn, filesystem_roots, file_write_scope).
- **Emergency stop**: `/estop on` instantly kills dangerous tool families.
- **Reflection loop**: automatic failure tracking with pattern-based fix proposals.
- **Self-evolution**: A-Evolve cycle with benchmark gating, version history, and rollback.

## What's Missing for v1

### Budget and Policy Enforcement
Scheduled roles and background jobs need hard limits before operators can trust them unattended:
- **Token budget**: maximum prompt + completion tokens per role run.
- **Cost budget**: maximum dollar cost per run (requires provider pricing data).
- **Wall-clock timeout**: maximum execution duration.
- **Tool call cap**: maximum number of tool invocations per run.

Without budgets, a runaway role or sub-agent can exhaust API quota silently. The docs intentionally avoid claiming that scheduled autonomy is production-safe until this ships.

### Lifecycle Dashboard
The monitoring API exists but has no dedicated UI. A TUI or web dashboard would show:
- Active roles and their run status
- Run history with success/failure/cost metrics
- Current estop state and capability policy
- Evolution version and metrics

### Skill Ecosystem
Skills can be installed and audited from the CLI, but discovery is manual. A lightweight registry or feed would help operators find useful skills.

## Architecture

```
Telegram/TUI/API (adapters)
        |
        v
  internal/runtime (mailbox scheduler)
        |
    +---+---+
    |       |
  chat    jobs/roles
  routing   |
    |     cron scheduler
    v       |
  session   role manifest
  worker    loader
    |       |
    v       v
  AI client + tools (with capability policy)
        |
        v
  Mission Control API (monitoring)
```

All execution flows through `internal/runtime`. Adapters (Telegram, TUI, API) submit work but never execute model logic directly. The Mission Control API reads state from the runtime, storage, and cron scheduler.

## Files

| Component | Location |
|-----------|----------|
| Runtime | `internal/runtime/runtime.go` |
| Chat router | `internal/runtime/chat_router.go` |
| Sub-agent spawning | `internal/runtime/subagent.go` |
| Role manifests | `internal/role/manifest.go` |
| Prebuilt roles | `internal/role/prebuilt/` |
| Mission Control API | `internal/control/mission.go` |
| Capability policy | `internal/tools/policy.go` |
| Evolution engine | `internal/evolution/engine.go` |
| Reflection | `internal/agent/reflection.go` |
