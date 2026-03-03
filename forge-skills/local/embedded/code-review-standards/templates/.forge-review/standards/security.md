# Security Standards

## Input Validation

All external input must be validated before use. This includes HTTP request parameters, CLI arguments, file content, environment variables, and database results used in further queries.

- Validate type, length, range, and format
- Use allowlists over denylists where possible
- Reject invalid input early with clear error messages

## Injection Prevention

### SQL Injection
Always use parameterized queries or prepared statements. Never concatenate user input into SQL strings.

**Good:**
```go
db.Query("SELECT * FROM users WHERE id = $1", userID)
```

**Bad:**
```go
db.Query("SELECT * FROM users WHERE id = " + userID)
```

### Command Injection
Never pass unsanitized input to shell commands. Use argument arrays instead of string interpolation.

**Good:**
```python
subprocess.run(["git", "log", "--oneline", branch_name], check=True)
```

**Bad:**
```python
os.system(f"git log --oneline {branch_name}")
```

### XSS (Cross-Site Scripting)
All user-supplied content rendered in HTML must be escaped. Use framework-provided auto-escaping. Avoid `innerHTML`, `dangerouslySetInnerHTML`, or template `|safe` filters with untrusted data.

## Authentication & Authorization

- Never store plaintext passwords — use bcrypt, scrypt, or argon2
- Validate authorization on every request, not just at the UI level
- Use constant-time comparison for secrets and tokens
- Implement rate limiting on authentication endpoints

## Secrets Management

- Never commit secrets, API keys, or credentials to version control
- Use environment variables or a secrets manager (Vault, AWS Secrets Manager)
- Rotate secrets regularly and after any suspected exposure
- Add secret patterns to `.gitignore` and pre-commit hooks

## Cryptography

- Use well-known libraries — never implement custom crypto
- Use TLS 1.2+ for all network communication
- Use authenticated encryption (AES-GCM, ChaCha20-Poly1305)
- Generate random values with cryptographically secure PRNGs

## Logging & Error Messages

- Never log sensitive data: passwords, tokens, PII, credit card numbers
- Sanitize error messages shown to users — do not expose stack traces or internal paths
- Log security-relevant events: login attempts, permission changes, data access

## Dependency Security

- Monitor dependencies for known CVEs (Dependabot, Snyk, Trivy)
- Pin versions in production — avoid floating ranges
- Review changelogs before upgrading major versions
- Minimize dependency surface area
