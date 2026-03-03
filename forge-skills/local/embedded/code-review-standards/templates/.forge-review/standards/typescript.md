# TypeScript Standards

## Type Safety

- Enable `strict: true` in `tsconfig.json`.
- Avoid `any` — use `unknown` when the type is truly unknown, then narrow with type guards.
- Use discriminated unions over type assertions.
- Prefer `interface` for object shapes, `type` for unions and intersections.

**Good:**
```typescript
type Result =
  | { status: "success"; data: User }
  | { status: "error"; message: string };

function handleResult(result: Result) {
  if (result.status === "success") {
    console.log(result.data.name);
  }
}
```

**Bad:**
```typescript
function handleResult(result: any) {
  console.log(result.data.name); // unsafe
}
```

## Null Handling

- Use optional chaining (`?.`) and nullish coalescing (`??`) over manual checks.
- Enable `strictNullChecks`. Handle `null` and `undefined` explicitly.
- Avoid non-null assertions (`!`) unless the invariant is documented.

## Error Handling

- Use typed error classes or result types over throwing raw strings.
- Always handle promise rejections — unhandled rejections crash Node.js.
- Use `try/catch` at async boundaries, not around every await.

**Good:**
```typescript
try {
  const user = await fetchUser(id);
  return { ok: true, data: user };
} catch (err) {
  logger.error("Failed to fetch user", { userId: id, error: err });
  return { ok: false, error: "User fetch failed" };
}
```

## Async Patterns

- Use `async/await` over raw promises and callbacks.
- Use `Promise.all()` for independent concurrent operations.
- Set timeouts on external calls — use `AbortController` for fetch.

## Immutability

- Use `const` by default. Only use `let` when reassignment is necessary.
- Use `readonly` for properties that should not change after construction.
- Prefer spread/map/filter over mutating arrays and objects.

## React (if applicable)

- Use functional components with hooks.
- Memoize expensive computations with `useMemo`/`useCallback` where profiling shows benefit.
- Lift state up or use context — avoid prop drilling beyond 2-3 levels.
- Extract custom hooks for reusable logic.

## Testing

- Use `describe`/`it` blocks with descriptive names.
- Test behavior, not implementation details.
- Mock external boundaries (API clients, databases), not internal functions.
- Use `testing-library` patterns: query by role/label, not CSS selectors.

## Formatting & Linting

- Use ESLint with the project's shared config.
- Use Prettier for formatting (or Biome).
- No `eslint-disable` without a comment explaining the exception.
- Enable `no-unused-vars`, `no-explicit-any`, `no-floating-promises`.
