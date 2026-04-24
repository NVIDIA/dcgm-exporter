# Bare-Metal Deployment: multi-user GPU utilization

This deployment guide targets **bare-metal Linux GPU servers running under systemd**.
Kubernetes / container deployment is explicitly out of scope
(see `specs/001-multi-user-gpu-util/spec.md` Clarification Q2).

## Prerequisites

- NVIDIA driver + NVIDIA DCGM installed; `nvidia-dcgm.service` active.
- Go 1.24+ (only needed at build time).
- The exporter must run as `root` to read `/proc/<PID>/environ` of other users.

## Configure

The exporter is driven by a single `config.yaml` whose schema is documented at
`specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md`. Only two top-level
sections are allowed: `labels` and `server`. No metric-selection fields (those remain
driven by the DCGM counters CSV).

A canonical example lives at the repository root (`config.yaml`). Copy it to
`/etc/dcgm-exporter/config.yaml` and edit the `STUDIO` static value to match the host.

The remainder of this runbook (install / enable / verify / rollback / troubleshooting)
will be filled in by task T035 (feature 001-multi-user-gpu-util, User Story 3).
