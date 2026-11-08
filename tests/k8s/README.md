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
* `gpu_inventory_test.go` - compares the default scenario's GPU identity labels and values with `nvidia-smi`.
* `exporter_configuration_test.go` - chart and exporter configuration behavior such as auth, TLS, pprof, debug dumps, hostname/model flags, and HPC job mapping.
* `exporter_access_test.go` - pod and service scrape access.
* `chart_mounts_test.go` - the chart's own directory mounts with default values; `chart_mounts_contract_test.go` holds the untagged pod checks.
* `chart_existing_config_test.go` - `configMap.existingName` and `configMap.key`: the exporter reads a ConfigMap the user manages; `chart_existing_config_contract_test.go` holds the untagged config document and checks.
* `chart_upgrade_test.go` - `helm upgrade` from the last released chart (`previousChartVersion` in `chart_upgrade_test.go`): the chart rejects the old chart's removed values, names their replacements, and upgrades with them. It is a `manual` scenario because it installs the released chart from its public repository.
* `chart_names_contract_test.go` - untagged helper that mirrors the chart's release-scoped object names (`<fullname>-metrics`).
* `kubernetes_attribution_test.go` - Kubernetes labels, pod UID, GPU ID modes, renamed GPU resources, DRA, and shared GPU attribution.
* `hardware_metrics_test.go` - hardware-backed metric families such as profiling, NVLink, NVSwitch, Grace CPU/Sysmon, and C2C.
* `hardware_selection_contract_test.go` - untagged helpers that pick IDs from a default run's scrape and check a selected-subset run reports only those switches, links, CPUs, cores or MIG instance. The scenarios that use them live in `hardware_metrics_test.go` and `mig_test.go`.
* `mig_test.go` - MIG labels, `DCGM_EXPORTER_DEVICES_STR` selection behavior (`entities.gpu.instances` is covered through that flag), including combined full-GPU plus MIG-instance selection, and attribution of a pod on a MIG instance.
* `gpu_operator_test.go` - GPU Operator deployment integration; `gpu_operator_contract_test.go` holds the untagged ClusterPolicy, DaemonSet, time-slicing and DRA object checks.
* `nvlink_fabric_contract_test.go` - untagged check that the exporter reports every NVLink `nvidia-smi` lists as active.
* `standalone_dcgm_test.go` - exporter connectivity to a standalone DCGM.
* `exporter_helpers_test.go`, `metric_assertions_test.go`, and `metric_validation_helpers_test.go` - shared setup helpers and metric assertions.

## Prerequisites

1. NVIDIA GPU-compatible hardware for use with DCGM (Requirements: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/getting-started.html)
2. Kubernetes cluster with configured NVIDIA container tool kit (https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/index.html).
For local GPU development on Linux, the repository also provides a k3d workflow that creates a GPU-backed cluster and deploys a locally built dcgm-exporter image.

## How to run Kubernetes integration tests

Run these tasks from the repository root:

```shell
task test:k8s
task test:e2e
```

`task test:k8s` runs `go run ./cmd/e2e tests --suite k8s`. When `KUBECONFIG` is
set, it passes that file as `--kubeconfig` and runs against that cluster. When
it is unset, the e2e CLI creates or reuses a local k3d cluster. The CLI checks
what the cluster supports, prepares each scenario group, and skips scenarios the
cluster cannot run. Narrow a run with CLI flags after `--`, for example
`task test:k8s -- --scenario k8s/tls`, and preview it with
`task test:k8s -- --dry-run`. `task test:e2e` builds the local prerequisites and
runs every suite.

### CPU helper contracts

The Kubernetes package also has untagged checks for manifests, metric contracts,
image selection, and lifecycle helpers. They do not contact a cluster, need a
kubeconfig, or require a GPU. Run them with ordinary Go tooling:

```shell
go test ./tests/k8s/...
```

`task test` includes these checks through `task test:unit`. Live Kubernetes
specs remain behind `--tags=k8s` and run through the e2e CLI (`task test:k8s`).

