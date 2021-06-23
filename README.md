# NVIDIA GPU Monitoring Tools

This repository contains Golang bindings and DCGM-Exporter for gathering GPU telemetry in Kubernetes.

**June 2021 - NOTICE: Some of the tools in this repository are graduating to their own repos. In the next few weeks both the DCGM Go bindings and the DCGM Exporter will be migrating to github.com/NVIDIA. This will allow for independent versioning, issues, MRs, etc. Efforts will be made to review the existing MRs and issues before the migration occurs.**

## Bindings

Golang bindings are provided for the following two libraries:
- [NVIDIA Management Library (NVML)](https://docs.nvidia.com/deploy/nvml-api/nvml-api-reference.html#nvml-api-reference) is a C-based API for monitoring and managing NVIDIA GPU devices.
- [NVIDIA Data Center GPU Manager (DCGM)](https://developer.nvidia.com/dcgm) is a set of tools for managing and monitoring NVIDIA GPUs in cluster environments. It's a low overhead tool suite that performs a variety of functions on each host system including active health monitoring, diagnostics, system validation, policies, power and clock management, group configuration and accounting.

You will also find samples for both of these bindings in this repository.

## DCGM-Exporter

The repository also contains DCGM-Exporter. It exposes GPU metrics exporter for [Prometheus](https://prometheus.io/) leveraging [NVIDIA DCGM](https://developer.nvidia.com/dcgm).

### Quickstart

To gather metrics on a GPU node, simply start the `dcgm-exporter` container:
```
$ docker run -d --gpus all --rm -p 9400:9400 nvcr.io/nvidia/k8s/dcgm-exporter:2.0.13-2.1.2-ubuntu18.04
$ curl localhost:9400/metrics
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
```
$ helm repo add gpu-helm-charts \
  https://nvidia.github.io/gpu-monitoring-tools/helm-charts
```
Update the repo:
```
$ helm repo update
```
And install the chart:
```
$ helm install \ 
    --generate-name \ 
    gpu-helm-charts/dcgm-exporter
```

Once the `dcgm-exporter` pod is deployed, you can use port forwarding to obtain metrics quickly:


```
$ kubectl create -f https://raw.githubusercontent.com/NVIDIA/gpu-monitoring-tools/master/dcgm-exporter.yaml

# Let's get the output of a random pod:
$ NAME=$(kubectl get pods -l "app.kubernetes.io/name=dcgm-exporter" \
                         -o "jsonpath={ .items[0].metadata.name}")

$ kubectl port-forward $NAME 8080:9400 &
$ curl -sL http://127.0.01:8080/metrics
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
To integrate DCGM-Exporter with Prometheus and Grafana, see the full instructions in the [user guide](https://docs.nvidia.com/datacenter/cloud-native/kubernetes/dcgme2e.html#gpu-telemetry). 
`dcgm-exporter` is deployed as part of the GPU Operator. To get started with integrating with Prometheus, check the Operator [user guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/getting-started.html#gpu-telemetry).

### Building from Source

`dcgm-exporter` is actually fairly straightforward to build and use.
Ensure you have the following:
- [Golang >= 1.14 installed](https://golang.org/)
- [DCGM installed](https://developer.nvidia.com/dcgm)

```
$ git clone https://github.com/NVIDIA/gpu-monitoring-tools.git
$ cd gpu-monitoring-tools
$ make binary
$ sudo make install
...
$ dcgm-exporter &
$ curl localhost:9400/metrics
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
You will find the default CSV file under `etc/dcgm-exporter/default-counters.csv` in the repository, which is copied on your system or container at 
`/etc/dcgm-exporter/default-counters.csv`

The format of this file is pretty straightforward:
```
# Format,,
# If line starts with a '#' it is considered a comment,,
# DCGM FIELD, Prometheus metric type, help message

# Clocks,,
DCGM_FI_DEV_SM_CLOCK,  gauge, SM clock frequency (in MHz).
DCGM_FI_DEV_MEM_CLOCK, gauge, Memory clock frequency (in MHz).
```

A custom csv file can be specified using the `-f` option or `--collectors` as follows:
```
$ dcgm-exporter -f /tmp/custom-collectors.csv
```

Notes:
- Always make sure your entries have 3 commas (',')
- The complete list of counters that can be collected can be found on the DCGM API reference manual: https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/group__dcgmFieldIdentifiers.html

### What about a Grafana Dashboard?

You can find the official NVIDIA DCGM-Exporter dashboard here: https://grafana.com/grafana/dashboards/12239

You will also find the `json` file on this repo under `grafana/dcgm-exporter-dashboard.json`

Pull requests are accepted!

## Issues and Contributing

[Checkout the Contributing document!](CONTRIBUTING.md)

* Please let us know by [filing a new issue](https://github.com/NVIDIA/gpu-monitoring-tools/issues/new)
* You can contribute by opening a [pull request](https://gitlab.com/nvidia/container-toolkit/gpu-monitoring-tools)
