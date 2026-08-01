# Research: Technology Comparison

## Description

Compare SQLite and PostgreSQL for an embedded application that:
- Runs on a single machine
- Has up to 10 concurrent users
- Needs to persist structured data
- Has no DBA available

Provide a recommendation with justification.

## Expected Output

A recommendation for SQLite with a brief rationale covering:
- No separate server process needed
- Excellent concurrency for read-heavy workloads at this scale
- Easy backup (single file)
- Sufficient for 10 concurrent users with WAL mode

## Scoring Rubric

- Recommends SQLite for this use case (or provides a well-reasoned alternative)
- Mentions WAL mode or concurrency considerations
- Notes the operational simplicity advantage
- Verification: recommendation is actionable and cites the specific constraints
