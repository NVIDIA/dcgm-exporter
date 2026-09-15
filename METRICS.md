# DCGM-Exporter Metrics Reference

This page explains the metrics `dcgm-exporter` exposes, how it decides which ones
to export, and what each default metric means.

For the complete, authoritative catalog of every DCGM field, see the
[DCGM API field identifiers reference](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html).
This page documents the subset `dcgm-exporter` ships enabled by default and how to
enable the rest.

## How metrics are selected

`dcgm-exporter` does not hard-code a metric list. It reads a **counter CSV file** and
exports one Prometheus metric per enabled line. The repository ships three example
files under `etc/`:

| File | Purpose |
|------|---------|
| `default-counters.csv` | The default set, exported when no `-f` flag is given. |
| `dcp-metrics-included.csv` | A fuller set that also enables Datacenter Profiling (DCP) metrics. |
| `1.x-compatibility-metrics.csv` | Metric set matching the older 1.x exporter, for migration. |

You can point the exporter at your own file with `dcgm-exporter -f /path/to/counters.csv`.
See [Changing Metrics](README.md#changing-metrics) in the README.

Each line has three comma-separated columns:

```
# DCGM FIELD, Prometheus metric type, help message
DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).
```

Two rules are worth internalizing:

- **A line beginning with `#` is disabled** and is not exported. Many fields ship
  commented out to keep the default scrape lean; uncomment them (or add them to a
  custom file) to enable.
- **The Prometheus metric name is the DCGM field name.** The field
  `DCGM_FI_DEV_GPU_TEMP` is exported as the metric `DCGM_FI_DEV_GPU_TEMP`.

## Metric types

The second column maps to a Prometheus type:

| Type | Meaning |
|------|---------|
| `gauge` | A point-in-time value that can go up or down (temperature, clock, utilization). |
| `counter` | A monotonically increasing total (energy consumed, PCIe replays, remapped rows). |
| `label` | Not a metric. The value is attached as a **label** on the other metrics (for example, driver version). |

Every exported metric also carries identifying labels for the GPU it describes
(index, UUID, model name, hostname) plus any enabled `label`-type fields.

## Field families

DCGM field names encode where the value comes from. Recognizing the prefix tells you
the metric's source and availability:

| Prefix | Source | Availability |
|--------|--------|--------------|
| `DCGM_FI_DEV_*` | Device-level telemetry from NVML (clocks, temperature, power, utilization, memory, errors, NVLink). | All supported GPUs. |
| `DCGM_FI_PROF_*` | Datacenter Profiling (DCP) engine: fine-grained engine/pipe activity ratios. | NVIDIA datacenter GPUs, Volta and newer. Requires DCP support. |
| `DCGM_EXP_*` | Values computed by `dcgm-exporter` itself (for example, windowed XID error counts, GPU health). | Exporter-derived. |
| `DCGM_FI_DRIVER_VERSION`, other `*_VERSION` / info fields | Static device/driver information. | Exposed as `label`-type. |

## Metrics enabled by default

The following are the metrics enabled in `etc/default-counters.csv`. The "Meaning"
column is the help text shipped with each metric.

### Clocks

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_SM_CLOCK` | gauge | SM clock frequency (in MHz). |
| `DCGM_FI_DEV_MEM_CLOCK` | gauge | Memory clock frequency (in MHz). |

### Temperature

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_MEMORY_TEMP` | gauge | Memory temperature (in C). |
| `DCGM_FI_DEV_GPU_TEMP` | gauge | GPU temperature (in C). |

### Power

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_POWER_USAGE` | gauge | Power draw (in W). |
| `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION` | counter | Total energy consumption since boot (in mJ). |

### PCIe

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_PCIE_REPLAY_COUNTER` | counter | Total number of PCIe retries. |

### Utilization

The sample period varies depending on the product.

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_GPU_UTIL` | gauge | GPU utilization (in %). |
| `DCGM_FI_DEV_MEM_COPY_UTIL` | gauge | Memory utilization (in %). |
| `DCGM_FI_DEV_ENC_UTIL` | gauge | Encoder utilization (in %). |
| `DCGM_FI_DEV_DEC_UTIL` | gauge | Decoder utilization (in %). |

### Errors and violations

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_XID_ERRORS` | gauge | Value of the last XID error encountered. |

### Memory usage

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_FB_FREE` | gauge | Framebuffer memory free (in MiB). |
| `DCGM_FI_DEV_FB_USED` | gauge | Framebuffer memory used (in MiB). |
| `DCGM_FI_DEV_FB_RESERVED` | gauge | Framebuffer memory reserved (in MiB). |

### NVLink

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL` | counter | Total number of NVLink bandwidth counters for all lanes. |

### vGPU

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_VGPU_LICENSE_STATUS` | gauge | vGPU License status. |

### Remapped rows

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS` | counter | Number of remapped rows for uncorrectable errors. |
| `DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS` | counter | Number of remapped rows for correctable errors. |
| `DCGM_FI_DEV_ROW_REMAP_FAILURE` | gauge | Whether remapping of rows has failed. |

### Static information (labels)

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_DRIVER_VERSION` | label | Driver version, attached as a label on other metrics. |

### Datacenter Profiling (DCP)

Supported on NVIDIA datacenter GPUs, Volta and newer. These are ratios in the range
0.0 to 1.0 unless noted otherwise.

| Metric | Type | Meaning |
|--------|------|---------|
| `DCGM_FI_PROF_GR_ENGINE_ACTIVE` | gauge | Ratio of time the graphics engine is active. |
| `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` | gauge | Ratio of cycles the tensor (HMMA) pipe is active. |
| `DCGM_FI_PROF_DRAM_ACTIVE` | gauge | Ratio of cycles the device memory interface is active sending or receiving data. |
| `DCGM_FI_PROF_PCIE_TX_BYTES` | gauge | Rate of data transmitted over the PCIe bus (headers and payload), in bytes per second. |
| `DCGM_FI_PROF_PCIE_RX_BYTES` | gauge | Rate of data received over the PCIe bus (headers and payload), in bytes per second. |

## Enabling more metrics

Many additional fields ship disabled by default, including ECC error counters
(`DCGM_FI_DEV_ECC_*`), retired pages (`DCGM_FI_DEV_RETIRED_*`), NVLink error counters,
throttling/violation durations, additional DCP pipe-activity ratios
(`DCGM_FI_PROF_SM_ACTIVE`, `DCGM_FI_PROF_PIPE_FP{16,32,64}_ACTIVE`, ...), and static
info labels (`DCGM_FI_DEV_SERIAL`, `DCGM_FI_DEV_VBIOS_VERSION`, ...).

To enable them, uncomment the relevant lines in your counters file or add the field to
a custom file passed with `-f`. The full set of available fields and their exact units
is documented in the
[DCGM API field identifiers reference](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html).
