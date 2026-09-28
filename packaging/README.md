# packaging

Host-package install extras that are not part of the container runtime image.

## systemd

[`config-files/systemd/nvidia-dcgm-exporter.service`](config-files/systemd/nvidia-dcgm-exporter.service)
is staged into packages at `/lib/systemd/system/nvidia-dcgm-exporter.service`
(see `hack/package/stage-payload.sh`).

It is kept here rather than under `etc/` because:

- Metric CSVs in `etc/` install to `/etc/dcgm-exporter/` and are used by
  containers, packages, and Helm.
- The unit is host-package-only (containers do not ship systemd units).

The unit runs `/usr/bin/dcgm-exporter -f /etc/dcgm-exporter/default-counters.csv`.
