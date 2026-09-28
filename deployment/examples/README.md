# Raw Kubernetes examples

These manifests install dcgm-exporter without Helm. They are version-synced by
`hack/versions.py` alongside the Helm chart under `deployment/`.

| File | Purpose |
|------|---------|
| [`dcgm-exporter.yaml`](dcgm-exporter.yaml) | DaemonSet and metrics Service |
| [`service-monitor.yaml`](service-monitor.yaml) | Prometheus Operator ServiceMonitor |
| [`gpu-tainted-nodes-patch.yaml`](gpu-tainted-nodes-patch.yaml) | Patch for GPU nodes tainted with `nvidia.com/gpu:NoSchedule` |

## Prerequisites

- Kubernetes cluster with GPU nodes and the NVIDIA Container Toolkit (or
  equivalent GPU device plugin / runtime).
- For `service-monitor.yaml`: Prometheus Operator CRDs installed in the
  cluster.

## Usage

```bash
kubectl apply -f deployment/examples/dcgm-exporter.yaml
kubectl apply -f deployment/examples/service-monitor.yaml
```

If GPU nodes are tainted with `nvidia.com/gpu:NoSchedule`, apply the base
manifest and then the patch:

```bash
kubectl apply -f deployment/examples/dcgm-exporter.yaml
kubectl patch daemonset dcgm-exporter \
  --patch-file deployment/examples/gpu-tainted-nodes-patch.yaml
```

For configurable installs (RBAC, TLS, custom collectors, ServiceMonitor options),
use the Helm chart instead: [deployment/README.md](../README.md).

These files are not packaged into the Helm chart (`examples/` is in
`.helmignore`).
