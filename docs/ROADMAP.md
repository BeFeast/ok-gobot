# ok-gobot Roadmap

Snapshot date: March 12, 2026.

This roadmap turns the findings from [Competitive Landscape](./COMPETITORS.md) into an implementation backlog for `ok-gobot`.

The goal is not to copy OpenFang or ZeroClaw wholesale. The goal is to borrow the parts that improve `ok-gobot` without destroying its main advantage: a small, understandable, Telegram-first single-binary operator bot.

Every backlog item is phrased as a user outcome per [PROCESS.md](./PROCESS.md). Infrastructure sub-tasks may reference internals, but the parent item describes what a user can observe when it ships.

## Sequencing

### Phase 1: Quick Wins

1. Operator can kill dangerous tools instantly without restarting the bot
2. User can list available models and see which providers are healthy
3. Operator gets a detailed report after migrating from OpenClaw

### Phase 2: Safety and Extensibility Foundation

4. Operator can restrict an agent to read-only tools without editing Go code
5. Denied tool calls show a clear reason in Telegram instead of silently failing
6. Operator can install and audit third-party skills from the CLI

### Phase 3: Productized Autonomy

7. Operator can cap a background task's tool calls, duration, and model cost
8. Operator can enable a prebuilt role that runs on schedule and posts a report
9. Operator can define new roles declaratively without writing Go code

## Backlog

### 1. Operator can kill dangerous tools instantly without restarting the bot

**User Story**
As an operator, I want to disable dangerous tool families (shell, SSH, browser,
cron, message, fetch) with a single command so that I can respond to incidents
without restarting the process or losing in-flight sessions.

**Why:** Highest-ROI safety feature. Gives operators a fast kill switch.

**Acceptance Criteria**
- [ ] `/estop on` disables dangerous tool families; `/estop off` re-enables them.
- [ ] Blocked tools return a user-visible reason in Telegram, TUI, and API.
- [ ] `/status` and the TUI show current estop state.
- [ ] CLI equivalent: `ok-gobot estop on|off|status`.

**Default Behavior**
Estop is off. All configured tools are available. This is correct because most
operators run the bot in a trusted environment and should not pay an opt-in cost
for normal operation.

**Likely files**
- `internal/cli/estop.go` (new)
- `internal/control/server.go`
- `internal/bot/approval.go`
- `internal/tools/tools.go`
- `internal/config/config.go`

### 2. User can list available models and see which providers are healthy

**User Story**
As a user, I want to see which models and providers are available so that I can
pick the right model for my task and diagnose failures without reading config files.

**Why:** Multi-provider usability. Users currently have no way to discover models
or understand why a request failed at the provider level.

**Acceptance Criteria**
- [ ] `ok-gobot providers` lists configured providers with health status.
- [ ] `ok-gobot models refresh` fetches and caches remote model catalogs.
- [ ] `ok-gobot doctor` distinguishes auth failure, endpoint failure, and model lookup failure.
- [ ] Model picker flows use cached catalogs when available.

**Default Behavior**
Provider list shows whatever is configured in `config.yaml`. Model catalog is
fetched on first `models refresh` and cached locally. No automatic background
fetching.

**Likely files**
- `internal/cli/providers.go` (new)
- `internal/cli/models.go` (new)
- `internal/cli/doctor.go`
- `internal/ai/client.go`

### 3. Operator gets a detailed report after migrating from OpenClaw

**User Story**
As an operator migrating from OpenClaw, I want a durable report of what was
copied, skipped, and deduplicated so that I can trust the migration and debug
problems without re-running it.

**Why:** Imports without an artifact are harder to trust and harder to debug.

**Acceptance Criteria**
- [ ] Every migration run emits a report (markdown or JSON) listing copied files, skipped sessions, duplicates, backup path, and key mapping.
- [ ] `--dry-run` prints the same categories as the real report.
- [ ] Failed migrations preserve enough detail for rollback and debugging.

**Default Behavior**
Report is written to `~/.ok-gobot/migration-report-<timestamp>.md` alongside
the database. `--dry-run` prints to stdout only.

**Likely files**
- `internal/cli/migrate.go`
- `internal/migrate/migrate.go`

### 4. Operator can restrict an agent to read-only tools without editing Go code

**User Story**
As an operator, I want to give an agent narrower permissions (no shell, no
network, read-only filesystem) through config so that I can run less-trusted
models safely without changing source code.

**Why:** `AllowedTools` is too coarse once you add more autonomy. Operators need
declarative, fine-grained capability control.

**Acceptance Criteria**
- [ ] Agent config accepts explicit permissions: shell, filesystem roots, network allowlists, cron, memory writes, sub-agent spawn.
- [ ] Existing `allowed_tools` configs still load without breakage.
- [ ] Runtime receives a structured policy object, not ad-hoc booleans.
- [ ] A restricted agent cannot escalate to a tool outside its policy.

**Default Behavior**
If no policy block is set, all tools in the agent's `allowed_tools` list remain
available (full backward compatibility). An empty `allowed_tools` still means
"all tools."

**Likely files**
- `internal/config/config.go`
- `internal/agent/registry.go`
- `internal/agent/resolver.go`
- `docs/ARCHITECTURE.md`

### 5. Denied tool calls show a clear reason in Telegram instead of silently failing

**User Story**
As a user, I want to see why a tool call was blocked so that I understand the
bot's limitations and can ask an operator to adjust permissions if needed.

**Why:** Capability policy is useless if denied actions are silent. Users lose
trust when the bot ignores requests without explanation.

