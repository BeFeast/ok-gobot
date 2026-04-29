# ok-gobot Roadmap

Last updated: April 29, 2026.
Previous snapshot: March 12, 2026.

This roadmap turns the findings from [Competitive Landscape](./COMPETITORS.md) into an implementation backlog for `ok-gobot`.

The goal is not to copy OpenFang or ZeroClaw wholesale. The goal is to borrow the parts that improve `ok-gobot` without destroying its main advantage: a small, understandable, Telegram-first single-binary operator bot.

Every backlog item is phrased as a user outcome per [PROCESS.md](./PROCESS.md). Infrastructure sub-tasks may reference internals, but the parent item describes what a user can observe when it ships.

## Sequencing

### Phase 1: Quick Wins -- SHIPPED

1. ~~Operator can kill dangerous tools instantly without restarting the bot~~ (shipped: `/estop`)
2. ~~User can list available models and see which providers are healthy~~ (shipped: `ok-gobot providers`, `ok-gobot models`)
3. ~~Operator gets a detailed report after migrating from OpenClaw~~ (shipped: migration report)

### Phase 2: Safety and Extensibility Foundation -- SHIPPED

4. ~~Operator can restrict an agent to read-only tools without editing Go code~~ (shipped: capability policy)
5. ~~Denied tool calls show a clear reason in Telegram instead of silently failing~~ (shipped: tool denial messages)
6. ~~Operator can install and audit third-party skills from the CLI~~ (shipped: `ok-gobot skills`)

### Phase 3: Productized Autonomy -- SHIPPED

7. ~~Operator can enable a prebuilt role that runs on schedule and posts a report~~ (shipped: prebuilt roles)
8. ~~Operator can define new roles declaratively without writing Go code~~ (shipped: markdown role manifests)

### Phase 4: Intelligence & Observability -- SHIPPED

9. ~~Multi-model routing by task type~~ (shipped: `ai.routing` config)
10. ~~Automatic reflection loop after tool failures~~ (shipped: reflector)
11. ~~Self-evolution loop~~ (shipped: A-Evolve-inspired engine)
12. ~~Utility score tracking and skill router~~ (shipped: skill scores)

### Phase 5: Mission Control v1 -- IN PROGRESS

13. Budget and policy enforcement for autonomous runs
14. Per-role and per-job cost caps (tool calls, duration, tokens)
15. Operator dashboard for role/job/schedule visibility (Mission Control API shipped; UI pending)

### Phase 6: Hardening

16. SECURITY.md with formal vulnerability reporting process
17. Reproducible local benchmarks for evolution gate
18. Full live agent benchmarking (currently heuristic-based)

## Shipped Backlog (Detail)

The items below were the original Phase 1-3 backlog entries. They are retained for historical context. All are now implemented.

### 1. Operator can kill dangerous tools instantly without restarting the bot

**Status: SHIPPED**

`/estop on` disables dangerous tool families (shell, SSH, browser, cron, message, fetch).
`/estop off` re-enables them. Blocked tools return a user-visible reason. `/status` shows estop state.
CLI: `ok-gobot estop on|off|status`.

### 2. User can list available models and see which providers are healthy

**Status: SHIPPED**

`ok-gobot providers` lists configured providers with health status.
`ok-gobot models` lists available models. `ok-gobot doctor` distinguishes auth, endpoint, and model lookup failures.

### 3. Operator gets a detailed report after migrating from OpenClaw

**Status: SHIPPED**

Migration runs emit a report listing copied files, skipped sessions, duplicates, backup path, and key mapping.

### 4. Operator can restrict an agent to read-only tools without editing Go code

**Status: SHIPPED**

Agent config accepts `capabilities` block: shell, filesystem_roots, network/network_allowlist, cron, memory_write, spawn, file_write_scope. Existing `allowed_tools` configs still load without breakage.

### 5. Denied tool calls show a clear reason in Telegram instead of silently failing

**Status: SHIPPED**

Every denied tool call returns "Tool [name] blocked: [reason]" in Telegram, TUI, and API.

### 6. Operator can install and audit third-party skills from the CLI

**Status: SHIPPED**

`ok-gobot skills list|install|remove|audit|history|rollback`. Install accepts local path or git URL. Security audit rejects symlinks, scripts, pipe-to-shell patterns, and path-escaping links.

### 7. Operator can cap a background task's tool calls, duration, and model cost

**Status: PARTIALLY SHIPPED**

Jobs runtime supports timeout and cancellation. Full per-task budget caps (max tool calls, token limits) are not yet enforced. This is a Phase 5 priority.

### 8. Operator can enable a prebuilt role that runs on schedule and posts a report

**Status: SHIPPED**

Three prebuilt roles: `researcher`, `monitor`, `release-watch`. Enabled by setting `roles_path` and placing role files. Reports delivered to `auth.admin_id`.

### 9. Operator can define new roles declaratively without writing Go code

**Status: SHIPPED**

Markdown role manifests with YAML frontmatter. Fields: worker, tools, schedule, approval, report_template. Placed in `roles_path` directory.

## Future Work

### Budget and Policy Enforcement

Operators need per-role and per-job budget caps before autonomous scheduled runs can be considered safe for production use. Until this ships:

- Operators should review role manifests carefully.
- Use capability policies to restrict tool access.
- Monitor job execution via Mission Control API.

### Evolution Benchmarks

The self-evolution gate currently uses heuristic scoring. Full live agent benchmarking with reproducible test suites is planned.

### Mission Control UI

The Mission Control API endpoints are shipped (`/api/mission/*`). A web-based operator dashboard for role/job/schedule visibility is planned but not yet built.

## Non-Goals for Now

- Multi-channel expansion to match OpenClaw/OpenFang/ZeroClaw
- Desktop app or giant dashboard work
- WASM sandboxing
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible local benchmarks exist

## Short Version

Phase 1-4 are shipped. The next priorities are:

1. **Budget enforcement** -- per-role and per-job cost caps for safe autonomous operation
2. **Mission Control UI** -- operator dashboard for roles, jobs, schedules
3. **Evolution benchmarks** -- reproducible live agent testing for the evolution gate
