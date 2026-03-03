# General Coding Standards

## Error Handling

All errors must be handled explicitly. Never silently ignore errors.

**Good:**
```go
result, err := doSomething()
if err != nil {
    return fmt.Errorf("doSomething failed: %w", err)
}
```

**Bad:**
```go
result, _ := doSomething()
```

## Naming Conventions

- Use descriptive, intention-revealing names
- Avoid single-letter variables except for loop indices and short-lived lambdas
- Boolean variables should read as assertions: `isReady`, `hasPermission`, `canRetry`

## Function Length

Functions should do one thing. If a function exceeds ~40 lines, consider splitting it. Long functions are harder to test, debug, and review.

## Magic Numbers

Replace magic numbers and strings with named constants. The name should explain the value's purpose.

**Good:**
```python
MAX_RETRY_ATTEMPTS = 3
TIMEOUT_SECONDS = 30
```

**Bad:**
```python
if attempts > 3:
    time.sleep(30)
```

## Comments

- Write comments that explain **why**, not **what**
- Keep comments up to date — stale comments are worse than no comments
- Use TODO/FIXME with a tracking reference: `// TODO(#123): migrate to new API`

## Dead Code

Remove dead code. Do not comment out code "for later." Version control preserves history.

## Dependencies

- Pin dependency versions in production code
- Justify new dependencies — prefer standard library when adequate
- Audit transitive dependencies for known vulnerabilities
