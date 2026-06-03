# Magic Demo: ok-gobot as an AI Employee from Telegram

The Magic Demo is the smallest end-to-end walkthrough that proves an operator
can drive ok-gobot like an AI employee from Telegram: register a role, dispatch
real work, watch a durable job complete, inspect the proof artifacts, and turn
a successful run into a reviewable skill draft.

This document is the operator-facing playbook for that flow. A maintainer should
be able to follow it without reading source code. Where the demo depends on a
role manifest that is not yet bundled, the manifest is supplied inline so the
demo is reproducible today.

> **Scope.** The demo intentionally stops at "skill draft saved". Skill install
> is always explicit and admin-gated — ok-gobot never self-installs the draft.
> The demo does not try to prove autonomous self-improvement and does not
> generate any video or Remotion artifacts.

## What the demo proves

- A role manifest visible from `/roles` becomes a durable job when an operator
  runs `/role_run`.
- The role-job runner executes the role through the standard agent runtime,
  produces a result, and writes proof artifacts to durable storage.
- The final status, summary, and artifacts are visible from Telegram via
  `/job <id>` and from CLI via `ok-gobot jobs inspect <id>`.
- A successful job can be distilled into a reviewable SKILL.md draft via
  `/skill_suggest <id>`. The draft is audited but never installed automatically.

## Local prerequisites

The demo runs against a normal ok-gobot install. Before starting, confirm:

| Requirement | Why | How to verify |
|---|---|---|
| Go 1.24+ with CGO enabled | Build/`go test ./...` for the smoke harness | `go version` |
| C compiler (`gcc`, Xcode CLT, or MinGW-w64) | Required for the SQLite driver | `cc --version` |
| `ok-gobot` binary on PATH | Driving CLI checks like `roles list`, `jobs inspect` | `ok-gobot version` |
| Telegram bot token from BotFather | Sending `/role_run` and `/job` | `ok-gobot config get telegram.token` |
| `auth.admin_id` set to your Telegram user ID | `/role_run` and `/skill_suggest` are admin-only | `ok-gobot config get auth.admin_id` |
| Configured AI provider key or OAuth | Lets the role actually call a worker | `ok-gobot doctor` |
| Google Chrome / Chromium on PATH | `frontend_verify` and `browser` tool need a real browser | `ok-gobot doctor` (Chrome check) |
| Writable artifact root | Proof files must land somewhere ok-gobot can read back | `ls ~/.ok-gobot/screenshots ~/.ok-gobot/artifacts` |
| Optional: `node`/`npm` or `bun` on PATH | Only required if the demo role builds a real frontend prototype locally | `node --version && npm --version` |

Run `ok-gobot doctor` first. The command validates config, the Telegram token,
the AI provider, the storage path, and optional dependencies. Resolve any red
items before continuing.

### Artifact root configuration

Local file previews (Mission Control, role-job notifications, and the
artifact preview endpoint) are restricted to a configured allowlist of roots.
Defaults are `~/.ok-gobot/screenshots` and `~/.ok-gobot/artifacts`. To add
project-specific roots, edit `config.yaml`:

```yaml
artifacts:
  roots:
    - "~/.ok-gobot/screenshots"
    - "~/.ok-gobot/artifacts"
    - "~/proof-artifacts"
```

Paths outside these roots are redacted from API responses, hidden from
Telegram final notifications, and rejected by `frontend_verify`'s screenshot
writer. The demo does not require committing any frontend build output into the
ok-gobot repo — proof lives under the operator's artifact roots, not in source
control.

### Network allowlist

If your agent profile uses `capabilities.network_allowlist`, make sure the
hosts the demo role needs (for example, `127.0.0.1`, `localhost`,
`api.openai.com`, `openrouter.ai`, or any image CDN you want to screenshot)
are listed. Denials look like:

```
network access to <host> is blocked by the agent's network_allowlist
```

The remediation is to add the host (or `allow_internal_networks: true` for
loopback prototypes) to the agent's capability policy.

## Step 0 — Confirm the `prototype-builder` role is registered

`prototype-builder` ships bundled
(`internal/role/prebuilt/prototype-builder.md`). No manual install step is
required: `/roles` and `ok-gobot roles list` both surface it as soon as the
bot starts. The bundled manifest uses `worker: standard`, tools `file`,
`patch`, `local`, `frontend_verify`, `message`, `max_tool_calls: 25`, and
`approval: auto`, so dangerous shell commands continue to flow through the
existing policy/approval gate.

Customise it by copying the bundled manifest into your `roles_path` (your
copy shadows the bundled version; the bundled template is never modified):

```bash
cp internal/role/prebuilt/prototype-builder.md ~/ok-gobot-roles/
```

Typical edits worth making for the demo:

- tighten `tools` to the minimum your operator workspace needs;
- lower `max_tool_calls` to bound cost;
- set an explicit `max_duration` if you want shorter timeboxes;
- adjust the `report_template` to match how your team scans Telegram updates.

## Step 1 — `/roles`

Send `/roles` from your admin Telegram chat. Expected output (other roles
omitted for brevity):

```
Available Roles:

prototype-builder — worker: standard
researcher — worker: standard
...
```

