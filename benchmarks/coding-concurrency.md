# Coding: Concurrency

## Description

Identify the race condition in the following Go code and propose a fix:

```go
var counter int

func increment() {
    counter++
}

func main() {
    for i := 0; i < 100; i++ {
        go increment()
    }
    time.Sleep(time.Second)
    fmt.Println(counter)
}
```

## Expected Output

Identify: `counter++` is not atomic — concurrent goroutines may read and write
simultaneously, causing lost updates.

Fix using `sync/atomic`:
```go
var counter int64

func increment() {
    atomic.AddInt64(&counter, 1)
}
```

Or using `sync.Mutex`:
```go
var (
    counter int
    mu      sync.Mutex
)

func increment() {
    mu.Lock()
    counter++
    mu.Unlock()
}
```

## Scoring Rubric

- Correctly identifies the data race on `counter`
- Proposes atomic or mutex-based fix
- Fix is compilable Go code
- Verification: fixed code passes `go test -race`
