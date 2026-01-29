# Memory System Quick Start

## 1. Enable Memory

Edit `~/.ok-gobot/config.yaml`:

```yaml
memory:
  enabled: true
  embeddings_base_url: "https://api.openai.com/v1"
  embeddings_api_key: ""  # Leave empty to reuse ai.api_key
  embeddings_model: "text-embedding-3-small"
```

## 2. Commands

### Save
```
memory save The user prefers Python over Go for scripting --category=preferences
```

### Search
```
memory search What programming languages does the user prefer?
```

### List
```
memory list
```

### Forget
```
memory forget 42
```

## 3. Use Cases

**User Preferences**
```
memory save User likes dark mode --category=preferences
memory save User's timezone is UTC+2 --category=personal
```

**Project Information**
```
memory save Database password is stored in .env file --category=project
memory save API endpoint is https://api.example.com --category=project
```

**Tasks & Reminders**
```
memory save Meeting with client on Friday at 3pm --category=tasks
memory save Deadline for report is next Monday --category=tasks
```

**Code Snippets**
```
memory save To deploy: git push heroku main --category=commands
memory save Connection string format: postgresql://user:pass@host/db --category=reference
```

## 4. Search Tips

- Use natural language questions
- Be specific about what you're looking for
- Results show similarity score (0.0-1.0)
- Higher scores = more relevant

Example queries:
- "What are user's programming preferences?"
- "Show me upcoming deadlines"
- "How do I deploy the application?"
- "What's the database connection info?"

## 5. Configuration Options

| Setting | Default | Description |
|---------|---------|-------------|
| enabled | false | Enable/disable memory |
| embeddings_base_url | OpenAI | API endpoint |
| embeddings_api_key | (reuse ai.api_key) | Separate API key |
| embeddings_model | text-embedding-3-small | Model to use |

## 6. Cost Considerations

- OpenAI `text-embedding-3-small`: $0.02 / 1M tokens
- Typical memory: ~50 tokens
- 1000 saves = ~$0.001 (negligible)
- Search has same cost per query

## 7. Troubleshooting

**Memory not saving?**
- Check `memory.enabled: true`
- Verify API key is set
- Check logs for errors

**Low similarity scores?**
- Try different wording
- Use more specific queries
- Consider upgrading to `text-embedding-3-large`

**Slow searches?**
- Normal with many memories (>1000)
- Each memory is compared sequentially
- Consider cleanup of old memories

## 8. Integration with Bot

The memory tool is automatically available when enabled. The bot can:
- Save information during conversations
- Recall relevant context when needed
- Maintain long-term knowledge beyond chat history

## 9. Advanced Usage

**Programmatic Access**
```go
// Initialize
manager := memory.NewMemoryManager(embClient, store)

// Save
ctx := context.Background()
manager.Remember(ctx, "content", "category")

// Search
results, _ := manager.Recall(ctx, "query", 5)
for _, r := range results {
    fmt.Printf("%.2f: %s\n", r.Similarity, r.Content)
}
```

## 10. Best Practices

✅ **Do:**
- Use descriptive categories
- Store complete thoughts
- Regular cleanup of outdated info
- Use for factual information

❌ **Don't:**
- Store sensitive passwords/keys
- Save duplicate information
- Create too-specific categories
- Store temporary information

## Need More?

- Full documentation: `docs/MEMORY.md`
- Example code: `examples/memory_example.go`
- Implementation details: `IMPLEMENTATION_SUMMARY.md`
