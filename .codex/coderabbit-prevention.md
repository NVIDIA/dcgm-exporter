# CodeRabbit Prevention Checklist

Apply before handing off a change.

## Contracts

- Do not invent metric labels, CLI flags, env vars, Helm values, or DCGM fields.
- Keep README, Helm values/templates, raw YAML, and tests aligned with code.
- Broad terminology changes need `rg` sweeps across Go, YAML, CSV, docs, tests,
  metrics, and CI.

## Go

- Prefer explicit errors with context.
- Avoid silent fallback for failed parsing, file I/O, metric registration,
  serialization, or scrape rendering.
- Do not add global mutable state without reset/restore tests.
- Keep mock seams in wrapper packages and test helpers.

## Tests

- Assert the behavior named by the test.
- Prefer typed field assertions over string/debug output matching.
- Use package tests for logic and reserve GPU/e2e tests for hardware or
  deployment contracts.

## Docs

- Comments and docs describe current behavior, not planned future cleanup.
- Examples use current commands and real paths.
- Security-sensitive examples avoid real-looking credentials.
