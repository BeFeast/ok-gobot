# Canonical Database Schema (Phase B)

This document is the single source of truth for the Phase B schema.

## Goals

- Keep existing `sessions`/`session_messages` readable during rollout.
- Introduce canonical session-key tables for runtime hub + multi-surface delivery.
- Use a dedicated `session_routes` table (not `last_route` JSON in `sessions_v2`).
- Keep explicit `subagent_runs` tracking.

## Canonical Tables

```sql
CREATE TABLE sessions_v2 (
  session_key TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL DEFAULT 'default',
  parent_session_key TEXT NOT NULL DEFAULT '',

  state TEXT NOT NULL DEFAULT '',
  model_override TEXT NOT NULL DEFAULT '',
  think_level TEXT NOT NULL DEFAULT '',
  usage_mode TEXT NOT NULL DEFAULT 'off',
  verbose INTEGER NOT NULL DEFAULT 0,
  deliver INTEGER NOT NULL DEFAULT 0,

  queue_depth INTEGER NOT NULL DEFAULT 0,
  queue_mode TEXT NOT NULL DEFAULT 'collect',
  queue_debounce_ms INTEGER NOT NULL DEFAULT 1500,

  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  context_tokens INTEGER NOT NULL DEFAULT 0,

  message_count INTEGER NOT NULL DEFAULT 0,
  compaction_count INTEGER NOT NULL DEFAULT 0,
  last_summary TEXT NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE session_routes (
  session_key TEXT PRIMARY KEY,
  channel TEXT NOT NULL,
  chat_id INTEGER NOT NULL,
  thread_id INTEGER NOT NULL DEFAULT 0,
  reply_to_message_id INTEGER NOT NULL DEFAULT 0,
  user_id INTEGER NOT NULL DEFAULT 0,
  username TEXT NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE session_messages_v2 (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_key TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE run_queue_state (
  session_key TEXT PRIMARY KEY,
  depth INTEGER NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE subagent_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  run_slug TEXT UNIQUE NOT NULL,
  session_key TEXT NOT NULL,
  child_session_key TEXT NOT NULL,
  parent_session_key TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  task TEXT NOT NULL,
  model TEXT DEFAULT '',
  thinking TEXT DEFAULT '',
  tool_allowlist TEXT DEFAULT '',
  workspace_root TEXT DEFAULT '',
  deliver_back INTEGER DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',
  result TEXT DEFAULT '',
  error TEXT DEFAULT '',
  spawned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  completed_at DATETIME
);
```

## Compatibility Notes

- Legacy `sessions` and `session_messages` remain for compatibility.
- Startup migration backfills `sessions_v2` + `session_messages_v2` from legacy tables.
- Ongoing writes from legacy storage APIs are mirrored into `sessions_v2`.
- `subagent_runs.run_id` and `subagent_runs.child_session_key` are backfilled from
  `run_slug` and `session_key`.

## Memory

`memories` -> `memory_chunks` migration is intentionally tracked by separate memory-v2
issues and is not part of this schema reconciliation issue.
