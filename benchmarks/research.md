# Research: Information Retrieval and Summary

## Description

Find and summarize the key differences between Go's `sync.Mutex` and `sync.RWMutex`.
Include:
- When to use each
- Performance tradeoffs
- A concrete example of a use case for each

## Expected Output

A structured summary (3–5 bullet points or a short paragraph) covering:
- `sync.Mutex`: exclusive lock for both reads and writes
- `sync.RWMutex`: allows multiple concurrent readers, exclusive writers
- Performance: RWMutex preferred when reads vastly outnumber writes
- Example use cases

## Scoring Rubric

- Correctly identifies that RWMutex allows concurrent reads
- Mentions the read-heavy workload use case
- Notes that RWMutex has higher overhead than Mutex for write-heavy workloads
- Verification: summary contains at least one concrete example
