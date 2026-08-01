# Memory: Multi-Turn Context Retention

## Description

In a simulated multi-turn conversation:
- Turn 1: "My project uses Go 1.24 and SQLite for storage."
- Turn 2: "What storage engine does my project use?"

The agent must answer Turn 2 using only the context established in Turn 1.

## Expected Output

"Your project uses SQLite for storage." (or equivalent).

## Scoring Rubric

- Correctly recalls SQLite from the prior turn
- Does not introduce other storage engines
- Does not ask for clarification when the answer was provided
- Verification: answer is derived from the conversation context, not general knowledge