### Scenario: Test the current DCGM-exporter release

The scenario installs the dcgm-exporter with default configuration, defined in the helm package [values](https://github.com/NVIDIA/dcgm-exporter/blob/main/deployment/values.yaml).

```shell
KUBECONFIG="$HOME/.kube/config" task test:k8s
```

### Scenario: Build the image, deploy and test DCGM-exporter after changes

1. Build the local image:

```shell
task build:image:local
```

2. Run tests

```shell
KUBECONFIG="$HOME/.kube/config" task test:k8s -- --exporter-image "<image reference built above>"
```

### Scenario: GPU metric contract and configuration matrix

The focused GPU contract target validates the default metric contract, TLS and basic-auth metrics parsing, custom metrics replacement, ConfigMap-backed metrics, Helm-mounted YAML config, old namespace labels, and exporter-owned clock event counters. The default scenario reads `etc/default-counters.csv` and validates every emitted default row. It also compares GPU identity labels and values with `nvidia-smi`: run on the test host for the local k3d cluster the CLI created, otherwise inside the workload pod for that pod's GPU only. Hardware-specific scenarios are selected by the tiered probes described below. The contract verifies that baseline GPU samples are tied to the Kubernetes workload pod, namespace, and container rather than accepting unrelated GPU samples.

For large refactors, run this target manually against a known GPU Kubernetes target when one is available. It is not part of the default CI suite because it requires a live GPU Kubernetes cluster.

When running against a multi-node GPU cluster, pass `--k8s-node-selector-key` and `--k8s-node-selector-value` so the exporter and workload pod are scheduled onto the same intended GPU node.

```shell
KUBECONFIG="$HOME/.kube/config" task test:k8s
```

To run while working in this directory:

```shell
KUBECONFIG="$HOME/.kube/config" task --dir ../.. test:k8s
```

## Go E2E CLI

`task test:e2e` runs the Go `e2e` CLI. With no kubeconfig, it creates or reuses a
local GPU-backed k3d cluster, installs the NVIDIA RuntimeClass and device
plugin, probes host and cluster capabilities, and runs the selected Kubernetes
labels. With `--kubeconfig`, it targets an existing cluster.

The default run name is `dcgm-exporter`; use `--run-name` or `E2E_RUN_NAME` to
set it. The run name derives the cluster (`<name>-gpu`), namespaces (`<name>`
and `<name>-dcgm`), and Helm release (`<name>`), each of which can be overridden
with its own flag or environment variable; setup and cleanup reuse or remove
namespaces only when their ownership labels match the run name, and concurrent
GPU Operator scenarios need separate clusters because the Operator manages
cluster-scoped resources. With `--cleanup=false`, retained Operator resources
remain owned by their original run name and must be cleaned before another name
uses the cluster; main-chart scenarios require an empty cluster even for
same-name reruns.

For an existing cluster, the Kubernetes target is the source of allocation facts.
The controller running `e2e` does not need a GPU or Docker merely to run an
ordinary, MIG, or DRA scenario. The target must already expose the allocation
the selected scenario uses: `nvidia.com/gpu` for ordinary GPU work,
`nvidia.com/mig-*` for MIG work, or NVIDIA full-GPU DRA `ResourceSlice` entries
for DRA work. The target cluster must also be able to pull the configured images.
If you provide private-registry login credentials, the controller also needs
the Docker CLI for `docker login`; it still does not need a Docker daemon.

```shell
# The controller needs kubectl, Helm, Go, and cluster credentials; the target supplies the GPU.
go run ./cmd/e2e tests \
  --kubeconfig /path/to/kubeconfig \
  --scenario k8s/mig \
  --exporter-image registry.example/dcgm-exporter:tag \
  --cuda-workload-image registry.example/cuda-workload:tag \
  --bootstrap=skip
```

Hardware-specific scenarios that need facts Kubernetes does not report remain
explicitly unqualified on an external target. The CLI does not substitute GPU
evidence from the controller for those target facts. For the same reason it
does not produce `E2E_DCGM_FIELD_SUPPORT` for an external target, so a CSV row
the exporter does not emit is reported as an unqualified skip unless you set
that variable yourself.

```shell
task test:e2e
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

Feature-specific paths use two probe tiers:

* A feature probe answers a coarse question about the host or cluster, such as
  whether a GPU has MIG on with a GPU instance, the topology contains NVLink or
  NVSwitch hardware, or the Kubernetes API serves DRA ResourceSlices.
* An applicability probe narrows a supported feature to a scenario contract,
  such as requiring multiple GPUs, NVLink 5 fields, or a locally published
  NVSwitch telemetry provider.

An unsupported feature skips every scenario that needs it. Unsupported
applicability skips only that scenario. If a nonempty selected plan has no
runnable groups after skips, setup fails so a run with no executed checks cannot
pass. Individual unsupported scenarios are still reported as skipped. An
unknown target capability is a setup failure because the harness cannot make a
reliable selection. For an external target, an unknown controller-host
capability is instead an explicit skip: that controller fact was intentionally
not probed and must not stand in for target evidence. Probes do not claim that a
feature is configured or healthy: after a scenario is selected, its setup and
test assertions must configure what they are allowed to configure and fail if
the required runtime behavior does not work.

For example, NVLink topology selects the NVLink fabric and bandwidth scenarios.
The fabric scenario then fails if a reported fabric is not
`Completed`/`Success`, if `nvidia-smi nvlink -s` shows no active link, or if the
exporter has no per-link NVLink sample with the GPU UUID and link index of each
link `nvidia-smi` reports active. On the owned local cluster `nvidia-smi` runs on
the test host; on other clusters it runs in the workload pod on the scraped
exporter's node. The bandwidth scenario fails if the exporter does not emit its
metric. NVLink 5 applicability independently selects the integrity
metric scenario. NVSwitch topology selects switch coverage, while architecture
and protocol generation decide whether local temperature, NSCQ link, or NVSDM
throughput telemetry applies. A GB200 with broken fabric configuration is
therefore tested and fails instead of being hidden by a capability skip.

MIG, shared GPU and renamed GPU groups temporarily reconfigure the NVIDIA device plugin in
owned local k3d runs and restore the default configuration afterward. MIG host
mutation occurs only when `--mig-configure` is `true` or a profile ID and a
selected MIG scenario needs it; the default `auto` uses MIG as it is. MIG
scenarios are skipped when no GPU has MIG on with a DCGM GPU instance and the
run may not change MIG. Mixed MIG scenarios are also skipped when no GPU has MIG
off, because a MIG change does not turn MIG off. A requested MIG change that
leaves no usable MIG state fails planning, unless the GPU advertises no MIG
profiles; then MIG scenarios are skipped. While MIG is changed, the harness
regenerates the host's `/etc/cdi/nvidia.yaml` so pods see the MIG device; after
MIG is restored it writes the original spec back with its mode and owner, or
removes the spec when the host had none, and removes `/etc/cdi` when the run
created it. A spec that is a symbolic link stops the MIG change before it starts.
Standalone DCGM
groups deploy `nv-hostengine` into a separate namespace using the configured
DCGM image. DRA selection for an owned local cluster is based on API capability
because setup can install the resources. An external target is planned the same
way when `--dra-configure=true` asks setup to install the NVIDIA DRA driver;
otherwise it must already report the required NVIDIA DRA resources. The CLI
never infers them from an ordinary GPU allocation or the controller. Setup
installs the driver only into an `nvidia-dra-driver-gpu` namespace the run owns
and fails when a node already has the `nvidia.com/dra-kubelet-plugin` label,
whatever its value, because cleanup removes that label from every node.

Live Dynamic MIG creation and removal are not supported. Start or restart the
exporter after the MIG layout changes so DCGM and the container runtime can
discover and grant access to the current devices. The E2E suite covers ordinary
GPU DRA and MIG instances that exist before dcgm-exporter starts.

### Workload GPU attribution

`default`, `podUID`, `kubernetesGpuId` and `customResourceNames` read the
workload pod's GPU from inside the pod (the `nvidia-smi --query-gpu` inventory
query, which lists only the pod's GPU there). Every whole-GPU `DCGM_FI_` sample
of that GPU must carry the pod's `pod`, `namespace` and `container` labels, and
no sample of another GPU may carry a `pod` label with the pod's name. MIG
instance samples are left out of the first check. `podUID` also requires
`pod_uid` to equal the pod's `metadata.uid`; `default`, with pod UID collection
off, requires no `pod_uid` on the pod's GPU. These checks also fail when a
sample without a GPU UUID label (NVSwitch, NVSwitch link, CPU or CPU core)
carries a `pod`, `namespace`, `container`, `pod_uid` or `pod_label_*` label,
or when a GPU NVLink sample (which names its GPU in `gpu_uuid`) of another GPU
names the pod.

`migPodAttribution` runs a workload that requests one `nvidia.com/mig-*`
resource, with Grace CPU metrics on. The workload is pinned to a node that can
allocate that resource. It reads the pod's MIG device from
`nvidia-smi -q -x` inside the pod. Every `DCGM_FI_` sample of that GPU instance
(its parent GPU's UUID and its `GPU_I_ID`) must name the pod, no other GPU or
MIG instance sample may (GPU NVLink samples included), and no CPU or CPU core
sample may carry pod labels. The scrape must have both CPU and CPU core samples.
It plans only on a host with Grace CPUs and a MIG resource.

`kubernetesGpuId` runs `--kubernetes-gpu-id-type=uid` and `device-name`. The
NVIDIA device plugin (with its default UUID device-ID strategy) gives the
kubelet GPU UUIDs as device IDs, so `uid` attributes the pod exactly. In
`device-name` mode, which matches `nvidia<N>` device names, no GPU names the
pod; the scenario checks this for 30 seconds. Device-name matching is covered
by `TestProcessPodMapper_WithD_Different_Format_Of_DeviceID` in
`internal/pkg/transformation`.

`customResourceNames` runs in the `embedded-renamed-gpu` group, which
time-slices GPUs with `renameByDefault: true` so the device plugin advertises
them as `nvidia.com/gpu.shared`. A workload requests that resource. Without
`--nvidia-resource-names` no GPU names the pod; the scenario checks this for 30
seconds. With `--nvidia-resource-names=nvidia.com/gpu.shared` only the pod's
GPU does. Like `sharedGpu`, it runs when `--shared-gpu-configure` is not
`false`. It runs only on the local k3d cluster e2e creates; on an existing
cluster it is skipped, because renaming the GPU resource there would hide
`nvidia.com/gpu` from the other scenarios. A failed device plugin change is
undone before the next group runs.

The `dra` scenario checks a live claim in both dedicated and virtual GPU modes.
Each mode installs its own exporter and workload, then requires the full claim
identity on one sample. Claim-attributed samples must not carry a `vgpu` label;
shared GPU attribution remains a separate series and scenario. This checks the
combined exporter configuration without requiring a time-sliced DRA allocation.

Runnable NVSwitch scenarios in owned local k3d clusters generate a current host
CDI spec with `nvidia-ctk`. On hosts that use NVSDM, the harness adds the NVSDM
and InfiniBand libraries; NSCQ hosts use the generated spec as is. The harness
copies the temporary spec into each GPU node, rebases its host paths through the
mounted host-driver root, and enables CDI mode for that scenario group. Cleanup
restores the node runtime to legacy mode so MIG coverage keeps its existing
runtime contract. Spec generation, library lookup, or cleanup failures fail the
scenario group instead of leaving an incomplete device view or a changed node
runtime.

Exact DCGM samples are functional assertions, not capability gates. This keeps
legacy and current metric groups independent while ensuring a selected scenario
fails when DCGM, the container runtime, host configuration, or exporter cannot
provide the behavior that the discovered hardware should support.

ARM/SBSA GB200 compute nodes are a deliberate telemetry case. Their NVLink and
NVSwitch topology remains supported, so fabric health is always tested. Local
NVSwitch entity metrics are skipped when NVIDIA does not publish the required
NVSDM provider for that architecture, and the skip reason names that provider
contract explicitly.

### Check retention and named reuse

Use a prepared GPU cluster and an exporter image built from the revision under
test. Run the following command twice with the same kubeconfig and run name:

```shell
go run ./cmd/e2e tests \
  --kubeconfig /path/to/kubeconfig \
  --run-name retention-check \
  --scenario k8s/serviceAccess \
  --exporter-image registry.example/dcgm-exporter:tag \
  --cuda-workload-image registry.example/cuda-workload:tag \
  --cleanup=false --bootstrap=skip --result-markers
```

Both runs must execute the native service-access specs successfully. Between
runs, check that namespace `retention-check` remains. Per-context teardown still
removes the exporter Helm releases and workload pods. The second run resets the owned
namespace and reuses its name. Set `--k8s-runtime-class`,
`--k8s-node-selector-key`, and `--k8s-node-selector-value` as required by the
prepared target.

For the standalone DCGM transition, use a target that supports IPv6-only
Services and has working IPv6 pod connectivity. Select both groups in one run:

```shell
go run ./cmd/e2e tests \
  --kubeconfig /path/to/kubeconfig \
  --run-name retention-ipv6-check \
  --scenario k8s/remoteDcgm \
  --scenario k8s/remoteDcgmIPv6Hostname \
  --exporter-image registry.example/dcgm-exporter:tag \
  --dcgm-image registry.example/dcgm:tag \
  --cleanup=false --bootstrap=skip --result-markers
```

Both standalone groups must execute and pass; an IPv6 capability skip does not
qualify this transition. Earlier selected standalone installations are removed
before a later standalone group starts, even with `--cleanup=false`. Only the
final standalone installation is retained. This lets the IPv6 group create a
new Service with IPv6 as its primary IP family. The native IPv6 spec checks its
Service, ready IPv6 endpoints, and exporter metrics through the Service hostname.
After the run, check that namespaces `retention-ipv6-check` and
`retention-ipv6-check-dcgm` remain. The same final-installation rule applies to
selected GPU Operator groups. Temporary runtime settings are still restored,
and generated credential files are removed. A kept GPU Operator DRA group, even
one whose setup failed, also keeps the `nvidia.com/dra-kubelet-plugin` node
label its driver needs; remove the label before the next DRA setup.

Clean each selected environment with its original name and kubeconfig:

```shell
go run ./cmd/e2e cluster cleanup --kubeconfig /path/to/kubeconfig --run-name retention-check
go run ./cmd/e2e cluster cleanup --kubeconfig /path/to/kubeconfig --run-name retention-ipv6-check
```

Repeat cleanup after verifying that those namespaces are absent. Both cleanup
invocations must succeed when the selected resources are already absent. Check
that unrelated namespaces remain. A reported cleanup error fails the run and
must be investigated alongside any original test failure.

### Check the metrics NetworkPolicy

`k8s/metricsNetworkPolicy` runs only when named, because it needs a network
plugin that enforces NetworkPolicy. The local k3d cluster's built-in k3s
policy controller does:

```shell
go run ./cmd/e2e tests --scenario k8s/metricsNetworkPolicy
```

### Check the chart's mounts

`k8s/defaultChartMounts` installs the chart with its default values and
`arguments={}`, checks that no exporter mount uses `subPath` and that
`DCGM_EXPORTER_CONFIG_FILE` names the file in `/etc/dcgm-exporter/chart/config`,
and scrapes `/metrics`. It repeats with `readOnlyRootFilesystem: true` and with
TLS (an HTTPS scrape). Last, it changes `customMetrics` with `helm upgrade` and
checks that the same exporter pod, without a restart, exports the new metric
list after the kubelet updates the mounted ConfigMap.
