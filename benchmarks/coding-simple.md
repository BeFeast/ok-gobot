# Coding: Simple Bug Fix

## Description

Fix a simple off-by-one error in a Go slice operation.
Given the following broken function, identify and fix the bug:

```go
func lastElement(s []int) int {
    return s[len(s)] // bug: should be len(s)-1
}
```

## Expected Output

A corrected function:

```go
func lastElement(s []int) int {
    return s[len(s)-1]
}
```

The response must include the corrected code and a one-sentence explanation of the bug.

## Scoring Rubric

- Identifies the off-by-one error (required for passing)
- Provides corrected code
- Explains why `len(s)` is out of bounds
- Verification: code would not panic on a non-empty slice
