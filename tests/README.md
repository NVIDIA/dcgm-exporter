# DCGM-Exporter Tests

Each directory holds tests that need the same infrastructure. Pick the lowest
layer that proves the behavior.

| Directory | Covers | Needs | Guide |
| --- | --- | --- | --- |
| `static` | Chart, source, package and Dockerfile checks | Nothing live | [README](static/README.md) |
| `host` | Exporter binary against DCGM on the host | GPU and host DCGM libraries for most scenarios; see below | [README](host/README.md) |
| `container` | Container images and runtime | Docker, GPU container runtime | [README](container/README.md) |
| `k8s` | Helm and Kubernetes scenarios | Kubeconfig or local GPU k3d cluster | [README](k8s/README.md) |
| `internal` | Shared test-only contracts and helpers | | |
| `cmd` | `e2e-dcgm-probe`, the direct DCGM probe the suites call | | |

Put exporter behavior assertions that several suites can reuse in `internal`.
Keep setup and teardown in the suite that owns the infrastructure.

Host scenarios that need no GPU run in the GPU-disabled `test-e2e-dcgm-mocks`
CI job:

- `host/configStartupErrors` and `host/configInspection` need only the
  exporter binary.
- The `host-config-injection` scenarios use `--dcgm-nvml-injection-yaml`
  instead of a GPU. `host/configServesMetrics` requires that fixture; the others
  also run on a GPU in `test-e2e-host`.
- The cache-count checks in `host/configFile`, `host/watchGroups` and
  `host/configPrecedence` need `nv-hostengine` on `PATH`.

The [E2E architecture guide](../docs/architecture/e2e/README.md) has the
details.

## Run suites

From the root `Taskfile.yml`:

```bash
task test:static
task test:host
task test:container
task test:k8s
task test:e2e    # builds the CLI and images, can create a local cluster, runs all suites
```

These run from the bundle that `task build:e2e` writes, as CI does:

```bash
task test:e2e:smoke      # the static/paths scenario; no GPU
task test:e2e:host       # host scenarios on a GPU host with DCGM
task test:e2e:mocks NVML_INJECTION_DIR=/path NVSDM_MOCK_DIR=/path  # NVML injection and NVSDM mock fixtures; no GPU
task test:e2e:container  # builds the image and runs the container suite; needs Docker and a GPU
```

- `test:e2e:host` builds its own exporter with coverage and writes
  `integration_binary.cov`. It needs the GPU driver, the DCGM libraries,
  `nv-hostengine` and Go.
- `test:e2e:mocks` needs root, a DCGM development build with NVML injection
  (`nv-hostengine` and `dcgmi` on `PATH`), and NVML injection and NVSDM mock
  fixtures that match `DCGM_VERSION` in `hack/versions.env`.

Direct `go test` notes:

- `go test ./tests/container` needs `--tags=container`; without it no tests are
  found.
- `go test ./tests/k8s` needs `--tags=k8s`; without it only helper unit tests
  run. Prefer `task test:k8s`, which runs the e2e CLI.

## Run one scenario with Go

Each E2E scenario maps to a Go test name or a Ginkgo label. List the mappings:

```bash
./bin/e2e tests --list-scenarios --verbose
```

Run the printed command with the prerequisites from the suite's README.

Static (no live infrastructure):

```bash
go test ./tests/static \
  -run '^(TestChartKubernetesRBACRenderContract)$'
```

Host (build the product binary first and pass it):

```bash
go build -o /tmp/dcgm-exporter ./cmd/dcgm-exporter

CGO_ENABLED=0 go test --tags=dcgm_uri_integration ./tests/host \
  -run '^TestHostIntegration$' \
  -args -exporter-binary=/tmp/dcgm-exporter \
  --ginkgo.label-filter=startupMetrics
```

Container (needs the exporter image and Docker with GPU access):

```bash
go test --tags=container ./tests/container \
  -run '^TestDockerImages$' \
  -args --ginkgo.label-filter=imageStartup
```

Kubernetes (needs the flags and GPU cluster in `tests/k8s/README.md`):

```bash
go test --tags=k8s ./tests/k8s \
  -run '^TestKubernetesIntegration$' \
  -args --ginkgo.label-filter=default
```

The E2E CLI adds bootstrap, image preparation, local k3d setup and cleanup, and
machine-readable results.

## E2E CLI

`task build:e2e` builds `bin/e2e`:

```bash
./bin/e2e tests --suite static
./bin/e2e cluster up
```

CLI unit tests run in `task test:unit`, or directly:

```bash
go test ./cmd/e2e ./internal/e2e/...
```

### Host dependencies

E2E expects a healthy Linux image, a working NVIDIA driver, and the package
sources the host owner manages.

- It checks `nvidia-smi` and GPU container access. It never installs a driver
  or reboots.
- It may install only the selected test dependencies and its own NVIDIA
  Container Toolkit and CUDA repository files. Other package sources, keys and
  software stay unchanged.
- Package installs run without prompts and keep configuration files the host
  has changed. Ubuntu's needrestart is skipped, so it neither asks nor restarts
  services. Package scripts can still start or restart their own services, and
  GPU runtime repair restarts Docker.
- On Ubuntu, installs run `sudo DEBIAN_FRONTEND=noninteractive
  NEEDRESTART_SUSPEND=1 apt-get ...`, so the `sudo` rule must allow setting
  variables: a rule for `ALL` commands or one tagged `SETENV`.
- Set a mode per dependency with `--bootstrap-component name=check|install|skip`
  or `E2E_BOOTSTRAP_COMPONENTS`. The command line wins.
