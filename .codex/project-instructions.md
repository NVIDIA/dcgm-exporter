# DCGM Exporter Codex Instructions

Use `AGENTS.md` and scoped `AGENTS.md` files as the source of truth.

## Implementation Rules

- Start feature work with the root `AGENTS.md` Feature Planning checklist and
  Repository Surface Map.
- Read the owning package, tests, Makefile target, and CI job before editing.
- Keep docs and examples tied to real flags, env vars, Helm values, metrics,
  and file paths from the repo.
- Prefer small package-local helpers. Do not add broad agent-only abstractions.
- Do not mutate generated artifacts unless the change requires regeneration and
  the matching command is run.
- Do not include unrelated worktree files, local settings, or coverage output in
  commits.

## Security

- Never log or commit secrets, auth headers, kubeconfig material, web config
  credentials, registry tokens, or secret-manager values.

## Metrics

- Metric names, labels, HELP text, and TYPE values are user-visible contracts.
- Exporter-owned counters must stay aligned across code, CSV examples, docs,
  and `llms.txt`.
- Hardware-backed behavior should be tested with fakes where possible and with
  GPU integration/e2e only when necessary and available.
