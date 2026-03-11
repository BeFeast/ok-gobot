# ok-gobot Roadmap

Snapshot date: March 12, 2026.

This roadmap turns the findings from [Competitive Landscape](./COMPETITORS.md) into an implementation backlog for `ok-gobot`.

The goal is not to copy OpenFang or ZeroClaw wholesale. The goal is to borrow the parts that improve `ok-gobot` without destroying its main advantage: a small, understandable, Telegram-first single-binary operator bot.

## Sequencing

### Phase 1: Quick Wins

1. Emergency stop / safe mode
2. Provider and model catalog
3. OpenClaw migration report

### Phase 2: Safety and Extensibility Foundation

4. Capability policy per agent
5. Runtime policy enforcement in tools
6. Skills install and audit

### Phase 3: Productized Autonomy

7. Structured sub-agent contracts and budgets
8. Packaged autonomous roles
9. Role manifests and autonomous report loop

## Backlog

### 1. Add `estop` and Safe Mode

**Why:** This is the highest-ROI idea to borrow from ZeroClaw. It gives operators a fast kill switch without redesigning the whole runtime.

**Scope**
- Add a runtime stop-state that can disable tool families: `local`, `ssh`, `browser`, `cron`, `message`, and networked fetch/search.
- Expose CLI commands such as `ok-gobot estop on|off|status`.
- Expose admin controls via Telegram and the control/TUI surface.
- Make blocked tools fail fast with a clear explanation instead of silently no-oping.

**Likely files**
- [internal/cli/root.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cli/root.go)
- `internal/cli/estop.go` (new)
- [internal/control/server.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/control/server.go)
- [internal/bot/approval.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/approval.go)
- [internal/tools/tools.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/tools.go)
- [internal/config/config.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/config/config.go)

**Acceptance criteria**
- An operator can disable dangerous execution paths without restarting the process.
- Blocked tools return a user-visible reason in Telegram, TUI, and API flows.
- `/status` or the TUI shows current `estop` state.

### 2. Add Provider and Model Catalog Commands

**Why:** This is the best near-term borrow from ZeroClaw's provider docs and commands. It improves multi-provider usability without changing the core product.

**Scope**
- Add `ok-gobot providers` and `ok-gobot models refresh`.
- Cache discovered models per provider.
- Show provider/model health in `doctor`.
- Reuse existing model alias support instead of inventing another abstraction layer.

**Likely files**
- [internal/cli/root.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cli/root.go)
- `internal/cli/providers.go` (new)
- `internal/cli/models.go` (new)
- [internal/cli/doctor.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cli/doctor.go)
- [internal/ai/client.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/ai/client.go)
- [internal/config/config.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/config/config.go)

**Acceptance criteria**
- Users can list supported providers and refresh remote model catalogs.
- `doctor` distinguishes auth failure, endpoint failure, and model lookup failure.
- Model picker flows use cached catalogs when available.

### 3. Write a Migration Report for `ok-gobot migrate`

**Why:** OpenFang is right about this UX detail. Imports without an artifact are harder to trust and harder to debug.

**Scope**
- Write a markdown or JSON report after migration.
- Include copied files, skipped sessions, duplicates, backup path, and canonical key mapping.
- Keep `--dry-run` output consistent with the saved report format.

**Likely files**
- [internal/cli/migrate.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cli/migrate.go)
- [internal/migrate/migrate.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/migrate/migrate.go)

**Acceptance criteria**
- Every migration run emits a durable report artifact.
- `--dry-run` prints the same categories of actions as the real report.
- Failed migrations still preserve enough detail for rollback/debugging.

### 4. Extend `AgentConfig` into Capability Policy

**Why:** This is the most valuable architectural idea from OpenFang. `AllowedTools` is too coarse once you add more autonomy.

**Scope**
- Extend agent config beyond `allowed_tools` with explicit permissions for shell, filesystem roots, network allowlists, cron, memory writes, and sub-agent spawn.
- Preserve current config compatibility so existing agents keep working.
- Keep the first version simple and declarative. Do not build a WASM sandbox.

**Likely files**
- [internal/config/config.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/config/config.go)
- [internal/agent/registry.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/registry.go)
- [internal/agent/resolver.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/resolver.go)
- [docs/ARCHITECTURE.md](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/docs/ARCHITECTURE.md)

**Acceptance criteria**
- An agent can be configured with narrower rights than another agent.
- Existing `allowed_tools` configs still load without breakage.
- Runtime components receive a structured policy object, not ad-hoc booleans.

### 5. Enforce Policy at Tool Execution Boundaries

**Why:** Capability policy is useless if it only filters registry wiring. The checks must live close to execution.

