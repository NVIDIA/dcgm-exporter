# tests - Agent Instructions

The root `AGENTS.md` still applies. This file adds rules for `tests/`.

The test tree contains multiple layers with different prerequisites. Pick the
lowest layer that proves the behavior.

## Test Layers

- Package/unit tests: normal `go test` packages, no GPU expected.
- `tests/static`: rendered chart, source, package, and Dockerfile checks with
  no live infrastructure.
- `tests/host`: GPU/DCGM-backed exporter behavior on the host.
- `tests/container`: container startup, metrics, health, and runtime checks;
  requires `--tags=container` plus local images or pullable images and
  Docker/GPU access. Without the tag, `go test ./tests/container` finds no tests.
- `tests/k8s`: Kubernetes/Helm deployment flows; live specs require
  `--tags=k8s` and a GPU-capable cluster or local k3d. Without the tag, only
  untagged helper unit tests compile.
- `tests/internal`: reusable test-only contracts and helpers.

## Guidance

- Do not weaken assertions to avoid GPU or cluster prerequisites. Use a lower
  test layer for logic that can be tested without hardware.
- Keep setup and teardown in the infrastructure-specific category, and move
  reusable exporter assertions to `tests/internal`.
- Prefer readiness probes and explicit scrape/health checks over fixed sleeps.
