# Coding: Error Handling

## Description

Review the following Go function and improve its error handling:

```go
func readConfig(path string) map[string]string {
    data, _ := os.ReadFile(path)
    var cfg map[string]string
    json.Unmarshal(data, &cfg)
    return cfg
}
```

## Expected Output

A revised function that:
1. Returns `(map[string]string, error)` instead of silently ignoring errors
2. Wraps errors with context using `fmt.Errorf("readConfig: %w", err)`
3. Handles the nil data case before unmarshaling

## Scoring Rubric

- Returns an error as the second return value
- Does not silently discard errors (no `_` for errors)
- Uses error wrapping with `%w`
- Verification: calling with a non-existent path returns a non-nil error
