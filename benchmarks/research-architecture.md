# Research: Architecture Decision

## Description

A team is building a Telegram bot backend in Go that needs to:
- Handle 1000 messages/day
- Store conversation history
- Support multiple AI providers
- Run on a single VPS with 2GB RAM

Recommend a data storage approach and justify the choice.

## Expected Output

Recommendation: SQLite with WAL mode.
Justification:
- 1000 messages/day is well within SQLite's capacity
- Single VPS: no need for a separate database server
- WAL mode enables concurrent reads
- Low memory footprint compared to PostgreSQL
- Simple backup: copy one file

## Scoring Rubric

- Recommends a storage solution appropriate for the scale described
- Considers the memory constraint
- Mentions operational simplicity
- Verification: recommendation is justified by the stated constraints