The bot lists every manifest from `roles_path` plus the bundled defaults.

## Step 2 — `/role_run prototype-builder Build a blue 3D rocket launch simulator`

Dispatch the role with a concrete brief:

```
/role_run prototype-builder Build a blue 3D rocket launch simulator
```

The bot replies with a job acknowledgement that includes the new job ID and
worker tier:

```
Role job started: `job-2026...`
Role: `prototype-builder`
Worker tier: `standard`
Use `/job job-2026...` to inspect.
```

Behind the scenes the bot calls `rolejob.AgentJobRunner`, which:

- Builds the agent task from the role prompt plus your input.
- Submits a run through the agent `RuntimeHub` under an isolated session key.
- Streams tool events and persists `frontend_verify` screenshots and text
  reports as job artifacts.
- Records a terminal status (`succeeded`, `failed`, `timed_out`, or
  `cancelled`) and a bounded summary.

## Step 3 — `/job JOB_ID`

Inspect the job at any time:

```
/job job-2026...
```

While running you'll see the 🏃 icon, attempt count, and (if available) recent
evidence events. On completion the bot updates the same chat with a final
notification containing the bounded summary and proof-artifact hints. Hints
that point at unsafe paths are redacted to "hidden" rather than leaked.

You can also inspect from the CLI:

```bash
ok-gobot jobs inspect <job-id>
ok-gobot jobs tail   <job-id>
```

## Step 4 — `/skill_suggest JOB_ID`

When the job succeeded and produced useful artifacts, distill a reusable
skill draft:

```
/skill_suggest job-2026...
```

The bot writes a `SKILL.md` under
`<soul>/skill-drafts/<draft-id>/<skill-name>/`, runs a static safety audit,
and replies with one of:

- `✅ Skill draft saved` — audit clean. Next step is human review.
- `⚠️ Skill draft saved but audit failed` — the draft is still on disk so you
  can edit and re-audit it.

Install is always a deliberate second step:

```bash
ok-gobot skills audit <soul>/skill-drafts/<draft-id>/<skill-name>
ok-gobot skills install <soul>/skill-drafts/<draft-id>/<skill-name>
```

The bot never installs a suggested skill on its own.

## Troubleshooting

### `Role "prototype-builder" not found`

`roles_path` is unset or the manifest didn't load.

- Check `ok-gobot config get roles_path`.
- Run `ok-gobot roles list` — if `prototype-builder` is missing, the file
  failed to parse. Look at startup logs for `[roles] skipping invalid manifest`
  entries.
- Reload after editing: `/reload` (admin only) or restart the bot.

### Browser/`frontend_verify` errors

- "chrome not found" / "exec: chromedp": install Chrome or Chromium. The
  `ok-gobot doctor` Chrome/Chromium check lists the locations it searches.
- "context deadline exceeded" while screenshotting: the role hit its
  `max_duration` (5 minutes by default). Either raise the manifest's
  `max_duration` or narrow the brief.
- "screenshot path is outside artifact_roots": add the destination directory
  to `artifacts.roots` in `config.yaml`. `frontend_verify` refuses to write
  proofs into unconfigured roots so they remain previewable.

### Missing package managers

The bundled `prototype-builder` manifest above intentionally avoids global
installs. If your variant needs `npm`/`bun`/`pnpm` and the role logs
"command not found", install the tool on the host or revise the role brief
to use only what the workspace already has. ok-gobot does not auto-install
package managers.

### Network allowlist blocks

Errors that mention "blocked by the agent's network_allowlist" point at the
agent capability policy. Either:

- Add the host(s) to `capabilities.network_allowlist`, or
- Set `capabilities.allow_internal_networks: true` for `127.0.0.1`/`localhost`
  prototypes only.

Both edits are per-agent in `config.yaml`.

### Empty proof in `/job` output

The runner only records `frontend_verify` artifacts when the tool returns a
parseable JSON payload containing a screenshot path or text report. If your
role uses a different verification tool, persist the artifact yourself via
the role's worker output rather than relying on automatic extraction.

### `/skill_suggest` says "require succeeded jobs"

`/skill_suggest` only operates on terminal `succeeded` jobs to avoid
distilling failures into reusable patterns. Re-run the role with a narrower
brief and try again on the new job ID.

## Acceptance harness

A focused smoke test exercises the end-to-end control path with fakes:

```bash
go test ./internal/rolejob/ -run MagicDemo
```

The harness wires a fake agent runtime that mirrors what
`/role_run prototype-builder` triggers in production:

1. Dispatches a role job via `rolejob.AgentJobRunner`.
2. Returns a final result with a `frontend_verify` tool event that includes a
   real screenshot file under a temporary artifact root.
3. Waits for the durable job to reach `succeeded`, then asserts the summary,
   the screenshot/text/url artifacts, and the lifecycle event trail are all
   visible.
4. Hands the finished job to `bootstrap.SuggestSkillFromJob` to confirm the
   skill-distillation step succeeds and writes an audited draft to disk —
   without installing it.

The harness uses fakes for the agent runtime and storage so it can prove the
control surface without booting a Telegram client, a real LLM, or a real
browser. CI runs the same test as part of `go test ./...`.
