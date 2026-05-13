# Dashboard Update: Hostname in GPU Legends

The Grafana dashboard now displays the hostname in all GPU graph legends (e.g., "GPU 0 on myhost").

**How it works:**
- The `legendFormat` for each GPU panel now includes `{{hostname}}`.
- This helps distinguish metrics from GPUs with the same number on different hosts.

**No changes are needed to your Prometheus queries or exporters if you already have the `hostname` label in your metrics.**

---

_This change addresses: https://github.com/NVIDIA/dcgm-exporter/issues/630_
