# DCGM Exporter Helm Chart

This chart runs NVIDIA DCGM Exporter on every GPU node of a Kubernetes cluster and serves GPU metrics for Prometheus.

For installation, configuration, verification, administration, and
troubleshooting, see [Install DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
Related documentation includes:

- [Configure Prometheus for DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/learn/getting-started-for-system-administrators/configure-prometheus-for-dcgm-exporter.html)
- [`dcgm-exporter` command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html)
- [DCGM Exporter Metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [DCGM Exporter Release Notes](https://docs.nvidia.com/datacenter/dcgm/latest/release-notes/dcgm-exporter.html)

## Quick start

```bash
# Build the self-contained chart from source.
task helm:package

helm install dcgm-exporter dist/dcgm-exporter.tgz
touch my-values.yaml   # put your settings in this file
helm upgrade dcgm-exporter dist/dcgm-exporter.tgz -f my-values.yaml
helm uninstall dcgm-exporter
```

The nodes need the NVIDIA driver. The chart creates a `ServiceMonitor`, so install the
Prometheus Operator CRDs first or add `--set serviceMonitor.enabled=false`.

`helm install dcgm-exporter ./deployment` also works when Git checks out
`deployment/files/default-counters.csv` as a symlink. Checkouts without symlink support,
such as Git for Windows with the default `core.symlinks=false`, need the packaged chart.

## Common settings

### NVIDIA runtime class

If the default container runtime does not inject the NVIDIA driver libraries, set the
RuntimeClass your cluster uses. `kubectl get runtimeclass` lists them.

```yaml
runtimeClassName: nvidia
```

### GPU node taints

`tolerations` replaces the default list, so keep the entries you still need.

```yaml
tolerations:
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
  - key: nvidia.com/gpu
    operator: Exists
    effect: NoSchedule
```

### ServiceMonitor

The `ServiceMonitor` sets no HTTPS or credentials. With TLS or basic auth on, disable it
and give Prometheus a scrape configuration with the matching scheme and credentials.

```yaml
serviceMonitor:
  enabled: false
```

## Exporter configuration

`config` is the exporter's own YAML file, written as a Helm value. The chart renders it
into a ConfigMap, mounts it and points the exporter at it. Change one key with `--set`;
the other defaults stay.

```bash
helm install dcgm-exporter dist/dcgm-exporter.tgz \
  --set config.sources.dcgm.watch.max_keep_age=5m
```

The default:

```yaml
config:
  version: 2
  metrics:                  # Helm replaces a list whole: repeat the entire entry
    - name: default
      file: /etc/dcgm-exporter/chart/metrics/default-counters.csv
      every: 30s
      enrichment:
        kubernetes: auto    # add pod, namespace and container names when available
        container: false
        dra: false
        hpc_jobs: false
  outputs:
    prometheus:
      type: prometheus
      schema: dcgm/v1
      listen: ":9400"       # the port must equal service.port
  observability:
    metrics:
      enabled: false        # true adds the exporter's own go_*, process_* and promhttp_* metrics
```

Every key is in the [command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html).
Check a file with `dcgm-exporter config validate`, and see where each setting comes from
with `dcgm-exporter config explain`.

The exporter reads the file at startup. A changed `config` rolls the pods on `helm upgrade`.

`arguments` and `extraEnv` override `config`. The chart's RBAC, port and volumes follow
`config` only, so the chart rejects flags and variables for the settings it relies on:
the listen address, the metrics and web config files, the debug dump switch and
directory, and the pod label, pod UID and DRA switches. Set those in `config`.

### Use your own config file

| Setting | Result |
| --- | --- |
| `config` (default) | The chart creates `<fullname>-config` from `config`. |
| `configMap.existingName` | The chart mounts your ConfigMap and ignores `config`. |
| `config: null` | The chart mounts no file, so the exporter uses the one in the image. |

```yaml
configMap:
  existingName: my-exporter-config
  key: config.yaml          # the key that holds the complete exporter file
```

With an existing ConfigMap the chart cannot read your file. It skips the checks that
read `config` and creates no ClusterRole. The install notes print the metrics path when
`customMetrics` is set, and the web config path when TLS or basic auth is on.
Restart the pods after you edit the ConfigMap, because the exporter reads the file at
startup.

### Mounted files

Each file is a whole-directory mount without `subPath`, so the kubelet keeps it current.

| Path in the container | Source |
| --- | --- |
| `/etc/dcgm-exporter/chart/config/<configMap.key>` | `<fullname>-config`, or `configMap.existingName` |
| `/etc/dcgm-exporter/chart/metrics/default-counters.csv` | `<fullname>-metrics`: the default list, or `customMetrics` |
| `/etc/dcgm-exporter/chart/web-config/web-config.yaml` | `<fullname>-web-config`, when TLS or basic auth is on |
| `/etc/dcgm-exporter/tls` | the TLS Secret, when TLS is on |

A relative `metrics[].file` or `server.web_config_file` starts from the config file's
directory. With an existing ConfigMap, set both to the paths above.

### Choose the metrics

List the metrics inline.

```yaml
config:
  version: 2
  metrics:
    - name: default
      every: 30s
      enrichment:           # repeat it: a switch left out means auto
        kubernetes: auto
        container: false
        dra: false
        hpc_jobs: false
      fields:
        - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
          prometheus_type: gauge
          help: GPU temperature (in C).
        - name: DCGM_FI_DEV_BOARD_POWER_WATTS
          prometheus_type: gauge
          help: Power draw (in W).
  outputs:
    prometheus:
      type: prometheus
      schema: dcgm/v1
```

Or keep your CSV in a ConfigMap, mount it as a directory and point `metrics[0].file` at it.

```yaml
extraConfigMapVolumes:
  - name: extra-metrics
    configMap:
      name: my-metrics
extraVolumeMounts:
  - name: extra-metrics
    mountPath: /etc/dcgm-exporter/extra
    readOnly: true
```

The running exporter reloads an edited list without a restart. Do not mount over
`/etc/dcgm-exporter/default-counters.csv`, the image's own list.

Select which devices are collected with `entities`, and collect some fields at another
rate with `overrides`. Both belong to the `metrics` entry, so repeat the whole entry.

```yaml
config:
  version: 2
  metrics:
    - name: default
      file: /etc/dcgm-exporter/chart/metrics/default-counters.csv
      every: 30s
      enrichment:           # repeat it: a switch left out means auto
        kubernetes: auto
        container: false
        dra: false
        hpc_jobs: false
      entities:
        gpu: {policy: explicit, gpus: [0, 1], instances: none}
      overrides:
        - name: fast-thermals
          match: [DCGM_FI_DEV_GPU_TEMP_CELSIUS, DCGM_FI_DEV_BOARD_POWER_WATTS]
          every: 5s
  outputs:
    prometheus:
      type: prometheus
      schema: dcgm/v1
```

The `match` list takes exact field names, `*` wildcards such as `DCGM_FI_DEV_NVLINK_*`,
and `/regex/` patterns. Exact DCGM names also match aliases with the same field ID.
XID patterns match both `DCGM_FI_DEV_XID_ERROR` and `DCGM_FI_DEV_XID_ERRORS`.
A `window` override is rejected if it selects a configured field that is not a window
counter, including through a DCGM alias. The command reference lists the rest.

#### customMetrics (deprecated)

`customMetrics` replaces the text of the chart's CSV. Move the lines into `fields`.

```yaml
# Before
customMetrics: |
  DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).
```

```yaml
# After
config:
  version: 2
  metrics:
    - name: default
      every: 30s
      enrichment:           # repeat it: a switch left out means auto
        kubernetes: auto
        container: false
        dra: false
        hpc_jobs: false
      fields:
        - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
          prometheus_type: gauge
          help: GPU temperature (in C).
  outputs:
    prometheus:
      type: prometheus
      schema: dcgm/v1
```

The exporter reads `customMetrics` only when `metrics[].file` is the chart's CSV path, and
the chart fails the render when it is not. With an existing ConfigMap or `config: null` the
chart cannot check, so set that path in your file.

### Scrape timeouts

Pod metadata can slow a scrape on dense GPU or MIG nodes. Keep the exporter timeouts and
the `ServiceMonitor` consistent.

```yaml
config:
  version: 2
  server:
    read_timeout: 10s
    write_timeout: 30s      # keep above serviceMonitor.scrapeTimeout

serviceMonitor:
  interval: 30s
  scrapeTimeout: 25s        # at most the interval
```

### Stop grace period

On a stop, the exporter waits up to `sources.dcgm.cleanup_timeout` (default `5m`) for
DCGM to release its watches. Kubernetes stops the pod after its grace period, 30s by
default, so it can end that wait before it times out. To see the timeout message at
shutdown, lower `config.sources.dcgm.cleanup_timeout` below the grace period. This chart
does not set `terminationGracePeriodSeconds`, so a longer grace period needs a DaemonSet
patch outside Helm.

### Port

`service.port` is the Service, container and probe port. The render fails when a
`config.outputs.<name>.listen` port differs, so change both together.

### Debug dumps

Dumps write metrics and device data to JSON files for troubleshooting. They hold runtime
data, so limit access and turn them off when you are done.

```yaml
config:
  version: 2
  debug_dump:
    enabled: true
    directory: /tmp/dcgm-exporter-debug
    retention: 24h
    max_total_bytes: 268435456

debugDump:
  sizeLimit: 512Mi          # the chart's emptyDir; keep it above max_total_bytes
```

The `emptyDir` is lost when the pod is replaced. To keep dumps, add a volume named
`debug-dumps` and mount it at the dump directory. The chart then adds no `emptyDir`.

```yaml
extraConfigMapVolumes:
  - name: debug-dumps
    persistentVolumeClaim: {claimName: exporter-debug}
extraVolumeMounts:
  - name: debug-dumps
    mountPath: /tmp/dcgm-exporter-debug
```

Copy the files out with `kubectl cp <namespace>/<pod>:/tmp/dcgm-exporter-debug ./debug-files`.

## Security

### Remote DCGM hostengine

When using `config.sources.dcgm.hostengine`, follow the
[remote hostengine security guidance](https://github.com/NVIDIA/dcgm-exporter/blob/main/SECURITY.md#remote-dcgm-hostengine-connections).
The HTTP TLS/basic-auth settings below do not secure the separate DCGM connection.

### TLS and basic auth

TLS or basic auth makes the chart render a web config Secret, mount it and set
`config.server.web_config_file`. The render fails if `config` names another file or is
`null`. With an existing ConfigMap, set `server.web_config_file` to the path above.

```yaml
tlsServerConfig:
  enabled: true
  existingSecret: my-exporter-tls  # tls.crt, tls.key and, for client auth, ca.crt
  clientAuthType: RequireAndVerifyClientCert

basicAuth:
  users:
    prometheus: change-me
```

The default `autoGenerated: true` creates a self-signed certificate, which is not for
production. Instead of `existingSecret`, the chart can create the Secret from
`tlsServerConfig.cert`, `key` and `ca`.

Only `RequireAndVerifyClientCert` makes client certificates mandatory.
`VerifyClientCertIfGiven` verifies a certificate only when the client sends one.
`RequestClientCert` and `RequireAnyClientCert` do not verify certificates.

The chart stores only password hashes in the Secret, but your values and the Helm release
data hold the passwords. With basic auth on, the probes check the TCP port.

### pprof

Profiling is off by default. Turn it on together with TLS or basic auth, which it needs.

```yaml
config:
  version: 2
  server:
    profiling: true
tlsServerConfig:
  enabled: true
```

### Network policy

The policy admits only `allowedPeers` to the metrics port.

```yaml
networkPolicy:
  enabled: true
  allowedPeers:
    - namespaceSelector:
        matchLabels: {kubernetes.io/metadata.name: monitoring}
      podSelector:
        matchLabels: {app.kubernetes.io/name: prometheus}
```

An empty list blocks every scrape, and a peer of `{}` admits all sources. The network
plugin must enforce NetworkPolicy, and the policy does not replace TLS or basic auth.
Kubelet health probes keep working. GPU Operator ignores these values; see
[the raw example](examples/README.md#metrics-ingress-policy).

The policy opens `service.port`, so the exporter must listen there. While the policy is on
the chart rejects `hostNetwork: true`, which NetworkPolicy does not cover, and every other
way to set the listener:

```text
--address, -a, --web-systemd-socket
DCGM_EXPORTER_LISTEN, DCGM_EXPORTER_SYSTEMD_SOCKET
config.server.systemd_socket
```

### Pod labels, DRA and permissions

Pod labels need an allowlist of RE2 patterns. `[".*"]` exports every label, which can
expose sensitive values.

```yaml
config:
  version: 2
  sources:
    kubernetes:
      pod_uid: true
      pod_labels:
        enabled: true
        allow: ["^app$", "^tier$"]
```

DRA labels need `enrichment.dra` and a `sources.dra` block. Repeat the whole `metrics`
entry, because Helm replaces the list.

```yaml
config:
  version: 2
  metrics:
    - name: default
      file: /etc/dcgm-exporter/chart/metrics/default-counters.csv
      every: 30s
      enrichment:
        kubernetes: auto
        container: false
        dra: true
        hpc_jobs: false
  sources:
    dra:
      mode: kubernetes
```

When `config` turns on pod labels, pod UID or DRA, the chart creates a ClusterRole that
can read Pods (and ResourceSlices for DRA) and mounts the service account token. To manage
permissions yourself, bind your own role and mount the token.

```yaml
kubernetes:
  rbac:
    create: false
serviceAccount:
  create: false
  name: exporter-with-external-rbac
  automountServiceAccountToken: true
```

## Parameters

Each table lists a chart value, what it does and its default. `values.yaml` has the same
list with comments.

### Image

| Name | Description | Value |
| --- | --- | --- |
| `image.repository` | Exporter image repository | `nvcr.io/nvidia/k8s/dcgm-exporter` |
| `image.tag` | Exporter version. Empty uses the chart's `appVersion` | `4.9.0` |
| `image.digest` | Pin the image by digest. The reference becomes `repository@digest` and `tag` is ignored | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `imagePullSecrets` | Secrets for pulling the image | `[]` |

### Exporter configuration

| Name | Description | Value |
| --- | --- | --- |
| `config` | The exporter's YAML configuration. `null` mounts no file | see [Exporter configuration](#exporter-configuration) |
| `configMap.existingName` | Mount this existing ConfigMap instead of rendering `config` | `""` |
| `configMap.key` | Key that holds the config file in the ConfigMap | `config.yaml` |
| `customMetrics` | Deprecated. CSV text that replaces the chart's metrics list | unset |
| `arguments` | Extra command-line flags for settings with no `config` key. They override `config` | `[]` |
| `extraEnv` | Extra environment variables. They override `config` | `[]` |
| `kubeletPath` | Directory of the kubelet pod-resources socket | `/var/lib/kubelet/pod-resources` |

### Names

| Name | Description | Value |
| --- | --- | --- |
| `nameOverride` | Replace the chart name | `""` |
| `fullnameOverride` | Replace the generated resource name | `""` |
| `namespaceOverride` | Install the objects in this namespace instead of the release namespace | `""` |

### Pod

| Name | Description | Value |
| --- | --- | --- |
| `hostPID` | Let the exporter see host processes | `false` |
| `hostNetwork` | Use the host network | `false` |
| `runtimeClassName` | RuntimeClass that provides the NVIDIA driver | `""` |
| `priorityClassName` | Pod priority class | `system-node-critical` |
| `rollingUpdate.maxUnavailable` | DaemonSet pods that can be down during an update; exactly one of this and `maxSurge` is nonzero | `1` |
| `rollingUpdate.maxSurge` | Nodes that can run an extra updated pod during an update; exactly one of this and `maxUnavailable` is nonzero | `0` |
| `podLabels` | Extra pod labels | `{}` |
| `podAnnotations` | Extra pod annotations | `{}` |
| `daemonSetAnnotations` | Extra DaemonSet annotations | `{}` |
| `podSecurityContext` | Pod security context | `{}` |
| `securityContext` | Container security context. The default runs as root and keeps only `SYS_ADMIN`, which profiling metrics (`DCGM_FI_PROF_*`) need | see `values.yaml` |
| `resources` | CPU, memory and ephemeral-storage requests and limits | 100m CPU, 128Mi memory and 100Mi ephemeral-storage requests; 200m CPU and 512Mi memory limits |
| `initContainers` | Extra init containers, as full container specs | `[]` |

### Scheduling

| Name | Description | Value |
| --- | --- | --- |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Pod tolerations. A new list replaces the default | tolerates the control-plane taint |
| `affinity` | Pod affinity | `{}` |

### Service and ServiceMonitor

| Name | Description | Value |
| --- | --- | --- |
| `service.enable` | Create the Service | `true` |
| `service.type` | Service type | `ClusterIP` |
| `service.internalTrafficPolicy` | `Cluster`, or `Local` to keep traffic on the node | `Cluster` |
| `service.clusterIP` | Fixed cluster IP | `""` |
| `service.port` | Service, container and probe port. `config.outputs.<name>.listen` must use it | `9400` |
| `service.annotations` | Service annotations | `{}` |
| `serviceMonitor.enabled` | Create a Prometheus Operator `ServiceMonitor` | `true` |
| `serviceMonitor.apiVersion` | `ServiceMonitor` API version | `monitoring.coreos.com/v1` |
| `serviceMonitor.interval` | Scrape interval | `30s` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout. Keep it below `config.server.write_timeout` and at most the interval | `25s` |
| `serviceMonitor.honorLabels` | Keep the labels of the scraped metrics on a conflict | `false` |
| `serviceMonitor.additionalLabels` | Extra labels, for example the label your Prometheus selects on | `{}` |
| `serviceMonitor.relabelings` | Relabelings applied before the scrape | `[]` |
| `serviceMonitor.metricRelabelings` | Relabelings applied to the scraped metrics | `[]` |

### TLS and basic auth

| Name | Description | Value |
| --- | --- | --- |
| `tlsServerConfig.enabled` | Serve HTTPS | `false` |
| `tlsServerConfig.autoGenerated` | Create a self-signed certificate. Not for production | `true` |
| `tlsServerConfig.existingSecret` | Secret with your certificate. A template is allowed | `""` |
| `tlsServerConfig.certFilename` | Certificate file name in the Secret | `tls.crt` |
| `tlsServerConfig.keyFilename` | Key file name in the Secret | `tls.key` |
| `tlsServerConfig.caFilename` | CA file name in the Secret | `ca.crt` |
| `tlsServerConfig.clientAuthType` | Client certificate policy. See [TLS and basic auth](#tls-and-basic-auth) | `""` |
| `tlsServerConfig.cert` | Certificate text for a Secret the chart creates. Ignored with `existingSecret` | `""` |
| `tlsServerConfig.key` | Key text for a Secret the chart creates. Ignored with `existingSecret` | `""` |
| `tlsServerConfig.ca` | CA text for a Secret the chart creates. Ignored with `existingSecret` | `""` |
| `basicAuth.users` | Map of user to password. The chart stores the hashes | `{}` |

### Kubernetes access

| Name | Description | Value |
| --- | --- | --- |
| `kubernetes.rbac.create` | Create the ClusterRole and binding that pod labels, pod UID and DRA need | `true` |
| `serviceAccount.create` | Create the ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name. Empty uses the generated name | unset |
| `serviceAccount.annotations` | ServiceAccount annotations | `{}` |
| `serviceAccount.automountServiceAccountToken` | Mount the API token. Unset mounts it when `config` needs the Kubernetes API and `kubernetes.rbac.create` is true | unset |

### Volumes and debug dumps

| Name | Description | Value |
| --- | --- | --- |
| `extraHostVolumes` | Extra `hostPath` volumes, each with `name` and `hostPath` | `[]` |
| `extraConfigMapVolumes` | Extra volumes, as full volume specs | `[]` |
| `extraVolumeMounts` | Mounts for the extra volumes | `[]` |
| `debugDump.sizeLimit` | Size of the `emptyDir` the chart adds when debug dumps are on | `512Mi` |

### Probes

The startup probe gives a slow start up to `periodSeconds` times `failureThreshold`
(5 minutes by default) before the other probes begin. Without it, the liveness and
readiness probes wait 45 seconds unless you set `initialDelaySeconds`.

| Name | Description | Value |
| --- | --- | --- |
| `startupProbe.enabled` | Use a startup probe | `true` |
| `startupProbe.periodSeconds` | Seconds between checks | `5` |
| `startupProbe.failureThreshold` | Failed checks before the pod restarts | `60` |
| `startupProbe.timeoutSeconds` | Seconds before a check times out. Unset uses the Kubernetes default | unset |
| `livenessProbe.periodSeconds` | Seconds between checks | `5` |
| `livenessProbe.initialDelaySeconds` | Seconds before the first check. Unset uses the default described above | unset |
| `livenessProbe.timeoutSeconds` | Seconds before a check times out. Unset uses the Kubernetes default | unset |
| `livenessProbe.failureThreshold` | Failed checks before the pod restarts. Unset uses the Kubernetes default | unset |
| `readinessProbe` | A map with `periodSeconds`, `initialDelaySeconds`, `timeoutSeconds` and `failureThreshold`. Unset keys use the Kubernetes default | `{}` |

### Network policy

| Name | Description | Value |
| --- | --- | --- |
| `networkPolicy.enabled` | Create a NetworkPolicy for the metrics port | `false` |
| `networkPolicy.allowedPeers` | Peers that may scrape, as NetworkPolicy `from` peers. `[]` blocks every scrape | `[]` |

### Restarting a blind exporter

A DCGM hostengine that starts before the NVIDIA driver is ready comes up
without NVML and reports no GPUs. An exporter that connects to it in that state
registers no GPU collector and serves an empty metrics page for the life of the
process, while `/health` still returns 200.

Setting `config.server.health_require_gpus=true` or adding
`--health-require-gpus` to `arguments` makes `/health` return 503 in that state,
so the liveness probe restarts the pod and it re-enumerates. It is off by
default because the exporter cannot distinguish a node with no GPUs from a
hostengine reporting none.

Two caveats:

- When `basicAuth.users` is set, the chart degrades both probes to `tcpSocket`,
  which cannot observe the 503. The flag then has no effect on restarts.
- The check counts collectors registered under `FE_GPU`. A counters file that
  contributes no `FE_GPU` fields also reads as zero, so do not enable this with
  a switch- or CPU-only counter set.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `no matches for kind "ServiceMonitor"` | The Prometheus Operator CRDs are missing. Install them, or set `serviceMonitor.enabled=false` |
| Render fails with `removed chart values` | Your values use a setting that moved into `config`. See [Upgrading](#upgrading) |
| Render fails with `must use service.port` | A `config.outputs.<name>.listen` port differs from `service.port`. Change both together |
| The exporter cannot load the NVIDIA libraries | The runtime does not inject the driver. Set `runtimeClassName` |
| A scrape fails with `networkPolicy.enabled` | Prometheus is not in `allowedPeers`. Add its namespace or pod labels |
| The pod is evicted with debug dumps on | The dumps outgrew `debugDump.sizeLimit`. Raise it above `config.debug_dump.max_total_bytes` |
| Edits to `config` have no effect | The exporter reads the file at startup. With `configMap.existingName`, restart the pods |
| Edits to the metrics list have no effect | Check that `config.metrics[].file` names the file you edited |

## Upgrading

The chart fails the render when your values still use a removed setting. The error names
each one and its replacement. Rewrite the values, then upgrade with `-f`.

```yaml
# 4.8
kubernetes:
  enablePodLabels: true
  podLabelAllowlistRegex: ["^app$"]
service:
  webReadTimeout: 10s
```

```yaml
# 4.9
config:
  version: 2
  sources:
    kubernetes:
      pod_labels:
        enabled: true
        allow: ["^app$"]
  server:
    read_timeout: 10s
```

`helm get values <release> -o yaml` prints your current values as a starting file. Do not
use `--reuse-values` or `--reset-then-reuse-values`, which bring back the removed values.

- The metrics ConfigMap is now `<fullname>-metrics`, and the chart no longer creates the
  `dcgm-exporter-read-cm` Role. The deprecated `--configmap-data` flag needs your own Role
  and a mounted token; use `config.metrics[].file` instead.
- The web config moved from a ConfigMap to a Secret. Delete the old ConfigMap if you apply
  rendered manifests without pruning.

## Support

For usage and troubleshooting, see [Install DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
For source or chart issues, create an issue in the project repository.

Enterprise users can review NVIDIA Knowledge Base articles and existing
support cases, or [submit a ticket](https://www.nvidia.com/en-us/data-center/products/ai-enterprise-suite/support/).
