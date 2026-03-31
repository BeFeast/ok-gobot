# Tool Use: Code Search

## Description

Use search tools to find all Go files that import the `database/sql` package
in the `internal/` directory tree.

## Expected Output

A list of file paths that contain `import "database/sql"` or
`"database/sql"` in their import block.

## Scoring Rubric

- Uses a grep or search tool with the pattern `database/sql`
- Reports file paths (not just a count)
- Does not hallucinate file paths that don't exist
- Verification: all reported files actually contain the import
- Parallel tool execution used where possible
