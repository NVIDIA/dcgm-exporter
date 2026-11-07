# packaging

Host-package install extras that are not part of the container runtime image.

## systemd

[`config-files/systemd/nvidia-dcgm-exporter.service`](config-files/systemd/nvidia-dcgm-exporter.service)
is staged by [`packaging/BUILD.bazel`](BUILD.bazel) into packages at
`/lib/systemd/system/nvidia-dcgm-exporter.service`.

It is kept here rather than under `etc/` because:

- Metric CSVs in `etc/` install to `/etc/dcgm-exporter/` and are used by
  containers, packages, and Helm.
- The unit is host-package-only (containers do not ship systemd units).

The unit runs `/usr/bin/dcgm-exporter` and sets
`DCGM_EXPORTER_CONFIG_FILE=/etc/dcgm-exporter/config.yaml`, the same variable the container images set.

### Configuration files

The packages install [`etc/config.yaml`](../etc/config.yaml) and the metric CSVs into
`/etc/dcgm-exporter/`. Their settings match running with no configuration file.

The packages mark these files as configuration files, so an upgrade does not silently overwrite your edits:

- **deb:** apt asks whether to keep your edited file or install the new one. Unattended upgrades keep yours.
- **rpm:** an upgrade keeps your edited file and writes the new one next to it as `<file>.rpmnew`.

The release packages are built outside this repository and must carry the same markings.

### Changing settings

Do not edit the shipped file. Add a drop-in with `systemctl edit nvidia-dcgm-exporter`:

- Set `Environment=DCGM_EXPORTER_CONFIG_FILE=<path>` to use a copy of the file.
- Set other `DCGM_EXPORTER_*` variables. They override the file.
- To add flags, clear `ExecStart=` first and then repeat the full command line.
