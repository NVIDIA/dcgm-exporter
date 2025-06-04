# DCGM-Exporter

This repository contains the DCGM-Exporter project. It exposes GPU metrics exporter for [Prometheus](https://prometheus.io/) leveraging [NVIDIA DCGM](https://developer.nvidia.com/dcgm).

### Documentation

Official documentation for DCGM-Exporter can be found on [docs.nvidia.com](https://docs.nvidia.com/datacenter/cloud-native/gpu-telemetry/dcgm-exporter.html).

### Quickstart

To gather metrics on a GPU node, simply start the `dcgm-exporter` container:

```shell
docker run -d --gpus all --cap-add SYS_ADMIN --rm -p 9400:9400 nvcr.io/nvidia/k8s/dcgm-exporter:4.2.3-4.1.3-ubuntu22.04
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

### How to include HPC jobs in metric labels

The DCGM-exporter can include High-Performance Computing (HPC) job information into its metric labels. To achieve this, HPC environment administrators must configure their HPC environment to generate files that map GPUs to HPC jobs.

#### File Conventions

These mapping files follow a specific format:

* Each file is named after a unique GPU ID (e.g., 0, 1, 2, etc.).
* Each line in the file contains JOB IDs that run on the corresponding GPU.

#### Enabling HPC Job Mapping on DCGM-Exporter

To enable GPU-to-job mapping on the DCGM-exporter side, users must run the DCGM-exporter with the --hpc-job-mapping-dir command-line parameter, pointing to a directory where the HPC cluster creates job mapping files. Or, users can set the environment variable DCGM_HPC_JOB_MAPPING_DIR to achieve the same result.

### Building from Source

In order to build dcgm-exporter ensure you have the following:

* [Golang >= 1.22 installed](https://golang.org/)
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

Notes:

* Always make sure your entries have 2 commas (',')
* The complete list of counters that can be collected can be found on the DCGM API reference manual: <https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html>

### What about a Grafana Dashboard?

You can find the official NVIDIA DCGM-Exporter dashboard here: <https://grafana.com/grafana/dashboards/12239>

You will also find the `json` file on this repo under `grafana/dcgm-exporter-dashboard.json`

Pull requests are accepted!

### Building the containers

This project uses [docker buildx](https://docs.docker.com/buildx/working-with-buildx/) for multi-arch image creation. Follow the instructions on that page to get a working builder instance for creating these containers. Some other useful build options follow.

Builds local images based on the machine architecture and makes them available in 'docker images'

```shell
make local
```

Build the ubuntu image and export to 'docker images'

```shell
make ubuntu22.04 PLATFORMS=linux/amd64 OUTPUT=type=docker
```

Build and push the images to some other 'private_registry'

```shell
make REGISTRY=<private_registry> push
```

## Issues and Contributing

[Checkout the Contributing document!](CONTRIBUTING.md)

* Please let us know by [filing a new issue](https://github.com/NVIDIA/dcgm-exporter/issues/new)
* You can contribute by opening a [pull request](https://github.com/NVIDIA/dcgm-exporter)

### Reporting Security Issues

We ask that all community members and users of DCGM Exporter follow the standard NVIDIA process for reporting security vulnerabilities. This process is documented at the [NVIDIA Product Security](https://www.nvidia.com/en-us/security/) website.
Following the process will result in any needed CVE being created as well as appropriate notifications being communicated
to the entire DCGM Exporter community. NVIDIA reserves the right to delete vulnerability reports until they're fixed.

Please refer to the policies listed there to answer questions related to reporting security issues.
