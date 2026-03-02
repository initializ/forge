## Type of Change

- [ ] Bug fix
- [ ] New feature
- [ ] Enhancement / refactor
- [ ] New skill
- [ ] Documentation
- [ ] CI / build

## Description

<!-- What does this PR do and why? -->

## General Checklist

- [ ] Tests pass for affected modules (`go test ./...`)
- [ ] Code is formatted (`gofmt -w`)
- [ ] Linter passes (`golangci-lint run`)
- [ ] `go vet` reports no issues
- [ ] No new egress domains added without justification

## Skill Contribution Checklist

<!-- Delete this section if not contributing a skill -->

- [ ] `forge skills validate` passes with no errors
- [ ] `forge skills audit` reports no policy violations
- [ ] `egress_domains` lists every domain the skill contacts
- [ ] No secrets or credentials are hardcoded
- [ ] SKILL.md includes `## Tool:` sections with input/output tables
- [ ] Skill tested locally with expected input/output

## Related Issues

<!-- Link related issues: Closes #123, Fixes #456 -->
