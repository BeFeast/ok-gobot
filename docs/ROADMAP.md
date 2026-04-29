# ok-gobot Roadmap

Last updated: April 29, 2026.

This roadmap tracks shipped capabilities and planned work for ok-gobot. The original backlog was derived from the [Competitive Landscape](./COMPETITORS.md) analysis (March 2026). Items are phrased as user outcomes per [PROCESS.md](./PROCESS.md).

## Shipped

### Phase 1: Quick Wins (complete)

1. **Operator can kill dangerous tools instantly without restarting the bot**
   `/estop on|off|status` in Telegram and CLI. Blocks shell, SSH, browser, cron, and message tool families. Status visible in `/status` and TUI.

2. **User can list available models and see which providers are healthy**
   `ok-gobot providers` lists configured providers. `ok-gobot config models` lists available models with aliases. `ok-gobot doctor` distinguishes auth, endpoint, and model errors.

3. **Operator gets a detailed report after migrating from OpenClaw**
   `ok-gobot migrate` with `--dry-run` support. Reports copied files, skipped sessions, and key mapping.

### Phase 2: Safety and Extensibility Foundation (complete)

4. **Operator can restrict an agent to read-only tools without editing Go code**
   Per-agent `capabilities` block in config: `shell`, `network`, `network_allowlist`, `cron`, `memory_write`, `spawn`, `filesystem_roots`, `file_write_scope`. Runtime receives a structured policy object. Existing `allowed_tools` configs still load.

5. **Denied tool calls show a clear reason in Telegram instead of silently failing**
   Every denied tool call returns a human-readable reason with remediation hint. Denial messages appear in Telegram, TUI, and API. Policy enforcement covers both agent-mediated and direct tool invocation.

6. **Operator can install and audit third-party skills from the CLI**
   `ok-gobot skills list|install|remove|audit`. Install from local path or git URL. Static safety audit rejects symlinks, scripts, pipe-to-shell patterns, and escaping links. Utility score tracking (uses, successes, failures) with score-sorted skill prompts.

### Phase 3: Productized Autonomy (complete)

7. **Operator can cap a background task's tool calls and duration, and choose a cost tier**
   Sub-agent spawning via `SpawnSubagent()` with tool-call and duration limits plus cost tier routing (cheap, standard, premium, local). Chat router automatically promotes heavy work to background jobs. Parent-child session tracking delivers results back. Hard token/cost budget enforcement is still planned for Mission Control v1.

8. **Operator can enable a prebuilt role that runs on schedule and posts a report**
   Three prebuilt roles ship: `researcher` (daily), `monitor` (every 30 min), `release-watch` (weekly). Enable by setting `roles_path` in config. Roles run through cron infrastructure and respect capability policy and estop.

9. **Operator can define new roles declaratively without writing Go code**
   Role manifests are markdown files with YAML frontmatter: `worker`, `tools`, `schedule`, `approval`, `report_template`. Stored in the `roles_path` directory. Prebuilt roles use the same format.

### Additional Shipped Features

10. **Self-evolution loop (A-Evolve inspired)**
    Solve-Observe-Evolve-Gate-Reload cycle. Failure pattern analysis, candidate mutation, benchmark gating, strict improvement gate, human approval for large diffs, version history with rollback, daily cycle limit. CLI: `ok-gobot evolution status|history|rollback|metrics`.

11. **Multi-model routing by task type**
    Route messages to different models by task type (vision, summarize, reasoning, coding). Explicit `[task:type]` tags or config-based routing. Falls back through routing default then global default.

12. **Automatic reflection loop after tool failures**
    Tracks failure counts per tool, proposes fixes based on error patterns (not found, timeout, permission denied, parse error, network). Records failures to daily memory notes.

13. **Rules-first chat routing**
    Incoming turns classified as reply (fast inline), clarification (follow-up question), or background job (heavy work launched as isolated task). Forced job prefixes: `job:`, `task:`, `background:`.

14. **Git worktree management for parallel tasks**
    `ok-gobot work <task>` spawns a git worktree with a dedicated worker agent. `ok-gobot worktrees list|cleanup|rm` manages tracked worktrees.

15. **Mission Control API**
    HTTP endpoints for monitoring: `/api/mission/roles`, `/api/mission/schedules`, `/api/mission/runs`, `/api/mission/stats`. Loopback-only access with CORS.

## Planned

### Mission Control v1

The next milestone consolidates the runtime, roles, jobs, and monitoring API into a coherent operator experience called Mission Control. See [MISSION-CONTROL.md](./MISSION-CONTROL.md) for the concept.

- **Budget and policy enforcement for scheduled autonomy.** Scheduled roles and background jobs need hard token/cost budgets and wall-clock limits before operators can trust them in unattended production. Without this gate, the docs intentionally avoid claiming that scheduled autonomy is production-safe.
- **Role and job lifecycle dashboard.** The Mission Control API exists but has no dedicated UI. A TUI or web dashboard showing active roles, run history, failures, and cost would make the operator loop complete.
- **Skill marketplace and versioning.** Skills can be installed and audited, but there is no discovery or versioning beyond `skills history|rollback`. A lightweight registry or feed would help.

### Future Considerations

- Multi-channel expansion beyond Telegram (not a current priority)
- WASM sandboxing for tool isolation
- Reproducible local benchmarks for evolution gating
- AI-powered fix generation in the reflection loop (currently heuristic)

## Non-Goals

- Desktop app or large dashboard
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible benchmarks exist
- Competing on channel count with OpenFang/ZeroClaw/OpenClaw

## Short Version

The original top-5 priority list (estop, provider catalog, migration report, capability policy, tool denial messages) is fully shipped. Current focus is Mission Control v1: budget enforcement, lifecycle dashboard, and skill ecosystem.
