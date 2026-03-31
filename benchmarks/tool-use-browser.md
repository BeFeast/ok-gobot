# Tool Use: Browser Automation

## Description

Describe the steps to use a headless browser tool to:
1. Navigate to a URL
2. Extract the page title
3. Return the title as the result

This is a capability test — the agent should describe the tool call sequence
it would use, not execute it against a live URL.

## Expected Output

A description of the tool call sequence:
1. Call `browser_navigate` or equivalent with a target URL
2. Call `browser_snapshot` or `browser_evaluate` to get the page title
3. Extract the title from the result and return it

## Scoring Rubric

- Identifies the correct tool names (navigate + snapshot or evaluate)
- Describes a 2-step sequence (navigate then extract)
- Does not attempt to use non-existent tools
- Verification: described sequence matches the available browser tool API
