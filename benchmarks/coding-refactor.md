# Coding: Refactoring

## Description

Refactor the following repetitive Go code to remove duplication:

```go
func processA(data []byte) error {
    if len(data) == 0 {
        return fmt.Errorf("processA: empty data")
    }
    // process A logic
    return nil
}

func processB(data []byte) error {
    if len(data) == 0 {
        return fmt.Errorf("processB: empty data")
    }
    // process B logic
    return nil
}
```

## Expected Output

Extract the validation into a helper:

```go
func validateData(name string, data []byte) error {
    if len(data) == 0 {
        return fmt.Errorf("%s: empty data", name)
    }
    return nil
}

func processA(data []byte) error {
    if err := validateData("processA", data); err != nil {
        return err
    }
    // process A logic
    return nil
}
```

## Scoring Rubric

- Extracts the repeated validation logic into a shared helper
- Both functions use the helper
- Error messages remain distinguishable (include function name)
- Verification: refactored code is equivalent in behavior to the original