**Scope**
- Enforce policy in tool entry points, not just in agent selection logic.
- Add consistent denied-action errors.
- Cover at least `local`, `ssh`, `browser`, `web_fetch`, `search`, `cron`, and `message`.

**Likely files**
- [internal/tools/tools.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/tools.go)
- [internal/tools/browser_tool.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/browser_tool.go)
- [internal/tools/web_fetch.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/web_fetch.go)
- [internal/tools/search.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/search.go)
- [internal/tools/cron.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/cron.go)
- [internal/tools/message.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/message.go)

**Acceptance criteria**
- Tool denial behavior is consistent across all restricted tools.
- Tests cover both allowed and denied flows.
- Policy checks survive direct tool invocation paths, not only agent-mediated runs.

### 6. Add Skills Install and Audit

**Why:** This is the cleanest piece to borrow from ZeroClaw's skills story. `ok-gobot` already has the workspace shape for it.

**Scope**
- Add `skills list|install|remove|audit`.
- Support install from local path or git URL.
- Run a static safety audit before accepting a skill: reject symlinks, scripts, pipe-to-shell patterns, and markdown links escaping the skill root.
- Keep skills markdown-first and compatible with the current workspace model.

**Likely files**
- [internal/cli/root.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cli/root.go)
- `internal/cli/skills.go` (new)
- [internal/tools/tools.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/tools/tools.go)
- [docs/INSTALL.md](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/docs/INSTALL.md)

**Acceptance criteria**
- A skill can be installed from a local path or git source into the workspace.
- Unsafe skill packages are rejected with actionable error messages.
- Installed skills are discoverable and removable via CLI.

### 7. Add Structured Sub-Agent Contracts and Budgets

**Why:** Before shipping autonomous role packs, sub-agents need clearer boundaries.

**Scope**
- Add task types or templates instead of raw free-form sub-agent spawning only.
- Add explicit budgets: max tool calls, max duration, model override, and optional memory write permission.
- Normalize sub-agent results into structured summaries for Telegram and TUI.

**Likely files**
- [internal/agent/subagent.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/subagent.go)
- [internal/bot/task_command.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/task_command.go)
- [internal/control/server_tui.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/control/server_tui.go)
- [internal/bot/subagent_notifier.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/subagent_notifier.go)

**Acceptance criteria**
- Operators can set limits on a sub-agent run.
- Sub-agent outcomes are rendered as structured summaries instead of only raw text.
- Failed or timed-out sub-agents produce understandable operator feedback.

### 8. Ship Packaged Autonomous Roles

**Why:** This is the good part of OpenFang's `Hands` idea. The win is not platform theater; the win is prebuilt, useful workflows.

**Scope**
- Start with 3-5 roles that fit `ok-gobot` and Telegram:
- `researcher`
- `monitor`
- `release-watch`
- `competitor-watch`
- `inbox-triage`
- Roles should run through existing cron + runtime infrastructure, not through a second orchestration system.

**Likely files**
- [internal/cron/scheduler.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/cron/scheduler.go)
- [internal/agent/subagent.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/subagent.go)
- [internal/bot/hub_handler.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/hub_handler.go)
- [internal/bot/subagent_notifier.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/subagent_notifier.go)

**Acceptance criteria**
- A role can be enabled with minimal config.
- A role runs on schedule and posts a bounded, readable report back to Telegram.
- A role respects capability policy and `estop`.

### 9. Add Role Manifests and an Autonomous Report Loop

**Why:** Once the first packaged roles exist, they need a small declarative format and repeatable output contract.

**Scope**
- Add a lightweight role manifest format describing prompt, tools, schedule, output template, and approval mode.
- Store role definitions in the workspace so they stay hackable.
- Persist outputs to markdown memory only when explicitly allowed.

**Likely files**
- [internal/agent/memory.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/memory.go)
- [internal/agent/registry.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/agent/registry.go)
- [internal/bot/hub_handler.go](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/internal/bot/hub_handler.go)
- [docs/FEATURES.md](/Users/i065699/work/projects/personal/AI/cli-agents/ok-gobot/docs/FEATURES.md)

**Acceptance criteria**
- Roles are defined declaratively, not hardcoded one by one in Go.
- Role outputs have a stable report structure.
- Memory writes from roles can be disabled by policy.

## Non-Goals for Now

- Multi-channel expansion to match OpenClaw/OpenFang/ZeroClaw
- Desktop app or giant dashboard work
- WASM sandboxing
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible local benchmarks exist

## Short Version

If only the first five things get done, the order should be:

1. `estop`
2. provider/model catalog
3. migration report
4. capability policy
5. skills audit/install

That sequence improves safety, operator UX, and extensibility without pushing `ok-gobot` into platform bloat.
