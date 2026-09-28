# Static Tests

Static tests validate repository artifacts without starting dcgm-exporter, Docker, or Kubernetes. This category is for rendered Helm chart checks, Dockerfile/source invariants, package layout checks, and other deterministic assertions that should run in ordinary CI.

Use the root Makefile target:

```bash
make test-static
```

For focused iteration, run the Go package directly:

```bash
go test ./tests/static
```

Static scenarios are plain Go tests, so focused selection uses `-run`:

```bash
go test ./tests/static -run '^TestChartKubernetesRBACRenderContract$'
go test ./tests/static -run '^TestChartImageRenderingContract$'
go test ./tests/static -run '^TestChartServiceMonitorRenderingContract$'
go test ./tests/static -run '^TestPackageSystemdUnitContract$'
go test ./tests/static -run '^TestRepoPathResolvesSourceAndPackageFiles$'
```

The static suite owns deterministic chart and package contracts such as image digest rendering, image pull secret pass-through, ServiceMonitor endpoint settings, and packaged systemd unit expectations.

Keep live infrastructure checks out of this directory. Host GPU behavior belongs in `tests/host`, container runtime behavior in `tests/container`, and Kubernetes deployment behavior in `tests/k8s`.
