# AGENTS.md — BeFeast/ok-gobot

Code facts for agents. Process, runtime, release and deploy authority live in the private vault area `Dev/Areas/ok-gobot/AGENTS.md`; it wins on process. Development happens on Forgejo; the GitHub repository is a one-way mirror, so never open PRs or issues there.

## Purpose and layout

A Go Telegram agent runtime: command handling, durable jobs, roles, tools, memory and selected native workflows (`/video_summary`, `/youtube_karaoke`). The acceptance bar is OpenClaw workflow parity: a Telegram command produces the result or artifact. Architecture: `docs/ARCHITECTURE.md`; process rules: `docs/PROCESS.md`, `docs/PROCESS-DEFAULTS.md`.

```
cmd/ok-gobot/           main binary
cmd/gen-config-schema/  regenerates config.schema.json
internal/               agent, ai, bot, memory, tools, storage, runtime and the rest
web/                    embedded web UI
skills/                 markdown skill packages
docs/                   architecture, deploy, memory, process
.forgejo/workflows/     ci.yml (CI / Security), release.yml (tag builds)
```

## Commands

All Go commands need `GOFLAGS=-tags=sqlite_fts5` (the Makefile exports it; CI sets it). Without the tag, lexical memory search silently degrades to an unranked LIKE scan.

| What | Command | Where | Time |
|---|---|---|---|
| Build | `make build` (binary in `bin/`) | a build host or CI, not the agent host | CI job «Build and test» (build, vet, schema check, race and stress tests, full tests, memory eval): 211 s on `main`, 2026-11-11 |
| Test | `make test`, or `go test ./... -count=1` | same | (in the job above) |
| Format and modules | `test -z "$(gofmt -l .)"`, `go mod tidy` with no diff | anywhere with Go | not measured |
| Config schema | `make config-schema`, then commit `config.schema.json` | anywhere with Go | not measured |
| Secret scan | `make secret-scan` (gitleaks with `.gitleaks.toml`) | anywhere | CI job «Secret scan»: 32–55 s |
| Vulnerability scan | `govulncheck ./...` | CI job «Vulnerability scan» | 62–76 s |

## Merge gate

Workflow `CI / Security` on the PR head: «Build and test», «Secret scan», «Vulnerability scan». No branch protection is configured. The PR template's default-behavior checklist applies to every new or changed configurable option. One async review by another model family for data-loss, install/update, signing or security risk (tool permissions, shell access, auth).

## Hard rules

- `config.schema.json` is generated: change the config structs and regenerate, never hand-edit.
- A second user message interrupts the active run and gets an immediate answer; never buffer it behind a long tool call.
- Releases are built only by `release.yml` from a tag that equals current `main`; no binaries built elsewhere are shipped.
- Every tracked file passes `internal/bootstrap/public_tree_privacy_test.go`.

## Public-repo hygiene

The repo is public and mirrored to GitHub. Never commit secrets, tokens, `.env` or runtime config with real values, personal names, internal hostnames, IP addresses or absolute personal paths. The privacy test and gitleaks enforce part of this; keep host-specific facts in the vault.
