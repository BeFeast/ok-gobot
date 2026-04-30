# Semantic Memory System

> Current architecture note: memory v2 is markdown-first. `MEMORY.md` and
> legacy `memory/*.md`, and scoped `memory/<scope>/<id>/*.md` files are the
> source of truth; SQLite stores an embedding index over
> those files. QMD can be configured as an optional read-only search backend for
> advanced local-first search, but it is never required. Legacy record-style
> commands such as `memory save/list/forget` are deprecated.

The semantic memory system allows the bot to store and recall information using vector embeddings for similarity search. This enables long-term memory beyond the conversation context window.

## Features

- **Semantic Search**: Find relevant memories based on meaning, not just keywords
- **Vector Embeddings**: Uses OpenAI-compatible embedding APIs
- **SQLite Storage**: Memories stored in the same database as conversations
- **Cosine Similarity**: Efficient similarity computation in Go
- **Optional QMD Backend**: Read-only integration with a pre-existing QMD index for BM25/vector/hybrid search
- **Category Organization**: Tag memories with categories
- **Simple Tool Interface**: Easy-to-use commands via the memory tool

## Configuration

Add the following to your `~/.ok-gobot/config.yaml`:

```yaml
memory:
  enabled: true
  mode: "eager"        # eager | retrieval_first | startup_recent
  backend: "builtin"        # builtin, qmd, or auto
  embeddings_base_url: "https://api.openai.com/v1"
  embeddings_api_key: ""  # Leave empty to reuse ai.api_key
  embeddings_model: "text-embedding-3-small"
  metadata_extraction: false
  metadata_model: "haiku"
  extra_paths:
    - name: "obsidian"
      path: "~/Obsidian/Personal"
      patterns: ["**/*.md"]   # default if omitted
      read_only: true         # default true
      scope: "personal"
    - name: "homelab"
      path: "/mnt/shared/memory/homelab"
      patterns: ["docs/**/*.md", "runbooks/**/*.md"]
  qmd:
    binary_path: "qmd"
    index: "index"
    index_path: ""          # Optional explicit QMD SQLite index path
    search_mode: "search"   # search, vsearch, or query
    timeout: "10s"
    fallback_cooldown: "1m"
    collections:
      workspace: ""          # QMD collection rooted at the soul/workspace directory
      daily_notes: ""        # QMD collection rooted at memory/
      session_transcripts: "" # QMD collection rooted at exported session transcripts
      extra_paths: []         # Additional pre-existing QMD collection names
  mcp:
    enabled: false
    host: "127.0.0.1"
    port: 9233
    endpoint: "/mcp"
    allow_writes: false
```

### Configuration Options

