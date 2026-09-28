# internal/pkg - Agent Instructions

`internal/pkg/` contains private implementation packages for metric collection,
device discovery, rendering, server behavior, reload support, wrappers, and test
helpers. The Go compiler enforces that these packages are not external API.

## Conventions

- Keep public surfaces small and package-owned. Prefer package-local helpers
  over new cross-package abstractions unless multiple production packages need
  the same behavior.
- Return errors with useful context. Do not hide operational failures with
  `_ =`, `.ok`-style patterns, or default values when the failure changes
  metric correctness.
- Use `log/slog` consistently. Do not log tokens, credentials, auth headers,
  kubeconfig contents, web config secrets, or raw sensitive file content.
- Keep wrappers such as `internal/pkg/os`, `internal/pkg/exec`, and
  `internal/pkg/elf` mock-friendly.

## Metrics

- Preserve Prometheus text contracts: stable metric names, one HELP/TYPE pair
  per family, finite scalar values, no negative counters, and no duplicate label
  sets.
- Do not invent labels. Label changes are user-visible contract changes and
  need tests in the relevant collector/renderer package plus docs updates.
- Exporter-owned counters must stay in sync across `const.go`,
  `exporter_counters.go`, `etc/default-counters.csv`, and `llms.txt`.

## Tests

- Prefer focused package tests for pure parsing, rendering, and transformation
  behavior.
- Use mocks and wrapper packages for DCGM/NVML/system interactions.
- Use GPU-backed integration tests only for behavior that cannot be proved with
  unit tests or in-process fakes.
