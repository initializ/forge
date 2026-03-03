# Python Standards

## Type Hints

- Use type hints for all public function signatures.
- Use `from __future__ import annotations` for forward references.
- Run `mypy` or `pyright` in CI.

**Good:**
```python
def fetch_user(user_id: int, *, include_deleted: bool = False) -> User | None:
    ...
```

## Error Handling

- Catch specific exceptions, never bare `except:`.
- Use custom exception classes for domain errors.
- Use context managers (`with`) for resource cleanup.

**Good:**
```python
try:
    result = api.fetch(endpoint)
except requests.HTTPError as e:
    logger.error("API request failed: %s", e)
    raise
```

**Bad:**
```python
try:
    result = api.fetch(endpoint)
except:
    pass
```

## Imports

- Group imports: stdlib, third-party, local — separated by blank lines.
- Use absolute imports. Avoid wildcard imports (`from module import *`).
- Sort imports with `isort` or `ruff`.

## String Formatting

- Use f-strings for readability: `f"Hello, {name}"`.
- Use `%s` formatting in logging calls (lazy evaluation): `logger.info("User %s logged in", user_id)`.

## Data Classes & Models

- Use `dataclasses` or `pydantic` for structured data — avoid raw dicts for domain objects.
- Use `Enum` for fixed sets of values.

## Security

- Never use `eval()`, `exec()`, or `__import__()` with user input.
- Use `subprocess.run()` with argument lists, never `shell=True` with untrusted input.
- Use `secrets` module for token generation, not `random`.
- Sanitize file paths: validate against path traversal (`../`).

## Testing

- Use `pytest` with descriptive test names: `test_fetch_user_returns_none_for_missing_id`.
- Use `pytest.fixture` for shared setup.
- Use `pytest.raises` for expected exceptions.
- Mock external dependencies at the boundary, not deep internals.

## Performance

- Use generators for large sequences: `(x for x in items)` over `[x for x in items]`.
- Profile before optimizing — use `cProfile` or `py-spy`.
- Use `functools.lru_cache` for expensive pure functions.

## Formatting

- Follow PEP 8. Use `ruff` or `black` for auto-formatting.
- Maximum line length: 88 (black default) or 120 (project configured).
- Use trailing commas in multi-line collections.
