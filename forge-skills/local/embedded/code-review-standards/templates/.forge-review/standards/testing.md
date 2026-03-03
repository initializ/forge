# Testing Standards

## Test Structure

- Follow the Arrange-Act-Assert (AAA) pattern in every test.
- One logical assertion per test. Multiple assertions are acceptable if they verify the same behavior.
- Name tests descriptively: `test_expired_token_returns_401`, not `test_auth_3`.

## Test Independence

- Tests must not depend on execution order.
- Each test sets up its own state and tears it down.
- Never share mutable state between tests.
- Use unique identifiers (UUIDs, timestamps) to avoid collisions in parallel runs.

## Test Coverage

- New code should include tests. PRs that add features without tests require justification.
- Cover the happy path, edge cases, and error paths.
- Critical paths (auth, payment, data mutation) require explicit test coverage.
- Coverage percentage is a guideline, not a target — 80% coverage with good tests beats 100% with trivial ones.

## Mocking

- Mock at boundaries: HTTP clients, databases, filesystems, time, randomness.
- Never mock the system under test.
- Prefer fakes (in-memory implementations) over mocks for complex dependencies.
- Verify mock expectations — unused mocks indicate dead test code.

## Test Data

- Use factory functions or builders for test data — avoid copy-pasting large literals.
- Keep test data minimal: only set fields relevant to the test.
- Use realistic but not real data. Never use production data in tests.

## Integration Tests

- Integration tests verify component interactions — they should use real dependencies where practical.
- Tag or separate integration tests so they can run independently: `go test -tags=integration`, `pytest -m integration`.
- Use containers (testcontainers, docker-compose) for database and service dependencies.
- Clean up external state after tests.

## Flaky Tests

- Flaky tests must be fixed immediately or quarantined.
- Common causes: timing dependencies, shared state, network calls, uncontrolled randomness.
- Use deterministic seeds for random values in tests.
- Use fake timers instead of `sleep` in tests.

## Performance Tests

- Benchmark critical paths. Use `testing.B` (Go), `pytest-benchmark`, or `vitest bench`.
- Set performance budgets for critical operations.
- Run benchmarks in CI to detect regressions.

## Security Tests

- Test authentication: valid token, expired token, missing token, wrong role.
- Test authorization: verify users cannot access other users' resources.
- Test input validation: SQL injection, XSS payloads, path traversal, oversized input.
- Test rate limiting and account lockout.
