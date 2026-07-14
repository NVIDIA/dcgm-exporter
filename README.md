# DCGM-Exporter

This repository contains the DCGM-Exporter project. It exposes GPU metrics exporter for [Prometheus](https://prometheus.io/) leveraging [NVIDIA DCGM](https://developer.nvidia.com/dcgm).

### Documentation

Official documentation for DCGM-Exporter can be found on [docs.nvidia.com](https://docs.nvidia.com/datacenter/cloud-native/gpu-telemetry/dcgm-exporter.html).

### Quickstart

To gather metrics on a GPU node, simply start the `dcgm-exporter` container:

<!-- sync:docker-run-example:start -->
```shell
docker run -d --gpus all --cap-add SYS_ADMIN --rm -p 9400:9400 nvcr.io/nvidia/k8s/dcgm-exporter:4.6.0-4.8.3-distroless
```
<!-- sync:docker-run-example:end -->

Then check the metrics endpoint:

```shell
curl localhost:9400/metrics
# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
...
DCGM_FI_DEV_SM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 139
DCGM_FI_DEV_MEM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 405
DCGM_FI_DEV_MEMORY_TEMP{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 9223372036854775794
...
```

### Quickstart on Kubernetes

Note: Consider using the [NVIDIA GPU Operator](https://github.com/NVIDIA/gpu-operator) rather than DCGM-Exporter directly.

For local Linux development with a directly attached NVIDIA GPU, or broader validation that defaults to local k3d but can also target an existing cluster with `--kubeconfig`, see the [Go E2E CLI](tests/k8s/README.md#go-e2e-cli).

Ensure you have already setup your cluster with the [default runtime as NVIDIA](https://github.com/NVIDIA/nvidia-container-runtime#docker-engine-setup).

The recommended way to install DCGM-Exporter is to use the Helm chart:

```shell
helm repo add gpu-helm-charts \
  https://nvidia.github.io/dcgm-exporter/helm-charts
```

Update the repo:

```shell
helm repo update
```

And install the chart:

```shell
helm install \
    --generate-name \
    gpu-helm-charts/dcgm-exporter
```

Once the `dcgm-exporter` pod is deployed, you can use port forwarding to obtain metrics quickly:

```shell
kubectl create -f https://raw.githubusercontent.com/NVIDIA/dcgm-exporter/master/dcgm-exporter.yaml

# Let's get the output of a random pod:
NAME=$(kubectl get pods -l "app.kubernetes.io/name=dcgm-exporter" \
                         -o "jsonpath={ .items[0].metadata.name}")

kubectl port-forward $NAME 8080:9400 &

curl -sL http://127.0.0.1:8080/metrics
# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
...
DCGM_FI_DEV_SM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52",container="",namespace="",pod=""} 139
DCGM_FI_DEV_MEM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52",container="",namespace="",pod=""} 405
DCGM_FI_DEV_MEMORY_TEMP{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52",container="",namespace="",pod=""} 9223372036854775794
...

```

To integrate DCGM-Exporter with Prometheus and Grafana, see the full instructions in the [user guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-telemetry/latest/).
`dcgm-exporter` is deployed as part of the GPU Operator. To get started with integrating with Prometheus, check the Operator [user guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/getting-started.html#gpu-telemetry).

### TLS and Basic Auth

Exporter supports TLS and basic auth using [exporter-toolkit](https://github.com/prometheus/exporter-toolkit). To use TLS and/or basic auth, users need to use `--web-config-file` CLI flag as follows

```shell
dcgm-exporter --web-config-file=web-config.yaml
```

A sample `web-config.yaml` file can be fetched from [exporter-toolkit repository](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-config.yml). The reference of the `web-config.yaml` file can be consulted in the [docs](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md).

### Pprof Profiling Endpoints

`dcgm-exporter` can expose Go profiling endpoints under `/debug/pprof/`
when started with `--enable-pprof`. These endpoints can reveal runtime
details such as goroutines, heap allocations, command-line arguments, and CPU
profiles, so enable them only with exporter-toolkit authentication or TLS
through `--web-config-file`.

When pprof is enabled, startup requires `--web-config-file` so the profiling
endpoints are protected by the same exporter-toolkit web configuration as
`/metrics`.

### IPv6 Support

DCGM-Exporter supports IPv6 addresses for both the remote hostengine connection (`-r`) and the metrics listen address (`-a`). IPv6 addresses must use bracket notation when combined with a port.

#### Remote Hostengine (CLI)

```shell
dcgm-exporter -r "[::1]:5555"
```

#### Remote Hostengine (Environment Variable)

```shell
export DCGM_REMOTE_HOSTENGINE_INFO="[::1]:5555"
dcgm-exporter
```

#### Metrics Listen Address

```shell
dcgm-exporter -a "[::]:9400"
```

**Note:** The brackets in `[::1]:5555` are required by the DCGM connection protocol. When using the CLI, the shell requires quoting (double or single quotes) around the address to prevent bracket interpretation.

#### Prerequisites

The remote `nv-hostengine` must be configured to listen on IPv6. Refer to the [DCGM documentation](https://docs.nvidia.com/datacenter/dcgm/latest/) for configuring `nv-hostengine` bind address options.

### Remote Hostengine URI Formats

Remote hostengine connections support `<HOST>:<PORT>` and these DCGM URI formats:

```shell
dcgm-exporter -r "tcp://<HOST>:<PORT>"
dcgm-exporter -r "unix:///<SOCKET_PATH>"
dcgm-exporter -r "vsock://<CID>:<PORT>"
```

For VSOCK, the `CID` and `PORT` values must match the `nv-hostengine` VSOCK listener. DCGM validates the connection string and reports startup errors if the endpoint is unavailable or malformed.

### DCGM compatibility for CPU serial labels

The `cpu_serial` label is added only to per-CPU (`FE_CPU`) metrics, and only when the DCGM version in use reports a non-empty CPU serial for the Grace CPU. It is never added to CPU-core (`FE_CPU_CORE`) metrics. With an older remote `nv-hostengine` (or whenever the serial is unavailable), dcgm-exporter continues to collect CPU metrics and omits the `cpu_serial` label.

### How to include HPC jobs in metric labels

The DCGM-exporter can include High-Performance Computing (HPC) job information into its metric labels. To achieve this, HPC environment administrators must configure their HPC environment to generate files that map GPUs to HPC jobs.

#### File Conventions

These mapping files follow a specific format:

* Each file is named after either a unique GPU ID or a unique GPU ID and a GPU instance (MIG) ID separated with a "." (e.g., 0, 1, 2.0, 2.1, 3, etc.).
* Each line in the file contains JOB IDs that run on the corresponding GPU/MIG instance.

#### Enabling HPC Job Mapping on DCGM-Exporter

To enable GPU-to-job mapping on the DCGM-exporter side, users must run the DCGM-exporter with the --hpc-job-mapping-dir command-line parameter, pointing to a directory where the HPC cluster creates job mapping files. Or, users can set the environment variable DCGM_HPC_JOB_MAPPING_DIR to achieve the same result.

### Runtime container labels

DCGM-exporter can add a `container` label from a host container runtime. This is disabled by default and is separate from Kubernetes labels.

Enable it with `--container-labels --container-runtime-socket=<socket>`, or set `DCGM_EXPORTER_CONTAINER_LABELS=true` and `DCGM_CONTAINER_RUNTIME_SOCKET=<socket>`. Mounting the runtime socket can expose privileged host control.

The mapper labels explicit GPU assignments only: GPU UUIDs, numeric GPU indexes resolved to UUIDs, resolvable MIG UUIDs, and `all` (`--gpus all` or `NVIDIA_VISIBLE_DEVICES=all`). Count-only assignments, such as `--gpus 1`, remain unlabeled. If the runtime socket is unavailable or slow, scrapes continue without container label enrichment. If no container name is available, DCGM-exporter uses a short container ID.

### Building from Source

In order to build dcgm-exporter ensure you have the following:

* [Go installed at the version pinned by this repository](https://go.dev/)
* [DCGM installed](https://developer.nvidia.com/dcgm)
* Have Linux machine with GPU, compatible with DCGM.

```shell
git clone https://github.com/NVIDIA/dcgm-exporter.git
cd dcgm-exporter
make binary
sudo make install
...
dcgm-exporter &
curl localhost:9400/metrics
# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
...
DCGM_FI_DEV_SM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 139
DCGM_FI_DEV_MEM_CLOCK{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 405
DCGM_FI_DEV_MEMORY_TEMP{gpu="0", UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 9223372036854775794
...
```

### Host systemd deployments

The package artifact includes `nvidia-dcgm-exporter.service` for host deployments.
The shipped service restarts on exporter failures, waits 10 seconds between restart attempts,
and disables systemd start-rate limiting so transient DCGM or driver disruption does not
leave the exporter permanently stopped.

For already-installed systems, use a systemd drop-in instead of editing the package-managed
unit under `/lib/systemd/system`:

```ini
# /etc/systemd/system/nvidia-dcgm-exporter.service.d/restart.conf
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=on-failure
RestartSec=10s
```

After creating or updating the drop-in, reload systemd and restart the exporter service:

```shell
sudo systemctl daemon-reload
sudo systemctl restart nvidia-dcgm-exporter.service
```

### Changing Metrics

With `dcgm-exporter` you can configure which fields are collected by specifying a custom CSV file.
You will find the default CSV file under `etc/default-counters.csv` in the repository, which is copied on your system or container to `/etc/dcgm-exporter/default-counters.csv`

The layout and format of this file is as follows:

```
# Format
# If line starts with a '#' it is considered a comment
# DCGM FIELD, Prometheus metric type, help message

# Clocks
DCGM_FI_DEV_SM_CLOCK,  gauge, SM clock frequency (in MHz).
DCGM_FI_DEV_MEM_CLOCK, gauge, Memory clock frequency (in MHz).
```

A custom csv file can be specified using the `-f` option or `--collectors` as follows:

```shell
dcgm-exporter -f /tmp/custom-collectors.csv
```

You can also provide an optional YAML config file with `--config-file` or `DCGM_EXPORTER_CONFIG_FILE`.
YAML is read during exporter startup. YAML file edits require restarting dcgm-exporter; hot reload only reloads the resolved CSV metric file when the active metric source is file based.
Legacy flags and environment variables that are explicitly set on startup override YAML.

```yaml
version: 1
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collection:
  interval: 30s
```

YAML metric sources are mutually exclusive. If `metrics` is omitted, dcgm-exporter uses the default CSV file. If `metrics` is present, specify either a mounted CSV file or inline fields:

```yaml
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature (in C).
```

For Kubernetes deployments, mount custom metric ConfigMaps as files and set
`metrics.file` to the mounted CSV path.

Use `collection.watchGroups` to watch selected fields at different intervals. Field names use glob-style
matching, unmatched fields use `collection.interval`, and any field that matches more than one named watch
group is rejected during startup. A watch group must match at least one configured field.

```yaml
version: 1
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collection:
  interval: 30s
  watchGroups:
    - name: fast-thermals
      interval: 5s
      fields:
        - DCGM_FI_DEV_GPU_TEMP
        - DCGM_FI_DEV_POWER_USAGE
    - name: slow-nvlink-prm
      interval: 5m
      fields:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
```

Exporter-derived backing fields for cumulative XID and clock-event counters are treated like normal fields
when partitioning watch groups. If they do not match a named watch group, they use `collection.interval`.

Notes:

* Always make sure your entries have 2 commas (',')
* The complete list of counters that can be collected can be found on the DCGM API reference manual: <https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html>

#### Cumulative XID and clock event counters

DCGM-Exporter includes opt-in exporter counters for cumulative XID errors and clock events:

```csv
DCGM_EXP_XID_ERRORS_TOTAL, counter, cumulative XID errors observed since exporter start
DCGM_EXP_CLOCK_EVENTS_TOTAL, counter, cumulative clock events observed since exporter start (edge-counted)
```

These counters are commented out in the default CSV. To enable them, add the rows to your custom collectors CSV or uncomment them in the default configuration.

The `_COUNT` and `_TOTAL` exporter metrics have different semantics:

* `DCGM_EXP_XID_ERRORS_COUNT` and `DCGM_EXP_CLOCK_EVENTS_COUNT` report events observed during the last collection window.
* `DCGM_EXP_XID_ERRORS_TOTAL` and `DCGM_EXP_CLOCK_EVENTS_TOTAL` maintain in-memory cumulative state and are intended for Prometheus counter functions such as `increase()` and `rate()`.

`DCGM_EXP_XID_ERRORS_TOTAL` watches `DCGM_FI_DEV_XID_ERRORS` and emits a separate series for each observed `xid` label. XID value `0` is treated as no error and is not counted.

`DCGM_EXP_CLOCK_EVENTS_TOTAL` watches `DCGM_FI_DEV_CLOCKS_EVENT_REASONS` and emits a separate series for each `clock_event` label. It increments when a clock throttle reason transitions from inactive to active; a reason that is already active when the collector starts initializes the collector state without adding to the total.

The `_TOTAL` counters reset when the exporter collector is recreated, including exporter restart and hot reload. They are polled in the background at `--collect-interval`, so event accounting is not tied to Prometheus scrape timing. Very low collect intervals increase DCGM polling load.

### Profiling Metrics

Please note that for Ampere and earlier generation GPUs, profiling metrics depend on the datacenter-gpu-manager-4-proprietary package. This package is included in the container.

### What about a Grafana Dashboard?

You can find the official NVIDIA DCGM-Exporter dashboard here: <https://grafana.com/grafana/dashboards/12239>

You will also find the `json` file on this repo under `grafana/dcgm-exporter-dashboard.json`

### You can find the DCGM-Exporter OpenObserve dashboard here

You can find the NVIDIA DCGM-Exporter dashboard here: <https://github.com/openobserve/dashboards/tree/main/NVIDIA%20GPU%20Monitoring>

To integrate DCGM-Exporter with OpenObserve, follow the blog [monitoring GPU with OpenObserve](https://openobserve.ai/blog/how-to-monitor-nvidia-gpu/)

Pull requests are accepted!

### Building the containers

This project uses [docker buildx](https://docs.docker.com/buildx/working-with-buildx/) for multi-arch image creation. Follow the instructions on that page to get a working builder instance for creating these containers. Some other useful build options follow.

Builds local images based on the machine architecture and makes them available in 'docker images'

```shell
make local
```

Build the distroless image and export to 'docker images'

```shell
make distroless PLATFORMS=linux/amd64 OUTPUT=type=docker
```

Build and push the images to some other 'private_registry'

```shell
make REGISTRY=<private_registry> push
```

## Issues and Contributing

[Checkout the Contributing document!](CONTRIBUTING.md)

* For community support, please [file a new issue](https://github.com/NVIDIA/dcgm-exporter/issues/new)
* You can contribute by opening a [pull request](https://github.com/NVIDIA/dcgm-exporter)

### Reporting Security Issues

We ask that all community members and users of DCGM Exporter follow the standard NVIDIA process for reporting security vulnerabilities. This process is documented at the [NVIDIA Product Security](https://www.nvidia.com/en-us/security/) website.
Following the process will result in any needed CVE being created as well as appropriate notifications being communicated
to the entire DCGM Exporter community. NVIDIA reserves the right to delete vulnerability reports until they're fixed.

Please refer to the policies listed there to answer questions related to reporting security issues.
