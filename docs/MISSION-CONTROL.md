# Mission Control v1

## Concept

Mission Control is the operator-facing view of ok-gobot's autonomous capabilities: agent profiles, role schedules, job runs, provider state, emergency-stop state, and system health in a single place.

The goal is not a full dashboard product. It is the minimum monitoring surface an operator needs to trust that scheduled roles are running correctly and to intervene when they are not.

## Current State (April 2026)

The Mission Control API exists on the authenticated HTTP API server. The local web UI provides a read-only jobs/workers view over the loopback control server.

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
| `/api/mission/runs` | GET | Recent job runs with status, summary, errors, attempt count, and preflight refusal reasons |
| `/api/mission/stats` | GET | Daily and total statistics (tokens, messages, sessions) |
| `/api/mission/estop` | GET | Current emergency-stop state |
| `/api/mission/providers` | GET | Active AI provider and model |
| `/api/mission/memory` | GET | Memory health: enabled state, backend, watcher, counts, freshness, and last error |
| `/api/mission/evidence?session_key=...` | GET | Concise structured evidence timeline for a session, including Markdown rendering |
| `/api/mission/supervisor` | GET | Current supervisor decision and the last idempotent safe action |

Job detail responses from `/api/jobs/{id}` include proof artifacts with `type`,
`label`, `path` or `url`, `created_at`, and `display` metadata. Mission Control
renders image/screenshot artifacts, URL artifacts, and inline text reports when
they are present.

Evidence timelines are stored as append-only structured rows in the same SQLite
store as durable jobs and session transcripts. Event summaries and payloads are
redacted and capped before rendering so operators can diagnose sessions without
using raw log tails as the only source of truth.

Run query parameters: `limit` (1-200), `status` (filter by run status). Stats query parameters: `days` (1-90).

### What It Monitors

- **Profiles** -- which agent profiles are loaded and what tools/models they use
- **Schedules** -- when each role is next due to run
- **Runs** -- whether recent job runs succeeded, failed, or were refused by preflight, with summaries and errors
- **Proof artifacts** -- screenshots, URLs, and text reports linked from job and worker rows
- **Evidence timelines** -- redacted structured session ledger for preflight, commands, checks, reviews, retries, and final outcomes
- **Stats** -- aggregate token usage and message counts
- **Controls** -- emergency-stop state and active provider/model
- **Memory** -- whether the memory index is enabled, fresh, watched, and error-free
- **Supervisor** -- current stuck-state decision, blocker reason, required approval action, and last safe recovery action

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

Mission Control artifact browsing is read-only. Supervisor state is exposed as a decision ledger: safe recovery actions are idempotent and approval-only actions such as merging PRs, closing issues, deleting worktrees, or changing global configuration are never performed without explicit approval.

### Maestro Intake Status

Use `ok-gobot maestro dry-run` before starting a worker from GitHub issues. The command reports the next eligible issue and every skipped candidate with a reason. By default, only issues with `maestro.ready_label` are eligible, and issues labeled `blocked`, `epic`, `meta`, `question`, `wontfix`, `duplicate`, or `invalid` are skipped.

Use `ok-gobot maestro status` when no worker is running. It explains whether a worker is idle because there is no eligible issue, or which issue would be selected next. Dependency lines such as `Depends on: #353` block intake until the dependency is closed or a referenced PR is merge-ready. Inline-code snippets, fenced examples, and example sections are ignored.

Maintainers can use `--override --override-reason "..."` to force the first open issue through the dry-run/status selection. The output and logs show that the maintainer override was used and list the gates it bypassed.

Local file previews are restricted to artifact roots. Defaults are
`~/.ok-gobot/screenshots` and `~/.ok-gobot/artifacts`; add more with:

```yaml
artifacts:
  roots:
    - "~/proof-artifacts"
```

Paths outside these roots are redacted from API responses and are not served by
the artifact preview endpoint.

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
