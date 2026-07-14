# tests - Agent Instructions

The test tree contains multiple layers with different prerequisites. Pick the
lowest layer that proves the behavior.

## Test Layers

- Package/unit tests: normal `go test` packages, no GPU expected.
- `tests/static`: rendered chart, source, package, and Dockerfile checks with
  no live infrastructure.
- `tests/host`: GPU/DCGM-backed exporter behavior on the host.
- `tests/container`: container startup, metrics, health, and runtime checks;
  requires local images or pullable images plus Docker/GPU access.
- `tests/k8s`: Kubernetes/Helm deployment flows; requires a GPU-capable
  Kubernetes cluster or the local e2e k3d commands.
- `tests/internal`: reusable test-only contracts and helpers.

## Guidance

- Do not weaken assertions to avoid GPU or cluster prerequisites. Use a lower
  test layer for logic that can be tested without hardware.
- Keep setup and teardown in the infrastructure-specific category, and move
  reusable exporter assertions to `tests/internal`.
- Prefer readiness probes and explicit scrape/health checks over fixed sleeps.
- When GPU, Docker, or Kubernetes prerequisites are unavailable, report the
  skipped gate and the missing prerequisite.
