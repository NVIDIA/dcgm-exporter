# NVIDIA GPU Monitoring Tools

## Bindings

This Github repository contains Golang bindings for the following two libraries:
- [NVIDIA Management Library (NVML)](https://docs.nvidia.com/deploy/nvml-api/nvml-api-reference.html#nvml-api-reference) is a C-based API for monitoring and managing NVIDIA GPU devices.
- [NVIDIA Data Center GPU Manager (DCGM)](https://developer.nvidia.com/data-center-gpu-manager-dcgm) is a set of tools for managing and monitoring NVIDIA GPUs in cluster environments. It's a low overhead tool suite that performs a variety of functions on each host system including active health monitoring, diagnostics, system validation, policies, power and clock management, group configuration and accounting.

You will also find samples for both of these bindings in this repository.

## DCGM exporter

This Github repository also contains the DCGM exporter software. It exposes GPU metrics exporter for [Prometheus](https://prometheus.io/) leveraging [NVIDIA Data Center GPU Manager (DCGM)](https://developer.nvidia.com/data-center-gpu-manager-dcgm).

Find the installation and run instructions [here](https://github.com/NVIDIA/gpu-monitoring-tools/blob/master/exporters/prometheus-dcgm/README.md).

### Quickstart

To gather metrics on a GPU node, simply start the dcgm-exporter container:
```
$ docker run -d --gpus all --rm -p 8080:8080 nvidia/dcgm-exporter:latest
$ curl localhost:8080/metrics
# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
...
DCGM_FI_DEV_SM_CLOCK{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 139
DCGM_FI_DEV_MEM_CLOCK{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 405
DCGM_FI_DEV_MEMORY_TEMP{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 9223372036854775794
...
```

### Quickstart on Kubernetes

Note: Consider using the [NVIDIA GPU Operator](https://github.com/NVIDIA/gpu-operator) rather than the DCGM exporter directly.
To gather metrics on your GPU nodes you can deploy the daemonset:
```
$ kubectl create -f https://raw.githubusercontent.com/NVIDIA/gpu-monitoring-tools/2.0.0-rc.0/daemonset.yaml

# Let's get the output of a random pod:
$ NAME=$(kubectl get pods -l "app.kubernetes.io/name=dcgm-exporter, app.kubernetes.io/version=2.0.0-rc.0" \
                         -o "jsonpath={ .items[0].metadata.name}")

$ kubectl proxy --port=8080
$ curl http://localhost:8080/api/v1/namespaces/default/pods/$NAME:8080/proxy
# HELP DCGM_FI_DEV_SM_CLOCK SM clock frequency (in MHz).
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock frequency (in MHz).
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature (in C).
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
...
DCGM_FI_DEV_SM_CLOCK{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 139
DCGM_FI_DEV_MEM_CLOCK{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 405
DCGM_FI_DEV_MEMORY_TEMP{gpu="0" UUID="GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52"} 9223372036854775794
...
```

## Issues and Contributing

[Checkout the Contributing document!](CONTRIBUTING.md)

* Please let us know by [filing a new issue](https://github.com/NVIDIA/gpu-monitoring-tools/issues/new)
* You can contribute by opening a [pull request](https://gitlab.com/nvidia/container-toolkit/gpu-monitoring-tools)
