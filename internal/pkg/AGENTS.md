# internal/pkg - Agent Instructions

The root `AGENTS.md` still applies. This file adds rules for `internal/pkg/`.

`internal/pkg/` contains private implementation packages for metric collection,
device discovery, rendering, server behavior, reload support, wrappers, and test
helpers. The Go compiler enforces that these packages are not external API.

## Conventions

- Keep public surfaces small and package-owned. Prefer package-local helpers
  over new cross-package abstractions unless multiple production packages need
  the same behavior.
- Do not hide operational failures with `.ok`-style patterns or default
  values when the failure changes metric correctness.
- Do not log tokens, auth headers, kubeconfig contents, web config secrets, or
  raw sensitive file content.
- Keep wrappers such as `internal/pkg/os` mock-friendly.
- Configuration code has three owners. `appconfig` holds the config types, the
  defaults, the flag-name strings, the checks on configuration values, and the
  resolved value; it imports no other internal package. `devicewatchlistmanager`
  holds the checks of overrides against metric definitions (`CheckOverrides`,
  which startup and `config validate` both call) and the warning that a count
  window is longer than the DCGM history.
  `configinspect` builds the `config show`, `explain` and `validate` reports as
  functions over `appconfig.Resolved`, and returns bytes without writing to a
  terminal. The import direction is `pkg/cmd` to `configinspect` to
  `devicewatchlistmanager` to `counters` to `appconfig`.
  `appconfig` also owns native-free OTLP source and entity types;
  `signalcatalog` aliases those types and owns signal definitions and bindings.
  Keep native DCGM field types and conversions at DCGM integration boundaries.

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
  behavior. Table tests for configuration rules and config reports belong in
  `appconfig`, `devicewatchlistmanager` and `configinspect`; `pkg/cmd` tests
  cover the CLI wiring.
- Use mocks and wrapper packages for DCGM/NVML/system interactions.
- Use GPU-backed integration tests only for behavior that cannot be proved with
  unit tests or in-process fakes.
