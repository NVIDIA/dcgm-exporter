# DCGM-Exporter Metrics

DCGM-Exporter emits the fields configured in the collectors CSV file passed with
`--collectors`/`-f`. The container default is
`/etc/dcgm-exporter/default-counters.csv`, which is built from
[`etc/default-counters.csv`](../etc/default-counters.csv) in this repository.

For every metric, DCGM-Exporter uses the DCGM field name as the Prometheus metric
name. Additional optional fields are available in the CSV files under
[`etc/`](../etc/) and in the
[DCGM field ID reference](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html).

## Clocks

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_SM_CLOCK` | SM clock frequency. Drops under thermal or power throttling. | gauge | MHz |
| `DCGM_FI_DEV_MEM_CLOCK` | Memory clock frequency. | gauge | MHz |

## Temperature

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_GPU_TEMP` | GPU die temperature. | gauge | Celsius |
| `DCGM_FI_DEV_MEMORY_TEMP` | HBM memory junction temperature. Throttles independently of the die; critical on A100/H100. | gauge | Celsius |

## Power

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_POWER_USAGE` | Instantaneous board power draw. Compare against TDP to detect sustained throttling. | gauge | Watts |
| `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION` | Total energy consumed since driver load. | counter | Millijoules |

## PCIe

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_PCIE_REPLAY_COUNTER` | Total number of PCIe retries. Non-zero values indicate link instability. | counter | Count |
| `DCGM_FI_PROF_PCIE_TX_BYTES` | Rate of data transmitted over PCIe, including protocol headers and payloads. | gauge | Bytes per second |
| `DCGM_FI_PROF_PCIE_RX_BYTES` | Rate of data received over PCIe, including protocol headers and payloads. | gauge | Bytes per second |

## Utilization

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_GPU_UTIL` | GPU utilization over the sample period. A high value does not imply efficient SM usage — use `DCGM_FI_PROF_*` metrics for that. | gauge | Percent |
| `DCGM_FI_DEV_MEM_COPY_UTIL` | Memory copy engine utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_ENC_UTIL` | Encoder utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_DEC_UTIL` | Decoder utilization over the sample period. | gauge | Percent |

## Memory (Framebuffer)

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_FB_FREE` | Framebuffer memory currently free. Monitor to catch OOM risk before it occurs. | gauge | MiB |
| `DCGM_FI_DEV_FB_USED` | Framebuffer memory currently used. Primary metric for LLM serving memory accounting (weights + KV-cache). | gauge | MiB |
| `DCGM_FI_DEV_FB_RESERVED` | Framebuffer memory currently reserved by the driver. | gauge | MiB |

## Errors and Events

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_XID_ERRORS` | Value of the last [XID error](https://docs.nvidia.com/deploy/xid-errors/) encountered. Non-zero typically requires investigation. | gauge | XID error code |

## Remapped Rows

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS` | Number of rows remapped for correctable errors. | counter | Rows |
| `DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS` | Number of rows remapped for uncorrectable errors. | counter | Rows |
| `DCGM_FI_DEV_ROW_REMAP_FAILURE` | Whether row remapping has failed (no spare rows remain). A value of `1` means the GPU should be replaced. | gauge | Boolean |

## NVLink

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL` | Total NVLink bandwidth counters for all lanes. | counter | Count |

## Miscellaneous

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_VGPU_LICENSE_STATUS` | vGPU license status. | gauge | Status code |

## DCP Profiling Metrics

> Supported on NVIDIA datacenter GPUs with Volta architecture or newer.
> Requires the DCGM profiling module to be loaded. Only one process may use
> the profiling API at a time — DCGM-Exporter and `nsys`/`ncu` cannot run
> concurrently.

Ratio metrics are reported from `0.0` to `1.0`; multiply by `100` in
PromQL when a percentage display is desired.

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_PROF_GR_ENGINE_ACTIVE` | Ratio of time the graphics/compute engine has work queued. | gauge | Ratio |
| `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` | Ratio of cycles the Tensor Core pipe is issuing instructions. The primary efficiency metric for LLM training and inference — target ≥ 0.6 for GEMM-heavy workloads. | gauge | Ratio |
| `DCGM_FI_PROF_DRAM_ACTIVE` | Ratio of cycles HBM is actively transferring data. High DRAM active with low `PIPE_TENSOR_ACTIVE` indicates a memory-bandwidth-bound kernel. | gauge | Ratio |

## Device Labels

Metrics with a `label` type add metadata to other exported metrics instead
of creating a standalone Prometheus time series.

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DRIVER_VERSION` | Driver version reported as a label on other metrics. | label | Version string |

## Notes

- Profiling metrics use the `DCGM_FI_PROF_*` prefix and are supported on
  NVIDIA datacenter Volta GPUs and newer.
- Ratio metrics are reported from `0.0` to `1.0`; multiply by `100` in
  PromQL when a percentage display is desired.
- Metrics with a `label` type add metadata to other exported metrics instead of
  creating a standalone Prometheus time series.
