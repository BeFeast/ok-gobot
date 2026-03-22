# Process: Default Behavior for Configurable Features

## Problem

Queue modes were added in a grab-bag commit (`a5bca2d`, Jan 29 2026) with
`collect` as the default. No issue tracked that default choice. The wrong
default shipped and went unnoticed for 6 weeks until it was corrected to
`interrupt`.

Burying default choices inside implementation commits makes them invisible
to review and impossible to find later when the default turns out to be wrong.

## Rule

When a feature has modes, options, or configurable behavior:

1. Create a **separate issue** titled **"Set default X to Y"** with rationale.
2. The issue body must answer: **"Why is this default correct for the project
   goals?"**
3. Do not bury default choices in implementation commits.

The issue does not need to block the implementation PR, but the default must
be an explicit, reviewable decision — not a side-effect of whatever value
happened to appear first in an enum.

## PR Review Checklist

Every PR that adds or changes a configurable option must satisfy:

- [ ] Does this PR add or change a configurable option?
- [ ] Is the default value documented and justified?
- [ ] Is there a separate issue tracking the default choice?

The checklist is enforced via `.github/PULL_REQUEST_TEMPLATE.md`.

## Inventory of Current Defaults

All configurable options with modes/enums as of the canonical config in
`docs/ARCHITECTURE.md` Section 9, plus runtime defaults in code.

| Option | Default | Modes | Rationale |
|---|---|---|---|
| `ai.provider` | `openrouter` | openrouter, openai, anthropic, chatgpt, droid, custom | Widest model access for a single key |
| `ai.droid.auto_level` | `""` (off) | low, medium, high | Conservative: no autonomy unless operator opts in |
| `auth.mode` | `open` | open, allowlist, pairing | Lowest friction for single-operator setup |
| `session.dm_scope` | `main` | main, per_user | Single shared session matches single-operator use case |
| `groups.default_mode` | `standby` | active, standby | Bot should not speak in groups unless explicitly activated |
| `tts.provider` | `openai` | openai, edge | Higher voice quality out of the box |
| `log_level` | `info` | debug, info, warn, error | Standard observability level |
| Queue mode | `interrupt` | collect, steer, interrupt | Most responsive UX — new message cancels stale run (fixed from original `collect` default) |
| estop | off (all tools enabled) | on, off | Operator starts with full capability; kill-switch is opt-in |
| `control.enabled` | `false` | true, false | Security: no network listener unless operator enables it |
| `api.enabled` | `false` | true, false | Security: no HTTP surface unless operator enables it |
| `memory.enabled` | `false` | true, false | Requires embeddings API key; off until configured |

Future features with modes must follow this process before merge.
