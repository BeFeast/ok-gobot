# Memory System Integration Guide

This guide shows how to integrate the semantic memory system into the ok-gobot application.

## Integration Steps

### 1. Initialize Memory Components (app.go)

In your main application initialization, after opening the database:

```go
import (
    "ok-gobot/internal/memory"
    "ok-gobot/internal/tools"
)

// In your App struct, add:
type App struct {
    // ... existing fields ...
    memoryManager *memory.MemoryManager  // Add this
}

// In initialization function:
func (a *App) Initialize() error {
    // ... existing initialization ...

    // Initialize memory system if enabled
    if a.config.Memory.Enabled {
        if err := a.initializeMemory(); err != nil {
            log.Printf("Warning: Failed to initialize memory: %v", err)
            // Don't fail startup, just log warning
        }
    }

    return nil
}
```

### 2. Memory Initialization Helper

```go
func (a *App) initializeMemory() error {
    // Determine API key
    apiKey := a.config.Memory.EmbeddingsAPIKey
    if apiKey == "" {
        apiKey = a.config.AI.APIKey
    }

    if apiKey == "" {
        return fmt.Errorf("no API key available for embeddings")
    }

    // Create embedding client
    embeddingClient := memory.NewEmbeddingClient(
        a.config.Memory.EmbeddingsBaseURL,
        apiKey,
        a.config.Memory.EmbeddingsModel,
    )

    // Create memory store using existing database
    memoryStore, err := memory.NewMemoryStore(a.db)
    if err != nil {
        return fmt.Errorf("failed to create memory store: %w", err)
    }

    // Create memory manager
    a.memoryManager = memory.NewMemoryManager(embeddingClient, memoryStore)

    log.Println("Memory system initialized successfully")
    return nil
}
```

### 3. Register Memory Tool (tools initialization)

When setting up the tool registry:

```go
func (a *App) initializeTools(cfg *config.Config) (*tools.Registry, error) {
    // ... existing tool registration ...

    // Register memory tool if memory is enabled
    if cfg.Memory.Enabled && a.memoryManager != nil {
        memoryTool := tools.NewMemoryTool(a.memoryManager)
        registry.Register(memoryTool)
        log.Println("Registered memory tool")
    }

    return registry, nil
}
```

### 4. Optional: Auto-Recall for Context Enhancement

Before sending messages to AI, optionally recall relevant memories:

```go
func (a *App) buildAIContext(userMessage string) []ai.Message {
    messages := []ai.Message{
        {Role: "system", Content: a.systemPrompt},
    }

    // Add relevant memories if enabled
    if a.config.Memory.Enabled && a.memoryManager != nil {
        ctx := context.Background()
        results, err := a.memoryManager.Recall(ctx, userMessage, 3)
        if err == nil && len(results) > 0 {
            var memoryContext strings.Builder
            memoryContext.WriteString("Relevant memories:\n")
            for _, r := range results {
                if r.Similarity > 0.7 { // Only high-confidence matches
                    memoryContext.WriteString(fmt.Sprintf("- %s\n", r.Content))
                }
            }
            messages = append(messages, ai.Message{
                Role:    "system",
                Content: memoryContext.String(),
            })
        }
    }

    messages = append(messages, ai.Message{
        Role:    "user",
        Content: userMessage,
    })

    return messages
}
```

### 5. Configuration Validation

Add memory config validation:

```go
func (c *Config) Validate() error {
    // ... existing validation ...

    // Validate memory config
    if c.Memory.Enabled {
        if c.Memory.EmbeddingsBaseURL == "" {
            return fmt.Errorf("memory.embeddings_base_url is required when memory is enabled")
        }

        apiKey := c.Memory.EmbeddingsAPIKey
        if apiKey == "" {
            apiKey = c.AI.APIKey
        }
        if apiKey == "" {
            return fmt.Errorf("no API key available for memory embeddings")
        }
    }

    return nil
}
```

## Complete Example

Here's a complete integration example in a single function:

```go
package app

import (
    "context"
    "database/sql"
    "fmt"
    "log"
    "strings"

    "ok-gobot/internal/config"
    "ok-gobot/internal/memory"
    "ok-gobot/internal/tools"
)

type App struct {
    config        *config.Config
    db            *sql.DB
    toolRegistry  *tools.Registry
    memoryManager *memory.MemoryManager
}

func NewApp(cfg *config.Config, db *sql.DB) (*App, error) {
    app := &App{
        config: cfg,
        db:     db,
    }

    // Initialize tool registry
    app.toolRegistry = tools.NewRegistry()

    // Initialize memory if enabled
    if cfg.Memory.Enabled {
        if err := app.setupMemory(); err != nil {
            log.Printf("Warning: Memory initialization failed: %v", err)
        }
    }

    return app, nil
}

func (a *App) setupMemory() error {
    // Get API key
    apiKey := a.config.Memory.EmbeddingsAPIKey
    if apiKey == "" {
        apiKey = a.config.AI.APIKey
    }

    // Create components
    client := memory.NewEmbeddingClient(
        a.config.Memory.EmbeddingsBaseURL,
        apiKey,
        a.config.Memory.EmbeddingsModel,
    )

    store, err := memory.NewMemoryStore(a.db)
    if err != nil {
        return err
    }

    a.memoryManager = memory.NewMemoryManager(client, store)

    // Register tool
    memTool := tools.NewMemoryTool(a.memoryManager)
    a.toolRegistry.Register(memTool)

    log.Println("✓ Memory system enabled")
    return nil
}

func (a *App) RecallMemories(query string, limit int) ([]memory.MemoryResult, error) {
    if a.memoryManager == nil {
        return nil, fmt.Errorf("memory system not initialized")
    }

    ctx := context.Background()
    return a.memoryManager.Recall(ctx, query, limit)
}

func (a *App) SaveMemory(content, category string) error {
    if a.memoryManager == nil {
        return fmt.Errorf("memory system not initialized")
    }

    ctx := context.Background()
    return a.memoryManager.Remember(ctx, content, category)
}
```

