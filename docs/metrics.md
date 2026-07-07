# DCGM-Exporter Metrics

DCGM-Exporter emits the fields configured in the collectors CSV file passed with
`--collectors`/`-f`. The container default is
`/etc/dcgm-exporter/default-counters.csv`, which is built from
[`etc/default-counters.csv`](../etc/default-counters.csv) in this repository.

The table below describes the metrics enabled by that default collectors file.
For every metric, DCGM-Exporter uses the DCGM field name as the Prometheus metric
name. Additional optional fields are available in the CSV files under
[`etc/`](../etc/) and in the
[DCGM field ID reference](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html).

| Metric | Description | Prometheus type | Unit |
| --- | --- | --- | --- |
| `DCGM_FI_DEV_SM_CLOCK` | SM clock frequency. | gauge | MHz |
| `DCGM_FI_DEV_MEM_CLOCK` | Memory clock frequency. | gauge | MHz |
| `DCGM_FI_DEV_MEMORY_TEMP` | Memory temperature. | gauge | Celsius |
| `DCGM_FI_DEV_GPU_TEMP` | GPU temperature. | gauge | Celsius |
| `DCGM_FI_DEV_POWER_USAGE` | Power draw. | gauge | Watts |
| `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION` | Total energy consumed since boot. | counter | Millijoules |
| `DCGM_FI_DEV_PCIE_REPLAY_COUNTER` | Total number of PCIe retries. | counter | Count |
| `DCGM_FI_DEV_GPU_UTIL` | GPU utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_MEM_COPY_UTIL` | Memory copy engine utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_ENC_UTIL` | Encoder utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_DEC_UTIL` | Decoder utilization over the sample period. | gauge | Percent |
| `DCGM_FI_DEV_XID_ERRORS` | Value of the last XID error encountered. | gauge | XID error code |
| `DCGM_FI_DEV_FB_FREE` | Framebuffer memory currently free. | gauge | MiB |
| `DCGM_FI_DEV_FB_USED` | Framebuffer memory currently used. | gauge | MiB |
| `DCGM_FI_DEV_FB_RESERVED` | Framebuffer memory currently reserved. | gauge | MiB |
| `DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL` | Total number of NVLink bandwidth counters for all lanes. | counter | Count |
| `DCGM_FI_DEV_VGPU_LICENSE_STATUS` | vGPU license status. | gauge | Status code |
| `DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS` | Number of remapped rows for uncorrectable errors. | counter | Rows |
| `DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS` | Number of remapped rows for correctable errors. | counter | Rows |
| `DCGM_FI_DEV_ROW_REMAP_FAILURE` | Whether remapping of rows has failed. | gauge | Boolean |
| `DCGM_FI_DRIVER_VERSION` | Driver version reported as a label on other metrics. | label | Version string |
| `DCGM_FI_PROF_GR_ENGINE_ACTIVE` | Ratio of time the graphics engine is active. | gauge | Ratio |
| `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` | Ratio of cycles the tensor pipe is active. | gauge | Ratio |
| `DCGM_FI_PROF_DRAM_ACTIVE` | Ratio of cycles the device memory interface is active sending or receiving data. | gauge | Ratio |
| `DCGM_FI_PROF_PCIE_TX_BYTES` | Rate of data transmitted over PCIe, including protocol headers and payloads. | gauge | Bytes per second |
| `DCGM_FI_PROF_PCIE_RX_BYTES` | Rate of data received over PCIe, including protocol headers and payloads. | gauge | Bytes per second |

## Notes

- Profiling metrics use the `DCGM_FI_PROF_*` prefix and are supported on
  NVIDIA datacenter Volta GPUs and newer.
- Ratio metrics are reported from `0.0` to `1.0`; multiply by `100` in
  PromQL when a percentage display is desired.
- Metrics with a `label` type add metadata to other exported metrics instead of
  creating a standalone Prometheus time series.
