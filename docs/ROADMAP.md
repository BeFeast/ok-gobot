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
6. **Operator can install and audit third-party skills from the CLI** -- SHIPPED. `ok-gobot skills list|install|remove|audit`. Safety audit rejects symlinks, scripts, pipe-to-shell, and escaping links. Skill versioning with rollback.

### Phase 3: Productized Autonomy -- MOSTLY SHIPPED

7. **Operator can cap a background task's tool calls, duration, and model cost** -- PARTIALLY SHIPPED. Timeout and cancellation work. Full per-task budget caps (tool call count, model cost) deferred to Phase 5.
8. **Operator can enable a prebuilt role that runs on schedule and posts a report** -- SHIPPED. Four prebuilt roles: `researcher`, `monitor`, `release-watch`, `homelab-runbook`. Roles load from `roles_path`, run via cron, deliver bounded reports to admin.
9. **Operator can define new roles declaratively without writing Go code** -- SHIPPED. Markdown-first role manifests with YAML frontmatter: prompt, tools, schedule, report_template, approval mode.

---

## Active Phases

### Phase 4: Intelligence and Feedback Loops -- SHIPPED

10. **Multi-model routing by task type** -- SHIPPED. `[task:vision]`, `[task:coding]`, `[task:summarize]`, `[task:reasoning]` tags route to configured models. Fallback to default model.
11. **Automatic reflection after tool failures** -- SHIPPED. Tracks repeated failures, triggers analysis after threshold, suggests fixes based on error patterns.
12. **Self-evolution loop (A-Evolve inspired)** -- SHIPPED. Observe-Analyze-Evolve-Gate-Promote cycle with safety constraints: max 1 evolution/24h, human approval for >20% prompt diff, auto-rollback after 3 production failures.
13. **Skill utility scoring and routing** -- SHIPPED. Skills tracked by utility score; skill router selects relevant skills per query.

### Phase 5: Hardened Autonomy -- IN PROGRESS

14. Full per-task budget caps: max tool calls, max model cost, per-task model override.
15. Policy enforcement gateway: centralized pre-execution check that validates budget + capability policy before every tool call.
16. Formal vulnerability reporting process (`SECURITY.md` shipped with this docs refresh).
17. Audit log for all autonomous actions (tool calls, cron runs, evolution promotions) with tamper-evident storage.

### Phase 6: Mission Control v1 -- PLANNED

18. Mission Control dashboard page: roles, schedules, recent runs, daily stats in a single view.
19. Role health monitoring: alert when a scheduled role fails repeatedly or exceeds token budgets.
20. One-click role enable/disable from the dashboard and TUI.
21. Operator playbook system: composable multi-role workflows with dependency ordering.

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
- **Hermes provider** -- native Hermes tool call parser for local models (Ollama + hermes3).
- **`/btw` side queries** -- ask questions during active task execution without interrupting.

## Non-Goals for Now

- Multi-channel expansion to match OpenClaw/OpenFang/ZeroClaw
- Desktop app or giant dashboard work
- WASM sandboxing
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible local benchmarks exist

## Short Version

If only five things get done next, the order should be:

1. Per-task budget caps -- operators can set hard limits on tool calls and model cost
2. Policy enforcement gateway -- single choke point for all pre-execution checks
3. Audit log -- tamper-evident record of all autonomous actions
4. Mission Control dashboard -- single-page view of roles, schedules, and runs
5. Role health monitoring -- automated alerts for failing scheduled roles

That sequence completes the hardened-autonomy story before expanding the Mission Control surface.
