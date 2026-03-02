# Per-Pod GPU Utilization Metrics (Time-Slicing)

## Overview

When CUDA time-slicing is active, multiple pods share a single physical GPU.
Standard DCGM per-device metrics (`dcgm_fi_dev_gpu_util`) report aggregate
utilization for the whole device — you cannot tell how much of the GPU proxy,
embeddings, or inference pods are each consuming.

This feature adds an opt-in collector that attributes SM utilization to
individual pods by joining:

1. **NVML `nvmlDeviceGetProcessUtilization()`** — per-PID SM and memory
   utilization sampled directly from the CUDA driver.
2. **Kubelet pod-resources gRPC API** — maps GPU UUIDs to
   `(pod, namespace, container)` tuples.
3. **`/proc/<pid>/cgroup`** — identifies which container a PID belongs to,
   linking NVML PIDs back to Kubernetes pod metadata.

### New metric

```
# HELP dcgm_fi_dev_sm_util_per_pod SM utilization attributed to a pod (time-slicing)
# TYPE dcgm_fi_dev_sm_util_per_pod gauge
dcgm_fi_dev_sm_util_per_pod{
  gpu="0",
  uuid="GPU-abc123",
  pod="synapse-proxy-7f9d4b-xkz2p",
  namespace="synapse-staging",
  container="proxy"
} 42
```

One gauge is emitted per `(pod, namespace, container, gpu_uuid)` tuple.
The value is the NVML SM utilization percentage (0–100) for that pod's
CUDA processes on that device.

## Requirements

- dcgm-exporter running with access to `/var/lib/kubelet/pod-resources/`
  (kubelet pod-resources gRPC socket)
- `hostPID: true` on the dcgm-exporter DaemonSet (to resolve
  `/proc/<pid>/cgroup` on the host)
- CUDA time-slicing configured via GPU Operator (or NVIDIA device plugin)
- dcgm-exporter v3.4.0+ (this feature)

## Enabling

### Standalone (without GPU Operator)

Add to the dcgm-exporter DaemonSet:

```yaml
spec:
  template:
    spec:
      hostPID: true
      containers:
        - name: dcgm-exporter
          env:
            - name: DCGM_EXPORTER_ENABLE_PER_POD_GPU_UTIL
              value: "true"
            # Optional: override default socket path
            # - name: DCGM_EXPORTER_POD_RESOURCES_SOCKET
            #   value: "/var/lib/kubelet/pod-resources/kubelet.sock"
          volumeMounts:
            - name: pod-resources
              mountPath: /var/lib/kubelet/pod-resources
              readOnly: true
      volumes:
        - name: pod-resources
          hostPath:
            path: /var/lib/kubelet/pod-resources
            type: Directory
```

### With GPU Operator (v24.x+)

Set in your `ClusterPolicy`:

```yaml
spec:
  dcgmExporter:
    perPodGPUUtil:
      enabled: true
      # podResourcesSocketPath defaults to /var/lib/kubelet/pod-resources/kubelet.sock
```

GPU Operator automatically mounts the pod-resources socket and sets
`hostPID: true` when this option is enabled.

## Prometheus alerts example

```yaml
groups:
  - name: gpu-time-slicing
    rules:
      - alert: GPUPodHighUtilization
        expr: dcgm_fi_dev_sm_util_per_pod > 80
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Pod {{ $labels.pod }} consuming >80% GPU SM for 5m"
          description: >
            Pod {{ $labels.namespace }}/{{ $labels.pod }}
            (container {{ $labels.container }}) is using
            {{ $value }}% of GPU {{ $labels.gpu }} ({{ $labels.uuid }}).
```

## Grafana panel

Import the example dashboard from `examples/time-slicing/grafana-dashboard.json`
or add a panel manually:

```
# GPU utilization by workload
sum by (pod, namespace) (
  dcgm_fi_dev_sm_util_per_pod{namespace=~"$namespace"}
)
```

## How it works

```
NVML ProcessUtilization
  └── map[pid] → smUtil%
        │
        ├── /proc/<pid>/cgroup → containerID
        │       └── matches pod-resources response
        │
        └── pod-resources gRPC ListPodResources()
              └── map[gpuUUID] → (pod, ns, container)
```

The collector runs on every scrape interval. PIDs with no matching pod
(e.g., host processes) are silently skipped. The metric is only emitted
for PIDs that can be fully attributed to a `(pod, namespace, container)` tuple.

## Limitations

- **Time-slicing only**: With MIG, DCGM already provides per-instance metrics
  (`dcgm_fi_dev_gpu_util` per MIG instance). This collector targets the
  time-slicing case where MIG is not available or not configured.
- **SM utilization only**: Memory bandwidth attribution across time-sliced
  processes is not currently supported by NVML.
- **Linux only**: Pod cgroup resolution uses `/proc/<pid>/cgroup`, which is
  Linux-specific.
- **RBAC**: The dcgm-exporter service account needs `get` on `pods` to
  enrich labels (optional; labels are omitted if unavailable).

## Related

- Issue: [#587 GPU utilization per pod with time-slicing](https://github.com/NVIDIA/dcgm-exporter/issues/587)
- GPU Operator ClusterPolicy: `spec.dcgmExporter.perPodGPUUtil`
- Time-slicing setup: [GPU Operator Time-Slicing Guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-sharing.html)
