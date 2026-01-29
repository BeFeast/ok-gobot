# Semantic Memory System

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
```

### Configuration Options

- **enabled**: Set to `true` to enable semantic memory
- **embeddings_base_url**: API endpoint for embeddings (OpenAI-compatible)
- **embeddings_api_key**: API key for embeddings (if empty, reuses `ai.api_key`)
- **embeddings_model**: Embedding model to use (default: `text-embedding-3-small`)

### Supported Embedding Providers

- **OpenAI**: `https://api.openai.com/v1` - Use models like `text-embedding-3-small` or `text-embedding-3-large`
- **OpenRouter**: Can route to various embedding providers
- **Custom**: Any OpenAI-compatible embedding API

## Usage

### Memory Tool Commands

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
memory search <query> [--limit=<n>]
```

Example:
```
memory search What programming languages does the user prefer? --limit=5
memory search upcoming meetings
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
CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    embedding BLOB NOT NULL,
    category TEXT,
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