## Testing Integration

### Unit Test

```go
func TestMemoryIntegration(t *testing.T) {
    // Setup
    cfg := &config.Config{
        Memory: config.MemoryConfig{
            Enabled:             true,
            EmbeddingsBaseURL:   "https://api.openai.com/v1",
            EmbeddingsAPIKey:    "test-key",
            EmbeddingsModel:     "text-embedding-3-small",
        },
    }

    db, _ := sql.Open("sqlite3", ":memory:")
    defer db.Close()

    app, err := NewApp(cfg, db)
    if err != nil {
        t.Fatal(err)
    }

    // Test memory tool is registered
    tool, ok := app.toolRegistry.Get("memory")
    if !ok {
        t.Fatal("memory tool not registered")
    }

    if tool.Name() != "memory" {
        t.Errorf("Expected tool name 'memory', got %s", tool.Name())
    }
}
```

### Integration Test

```go
func TestMemoryWorkflow(t *testing.T) {
    // Skip if no API key
    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
        t.Skip("OPENAI_API_KEY not set")
    }

    cfg := &config.Config{
        Memory: config.MemoryConfig{
            Enabled:             true,
            EmbeddingsBaseURL:   "https://api.openai.com/v1",
            EmbeddingsAPIKey:    apiKey,
            EmbeddingsModel:     "text-embedding-3-small",
        },
    }

    db, _ := sql.Open("sqlite3", ":memory:")
    defer db.Close()

    app, _ := NewApp(cfg, db)

    // Save a memory
    err := app.SaveMemory("User prefers Python", "preferences")
    if err != nil {
        t.Fatal(err)
    }

    // Search for it
    results, err := app.RecallMemories("What languages does user like?", 5)
    if err != nil {
        t.Fatal(err)
    }

    if len(results) == 0 {
        t.Fatal("Expected at least one result")
    }

    t.Logf("Found: %s (similarity: %.2f)", results[0].Content, results[0].Similarity)
}
```

## Migration Guide

If you have existing bot code, here's how to migrate:

### Before (No Memory)
```go
type Bot struct {
    client    *ai.Client
    storage   *storage.Store
    tools     *tools.Registry
}
```

### After (With Memory)
```go
type Bot struct {
    client        *ai.Client
    storage       *storage.Store
    tools         *tools.Registry
    memory        *memory.MemoryManager  // Add this
}

func (b *Bot) Initialize(cfg *config.Config) error {
    // ... existing init ...

    // Add memory initialization
    if cfg.Memory.Enabled {
        b.initMemory(cfg)
    }
}
```

## Performance Monitoring

Add logging to track memory operations:

```go
func (a *App) SaveMemory(content, category string) error {
    start := time.Now()
    defer func() {
        log.Printf("Memory save took %v", time.Since(start))
    }()

    // ... save logic ...
}

func (a *App) RecallMemories(query string, limit int) ([]memory.MemoryResult, error) {
    start := time.Now()
    defer func() {
        log.Printf("Memory recall took %v", time.Since(start))
    }()

    // ... recall logic ...
}
```

## Error Handling

Handle memory errors gracefully:

```go
func (a *App) processMessage(msg string) string {
    // Try to enhance with memories, but don't fail if memory system has issues
    var memoryContext string
    if a.memoryManager != nil {
        results, err := a.RecallMemories(msg, 3)
        if err != nil {
            log.Printf("Memory recall failed: %v", err)
        } else if len(results) > 0 {
            var sb strings.Builder
            sb.WriteString("Relevant context:\n")
            for _, r := range results {
                sb.WriteString(fmt.Sprintf("- %s\n", r.Content))
            }
            memoryContext = sb.String()
        }
    }

    // Process message with or without memory context
    // ...
}
```

## Environment Variables

Support environment variable configuration:

```bash
export OKGOBOT_MEMORY_ENABLED=true
export OKGOBOT_MEMORY_EMBEDDINGS_BASE_URL="https://api.openai.com/v1"
export OKGOBOT_MEMORY_EMBEDDINGS_API_KEY="sk-..."
export OKGOBOT_MEMORY_EMBEDDINGS_MODEL="text-embedding-3-small"
```

These will be automatically picked up by viper's environment variable support.

## Deployment Checklist

- [ ] Memory enabled in config
- [ ] API key configured (or reusing AI key)
- [ ] Database migrations run
- [ ] Tool registered in registry
- [ ] Error handling in place
- [ ] Logging configured
- [ ] Integration tests pass
- [ ] Performance acceptable (<1s for save/search)
- [ ] Cost monitoring in place

## Rollback Plan

If you need to disable memory:

1. Set `memory.enabled: false` in config
2. Restart bot
3. Memory table remains in database (can be cleaned up later)
4. Tool commands will return "not enabled" error

To completely remove:

```sql
DROP TABLE IF EXISTS memories;
DROP INDEX IF EXISTS idx_memories_category;
DROP INDEX IF EXISTS idx_memories_created_at;
```

## Support

For issues or questions:
- Check `docs/MEMORY.md` for full documentation
- Review `examples/memory_example.go` for usage patterns
- Test with `go test ./internal/memory/...`
