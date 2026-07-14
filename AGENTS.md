# DCGM Exporter - Agent Instructions

## Project

DCGM Exporter is a Go service and container image that exposes NVIDIA DCGM GPU
metrics in Prometheus text format. It is deployed directly, through packages, or
through the Helm chart under `deployment/`.

## Source Of Truth

When sources disagree, follow this repository in this order:

1. Current Go code, tests, and package ownership.
2. `Makefile`, `.gitlab-ci.yml`, `.gitlab/ci/*.yml`, and
   `.github/workflows/*` (when present) for build and validation gates.
3. `go.mod`, `go.sum`, `hack/versions.env`, Dockerfiles, Helm templates, and
   checked-in YAML/CSV artifacts.
4. `README.md`, `CONTRIBUTING.md`, scoped `AGENTS.md`, and `llms.txt`.
5. Version-matched official documentation for Go, DCGM, Kubernetes, Helm,
   Prometheus, and exporter-toolkit.

Do not invent flags, environment variables, metric names, labels, Helm values,
or DCGM field names. Verify them from code or checked-in artifacts first.

## Stack

- Go module: `github.com/NVIDIA/dcgm-exporter`.
- Go toolchain: use `go.mod` and `hack/versions.env`.
- CLI: `urfave/cli/v2` in `pkg/cmd/app.go`.
- Metrics: DCGM/go-dcgm collection, exporter-owned counters, and Prometheus
  text rendering under `internal/pkg/`.
- Deployment: Dockerfiles, packages, raw YAML, and Helm chart.
- Tests: short Go tests, GPU integration tests, Docker image tests, Helm tests,
  and Kubernetes e2e tests.

## Repository Surface Map

- CLI and runtime startup: `pkg/cmd`, `internal/pkg/server`,
  `internal/pkg/logging`, and reload/watcher packages.
- Metrics: `etc/*.csv`, `internal/pkg/counters`, `internal/pkg/collector`,
  `internal/pkg/rendermetrics`, and metric contract tests.
- Helm and Kubernetes: `deployment/`, raw YAML examples, `tests/helm`, and
  GPU-backed Kubernetes e2e tests.
- Docker, packages, releases, and CI: `docker/`, `packaging/`, `.gitlab/ci/`,
  `.github/workflows/`, `hack/versions.env`, and version sync scripts.
- Test infrastructure: package tests under `internal/pkg`, plus
  `tests/docker`, `tests/integration`, `tests/e2e`, and shared test helpers.

## Feature Planning

Before editing, map the requested change to the owning surface above and read
the closest scoped `AGENTS.md`.

- Verify behavior from current code, tests, generated artifacts, and CI targets
  before using README-style docs as authority.
- List user-visible contracts affected: CLI flags/env vars, metrics and labels,
  Helm values/templates, Docker/package behavior, raw YAML, or docs examples.
- Choose the lowest useful test layer that proves the behavior. Prefer package
  tests for pure Go logic, chart tests for Helm contracts, and GPU/Docker/e2e
  gates only when that layer is required.
- Name prerequisite-dependent gates honestly. Do not report GPU, Docker,
  Kubernetes, DCGM, or network-dependent checks as passing when they were not
  run.
- Keep new Go code small and idiomatic: package-owned helpers, clear error
  context, table tests for contract cases, and no broad abstractions unless
  multiple production packages need them.

## Build And Validation

Run targeted checks first, then broader gates as risk increases:

```bash
go test ./internal/pkg/counters
make test-main
make lint
make check-fmt
make validate
```

Use conditional gates when relevant:

- `make generate` after changing generated mocks or `go:generate` inputs.
- `make validate-versions` after changing `hack/versions.env`, Dockerfile base
  versions, CI version variables, Helm chart versions, raw YAML versions, or
  README version examples.
- `make test-images` after Docker image, entrypoint, package-runtime, or image
  test changes, when local Docker and GPU prerequisites are available.
- `make test-integration` and `make test-e2e-k8s-gpu` only when the required
  Linux, DCGM, Docker, Kubernetes, and GPU prerequisites are available.

## Repository Conventions

- Keep changes scoped. Do not include unrelated local files or worktree state.
- Do not commit `.claude/settings.local.json`, local IDE files, credentials,
  logs, coverage outputs, `.go/`, `.cache/`, or `dist/`.
- Use `gofumpt`/`goimports` through the Makefile targets rather than ad hoc
  formatting.
- Commit messages must be descriptive and signed off with DCO, for example
  `git commit -s`.
- For version bumps, `hack/versions.env` is the source; run
  `make sync-versions` and `make validate-versions`.

## Domain Notes

- `pkg/cmd/app.go` owns CLI flags, environment variables, defaults, runtime
  startup, reload wiring, logging format, exporter-toolkit web config, and
  pprof opt-in behavior.
- `etc/default-counters.csv` is a primary metric source. Metric rows have
  exactly three CSV fields: DCGM/exporter field, Prometheus type, and help text.
- `deployment/values.yaml` and Helm templates must stay aligned with CLI/env
  behavior and raw YAML examples.
- Docker, packaging, and CI scripts must use strict shell behavior, preserve
  checksum/signature verification, and avoid swallowing verification failures.
- GPU-backed tests are valuable but environment-dependent; document skipped
  GPU/Kubernetes/Docker checks rather than pretending they ran.
