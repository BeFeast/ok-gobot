# Tool Use: File Operations

## Description

Use file tools to:
1. List Go source files in the `internal/storage/` directory
2. Count the number of `.go` files found
3. Report the file names

## Expected Output

A list of `.go` file names found in `internal/storage/` and a count.
The response must use actual file tool calls (glob or list) rather than guessing.

## Scoring Rubric

- Uses a file listing or glob tool (not hardcoded output)
- Reports correct file count
- Lists actual file names found
- Verification: result is consistent with the actual directory contents
- Parallel tool execution preferred over sequential
