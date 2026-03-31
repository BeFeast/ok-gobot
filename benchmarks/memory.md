# Memory: Recall and Use Stored Facts

## Description

Given a previously stored fact (simulated in the task prompt):
"The database migration system uses SQLite with CGO_ENABLED=1."

Answer the following question using this fact:
"What build flag is required when compiling the project?"

## Expected Output

The answer: `CGO_ENABLED=1` is required because the project uses SQLite via
the `github.com/mattn/go-sqlite3` package, which requires cgo.

## Scoring Rubric

- Correctly identifies `CGO_ENABLED=1`
- Explains the reason (sqlite3 requires cgo)
- Does not confuse build flags with environment variables
- Verification: answer is factually correct and cites the given fact