- **enabled**: Set to `true` to enable semantic memory
- **mode**: How memory is injected into the system prompt — see
  [Memory prompt modes](#memory-prompt-modes) below.
- **backend**: `builtin` uses ok-gobot SQLite embeddings; `qmd` prefers QMD with built-in fallback; `auto` behaves like `qmd` but is intended for local profiles that may not always have QMD installed
- **embeddings_base_url**: API endpoint for embeddings (OpenAI-compatible)
- **embeddings_api_key**: API key for embeddings (if empty, reuses `ai.api_key`)
- **embeddings_model**: Embedding model to use (default: `text-embedding-3-small`)
- **extra_paths**: Additional configured memory source paths shown in diagnostics
- **metadata_extraction**: When `true`, extracts structured metadata (`people/topics/action_items/type`) during indexing
- **metadata_model**: Lightweight LLM model used for metadata extraction (default: `haiku`)
- **extra_paths**: Additional named markdown roots to index (Obsidian vaults, shared-memory exports). See "External markdown collections" below.
- **qmd.binary_path**: QMD CLI path. QMD is optional; missing binaries fall back to the built-in backend.
- **qmd.index / qmd.index_path**: Select the QMD index. `index_path` maps to QMD's `INDEX_PATH` and avoids relying on `~/.cache/qmd/<index>.sqlite`.
- **qmd.search_mode**: `search` is BM25 and the safe default. `vsearch` and `query` can use QMD's local embedding/reranking models and should only be enabled after you have intentionally prepared those models with QMD.
- **qmd.fallback_cooldown**: How long ok-gobot suppresses QMD retries after a failure before trying QMD again.
- **qmd.collections**: Names of pre-existing QMD collections. ok-gobot v1 does not create, update, embed, or delete QMD collections.
- **mcp.enabled**: Enable optional MCP server exposing `memory_search`, `memory_get`, `memory_capture`
- **mcp.host / mcp.port / mcp.endpoint**: MCP bind/interface settings (`127.0.0.1` by default for local-only access)
- **mcp.allow_writes**: Must be explicitly `true` to allow `memory_capture` writes

### External markdown collections (`extra_paths`)

`memory.extra_paths` indexes additional markdown roots — Obsidian vaults,
shared-memory exports, HomeLab documentation — into the same retrieval
pipeline as the workspace `MEMORY.md` / `memory/*.md` files. Each entry has:

| Field       | Required | Default      | Notes                                                                              |
| ----------- | -------- | ------------ | ---------------------------------------------------------------------------------- |
| `name`      | yes      | —            | Collection identifier (`[a-z0-9][a-z0-9_-]*`); must be unique.                     |
| `path`      | yes      | —            | Absolute or `~/`-prefixed path. NFS / shared mounts are supported.                 |
| `patterns`  | no       | `["**/*.md"]`| Globs relative to `path`. `**` matches any number of segments.                     |
| `read_only` | no       | `true`       | Reserved for future write enablement; this issue never writes to extra paths.      |
| `scope`     | no       | `""`         | Optional human-readable label (shown in `memory status`).                          |

Behavior:

- **Source labels.** Every chunk indexed from an extra path is stored with
  the `source_file = "extra:<name>/<relative-path>"` prefix. Both
  `memory_search` and `memory_get` surface this label so callers can tell
  workspace memory and external collections apart.
- **Hidden directories are skipped.** Any directory or file whose name starts
  with `.` is excluded by default — this keeps secrets, `.git`, `.obsidian`
  metadata, and the like out of the index.
- **Missing roots are non-fatal.** If a configured path is not mounted (or has
  not yet been created), the bot logs a warning, skips indexing, and
  `ok-gobot memory status` reports `[missing]` with a diagnostic message.
- **Watcher.** A best-effort filesystem watcher refreshes the index when files
  change. If the underlying filesystem doesn't support watches (some NFS
  mounts), indexing still happens at startup and on `memory index --force`.
- **Path-traversal protection.** `memory_get extra:<name>/<path>` validates
  that the resolved path stays inside the configured collection root and
  rejects symlink escapes.

### Memory prompt modes

The bootstrap loader always inlines stable identity context — `SOUL.md`,
`IDENTITY.md`, `USER.md`, `TOOLS.md`, `AGENTS.md`, `HEARTBEAT.md` — regardless
of mode. `memory.mode` only controls whether `MEMORY.md` and the per-day
files in `memory/<date>.md` are inlined into the prompt or kept as
retrieval-only sources reachable through `memory_search` / `memory_get`.

| Mode              | MEMORY.md | Today's daily note | Yesterday's daily note | Older notes |
| ----------------- | :-------: | :----------------: | :--------------------: | :---------: |
| `eager` (default) |     ✅    |          ✅         |            ✅           |  retrieval  |
| `retrieval_first` |     ✅    |          ⚪         |            ⚪           |  retrieval  |
| `startup_recent`  |     ✅    |          ✅         |            ⚪           |  retrieval  |

`retrieval_first` is the recommended mode once `memory.enabled: true` and the
markdown index is populated. Daily notes can grow without bound; in
retrieval_first mode the agent is instructed to call `memory_search` /
`memory_get` and cite source paths instead of relying on an inflated system
prompt. The `## Memory` section of the system prompt also advertises which
daily notes are reachable only via retrieval, so the agent has a
deterministic starting point for `memory_get`.

