---
name: issue-intake
description: Promote a refined idea into a configured Git forge issue and a parallel Obsidian spec note.
---

# Issue Intake

Use this only after the project, scope, and desired outcome have been clarified in normal conversation.

## Required deployment configuration

Project routing and the issue backend must be deployment-owned rather than embedded in this public skill. Read them from the workspace's `TOOLS.md` or an operator-maintained `issue-intake-routing.yaml`. Each route must define:

- project alias;
- forge provider (`forgejo` or `github`);
- repository owner/name;
- vault-relative project home;
- a fixed issue-submit wrapper or documented CLI invocation.

If no matching route or submit command is configured, stop and report the missing configuration. Do not guess a repository, hostname, token, or vault path.

## Workflow

1. Compose an English issue title in the form `<area>: <short imperative outcome>`.
2. Compose the body with `Problem`, `Desired outcome`, `Acceptance criteria`, and `Affected surfaces`. Acceptance criteria must be independently verifiable and include the repository's normal CI gate.
3. Remove secrets, private hostnames, IPs, usernames, and filesystem paths from any issue that may be externally visible. Never use auto-closing keywords; use neutral references such as `Refs #N`.
4. Select labels from the configured route. A ready work item may use its configured ready label; an epic or draft must use the configured parked/blocking label and must not enter an automatic worker queue.
5. Submit through the fixed configured wrapper/CLI using the `local` tool. `local` is allowed only when its fail-closed Telegram approval callback is active; the operator must approve the invocation. Never interpolate a token into the command or reply.
6. Write a vault-relative spec note through the `obsidian` tool with `type: spec`, promotion status, issue URL/number, project tags, a Russian summary, and the promotion record.
7. If issue submission fails, still save a `status: draft` note without an issue number. If the vault write fails after submission, report the exact vault-relative target that failed.
8. Reply with the issue URL, vault-relative note path, labels, and whether automation may pick it up.
