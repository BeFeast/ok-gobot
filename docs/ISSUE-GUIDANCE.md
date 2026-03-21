# Issue Guidance: Decompose by User Outcome

## Rule

Every feature issue must be phrased as a **user outcome**, not an infrastructure task.

v2.0 decomposed work by technical phase (wiring, sessions, Telegram UX, TUI, subagents). This built infrastructure without specifying the user-facing policy it should serve. The result: mechanisms existed but nobody defined when or why they should activate.

Going forward, at least one issue per feature must describe the behavior a user will observe.

## Examples

| Bad (infrastructure-first) | Good (outcome-first) |
|-|-|
| Implement sub-agent spawn API | When a tool call takes >20s, auto-spawn it as a subagent and notify the user |
| Add queue modes | User can interrupt the bot mid-operation and get an immediate response |
| Build capability policy engine | Operator can restrict an agent to read-only tools without editing Go code |

## Template

Every feature issue should follow this structure:

```
## User Story
As a [role], I want [behavior] so that [benefit].

## Acceptance Criteria
- [ ] [Observable behavior 1]
- [ ] [Observable behavior 2]

## Default Behavior
[What happens out of the box, and why this default is correct]
```

The GitHub issue templates in `.github/ISSUE_TEMPLATE/` enforce this structure for features and bugs. Blank issues remain enabled so contributors can open process, research, and other non-template issues.

## Why This Matters

- **Acceptance criteria become testable.** "User sees a notification within 2s" is verifiable; "implement notification system" is not.
- **Scope stays bounded.** Outcome framing prevents infrastructure from growing beyond what the user needs.
- **Priority is clearer.** Outcomes can be ranked by user impact; infrastructure tasks cannot.
