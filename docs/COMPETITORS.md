# Competitive Landscape: OpenFang, ZeroClaw, OpenClaw, and ok-gobot

Snapshot date: March 12, 2026.

This document compares two newer Rust competitors, [OpenFang](https://github.com/RightNow-AI/openfang) and [ZeroClaw](https://github.com/zeroclaw-labs/zeroclaw), against [OpenClaw](https://github.com/openclaw/openclaw) and this project, [ok-gobot](../README.md).

Important caveat: external benchmark, security-layer, and channel-count claims are mostly self-reported in the competitors' own READMEs and docs. Treat them as product-positioning signals until reproduced locally.

## Executive Summary

- **OpenFang** is the most aggressively productized "agent OS" play. Its core pitch is autonomous, scheduled, pre-built capability packs ("Hands"), wide channel coverage, and a security-heavy Rust kernel.
- **ZeroClaw** is the leanest runtime/infrastructure play. Its core pitch is very small binary/runtime cost, swappable providers/channels/tools/memory, and a secure-by-default Rust base.
- **OpenClaw** is still the broadest personal-assistant product surface. Its strength is the combination of messaging channels, desktop/mobile nodes, voice, Canvas, wizard-driven onboarding, and a mature-looking ecosystem surface.
- **ok-gobot** is the most opinionated and narrow. Its strength is a practical Telegram-first operator workflow, low deployment friction, Go simplicity, and direct integration with external coding-agent CLIs.

## Repo Snapshot

| Project | Language | Current thesis | GitHub snapshot |
|---------|----------|----------------|-----------------|
| **OpenFang** | Rust | Autonomous agent OS with bundled "Hands" and dashboard | Created February 24, 2026. 13.8k stars / 1.6k forks. Dual-license in Cargo manifest (`MIT OR Apache-2.0`); GitHub metadata reports Apache-2.0. |
| **ZeroClaw** | Rust | Fast, small, fully swappable assistant infrastructure | Created February 13, 2026. 26.2k stars / 3.4k forks. Dual-license in Cargo manifest (`MIT OR Apache-2.0`); GitHub metadata reports Apache-2.0. |
| **OpenClaw** | TypeScript | Personal AI assistant across channels, apps, and devices | Created November 24, 2025. 303.7k stars / 57.4k forks. MIT. |
| **ok-gobot** | Go | Fast single-binary Telegram bot and OpenClaw rewrite with opinionated defaults | Local project. README positions it as a Go rewrite of OpenClaw. |

## Comparison Matrix

| Dimension | OpenFang | ZeroClaw | OpenClaw | ok-gobot |
|-----------|----------|----------|----------|----------|
| **Primary product idea** | Agent OS with built-in autonomous workloads | Runtime OS for agent workflows | Personal assistant control plane | Telegram-first personal/operator bot |
| **Runtime / packaging** | Rust workspace, one binary, desktop app, Docker | Rust single binary, size-first build profile, optional feature flags | Node 22+, pnpm workspace, UI + apps + extensions | Go single binary, simple build/install, launchd/systemd |
| **Default interaction model** | Activate prebuilt "Hands" that run for you | Run agent/gateway/daemon/channels as composable infra | Talk to one assistant over many channels/devices | Talk to one or more agents through Telegram, TUI, or API |
| **Autonomy** | Strongest. Built around scheduled, continuous, proactive agents | Moderate. Has daemon, cron, channels, estop, but less productized autonomy | Moderate. Strong automation/webhooks/cron, but framed as assistant surfaces | Moderate. Has cron, sub-agents, queue modes, compaction, but not an autonomous OS |
| **Channels** | Claims ~40 adapters across chat/social/enterprise platforms | Broad multi-channel matrix with compile-time toggles | Very broad messaging + device node footprint | Telegram only |
| **Apps / UI surfaces** | Dashboard, TUI, desktop app | Gateway/daemon/web assets, more infra-oriented | Control UI, WebChat, macOS app, iOS node, Android node, Canvas, voice | Telegram UI, TUI, WebSocket control protocol, REST API, simple web UI |
| **Multi-agent model** | First-class manifests, capabilities, agent spawn | Agent runtime plus provider/model switching and integrations | Multi-agent routing per channel/account/peer | Multi-agent profiles plus isolated sub-agent runs |
| **Memory model** | SQLite + vector memory with confidence decay | Configurable storage/memory posture, SQLite/Postgres options, multiple memory modes | Session-centric assistant runtime with skills/extensions ecosystem | Markdown-first memory plus SQLite semantic index and optional memory MCP |
| **Tool / extension story** | 53 tools, MCP, A2A, WASM modules, FangHub | Skills, integrations, provider/channel/tool swapping, security audit on install | Skills platform, plugin SDKs, browser/canvas/node tooling | Built-in local/ssh/file/patch/search/browser/media/memory tools; CLI-agent transports; skill-like workspace files |
| **Security posture** | Most explicit and systematized: capabilities, WASM sandbox, audit chain, taint tracking, manifest signing | Strong infra controls: allowlists, pairing, workspace scoping, estop, sandbox feature flags, skill audit | Sensible assistant safety defaults, DM pairing, remote access controls, but heavier and less isolation-centric | Practical controls: approvals, DM auth modes, SSRF protection, rate limiting, redaction; less formal sandboxing |
| **Operational posture** | Broadest promise, highest complexity | Leanest runtime and strongest low-resource story | Broadest ecosystem and surface area, highest Node complexity | Simplest scope and lowest cognitive overhead for one operator |
| **Migration stance** | Explicitly migrates from OpenClaw | Explicitly migrates from OpenClaw | Ecosystem source project others migrate away from | Explicitly migrates from OpenClaw DB/workspace |

## OpenFang vs ZeroClaw

These two projects are both "Rust answers to OpenClaw", but they attack from different directions.

- **OpenFang** attacks from the top of the stack. It wants to be the finished product: bundled agent packs, dashboard, desktop app, migrations, market-style distribution, capability model, and a strong "agents that work for you" story.
- **ZeroClaw** attacks from the bottom of the stack. It wants to be the runtime substrate: small binary, low RAM, trait-driven architecture, provider/channel flexibility, compile-time feature control, and safety knobs for operators.
- If you care about **product packaging and autonomous workflows**, OpenFang is closer.
- If you care about **runtime minimalism and infrastructure composability**, ZeroClaw is closer.

## How They Compare to OpenClaw

OpenClaw is still the center of gravity in this niche.

- All three challengers define themselves partly against OpenClaw.
- OpenFang and ZeroClaw both publish benchmark-style comparisons against OpenClaw in their READMEs.
- OpenFang, ZeroClaw, and ok-gobot all ship explicit migration paths from OpenClaw.

That usually means OpenClaw remains the best-known reference architecture for:

- multi-channel assistant delivery
- local-first control plane
- session routing
- onboarding and operational UX
- skills/plugins as ecosystem surface

Where the challengers differentiate:

- **OpenFang** says OpenClaw is not autonomous enough.
- **ZeroClaw** says OpenClaw is too heavy and too coupled.
- **ok-gobot** says OpenClaw is broader than necessary for a Telegram-first operator bot and too operationally heavy for that narrower use case.

## How They Compare to ok-gobot

### Where ok-gobot is stronger

- **Sharper scope**: Telegram-only is a limitation, but it also keeps the product coherent.
- **Operational simplicity**: single Go binary, straightforward config, small surface area.
- **Practical coding-agent bridge**: using Claude Code, Codex, Gemini CLI, Droid, or OpenCode as backends is a concrete differentiator.
- **Markdown-first workspace**: `IDENTITY.md`, `SOUL.md`, `USER.md`, `AGENTS.md`, `TOOLS.md`, `MEMORY.md` is easy to understand and hack.
- **Faster path from OpenClaw to a personal bot**: the project has a focused migration command and a simpler target runtime.

### Where ok-gobot is weaker

- **Channel breadth**: it does not compete with OpenClaw/OpenFang/ZeroClaw on messaging footprint.
- **Productized autonomy**: it does not yet have the "activate a pre-built autonomous worker" story that OpenFang pushes.
- **Formal isolation/security**: it has practical safety controls, but not the kind of capability/sandbox architecture marketed by OpenFang and ZeroClaw.
- **Broader app surface**: it does not currently match OpenClaw's device-node, Canvas, and voice ecosystem.

## Recommended Positioning for ok-gobot

If you want ok-gobot to stay differentiated, the best angle is not "we do everything OpenClaw does, but in Go."

Better position it as:

- **the small, hackable, Telegram-first OpenClaw descendant**
- **the pragmatic single-binary operator bot**
- **the easiest bridge between chat ops and modern coding-agent CLIs**

That framing avoids direct head-on competition where the others are stronger:

- do not over-claim on channel count
- do not over-claim on sandbox/security architecture
- do not over-claim on autonomous packaged workflows

Instead, lean into:

- low friction
- fast startup
- understandable architecture
- opinionated defaults
- OpenClaw migration compatibility
- coding/ops usefulness over platform maximalism

## Bottom-Line Read

- Pick **OpenFang** if you want the most ambitious all-in autonomous-agent product story.
- Pick **ZeroClaw** if you want the leanest and most swappable Rust runtime substrate.
- Pick **OpenClaw** if you want the richest assistant surface across channels, devices, and UX layers.
- Pick **ok-gobot** if you want the most direct, small, Telegram-centric operator assistant with a clean path to external coding-agent backends.

## Sources

- [OpenFang repository](https://github.com/RightNow-AI/openfang)
- [OpenFang README](https://raw.githubusercontent.com/RightNow-AI/openfang/main/README.md)
- [OpenFang example config](https://raw.githubusercontent.com/RightNow-AI/openfang/main/openfang.toml.example)
- [OpenFang migration guide](https://raw.githubusercontent.com/RightNow-AI/openfang/main/MIGRATION.md)
- [OpenFang security guide](https://raw.githubusercontent.com/RightNow-AI/openfang/main/docs/security.md)
- [ZeroClaw repository](https://github.com/zeroclaw-labs/zeroclaw)
- [ZeroClaw README](https://raw.githubusercontent.com/zeroclaw-labs/zeroclaw/master/README.md)
- [ZeroClaw channels reference](https://raw.githubusercontent.com/zeroclaw-labs/zeroclaw/master/docs/reference/api/channels-reference.md)
- [ZeroClaw providers reference](https://raw.githubusercontent.com/zeroclaw-labs/zeroclaw/master/docs/reference/api/providers-reference.md)
- [ZeroClaw commands reference](https://raw.githubusercontent.com/zeroclaw-labs/zeroclaw/master/docs/reference/cli/commands-reference.md)
- [OpenClaw repository](https://github.com/openclaw/openclaw)
- [OpenClaw README](https://raw.githubusercontent.com/openclaw/openclaw/main/README.md)
- [ok-gobot README](../README.md)
- [ok-gobot Features](./FEATURES.md)
- [ok-gobot API](./API.md)
- [ok-gobot Architecture](./ARCHITECTURE.md)
- [ok-gobot Memory](./MEMORY.md)
