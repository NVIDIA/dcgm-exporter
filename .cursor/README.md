# Cursor Agent Configuration

This directory contains project guidance for Cursor. It mirrors the intent of
`AGENTS.md` without granting permissions or installing hooks.

## Structure

```text
.cursor/
  README.md
  commands/
  rules/
  skills/
```

No `hooks.json` is provided. Formatting, validation, and security checks are
explicit commands that agents or developers run intentionally.

## Quick Reference

| Area | Primary guidance |
|---|---|
| Go implementation | `rules/go-standards.mdc` |
| Metrics and CSV contracts | `rules/metrics-contracts.mdc` |
| Helm/Kubernetes | `rules/helm-kubernetes.mdc` |
| Shell, CI, security | `rules/shell-ci-security.mdc` |
| Tests | `rules/testing.mdc` |

Use `commands/plan-feature.mdc` before broad feature work and
`commands/validate-after-change.mdc` before handoff.
