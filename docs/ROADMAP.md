# ok-gobot Roadmap

Last updated: April 29, 2026.

This roadmap turns the findings from [Competitive Landscape](./COMPETITORS.md) into an implementation backlog for `ok-gobot`.

The goal is not to copy OpenFang or ZeroClaw wholesale. The goal is to borrow the parts that improve `ok-gobot` without destroying its main advantage: a small, understandable, Telegram-first single-binary operator bot.

Every backlog item is phrased as a user outcome per [PROCESS.md](./PROCESS.md). Infrastructure sub-tasks may reference internals, but the parent item describes what a user can observe when it ships.

---

## Shipped Backlog

The original Phase 1-3 items are all shipped or substantially shipped. This section records their final status.

### Phase 1: Quick Wins -- SHIPPED

1. **Operator can kill dangerous tools instantly without restarting the bot** -- SHIPPED. `/estop on|off|status` + CLI `ok-gobot estop on|off|status`. Blocked tools return a user-visible reason.
2. **User can list available models and see which providers are healthy** -- SHIPPED. `ok-gobot providers`, `ok-gobot models`, model catalog with 24h cache.
3. **Operator gets a detailed report after migrating from OpenClaw** -- SHIPPED. `ok-gobot migrate` with markdown report, `--dry-run`, backup path.

### Phase 2: Safety and Extensibility Foundation -- SHIPPED

4. **Operator can restrict an agent to read-only tools without editing Go code** -- SHIPPED. Per-agent `capabilities` block in config: `shell`, `network`, `network_allowlist`, `cron`, `memory_write`, `spawn`, `filesystem_roots`, `file_write_scope`.
5. **Denied tool calls show a clear reason in Telegram instead of silently failing** -- SHIPPED. Consistent "Tool [name] blocked: [reason]" messages across Telegram, TUI, and API.
6. **Operator can install and audit third-party skills from the CLI** -- SHIPPED. `ok-gobot skills list|install|remove|audit|history|rollback`. Safety audit rejects symlinks, scripts, pipe-to-shell, and escaping links. Skill versioning with rollback.

### Phase 3: Productized Autonomy -- MOSTLY SHIPPED

7. **Operator can cap a background task's tool calls, duration, and model cost** -- PARTIALLY SHIPPED. Tool-call limits, duration timeouts, cancellation, and `budget_exceeded` job state are implemented. Token/cost accounting and enforcement remain Phase 5 work.
8. **Operator can enable a prebuilt role that runs on schedule and posts a report** -- SHIPPED. Four prebuilt role templates: `researcher`, `monitor`, `release-watch`, `homelab-runbook`. Roles copied into `roles_path` run via cron and deliver bounded reports to admin.
9. **Operator can define new roles declaratively without writing Go code** -- SHIPPED. Markdown-first role manifests with YAML frontmatter: prompt, tools, schedule, report_template, approval mode.

---

## Active Phases

### Phase 4: Intelligence and Feedback Loops -- SHIPPED

10. **Multi-model routing by task type** -- SHIPPED. `[task:vision]`, `[task:coding]`, `[task:summarize]`, `[task:reasoning]` tags route to configured models. Fallback to default model.
11. **Automatic reflection after tool failures** -- SHIPPED. Tracks repeated failures, triggers analysis after threshold, suggests fixes based on error patterns.
12. **Self-evolution loop (A-Evolve inspired)** -- SHIPPED. Observe-Analyze-Evolve-Gate-Promote cycle with safety constraints: max 1 evolution/24h, human approval for >20% prompt diff, auto-rollback after 3 production failures.
13. **Skill utility scoring and routing** -- SHIPPED. Skills tracked by utility score; skill router selects relevant skills per query.

### Phase 5: Hardened Autonomy -- IN PROGRESS

14. **Token/cost budget enforcement** -- IN PROGRESS. Role manifests and delegated-run contracts carry `max_tokens` and `max_cost_usd`, but runtime enforcement is not complete.
15. **Policy enforcement gateway** -- IN PROGRESS. Per-agent capability policy and tool-call limits are shipped; the remaining work is a centralized pre-execution check that combines budget, capability, and audit policy before every autonomous action.
16. **Formal vulnerability reporting process** -- SHIPPED. Root [SECURITY.md](../SECURITY.md) now documents reporting, threat model, safe defaults, and hardening checklist.
17. **Audit log for all autonomous actions** -- PLANNED. Tool calls, cron runs, and evolution promotions should be recorded with tamper-evident storage.

### Phase 6: Mission Control v1 -- PARTIALLY SHIPPED

18. Mission Control API: roles/profiles, schedules, recent runs, daily stats, estop, provider/model -- SHIPPED.
19. Mission Control dashboard page: roles, schedules, recent runs, daily stats in a single view -- PLANNED.
20. Role health monitoring: alert when a scheduled role fails repeatedly or exceeds enforced budgets -- PLANNED.
21. One-click role enable/disable from the dashboard and TUI -- PLANNED.
22. Operator playbook system: composable multi-role workflows with dependency ordering -- PLANNED.

---

## Recently Shipped (not in original roadmap)

These capabilities shipped outside the original Phase 1-3 plan:

- **Rules-first chat routing** -- incoming turns classified as reply, clarify, or background job before execution.
- **Agent lifecycle hooks** -- SessionStart, PreToolUse, PostToolUse, SessionEnd.
- **Session fork/branch** -- fork a session to explore alternatives without losing the original.
- **Voice/STT** -- Whisper API transcription for Telegram voice messages.
- **Frontend verify tool** -- CDP screenshot + LLM visual comparison for UI testing.
- **Parallel batch execution** -- `/batch` command fans out tasks in parallel.
- **PR babysitting** -- `ok-gobot babysit` auto-maintains PRs.
- **Auto-worktree management** -- isolated git worktrees per background task.
- **Export training data** -- `ok-gobot export` for fine-tuning datasets.
- **Hermes model parser** -- native Hermes tool call parser for local/OpenAI-compatible models (Ollama + hermes3).
- **`/btw` side queries** -- ask questions during active task execution without interrupting.
- **Mission Control API** -- authenticated HTTP endpoints for roles/profiles, schedules, runs, stats, estop, and provider/model state.
- **Root security policy** -- vulnerability reporting, threat model, safe defaults, and hardening checklist in [SECURITY.md](../SECURITY.md).

## Non-Goals for Now

- Multi-channel expansion to match OpenClaw/OpenFang/ZeroClaw
- Desktop app or giant dashboard work
- WASM sandboxing
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible local benchmarks exist

## Short Version

If only five things get done next, the order should be:

1. Token/cost budget enforcement -- operators can set hard limits beyond the shipped tool-call and duration caps
2. Policy enforcement gateway -- single choke point for all pre-execution checks
3. Audit log -- tamper-evident record of all autonomous actions
4. Mission Control dashboard -- single-page view of roles, schedules, and runs
5. Role health monitoring -- automated alerts for failing scheduled roles

That sequence completes the hardened-autonomy story before expanding the Mission Control surface.
