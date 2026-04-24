# Release

This documents the release process as well as the versioning strategy for the DCGM exporter.

## Feature 001-multi-user-gpu-util (bare-metal multi-user GPU utilization)

This branch adds a per-user, per-project decoration of `DCGM_FI_DEV_GPU_UTIL`
for bare-metal (non-Kubernetes) servers:

- **Flag**: `--config <path>` (or `DCGM_EXPORTER_CONFIG` env). When omitted,
  defaults to `/etc/dcgm-exporter/config.yaml`. Missing file is a hard
  failure with an unambiguous log line.
- **Config path**: `/etc/dcgm-exporter/config.yaml`. Schema documented at
  `specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md`; only
  `labels:` and `server:` sections are accepted.
- **Label schema on `DCGM_FI_DEV_GPU_UTIL`**:
  - `USER` — always injected, resolved from the process UID (falls back to
    `uid:<n>` when the system account is unknown).
  - Plus every `labels.static[]` / `labels.env[]` name declared in
    `config.yaml` (static values are resolved once at startup; env values
    are read per-process per-cycle from `/proc/<pid>/environ`).
  - Multi-process GPUs are split by process count across `(USER, env-values)`
    groups. Sum of splits equals the GPU's true UTIL exactly (closure
    compensation).
- **Deployment**: `sudo make install` + `sudo systemctl enable --now
  dcgm-exporter`. The systemd unit lives at
  `packaging/config-files/systemd/nvidia-dcgm-exporter.service` and is
  installed to `/etc/systemd/system/dcgm-exporter.service`. Complete
  runbook: `docs/bare-metal-deployment.md`.
- **Scope**: Bare-metal only. Kubernetes/container deployment is out of
  scope for this feature — upstream Helm charts and DaemonSet templates are
  untouched.
- **DCGM requirement**: DCGM 4.x (`libdcgm.so.4`). DCGM 3.x is not
  compatible with the pinned upstream `github.com/NVIDIA/go-dcgm`.

End-to-end validation evidence: `specs/001-multi-user-gpu-util/validation-log.md`.

---

This documents the release process as well as the versioning strategy for the DCGM exporter.

## Versioning

The DCGM container has three major components:
- The DCGM Version (e.g: 4.2.3)
- The Exporter Version (e.g: 4.1.1)
- The platform of the container (e.g: distroless, ubuntu22.04, ubi9)

The overall version of the DCGM container has three forms:
- The long form: `${DCGM_VERSION}-${EXPORTER_VERSION}-${PLATFORM}`
- The short form: `${DCGM_VERSION}`
- The latest tag: `latest`

The long form is a unique tag that once pushed will always refer to the same container.
This means that no updates will be made to that tag and it will always point to the same container.

The short form refers to the latest EXPORTER_VERSION with the platform fixed to distroless.
The latest tag refers to the latest short form (i.e: latest DCGM_VERSION and EXPORTER_VERSION).

Note: We do not maintain multiple version branches. The Exporter functions with the latest go-dcgm bindings.

## Releases

Newer versions are released on demand but tend to follow DCGM's release cadence.
