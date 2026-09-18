# DCGM Exporter Helm Chart

This Helm chart deploys NVIDIA DCGM Exporter to monitor GPU metrics in Kubernetes clusters.

For installation, configuration, verification, administration, and
troubleshooting, see [Install DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
Related documentation includes:

- [Configure Prometheus for DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/learn/getting-started-for-system-administrators/configure-prometheus-for-dcgm-exporter.html)
- [`dcgm-exporter` command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html)
- [DCGM Exporter Metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [DCGM Exporter Release Notes](https://docs.nvidia.com/datacenter/dcgm/latest/release-notes/dcgm-exporter.html)

## Quick Start

```bash
# Install with default configuration
helm install dcgm-exporter ./deployment

# Install with custom values (create your own values file)
helm install dcgm-exporter ./deployment -f my-debug-values.yaml
```

### GPU node taints

If GPU nodes are tainted with `nvidia.com/gpu:NoSchedule`, create a values
file with a matching toleration:

```yaml
# gpu-tainted-nodes-values.yaml
tolerations:
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
  - key: nvidia.com/gpu
    operator: Exists
    effect: NoSchedule
```

Install or upgrade with that file:

```bash
helm upgrade --install dcgm-exporter ./deployment \
  -f gpu-tainted-nodes-values.yaml
```

The `tolerations` value replaces the chart default list. Include every
toleration needed by your cluster.

The chart enables its `ServiceMonitor` by default. If Prometheus Operator and
the `ServiceMonitor` custom resource definition are not installed, use
`--set serviceMonitor.enabled=false`.

Chart-managed TLS and basic authentication protect the exporter endpoint, but
the default `ServiceMonitor` does not configure HTTPS or authentication. When
you enable `tlsServerConfig` or `basicAuth`, set
`serviceMonitor.enabled=false` and supply a Prometheus scrape configuration
with the matching scheme, TLS settings, and credentials.

## Configuration

### YAML Exporter Configuration

The chart can mount an optional dcgm-exporter YAML config and set `DCGM_EXPORTER_CONFIG_FILE`.
The YAML file is read at exporter startup; changing it requires restarting the pod.

```yaml
arguments:
  - --watch-max-keep-age=10m
  - --watch-max-keep-samples=0

config:
  enabled: true
  create: true
  data: |
    version: 2
    metrics:
      file: /etc/dcgm-exporter/default-counters.csv
      enableExporterMetrics: false
    collections:
      - name: scrape
        every: 30s
        metrics:
          include: ["*"]
```

Exporter metrics (`go_*`, `process_*`, and `promhttp_*`) are disabled by default
to preserve existing `/metrics` output. The setting enables or disables all of
these metrics together. It does not change the `DCGM_FI_*` or `DCGM_EXP_*`
metrics selected by the CSV file. Set `metrics.enableExporterMetrics: true`, pass
`--enable-exporter-metrics`, or set
`DCGM_EXPORTER_ENABLE_EXPORTER_METRICS=true` to enable them.

Inline metric definitions can be supplied without a CSV file:

```yaml
config:
  enabled: true
  create: true
  data: |
    version: 2
    metrics:
      fields:
        - name: DCGM_FI_DEV_GPU_TEMP
          prometheusType: gauge
          help: GPU temperature (in C).
```

For Kubernetes deployments, mount custom metric ConfigMaps as files and point
YAML `metrics.file` at the mounted CSV path. The chart defaults mount the
`exporter-metrics-config-map` `metrics` key at
`/etc/dcgm-exporter/default-counters.csv`; set `customMetrics` to replace that
CSV content.

```yaml
customMetrics: |
  DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).
  DCGM_FI_DEV_POWER_USAGE, gauge, Power draw (in W).
```

When using an existing ConfigMap for the YAML file, set `config.create=false` and `config.name` to the existing ConfigMap name.

Use one or more named `collections` to assign a cadence to DCGM field patterns. The all-fields
`scrape` collection supplies the default cadence; narrower collections override it. Go keeps its
existing DCGM field-pattern matching, so `metrics.include` values are not nv-exporter catalog SignalIds.
Startup fails if an override pattern matches no configured fields or two overrides match the same field.

Version 2 is the canonical collection schema. Version 1 remains supported for existing configuration files,
but accepts only its original singular `collection` block. New files should use version 2 and `collections`;
the two schemas cannot be mixed.

```yaml
config:
  enabled: true
  create: true
  data: |
    version: 2
    metrics:
      file: /etc/dcgm-exporter/default-counters.csv
    sources:
      dcgm:
        watch:
          maxKeepAge: 10m
          maxKeepSamples: 0
    collections:
      - name: scrape
        every: 30s
        metrics:
          include: ["*"]
      - name: fast-thermals
        every: 5s
        sources:
          dcgm:
            watch:
              maxKeepAge: 0s
              maxKeepSamples: 2
        metrics:
          include:
            - DCGM_FI_DEV_GPU_TEMP
            - DCGM_FI_DEV_POWER_USAGE
      - name: slow-nvlink-prm
        every: 5m
        metrics:
          include:
            - DCGM_FI_DEV_NVLINK_PPCNT_*
```

The chart uses its existing `arguments` and `config.data` passthroughs for field-watch retention; there are
no chart-specific retention keys. `sources.dcgm.watch` sets the default policy, and a collection's
`sources.dcgm.watch` overrides it. `extraEnv` can alternatively set `DCGM_EXPORTER_WATCH_MAX_KEEP_AGE`
and `DCGM_EXPORTER_WATCH_MAX_KEEP_SAMPLES`. Keep XID and clock-event fields on the compatibility `10m`/`0`
policy unless a shorter history has been qualified for the intended `_COUNT` window and `_TOTAL` polling
interval.

When upgrading from older chart values, the default `arguments: []` may remove
the rendered container `args:` stanza and roll the DaemonSet. The exporter still
uses its built-in runtime defaults when no arguments are set.

### Scrape Timeout Configuration

Kubernetes pod metadata enrichment can add scrape latency on dense GPU or MIG nodes. The chart exposes the exporter HTTP server timeouts and the ServiceMonitor scrape budget so they can be kept aligned.

```yaml
service:
  webReadTimeout: 10s
  webWriteTimeout: 30s

serviceMonitor:
  interval: 30s
  scrapeTimeout: 25s
```

- `service.webReadTimeout`: Maximum time for the exporter to read an HTTP scrape request.
- `service.webWriteTimeout`: Maximum time for the exporter to generate and write an HTTP scrape response.
- `serviceMonitor.scrapeTimeout`: Maximum scrape duration used by Prometheus Operator when the ServiceMonitor is enabled. Keep this lower than `service.webWriteTimeout` and no greater than `serviceMonitor.interval`.

### Concurrent Scrape Configuration

Set the limit in the exporter YAML configuration:

```yaml
config:
  enabled: true
  data: |
    version: 2
    server:
      maxConcurrentScrapes: 16
```

Alternatively, pass `--max-concurrent-scrapes=16` through `arguments` or use
`extraEnv` to set `DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES`.

### Detached GPU Lifecycle Detection

On systems that support detached GPUs, enable DCGM bind/unbind detection in the
exporter YAML configuration. After DCGM reports that GPU reinitialization is
complete, the exporter rebuilds its DCGM, NVML, and metrics state to match the
resulting topology.

```yaml
config:
  enabled: true
  data: |
    version: 2
    sources:
      dcgm:
        detectBindUnbind:
          enabled: true
          pollInterval: 1s
```

This requires DCGM 4.5 or later and an NVIDIA driver from the 590 series or
later. The polling interval defaults to one second when omitted. Do not use
this setting on older driver stacks, where detached-GPU support is unavailable.

The YAML setting is preferred. Existing deployments can continue using
`--enable-gpu-bind-unbind-watch` and `--gpu-bind-unbind-poll-interval`, or the
corresponding `DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH` and
`DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL` environment variables. Explicit
CLI or environment values override YAML.

### Debug Dump Functionality

The chart supports runtime object dumping for troubleshooting purposes. This feature allows dcgm-exporter to write debug information to files that can be analyzed later.

#### Enable Debug Dumps

```yaml
debugDump:
  enabled: true
  directory: "/tmp/dcgm-exporter-debug"  # Default location
  retention: 48  # hours (0 = no cleanup) - extended from default 24h for production use
  compression: true
```

#### Configuration Options

- `enabled`: Enable/disable debug dump functionality (default: `false`)
- `directory`: Directory to store debug dump files (default: `/tmp/dcgm-exporter-debug`)
- `retention`: Retention period in hours (default: `24`, `0` = no cleanup)
- `compression`: Use gzip compression for dump files (default: `true`)

**Note on directory choice:**
- `/tmp/dcgm-exporter-debug` (default): Temporary location, files may be lost on reboot
- `/var/log/dcgm-exporter-debug`: Persistent location, recommended for production troubleshooting

#### Persistent Storage with hostPath Volume

For production environments, you can mount the debug directory using a hostPath volume to persist logs under `/var/log/`. This ensures debug files survive pod restarts and node reboots.

The DaemonSet automatically creates a hostPath volume mount when debug dumps are enabled. Here's the relevant configuration from `deployment/templates/daemonset.yaml`:

```yaml
# Volume definition
volumes:
- name: "debug-dumps"
  hostPath:
    path: {{ .Values.debugDump.directory }}
    type: DirectoryOrCreate

# Volume mount in container
volumeMounts:
- name: "debug-dumps"
  mountPath: {{ .Values.debugDump.directory }}
```

**Example configuration for persistent storage:**

```yaml
debugDump:
  enabled: true
  directory: "/var/log/dcgm-exporter-debug"  # Persistent location
  retention: 48  # hours - extended from default 24h for production use
  compression: true
```

This configuration will:
- Create a hostPath volume at `/var/log/dcgm-exporter-debug` on each node
- Mount this directory into the container
- Persist debug files across pod restarts and node reboots
- Automatically create the directory if it doesn't exist (`DirectoryOrCreate` type)

#### Accessing Debug Files

When debug dumps are enabled, files are stored on the host filesystem at the specified directory. You can access them by:

1. **From the host node:**
   ```bash
   ls -la /tmp/dcgm-exporter-debug/
   ```

2. **From within the pod:**
   ```bash
   kubectl exec -n <namespace> <pod-name> -- ls -la /tmp/dcgm-exporter-debug/
   ```

3. **Copy files from pod:**
   ```bash
   kubectl cp <namespace>/<pod-name>:/tmp/dcgm-exporter-debug/ ./debug-files/
   ```

#### Example Usage

**Create a custom values file for debug dumps:**

Create a file named `my-debug-values.yaml` with the following content:

```yaml
debugDump:
  enabled: true
  directory: "/var/log/dcgm-exporter-debug"  # Persistent location
  retention: 48  # hours - extended from default 24h for production use
  compression: true
```

**Install with debug dumps enabled:**

```bash
# Install with custom debug configuration
helm install dcgm-exporter ./deployment -f my-debug-values.yaml

# Check if debug files are being created (adjust path based on your configuration)
kubectl exec -n dcgm-exporter <pod-name> -- ls -la /var/log/dcgm-exporter-debug/
```

**Or use default configuration:**

```bash
# Install with default debug directory (/tmp/dcgm-exporter-debug)
helm install dcgm-exporter ./deployment --set debugDump.enabled=true

# Check if debug files are being created (using default directory)
kubectl exec -n dcgm-exporter <pod-name> -- ls -la /tmp/dcgm-exporter-debug/
```

### Other Configuration Options

See `values.yaml` for all available configuration options including:
- Image configuration
- Service settings
- Resource limits
- Kubernetes integration
- TLS configuration
- Basic authentication

### Securing Pprof

If you add `--enable-pprof` to `arguments`, also enable `tlsServerConfig`
and/or configure `basicAuth.users`. The chart will then mount the
exporter-toolkit web config and set
`DCGM_EXPORTER_WEB_CONFIG_FILE=/etc/dcgm-exporter/web-config.yaml`.

`dcgm-exporter` rejects startup when pprof is enabled without a web config
file, because `/debug/pprof/` can expose runtime profiling details and must be
protected by exporter-toolkit authentication or TLS.

### Restarting a blind exporter

A DCGM hostengine that starts before the NVIDIA driver is ready comes up
without NVML and reports no GPUs. An exporter that connects to it in that state
registers no GPU collector and serves an empty metrics page for the life of the
process, while `/health` still returns 200.

Adding `--health-require-gpus` to `arguments` makes `/health` return 503 in that
state, so the liveness probe restarts the pod and it re-enumerates. It is off by
default because the exporter cannot distinguish a node with no GPUs from a
hostengine reporting none.

Two caveats:

- When `basicAuth.users` is set, the chart degrades both probes to `tcpSocket`,
  which cannot observe the 503. The flag then has no effect on restarts.
- The check counts collectors registered under `FE_GPU`. A counters file that
  contributes no `FE_GPU` fields also reads as zero, so do not enable this with
  a switch- or CPU-only counter set.

## Troubleshooting

### Debug Dump Files

When debug dumps are enabled, the following types of files may be created:
- Device information dumps
- Metrics dumps
- Runtime state information

These files are compressed with gzip if compression is enabled and are automatically cleaned up based on the retention period.

### Common Issues

1. **Permission denied errors**: Ensure the debug directory has appropriate permissions
2. **Disk space issues**: Monitor the debug directory size and adjust retention as needed
3. **Missing files**: Check that debug dumps are enabled and the directory is properly mounted

## Support

For usage and troubleshooting, see [Install DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
For source or chart issues, create an issue in the project repository.

Enterprise users can review NVIDIA Knowledge Base articles and existing
support cases, or [submit a ticket](https://www.nvidia.com/en-us/data-center/products/ai-enterprise-suite/support/).
