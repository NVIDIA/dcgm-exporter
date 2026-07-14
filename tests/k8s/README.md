# Kubernetes Integration Tests

The Kubernetes integration tests maintain confidence that dcgm-exporter works as expected
after changes. The tests reproduce a typical deployment scenario in a Kubernetes
environment and verify how the following components work together:
* Helm package - helm package can deploy the specified dcgm-exporter image;
* container image - container image contains all necessary components to run the dcgm-exporter;
* dcgm-exporter - binary executable starts, reads GPU metrics and produces expected results.

The basic test executes the following scenario:

1. Connect to the Kubernetes cluster;
2. Create a namespace;
3. Install the dcgm-exporter helm package;
4. The test waits until the dcgm-exporter is up and running;
5. When the dcgm-exporter is up and running, the test deploys a pod, that runs a workload on GPU.
6. The test reads `/metrics` endpoint output and verifies that the GPU metrics are available and contain labels, such as
`namespace`, `container` and `pod`.

If there aren't any errors during execution of steps from 1 to 6, the Kubernetes integration test is considered as passed.

## Test file layout

Keep reusable Kubernetes coverage in this package, grouped by the behavior under test:

* `suite_test.go` - suite setup, shared constants, and lifecycle hooks.
* `exporter_metrics_test.go` - default metrics, custom metrics replacement, ConfigMap-backed metrics, Helm-mounted YAML config, old namespace labels, and clock event counters.
* `exporter_configuration_test.go` - chart and exporter configuration behavior such as auth, TLS, pprof, debug dumps, hostname/model flags, and HPC job mapping.
* `exporter_access_test.go` - pod and service scrape access.
* `kubernetes_attribution_test.go` - Kubernetes labels, pod UID, GPU ID mode, DRA, and shared GPU attribution.
* `hardware_metrics_test.go` - hardware-backed metric families such as profiling, NVLink, NVSwitch, Grace CPU/Sysmon, and C2C.
* `mig_test.go` - MIG labels and `DCGM_EXPORTER_DEVICES_STR` selection behavior, including combined full-GPU plus MIG-instance selection.
* `gpu_operator_test.go` - GPU Operator deployment integration.
* `dcgm_failure_injection_test.go` - exporter handling of injected DCGM health values.
* `standalone_dcgm_test.go` - exporter connectivity to a standalone DCGM.
* `exporter_helpers_test.go`, `metric_assertions_test.go`, and `metric_validation_helpers_test.go` - shared setup helpers and metric assertions.

## Prerequisites

1. NVIDIA GPU-compatible hardware for use with DCGM (Requirements: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/getting-started.html)
2. Kubernetes cluster with configured NVIDIA container tool kit (https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/index.html).
For local GPU development on Linux, the repository also provides a k3d workflow that creates a GPU-backed cluster and deploys a locally built dcgm-exporter image.

## How to run Kubernetes integration tests

Use root Makefile targets as the public entrypoints:

```shell
make test-integration-k8s
make test-e2e
```

`make test-integration-k8s` runs this reusable Kubernetes suite against the kubeconfig you provide. `make test-e2e` runs the higher-level Go `e2e` CLI, which can create local k3d by default, probe host and cluster capabilities, and then call this suite with the selected labels.

The `tests/k8s/Makefile` is a focused local-suite helper for Ginkgo labels and JUnit output. Use it when iterating inside this package, but prefer the root targets for documented workflows.

### Scenario: Test the current DCGM-exporter release

The scenario installs the dcgm-exporter with default configuration, defined in the helm package [values](https://github.com/NVIDIA/dcgm-exporter/blob/main/deployment/values.yaml).

```shell
KUBECONFIG="~/.kube/config" make test-integration-k8s
```

### Scenario: Build images, deploy and test DCGM-exporter after changes

1. Build local images;

```shell
make local
```

2. Run tests

```shell
KUBECONFIG="~/.kube/config" IMAGE_REPOSITORY="nvidia/dcgm-exporter" make test-integration-k8s
```

### Scenario: GPU metric contract and configuration matrix

The focused GPU contract target validates the default metric contract, TLS and basic-auth metrics parsing, custom metrics replacement, ConfigMap-backed metrics, Helm-mounted YAML config, old namespace labels, and exporter-owned clock event counters. The default scenario reads `etc/default-counters.csv` and validates every emitted default row; fields with unknown support are reported as skips, while fields marked supported by e2e-provided evidence are required. The contract verifies that baseline GPU samples are tied to the Kubernetes workload pod, namespace, and container rather than accepting unrelated GPU samples. Capability-specific checks for profiling, MIG, NVLink/NVSwitch, B200, and GB200 are enabled when those signals are present in the scraped metrics, with explicit skip logs otherwise.

For large refactors, run this target manually against a known GPU Kubernetes target when one is available. It is not part of the default CI suite because it requires a live GPU Kubernetes cluster.

When running against a multi-node GPU cluster, set `NODE_SELECTOR_KEY` and `NODE_SELECTOR_VALUE` so the exporter and workload pod are scheduled onto the same intended GPU node.

```shell
KUBECONFIG="~/.kube/config" make test-integration-k8s
```

To run directly from this directory:

```shell
KUBECONFIG="~/.kube/config" make test-integration-k8s
```

## Go E2E CLI

`make test-e2e` runs the Go `e2e` CLI. With no kubeconfig, it
creates or reuses a local GPU-backed k3d cluster, installs the NVIDIA RuntimeClass
and device plugin, probes host and cluster capabilities, and runs the selected
Kubernetes labels. With `--kubeconfig`, it targets an existing cluster and only
owns validation namespaces and workloads.

```shell
make test-e2e
go run ./cmd/e2e tests --kubeconfig /path/to/kubeconfig
go run ./cmd/e2e tests --dry-run
go run ./cmd/e2e tests --list-scenarios
go run ./cmd/e2e tests --scenario k8s/nvlink
go run ./cmd/e2e cluster up
go run ./cmd/e2e cluster deploy
go run ./cmd/e2e cluster status
go run ./cmd/e2e cluster logs
go run ./cmd/e2e cluster cleanup
```

The default local cluster is `dcgm-exporter-gpu`, the default namespace is
`dcgm-exporter`, and the selected local node is labeled
`dcgm-exporter.nvidia.com/gpu-node=enabled` for exporter and workload scheduling.
The local k3d node image is built from `rancher/k3s` at the `K3S_VERSION`
pinned in `hack/versions.env` and the pinned CUDA Ubuntu base image. Docker
Hub images (`rancher/k3s` and `busybox`) pull from Docker Hub by default.
Live container and Kubernetes runs require a complete exporter image reference
through `E2E_EXPORTER_IMAGE` or `--exporter-image`; local k3d mode imports a
matching host-local Docker image into the cluster when it exists in the Docker
cache.

Pass `--result-markers` to emit machine-readable `&&&&` markers for setup,
group execution, pre-execution skipped scenarios, and Ginkgo specs. Harness-level
unavailable work is reported as `SKIPPED`; Ginkgo runtime skips are reported as
`WAIVED`. Every skipped scenario first emits its matching `RUNNING` marker so
automation receives a complete lifecycle.

Feature-specific paths are selected from probed capability state. MIG and shared
GPU groups temporarily reconfigure the NVIDIA device plugin in owned local k3d
runs and restore the default device-plugin configuration afterward. Standalone
DCGM groups deploy `nv-hostengine` into a separate namespace using the
configured DCGM image. GPU Operator and DRA scenarios run when the target cluster
already advertises those capabilities.
