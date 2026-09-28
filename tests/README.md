# DCGM-Exporter Tests

Tests are organized by the infrastructure they require:

- `static`: no live infrastructure; chart, source, package, and Dockerfile static checks.
- `host`: direct exporter/DCGM tests on the host; requires a GPU and host DCGM libraries.
- `container`: Docker/container runtime tests; requires Docker and GPU container runtime access.
- `k8s`: Helm/Kubernetes scenarios; requires a kubeconfig or local GPU k3d cluster.
- `ci`: shell tests for CI and release-helper scripts.
- `internal`: shared test-only contracts and helpers.

Prefer putting exporter behavior assertions in `internal` when they can be reused across host, container, and Kubernetes tests. Environment-specific setup and teardown should stay in the category that owns that infrastructure.

Use the root `Makefile` as the public entrypoint for test categories:

```bash
make test-static
make test-integration-host
make test-integration-container
make test-integration-k8s
make test-e2e
```

The `tests/container/Makefile` and `tests/k8s/Makefile` files are focused local-suite helpers for Ginkgo-specific options. Other test categories intentionally rely on the root Makefile or direct `go test` commands.

`make build-e2e` builds the reusable CLI at `bin/e2e`. Focused E2E and cluster workflows use that binary directly, for example `./bin/e2e tests --suite static` or `./bin/e2e cluster up`. E2E CLI implementation tests are included in `make test-main` and can be run directly with `go test ./cmd/e2e ./internal/e2e/...`.