> Practical guidance: pair `retrieval_first` with Active Memory (issue #305)
> for best results. Without proactive memory injection, the model is the only
> safety net for remembering to search before answering.

`/context` reports the active mode plus which notes are inlined vs.
retrieval-only. `/status` shows a one-line summary
(`🧠 Memory: mode=… · tools=on|off`).

### Supported Embedding Providers

- **OpenAI**: `https://api.openai.com/v1` - Use models like `text-embedding-3-small` or `text-embedding-3-large`
- **OpenRouter**: Can route to various embedding providers
- **Custom**: Any OpenAI-compatible embedding API

## Usage

### Operational CLI

Inspect the persisted index:

```bash
ok-gobot memory status
```

The status output includes whether memory is enabled, backend type, watcher
state, indexed source/chunk counts, last indexed time, any last error,
configured extra paths, QMD status, and an action when memory is disabled,
stale, or in an error state.

Include backend diagnostics, including QMD binary, configured collections,
index/update state, embedding readiness, and last error:

```bash
ok-gobot memory status --deep
```

`memory status --deep` intentionally does not run `qmd status`. QMD 2.1.0's
status command can initialize local model tooling. ok-gobot instead uses the
stable read-only contract `qmd --version` and QMD SQLite index metadata.

Explicit QMD lifecycle checks are available when `memory.backend` is `qmd` or
`auto`:

```bash
ok-gobot qmd status
ok-gobot qmd update   # alias: ok-gobot qmd index
ok-gobot qmd smoke "release decisions"
```

`qmd status` and `qmd smoke` show whether QMD is used, skipped, or unavailable,
including the fallback reason. `qmd update` delegates to the configured QMD CLI
only when an operator runs it; startup and tests do not run QMD update/embed or
download models.

Force a rebuild of the managed markdown sources:

```bash
ok-gobot memory index --force
```

When `memory.enabled: true`, bot startup automatically indexes:

- `MEMORY.md`
- `memory/*.md`
- Every collection configured under `memory.extra_paths` (see "External
  markdown collections" above).
- `memory/users/<telegram-user-id>/*.md`
- `memory/chats/<telegram-chat-id>/*.md`
- `memory/sessions/<session-id>/*.md`
- `memory/roles/<role-name>/*.md`
- `memory/jobs/<job-id>/*.md`

The bot also starts debounced filesystem watchers for the workspace memory
sources and for each extra path. Changes update the `memory_chunks` index;
deleted files remove their chunks.

Other markdown files are intentionally ignored unless they are indexed with an
explicit external source label such as `extra:<collection>/...`.

### Scoped Recall Policy

Telegram active recall is scoped before results enter prompts or Telegram output:

- Private chats can recall only matching `users/<id>`, `chats/<id>`, current `sessions/<id>`, current role, current job, and explicitly allowed extra path labels.
- Group chats do not recall memory by default. A group must be active to recall its own `chats/<id>` memory, and it still cannot recall private user memory.
- Legacy global `MEMORY.md` and `memory/*.md` are treated as private legacy memory and are not available in group recall.
- Extra paths keep their `extra:<collection>/...` source label and are denied unless that label is explicitly allowed by runtime policy.
- Memory snippets returned by `memory_search` and `memory_get` are redacted before being passed back to the model.

The current policy summary is visible in `/status` and in memory tool output.

### Curation Drafts (manual promotion)

Daily notes accumulate quickly. To turn them into durable, audited promotions
without ever silently rewriting `MEMORY.md`, use:

```bash
ok-gobot memory curate --since 2026-04-15 --until 2026-04-21
```

This scans `<soul>/memory/*.md` in the date range and writes a draft under
`<soul>/memory/drafts/<id>.{md,json}`. It does **not** modify `MEMORY.md`.

The draft groups extracted candidates by section (durable user preferences,
project decisions, infrastructure facts, todos/follow-ups, stale/conflicting
facts), keeps a source link to the original daily note line, and runs a safety
audit that flags credentials, destructive shell snippets, low-confidence
candidates, and conflicts where the same fact appears with different values.

To apply a draft to `MEMORY.md` you must explicitly confirm:

```bash
ok-gobot memory curate apply <id> --yes
```

Apply is blocked automatically if the audit reports any error-severity finding.
Other actions:

- `ok-gobot memory curate list` — list drafts
- `ok-gobot memory curate show <id>` — render the draft and audit
- `ok-gobot memory curate reject <id> [--notes ...]` — keep on disk, mark rejected
- `ok-gobot memory curate delete <id>` — remove the draft files

In Telegram the same flow is exposed to the admin via `/memory_curate`.
The `apply` subcommand requires the literal `yes` token as confirmation.

The optional scheduled suggestion mode is **disabled by default**. Set
`memory.curation.enabled: true` and a cron expression in `memory.curation.schedule`
to have the bot generate draft suggestions on a cadence. The scheduled mode
never auto-applies — admin approval is always required for `MEMORY.md` writes.

When `memory.backend: qmd`, ok-gobot still maintains the built-in SQLite index so
fallback remains available. QMD failures are cached for `qmd.fallback_cooldown`
to avoid retry storms; during that window searches use the built-in backend.

## Built-in vs QMD

Use the built-in backend when:

- You want ok-gobot to run with no Node/Bun/QMD installation.
- Your memory sources are `MEMORY.md` and `memory/*.md`.
- OpenAI-compatible embeddings are acceptable.
- You want the simplest operational path.

Use QMD when:

- You already maintain a QMD index and want BM25, vector search, hybrid query, reranking, or query expansion.
- You want local-first search over additional markdown collections.
- You are prepared to manage QMD updates and embeddings yourself.

QMD search integration is read-only during normal bot replies. Create and
maintain collections with QMD directly, for example:

```bash
qmd collection add ~/ok-gobot-soul --name ok-workspace
qmd collection add ~/ok-gobot-soul/memory --name ok-daily
qmd update
qmd embed
```

Then reference those collection names:

```yaml
memory:
  enabled: true
  backend: "qmd"
  qmd:
    search_mode: "search"
    collections:
      workspace: "ok-workspace"
      daily_notes: "ok-daily"
      session_transcripts: "ok-sessions"
      extra_paths:
        - "project-docs"
```

QMD search results are normalized to sources that `memory_get` can read when the
collection is rooted at the expected location:

- `workspace` results become `MEMORY.md` or another path relative to the soul directory.
- `daily_notes` results become `memory/<file>.md`.
- `session_transcripts` results become `sessions/<file>.md`.
- `extra_paths` results use the QMD collection-relative path; keep those paths under the soul directory if you need `memory_get` to read them.

To use QMD's heavier model-backed behavior, explicitly set `search_mode: "query"`
or `search_mode: "vsearch"` after configuring QMD. ok-gobot does not download
QMD models itself and defaults to `search` to avoid triggering model setup.

Telegram operators can use `/memory_status` for memory health and `/qmd` for the
QMD used/skipped/unavailable state in a compact format.

### Agent Tool Commands

The active agent tools are:

- `memory_search <query> [limit] [expand]`
- `memory_get <source> [header_path]`

`memory_search` returns matching indexed chunks. With `expand=true`, each hit is
expanded to the full markdown branch sharing the same source file and header
path. `memory_get` reads exact source content or a section path from the
markdown source of truth.

### Legacy Memory Tool Commands

The memory tool provides four subcommands:

#### Save a Memory

```
memory save <text> [--category=<category>]
```

Example:
```
memory save The user prefers Python over Go for scripting --category=preferences
memory save Meeting scheduled for Friday at 3pm
```

#### Search Memories

```
memory search <query> [--limit=<n>] [--person=<name>]
```

Example:
```
memory search What programming languages does the user prefer? --limit=5
memory search upcoming meetings
memory search release decisions --person=Anton
```

Returns the most semantically similar memories with similarity scores.

#### List Recent Memories

```
memory list
```

Shows the 10 most recent memories regardless of content.

#### Forget a Memory

```
memory forget <id>
```

Example:
```
memory forget 42
```

Deletes the memory with the specified ID.

## Architecture

### Components

1. **EmbeddingClient** (`internal/memory/embeddings.go`)
   - Handles communication with embedding APIs
   - Converts text to vector embeddings
   - Uses OpenAI-compatible API format

2. **MemoryStore** (`internal/memory/store.go`)
   - SQLite storage for memories and embeddings
   - Cosine similarity computation in Go
   - Binary encoding of float32 vectors

3. **MemoryManager** (`internal/memory/manager.go`)
   - Selects the configured backend
   - Falls back from QMD to built-in SQLite when QMD is missing or unhealthy
   - Provides high-level Remember/Recall compatibility aliases

4. **QMDBackend** (`internal/memory/qmd.go`)
   - Calls the `qmd` CLI read-only using `--json`
   - Parses QMD citations into `memory_get`-compatible source paths when possible
   - Reads QMD SQLite metadata for deep diagnostics without running `qmd status`

5. **Memory Tools** (`internal/tools/memory_search_tool.go`, `internal/tools/memory_get_tool.go`)
   - Tool interface for agent use
   - Delegates search to the selected manager backend

### Database Schema

```sql
CREATE TABLE memory_chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    embedding BLOB NOT NULL,
    category TEXT,
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### Similarity Search

The system uses cosine similarity to find relevant memories:

1. User query is converted to an embedding vector
2. All stored embeddings are loaded from the database
3. Cosine similarity is computed for each memory
4. Results are sorted by similarity score
5. Top K results are returned

Formula:
```
similarity(A, B) = (A · B) / (||A|| × ||B||)
```

## Performance Considerations

- **Embedding API Latency**: Each save/search requires an API call (~100-500ms)
- **In-Memory Search**: All embeddings loaded into memory for similarity computation
- **Scalability**: Suitable for thousands of memories; for larger scale, consider vector databases
- **Model Choice**: `text-embedding-3-small` balances cost and quality (1536 dimensions)

## Example Integration

```go
import (
    "context"
    "ok-gobot/internal/memory"
)

// Initialize
embeddingClient := memory.NewEmbeddingClient(
    "https://api.openai.com/v1",
    apiKey,
    "text-embedding-3-small",
)

store, _ := memory.NewMemoryStore(db)
manager := memory.NewMemoryManager(embeddingClient, store)

// Save a memory
ctx := context.Background()
manager.Remember(ctx, "User likes coffee", "preferences")

// Recall memories
results, _ := manager.Recall(ctx, "What drinks does user like?", 5)
for _, result := range results {
    fmt.Printf("%.2f: %s\n", result.Similarity, result.Content)
}
```

## Comparison with File-Based Memory

| Feature | Semantic Memory | File-Based (MEMORY.md) |
|---------|----------------|------------------------|
| Search | Semantic similarity | Grep/keyword search |
| Structure | Database | Markdown file |
| Context | Automatic retrieval | Manual inclusion |
| Scalability | Thousands of items | Limited by context window |
| Cost | API calls per query | None |
| Best For | Specific facts | General context/personality |

## Best Practices

1. **Categorize Memories**: Use categories for organization (`preferences`, `tasks`, `facts`, etc.)
2. **Meaningful Content**: Store complete thoughts, not fragments
3. **Regular Cleanup**: Use `forget` to remove outdated information
4. **Monitor Costs**: Each save/search calls the embedding API
5. **Complement File Memory**: Use both systems for comprehensive context

## Troubleshooting

### Memory not working
- Check that `memory.enabled: true` in config
- Verify API key is set (either `memory.embeddings_api_key` or reuse `ai.api_key`)
- Check logs for embedding API errors

### Low similarity scores
- Embedding model may not be appropriate for your use case
- Try `text-embedding-3-large` for better quality
- Ensure queries and memories use similar vocabulary

### Slow searches
- Number of memories affects search time
- Consider limiting stored memories
- Use categories to narrow search scope (not yet implemented)

## Future Enhancements

- Category filtering in search
- Time-based memory decay
- Automatic summarization of old memories
- Vector index for faster search (e.g., HNSW)
- Multi-modal embeddings (images, code)
- Memory importance scoring
