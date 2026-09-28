# Review Guardrails

Use these before MR handoff.

## Planning

- The change is mapped to an owning surface from root `AGENTS.md`.
- User-visible contracts, docs, and prerequisite-dependent gates are accounted
  for without duplicating root guidance.

## Metrics And CLI

- Metric CSV rows load through `internal/pkg/counters`.
- Exporter-owned counters are documented in `llms.txt`.
- CLI flags, env vars, defaults, Helm values, raw YAML, and README examples are
  aligned.
- Remote hostengine URI examples match `pkg/cmd/app.go`.

## Tests

- New behavior has the lowest useful test layer.
- GPU/Docker/Kubernetes gates are run when prerequisites exist, or the skip is
  documented.
- Tests avoid tautological assertions and fixed sleeps when readiness can be
  observed.

## CI, Docker, And Scripts

- Verification commands are not masked with `|| true` outside cleanup.
- Shell scripts keep strict mode and quote path variables.
- Version pins go through `hack/versions.env`, `make sync-versions`, and
  `make validate-versions`.
