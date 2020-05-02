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
$ docker run -d --gpus all --rm -p 9400:9400 nvidia/dcgm-exporter:latest
$ curl localhost:9400/metrics
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
$ kubectl create -f https://raw.githubusercontent.com/NVIDIA/gpu-monitoring-tools/2.0.0-rc.8/dcgm-exporter.yaml

# Let's get the output of a random pod:
$ NAME=$(kubectl get pods -l "app.kubernetes.io/name=dcgm-exporter, app.kubernetes.io/version=2.0.0-rc.8" \
                         -o "jsonpath={ .items[0].metadata.name}")

$ kubectl proxy --port=8080 &
$ BASE=http://localhost:8080/api/v1/namespaces/default
$ curl $BASE/pods/$NAME:9400/proxy/metrics
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

# If you are using the Prometheus operator
# Note on exporters here:
# https://github.com/coreos/prometheus-operator/blob/release-0.38/Documentation/user-guides/running-exporters.md

$ helm repo add stable https://kubernetes-charts.storage.googleapis.com
$ helm install stable/prometheus-operator --generate-name \
    --set "prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false"
$ kubectl create -f \
    https://raw.githubusercontent.com/NVIDIA/gpu-monitoring-tools/2.0.0-rc.8/service-monitor.yaml

# Note might take ~1-2 minutes for prometheus to pickup the metrics and display them
# You can also check in the WebUI the servce-discovery tab (in the Status category)
$ NAME=$(kubectl get svc -l app=prometheus-operator-prometheus -o jsonpath='{.items[0].metadata.name}')
$ curl "$BASE/services/$NAME:9090/proxy/api/v1/query?query=DCGM_FI_DEV_MEMORY_TEMP"
{
	status: "success",
	data: {
		resultType: "vector",
		result: [
			{
				metric: {
					UUID: "GPU-604ac76c-d9cf-fef3-62e9-d92044ab6e52",
					__name__: "DCGM_FI_DEV_MEMORY_TEMP",
					...
					pod: "dcgm-exporter-fn7fm",
					service: "dcgm-exporter"
				},
				value: [
					1588399049.227,
					"9223372036854776000"
				]
			},
			...
		]
	}
}
```


### Building From source and Running on Bare Metal

The dcgm-exporter is actually fairly straightforward to build and use. Ensure you have go >= 1.14 installed.
```
$ git clone https://github.com/NVIDIA/gpu-monitoring-tools.git
$ cd gpu-monitoring-tools
$ make binary
$ sudo make install
...
$ dcgm-exporter &
$ curl localhost:8081/metrics
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
