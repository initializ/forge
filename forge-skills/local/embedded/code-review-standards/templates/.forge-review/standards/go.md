# Go Standards

## Error Handling

- Always check returned errors. Use `errcheck` or `golangci-lint` to enforce.
- Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`
- Use `errors.Is()` and `errors.As()` for error inspection, not string matching.
- Define sentinel errors as package-level variables: `var ErrNotFound = errors.New("not found")`

## Goroutines & Concurrency

- Every goroutine must have a clear shutdown path. Use `context.Context` for cancellation.
- Protect shared state with `sync.Mutex` or use channels. Document the choice.
- Never use `go func()` without considering error propagation and lifecycle.
- Use `sync.WaitGroup` or `errgroup.Group` to wait for goroutine completion.

**Good:**
```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error {
    return processItem(ctx, item)
})
if err := g.Wait(); err != nil {
    return err
}
```

## Resource Management

- Use `defer` for cleanup immediately after acquiring a resource.
- Close `io.Closer` values: files, HTTP response bodies, database connections.
- Use `context.WithTimeout` for operations that may hang.

**Good:**
```go
resp, err := http.Get(url)
if err != nil {
    return err
}
defer resp.Body.Close()
```

## Interfaces

- Define interfaces where they are used, not where they are implemented.
- Keep interfaces small — prefer 1-2 methods.
- Accept interfaces, return concrete types.

## Struct Initialization

- Use named fields in struct literals. Positional initialization breaks with field additions.

**Good:**
```go
srv := &Server{
    Addr:    ":8080",
    Handler: mux,
}
```

**Bad:**
```go
srv := &Server{":8080", mux}
```

## Testing

- Use table-driven tests for functions with multiple input/output cases.
- Use `t.Helper()` in test helper functions for better error reporting.
- Use `testify/assert` or `testify/require` for readable assertions.
- Name test cases descriptively: `"empty input returns error"`.

## Package Design

- Avoid package-level mutable state (global variables).
- Avoid `init()` functions — they complicate testing and initialization order.
- Package names should be short, lowercase, singular: `user`, not `users` or `userService`.

## Formatting & Linting

- Run `gofmt` / `goimports` on all code.
- Use `golangci-lint` with the project's configuration.
- No lint suppressions without a comment explaining why.
