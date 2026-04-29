# Process Rules

## 1. Decompose Features by User Outcome

Every feature must have at least one issue phrased as a user outcome.
Infrastructure work is fine as a sub-task, but the parent issue must describe
what a user can observe when it ships.

### Why

v2.0 decomposed 23 issues by technical phase (wiring, sessions, Telegram UX,
TUI, subagents). The subagent mechanism shipped, but no issue ever said "the
model should use /task for long operations" or "tools should auto-spawn on
timeout." Infrastructure was built; policy was never specified. The result was
working plumbing with no defined user-facing behavior.

### Rule

| Bad (infrastructure-layer) | Good (user-outcome) |
|---|---|
| Implement sub-agent spawn API | When a tool call takes >20 s, auto-spawn it as a subagent and notify the user |
| Add queue modes | User can interrupt the bot mid-operation and get an immediate response |
| Extend AgentConfig into capability policy | Operator can restrict an agent to read-only tools without editing Go code |
| Enforce policy at tool execution boundaries | Denied tool calls show a clear reason in Telegram instead of silently failing |

### Template

Use the GitHub issue template at `.github/ISSUE_TEMPLATE/feature.md`:

```
## User Story
As a [role], I want [behavior] so that [benefit].

## Acceptance Criteria
- [ ] [Observable behavior 1]
- [ ] [Observable behavior 2]

## Default Behavior
[What happens out of the box, and why this default is correct.]
```

### Checklist Before Filing a Feature Issue

1. **Title** names a user-visible outcome, not an internal component.
2. **User Story** identifies a role (operator, end-user, developer) and a
   concrete behavior.
3. **Acceptance Criteria** are observable — a tester can verify each one
   without reading source code.
4. **Default Behavior** states what happens if the operator does zero
   configuration.
5. Infrastructure sub-tasks may reference internal components, but each must
   link back to the parent outcome issue.

## 2. Prebuilt Role Conventions

When adding or modifying prebuilt roles in `internal/role/prebuilt/`:

1. **Markdown-first.** Roles are plain `.md` files with YAML frontmatter — no
   hardcoded Go branches or switch statements.
2. **Disabled by default.** Bundled roles are templates. They are only active
   when an operator copies them to `roles_path` and the bot loads them on
   startup.
3. **Explicit tool allowlist.** Every role must set `tools:` to the minimum set
   it needs. Never leave it empty (which means "all tools allowed").
4. **Budget required.** Every role must set `budget:` to cap tool calls per run.
5. **Report template required.** Every role must include a `report_template:`
   so output is formatted consistently for Telegram delivery.
6. **Tests.** `internal/role/bundled_test.go` verifies that all prebuilt roles
   parse successfully, have budgets, have report templates, have tool
   restrictions, and render their templates without error.
