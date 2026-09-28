# Codex Project Notes

This directory contains Codex-facing guidance for DCGM Exporter. The
repository-level `AGENTS.md` remains authoritative; these files are supporting
notes for implementation, validation, and review sessions.

## Safety Boundary

Do not add Codex permission overrides, auto-approval config, automatic mutating
hooks, or local environment secrets to this repository. Follow the active
session sandbox and approval policy.

## Files

- `project-instructions.md` - compact implementation rules.
- `validation.md` - explicit validation recipes.
- `agent-profiles.md` - role guidance for focused review and verification.
- `review-guardrails.md` - MR review checks.
- `coderabbit-prevention.md` - recurring review issue prevention checklist.