- Components: `docker`, `buildx`, `container-toolkit`, `gpu-runtime`,
  `host-dcgm`, `kubernetes-tools`, `vsock`.

Bootstrap reports each selected package as:

| State | Meaning |
| --- | --- |
| ready | Installed. For `host-dcgm`, the runtime packages share one version whose upstream part is `DCGM_VERSION` in `hack/versions.env`, the NVIDIA CUDA repository offers that exact build, and the DCGM library is visible to the dynamic loader. On Ubuntu, when `apt-cache madison` fails or the approved repository lists no build of the pin, only the installed upstream version is compared. |
| missing | Not installed, or for `host-dcgm` installed but not ready. E2E prints `host-dcgm not ready: <reason>`. When a run needs the DCGM development package, it is reported as `dcgm-development`, ready when it matches the runtime's version, the approved repository offers it and the injection library is visible; otherwise E2E prints `dcgm-development not ready: <reason>`. |
| blocked | Missing, and its mode is `check`, so E2E will not install it. |
| unknown | The package manager could not inspect it. E2E prints the package manager's error. |

Only VSOCK is reported as blocked without stopping the run. Every other
component still stops the run in `check` mode when it is missing, even if just
some scenarios need it. In `check` mode, missing VSOCK loopback prints
`vsock=blocked` and the run continues, and the VSOCK scenarios fail unless
`--bootstrap-component vsock=skip` is set.

On Ubuntu, installing `host-dcgm` or `dcgm-development` names each DCGM
package with the selected build, together with every split package (`dcgmi`,
`libdcgm`, `nv-hostengine`, the modules) that those packages require at exactly
that build, as `apt-cache show` reports it. A host APT pin that prefers another
DCGM build therefore does not stop the install. Each of these packages must be
offered at that build by the approved NVIDIA CUDA repository and by no other
source, so APT fetches it from there; otherwise the install stops, naming the
package and the source that failed the check.

### Preflight checks

In order:

1. Bootstrap, and any requested Docker login.
2. Selected scenario images: `docker image inspect`, then
   `docker manifest inspect` when the image is not local. It never pulls.
3. GPU runtime smoke check, local cluster setup, then suite execution.

`--bootstrap-only` runs bootstrap and the smoke check, then stops before the
scenario-image checks.

For Kubernetes scenarios, after the CLI creates a local cluster or accepts the
supplied kubeconfig, and before it creates registry-auth resources, it runs
`kubectl get --raw=/readyz`. A ready API does not prove nodes can pull an image.

### Local k3d cluster

- `--k3d-cgroup-v1=auto` (default) adds the kubelet cgroup-v1 setting only when
  the host uses cgroup v1. `true` always adds it; `false` rejects a cgroup-v1
  host.
- The setting applies to E2E-owned k3d clusters only. `--kubeconfig` ignores it.
- Helm repository metadata and caches live in `.local-e2e/helm-<run-name>/` and
  are removed by cluster cleanup. Your own Helm files, registry credentials,
  plugins and Kubernetes context are never touched.

## CPU-only performance benchmarks

Developer benchmarks for two boundaries, using prebuilt synthetic GPU metrics.
They need the Go/CGO toolchain, no GPU or DCGM.

| Benchmark | Times | Outside the timed loop |
| --- | --- | --- |
| `BenchmarkRender` (`internal/pkg/rendermetrics`) | Metric-family construction, validation, sorting, and Prometheus text encoding to `io.Discard` | Fixture construction, its preflight render, output retention |
| `BenchmarkFakeDCGMScrape` (`internal/pkg/server`) | Response reset and the in-process `/metrics` handler: scrape coordination, registry gather, an empty transformation pass, rendering, response buffering, count-only writes | Fixture, server and request construction, DCGM calls, production transformations, HTTP transport, response retention |

Both run 1, 26 and 104 metric families across 1, 2, 4 and 8 GPU entities:

- 26 is the 27 active rows of `etc/default-counters.csv` minus
  `DCGM_FI_SYSTEM_DRIVER_VERSION`, a label-only row.
- 104 is a synthetic stress case at four times that. It is not a production
  preset.

Go reports time and allocations for both, plus throughput for the renderer.
Custom metrics: the renderer reports output bytes and series per operation. The
server reports average response bytes (which include exporter-owned families
beyond the fixture) and synthetic series per operation.

Run the matrix:

```bash
go test ./internal/pkg/rendermetrics ./internal/pkg/server \
  -run '^$' \
  -bench 'Benchmark(Render|FakeDCGMScrape)$' \
  -benchmem \
  -count=1
```

To compare two revisions, run the same command with `-count=5` at each, on the
same host, toolchain and load, then compare with `benchstat`:

```bash
# At <before-commit>:
go test ./internal/pkg/rendermetrics ./internal/pkg/server \
  -run '^$' -bench 'Benchmark(Render|FakeDCGMScrape)$' -benchmem -count=5 \
  > /tmp/dcgm-exporter-before.txt

# At <after-commit>:
go test ./internal/pkg/rendermetrics ./internal/pkg/server \
  -run '^$' -bench 'Benchmark(Render|FakeDCGMScrape)$' -benchmem -count=5 \
  > /tmp/dcgm-exporter-after.txt

benchstat /tmp/dcgm-exporter-before.txt /tmp/dcgm-exporter-after.txt
```

Results are not comparable across machines. There are no portable baselines,
regression thresholds or CI gate, and these are not GPU, DCGM, hostengine,
deployment or qualification results.
