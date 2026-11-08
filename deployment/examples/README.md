# Raw Kubernetes examples

These manifests install dcgm-exporter without Helm. They are version-synced by
`hack/versions.py` alongside the Helm chart under `deployment/`.

| File | Purpose |
|------|---------|
| [`dcgm-exporter.yaml`](dcgm-exporter.yaml) | DaemonSet and metrics Service |
| [`networkpolicy.yaml`](networkpolicy.yaml) | Optional metrics ingress policy; apply separately |
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

### Migrating from versioned selectors

The raw DaemonSet selector is stable as of 4.9.0. If you previously installed
an example with app.kubernetes.io/version in the DaemonSet selector, recreate
the DaemonSet once before applying this manifest because Kubernetes selectors
are immutable:

```bash
kubectl delete daemonset dcgm-exporter
kubectl apply -f deployment/examples/dcgm-exporter.yaml
```

The Service selector is mutable and does not need to be deleted.

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

## Metrics ingress policy

[`networkpolicy.yaml`](networkpolicy.yaml) admits only the example monitoring
pods to TCP port 9400 on the exporter pods. Replace the selectors with your
monitoring installation's labels and apply it in the exporter's namespace. It
needs a CNI that enforces NetworkPolicy and does not cover a `hostNetwork`
listener.

GPU Operator manages its own exporter DaemonSet and RBAC. Read its pod labels,
listen port and `hostNetwork` setting before writing a policy for it:

```sh
kubectl -n "$EXPORTER_NAMESPACE" get daemonset "$EXPORTER_DAEMONSET" -o yaml
```

Pod labels need `--kubernetes-pod-label-allowlist-regex` or
`DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX`, for example `^app$,^tier$`;
`.*` exports every pod label.
