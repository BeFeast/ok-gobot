# Semantic Memory System

> Current architecture note: memory v2 is markdown-first. `MEMORY.md` and
> `memory/*.md` are the source of truth; SQLite stores an embedding index over
> those files. Legacy record-style commands such as `memory save/list/forget`
> are deprecated.

The semantic memory system allows the bot to store and recall information using vector embeddings for similarity search. This enables long-term memory beyond the conversation context window.

## Features

- **Semantic Search**: Find relevant memories based on meaning, not just keywords
- **Vector Embeddings**: Uses OpenAI-compatible embedding APIs
- **SQLite Storage**: Memories stored in the same database as conversations
- **Cosine Similarity**: Efficient similarity computation in Go
- **Category Organization**: Tag memories with categories
- **Simple Tool Interface**: Easy-to-use commands via the memory tool

## Configuration

Add the following to your `~/.ok-gobot/config.yaml`:

```yaml
memory:
  enabled: true
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
  mcp:
    enabled: false
    host: "127.0.0.1"
    port: 9233
    endpoint: "/mcp"
    allow_writes: false
```

### Configuration Options

- **enabled**: Set to `true` to enable semantic memory
- **embeddings_base_url**: API endpoint for embeddings (OpenAI-compatible)
- **embeddings_api_key**: API key for embeddings (if empty, reuses `ai.api_key`)
- **embeddings_model**: Embedding model to use (default: `text-embedding-3-small`)
- **metadata_extraction**: When `true`, extracts structured metadata (`people/topics/action_items/type`) during indexing
- **metadata_model**: Lightweight LLM model used for metadata extraction (default: `haiku`)
- **extra_paths**: Additional named markdown roots to index (Obsidian vaults, shared-memory exports). See "External markdown collections" below.
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

Force a rebuild of the managed markdown sources:

```bash
ok-gobot memory index --force
```

When `memory.enabled: true`, bot startup automatically indexes:

- `MEMORY.md`
- `memory/*.md`
- Every collection configured under `memory.extra_paths` (see "External
  markdown collections" above).

The bot also starts debounced filesystem watchers for the workspace memory
sources and for each extra path. Changes update the `memory_chunks` index;
deleted files remove their chunks.

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
   - Coordinates embedding client and store
   - Provides high-level Remember/Recall interface

4. **MemoryTool** (`internal/tools/memory_tool.go`)
   - Tool interface for agent use
   - Parses commands and delegates to manager

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
