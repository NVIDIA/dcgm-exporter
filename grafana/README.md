# grafana

Checked-in Grafana dashboard for DCGM Exporter metrics:
[`dcgm-exporter-dashboard.json`](dcgm-exporter-dashboard.json).

## Import

1. In Grafana: Dashboards → New → Import.
2. Upload `dcgm-exporter-dashboard.json`, or paste its contents.
3. Select a Prometheus datasource that scrapes dcgm-exporter.

A published variant is also available on Grafana.com as dashboard
[12239](https://grafana.com/grafana/dashboards/12239/).

Panels assume metric names from the collectors CSV in use (typically
`etc/default-counters.csv`). If you customize collectors, some panels may be
empty until matching metrics are exported.
