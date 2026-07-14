---
name: comprehensive-testing
description: Use when adding or selecting tests for DCGM Exporter changes.
---

# Comprehensive Testing

Pick the lowest layer:

- Package tests for pure Go logic.
- `tests/helm` for chart contracts.
- `tests/docker` for image startup and endpoint behavior.
- `tests/integration` for DCGM/GPU executable behavior.
- `tests/e2e` for Kubernetes/Helm deployment behavior.

Document unavailable GPU, Docker, Kubernetes, or DCGM prerequisites.