**Acceptance Criteria**
- [ ] Every denied tool call returns a consistent, human-readable reason.
- [ ] The denial message appears in Telegram, TUI, and API responses.
- [ ] Tests cover both allowed and denied flows for `local`, `ssh`, `browser`, `web_fetch`, `search`, `cron`, and `message`.
- [ ] Policy checks survive direct tool invocation, not only agent-mediated runs.

**Default Behavior**
No tools are denied unless the operator configures capability policy or activates
estop. When a tool is denied, the user sees: "Tool [name] blocked: [reason]."

**Likely files**
- `internal/tools/tools.go`
- `internal/tools/browser_tool.go`
- `internal/tools/web_fetch.go`
- `internal/tools/search.go`
- `internal/tools/cron.go`
- `internal/tools/message.go`

### 6. Operator can install and audit third-party skills from the CLI

**User Story**
As an operator, I want to install, list, and remove skills from the CLI so that
I can extend the bot's capabilities without editing source code, and audit
installed skills for safety.

**Why:** `ok-gobot` already has the workspace shape for skills. A safe
install/audit path makes the extension model usable in practice.

**Acceptance Criteria**
- [ ] `ok-gobot skills list|install|remove|audit` works from the CLI.
- [ ] Install accepts a local path or git URL.
- [ ] Static safety audit rejects symlinks, scripts, pipe-to-shell patterns, and markdown links escaping the skill root.
- [ ] Installed skills are markdown-first and compatible with the workspace model.

**Default Behavior**
No skills installed out of the box. `skills list` shows an empty list.
`skills install` runs the safety audit before accepting; unsafe packages are
rejected with actionable error messages.

**Likely files**
- `internal/cli/skills.go` (new)
- `internal/tools/tools.go`
- `docs/INSTALL.md`

### 7. Operator can cap a background task's tool calls, duration, and model cost

**User Story**
As an operator, I want to set limits on background tasks (max tool calls, max
duration, model override) so that a runaway subagent cannot consume unbounded
resources or time.

**Why:** Subagents currently have no budget constraints. A stuck or recursive
subagent can exhaust API quota silently.

**Acceptance Criteria**
- [ ] Operator can set max tool calls, max duration, and model override per task.
- [ ] A task that hits its limit stops and returns a structured summary, not raw output.
- [ ] Failed or timed-out tasks produce understandable feedback in Telegram and TUI.
- [ ] Memory writes from subagents can be disabled by policy.

**Default Behavior**
Default budget: 50 tool calls, 5-minute wall-clock timeout. Model inherits from
the parent agent. Memory writes allowed unless the agent's capability policy
disables them.

**Likely files**
- `internal/agent/subagent.go`
- `internal/bot/task_command.go`
- `internal/control/server_tui.go`
- `internal/bot/subagent_notifier.go`

### 8. Operator can enable a prebuilt role that runs on schedule and posts a report

**User Story**
As an operator, I want to enable a prebuilt role (researcher, monitor,
release-watch) with minimal config so that useful autonomous workflows run on
schedule and post bounded, readable reports to Telegram.

**Why:** The value of autonomy features is prebuilt, useful workflows — not
platform infrastructure.

**Acceptance Criteria**
- [ ] At least 3 prebuilt roles ship: `researcher`, `monitor`, `release-watch`.
- [ ] A role can be enabled by adding its name to config — no Go code needed.
- [ ] Each role runs on schedule via existing cron infrastructure.
- [ ] Reports are bounded in length and formatted for Telegram readability.
- [ ] Roles respect capability policy and estop.

**Default Behavior**
No roles enabled out of the box. Enabling a role requires explicit config. Roles
run through existing cron + runtime infrastructure, not a second orchestration
system.

**Likely files**
- `internal/cron/scheduler.go`
- `internal/agent/subagent.go`
- `internal/bot/hub_handler.go`
- `internal/bot/subagent_notifier.go`

### 9. Operator can define new roles declaratively without writing Go code

**User Story**
As an operator, I want to define custom autonomous roles using a simple manifest
format (prompt, tools, schedule, output template) so that I can create new
workflows without modifying the bot's source.

**Why:** Hardcoded roles do not scale. Operators need a hackable, workspace-local
format for role definitions.

**Acceptance Criteria**
- [ ] Role manifests are defined in a lightweight declarative format stored in the workspace.
- [ ] A manifest specifies: prompt, tools, schedule, output template, and approval mode.
- [ ] Role outputs have a stable, structured report format.
- [ ] Memory writes from roles can be disabled by policy.

**Default Behavior**
No custom roles defined. Prebuilt roles (item 8) use the same manifest format
internally, serving as examples.

**Likely files**
- `internal/agent/memory.go`
- `internal/agent/registry.go`
- `internal/bot/hub_handler.go`
- `docs/FEATURES.md`

## Non-Goals for Now

- Multi-channel expansion to match OpenClaw/OpenFang/ZeroClaw
- Desktop app or giant dashboard work
- WASM sandboxing
- A generic "swap everything" runtime abstraction
- Benchmark marketing before reproducible local benchmarks exist

## Short Version

If only the first five things get done, the order should be:

1. estop — operator can kill dangerous tools instantly
2. provider/model catalog — user can see what's available and healthy
3. migration report — operator gets a trustworthy import artifact
4. capability policy — operator restricts agents through config
5. tool denial messages — users see why something was blocked

That sequence improves safety, operator UX, and extensibility without pushing `ok-gobot` into platform bloat.
