# Host Integration Tests

Host tests execute a built `dcgm-exporter` product binary and validate behavior
against host DCGM and GPU libraries. The test binary itself is cgo-free; DCGM
linkage lives in the product binary under test.

## Prerequisites

- Linux host with an NVIDIA GPU.
- NVIDIA driver and DCGM installed on the host.
- Go installed at the version pinned by this repository when running from source.
- A built `dcgm-exporter` product binary, passed with `-exporter-binary` for raw
  `go test` runs. The root Makefile and e2e CLI build and pass this automatically.
- `nv-hostengine`, root command access, and `/dev/vsock` for the
  `dcgm_uri_integration` suite.

## Primary Command

Run from the repository root:

```bash
make test-integration-host
```

Host live scenarios use Ginkgo labels. For focused iteration, run the package
directly with a label filter:

```bash
go build -o /tmp/dcgm-exporter ./cmd/dcgm-exporter
go test ./tests/host -v -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=startupMetrics
go test ./tests/host -v -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter='startupTLS || reload'
```

The direct DCGM URI scenario is behind the `dcgm_uri_integration` build tag
because it needs `nv-hostengine`, root command access, and VSOCK support. Run it
explicitly with the `dcgmUri` label:

```bash
E2E_REQUIRE_VSOCK=1 E2E_REQUIRE_DCGM=1 go test --tags=dcgm_uri_integration ./tests/host -v \
  -args -exporter-binary=/tmp/dcgm-exporter --ginkgo.label-filter=dcgmUri
```

The e2e CLI selects ordinary host scenarios and direct URI coverage through
Ginkgo labels such as
`startupMetrics`, `configFile`, `hpcJobMapping`, `startupTLS`, `reload`, `ipv6Listen`,
`systemdSocket`, and `dcgmUri`.

Pass `--result-markers` to emit `&&&&` markers for each executed spec.
`--no-result-markers` disables both the host suite lifecycle and per-spec
markers. Runtime Ginkgo skips are reported as `WAIVED`.

Expect startup and some scrape checks to take at least one DCGM collection interval. By default, dcgm-exporter uses a 30-second polling interval, so tests wait for metrics instead of relying on fixed short sleeps.

## File Layout

- `startup_metrics_test.go`: direct startup, metrics scrape, YAML config file, and HPC job mapping behavior.
- `startup_tls_test.go`: TLS and basic-auth behavior when running on the host.
- `ipv6_listen_test.go`: direct IPv6 loopback listen and metrics scrape.
- `systemd_socket_test.go`: inherited systemd socket activation listener behavior.
- `reload_test.go`: SIGHUP and file-watcher reload behavior.
- `suite_test.go`: Ginkgo suite registration for ordinary host scenarios.
- `remote_dcgm_uri_suite_test.go`: Ginkgo suite registration for the `dcgm_uri_integration` build-tagged scenario.
- `remote_dcgm_uri_test.go`: opt-in live remote DCGM URI checks behind the `dcgm_uri_integration` build tag.
- `helpers_test.go`: shared host test helpers.

## Guidance

- Keep host-specific setup, process management, YAML config, HPC/SLURM-style job mapping, and signal behavior in this directory.
- Move reusable scrape or metric assertions into `tests/internal` when they can also be used by container or Kubernetes tests.
- Report missing GPU, DCGM, or `nv-hostengine` prerequisites as skips when the test is optional for the normal host target.
