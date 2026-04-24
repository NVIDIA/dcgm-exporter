# Bare-Metal Deployment: Multi-User GPU Utilization

This runbook is the authoritative "how to deploy and operate this dcgm-exporter
fork on a bare-metal Linux GPU server" document. It covers the feature
`001-multi-user-gpu-util` which decorates `DCGM_FI_DEV_GPU_UTIL` with
per-user, per-project labels while leaving every other counter untouched.

> **Scope**: This runbook targets **bare-metal + systemd only**. Kubernetes
> and container deployment are explicitly out of scope (see
> `specs/001-multi-user-gpu-util/spec.md` Clarification Q2). Upstream's Helm
> chart and DaemonSet templates continue to exist but are not updated by
> this feature.

## Contents

1. [Prerequisites](#1-prerequisites)
2. [Build](#2-build)
3. [Install](#3-install)
4. [Configure](#4-configure)
5. [Enable the service](#5-enable-the-service)
6. [Verify](#6-verify)
7. [Rollback](#7-rollback)
8. [Troubleshooting](#8-troubleshooting)

## 1. Prerequisites

- Linux host (Ubuntu 22.04/24.04 or RHEL 9, kernel ≥ 5.10).
- NVIDIA driver installed and working (`nvidia-smi` prints your GPUs).
- **NVIDIA DCGM 4.x** installed and `nvidia-dcgm.service` active
  (DCGM 3.x is not compatible with the pinned upstream
  `github.com/NVIDIA/go-dcgm`):
  ```bash
  sudo apt install datacenter-gpu-manager-4-cuda12
  sudo systemctl enable --now nvidia-dcgm
  dcgmi discovery -l
  ```
- Go 1.24+ (build host only, not required on the target).
- `root` privileges on the target host — required so the exporter can read
  `/proc/<PID>/environ` of every user.

## 2. Build

```bash
git clone https://github.com/<your-fork>/dcgm-exporter.git
cd dcgm-exporter
git checkout 001-multi-user-gpu-util
make binary
# ./cmd/dcgm-exporter/dcgm-exporter is produced
```

## 3. Install

```bash
sudo make install
```

`make install` performs the following (see the `install:` target in the
repository root `Makefile`):

| Source | Destination | Notes |
|--------|-------------|-------|
| `cmd/dcgm-exporter/dcgm-exporter` | `/usr/bin/dcgm-exporter` | overwritten every install |
| `etc/default-counters.csv` | `/etc/dcgm-exporter/default-counters.csv` | overwritten every install |
| `config.yaml` (repo root) | `/etc/dcgm-exporter/config.yaml` | **only if absent** — operator edits preserved |
| `packaging/config-files/systemd/nvidia-dcgm-exporter.service` | `/etc/systemd/system/dcgm-exporter.service` | overwritten every install |

## 4. Configure

Edit `/etc/dcgm-exporter/config.yaml`. Its schema is described in
[`specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md`](../specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md).
Only two top-level sections are allowed: `labels` and `server`. `config.yaml`
never selects which DCGM metrics to collect — that remains driven by the
existing `default-counters.csv`.

Minimal example (also shipped as the repository-root `config.yaml`):

```yaml
labels:
  static:
    - name: STUDIO
      value: "ai-lab"          # empty -> read $STUDIO env -> "unknown"

  env:
    - name: PROJECT             # env_var defaults to name
    # - name: EXPERIMENT
    #   env_var: EXPERIMENT_NAME

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
```

### Rules worth remembering

- Top-level unknown keys (e.g. `kubernetes:`, `log_level:`) cause the service
  to **refuse to start** with a field-path error.
- Label names must match `^[a-zA-Z_][a-zA-Z0-9_]*$` and must not clash with
  system-reserved names (`USER`, `gpu`, `UUID`, `device`, `modelName`,
  `Hostname`, plus all `container/namespace/pod/exported_*` names).
- `labels.static[].value` empty → falls back to `os.Getenv(name)` → else
  literally `"unknown"`. Decided at startup; changing it requires
  `systemctl restart dcgm-exporter`.
- Missing `config.yaml` at `/etc/dcgm-exporter/config.yaml` (and no
  `--config <path>` override) is a **hard failure** — the service logs
  `config.yaml not found at <path>` and exits non-zero.

## 5. Enable the service

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dcgm-exporter
sudo systemctl status dcgm-exporter --no-pager
# Expected: active (running)
```

Logs go to `/var/log/dcgm-exporter.log` (via `StandardOutput=append:` /
`StandardError=append:` in the unit file); use
`journalctl -u dcgm-exporter -n 200` for the systemd-owned view.

## 6. Verify

### 6.1 Idle GPU

With no compute workload running on GPU 0:

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
# DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="none",STUDIO="ai-lab",PROJECT="none"} 0
```

A placeholder record is emitted to preserve time-series continuity.

### 6.2 Single-user workload (User Story 1)

Start any CUDA workload with `PROJECT` exported. For example, compile the
demo kernel in `specs/001-multi-user-gpu-util/validation-log.md`:

```bash
PROJECT=llm-training ./your-training-program &
```

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
# DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="<you>",STUDIO="ai-lab",PROJECT="llm-training"} <util>
```

### 6.3 Multi-user weighted split (User Story 2)

With multiple concurrent processes on the same GPU:

```bash
# shell 1 (as root)
PROJECT=proj-a ./your-training-program &
PROJECT=proj-a ./your-training-program &
PROJECT=proj-a ./your-training-program &

# shell 2 (as a different user)
sudo -u ubuntu env PROJECT=proj-b ./your-training-program &
```

`DCGM_FI_DEV_GPU_UTIL` emits one row per `(USER, env-values)` group:

```
DCGM_FI_DEV_GPU_UTIL{...,USER="root",   PROJECT="proj-a"} 23
DCGM_FI_DEV_GPU_UTIL{...,USER="ubuntu", PROJECT="proj-b"}  8
```

The sum of the two values equals the true GPU utilization (e.g. 31 = 23 + 8),
allocated per-process-count with closure compensation so rounding never
drops a percentage point on the floor.

### 6.4 Non-UTIL counters are unchanged (SC-007)

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_TEMP'
# DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-...",...} 41
```

No `USER` / `STUDIO` / `PROJECT` labels — byte-identical to upstream
dcgm-exporter output.

### 6.5 Crash auto-heal

```bash
sudo systemctl kill --signal=SIGKILL dcgm-exporter
sudo systemctl status dcgm-exporter --no-pager
# Back to 'active (running)' within ~3s
```

### 6.6 Configuration changes apply on restart

```bash
sudo sed -i 's/ai-lab/ai-lab-phase2/' /etc/dcgm-exporter/config.yaml
sudo systemctl restart dcgm-exporter
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL{' | head -1
# STUDIO now shows ai-lab-phase2
```

## 7. Rollback

Two rollback paths, depending on what you want to revert:

### Revert the label customization only, keep the binary

Make labels revert to the minimum default (`STUDIO` static + `PROJECT` env)
without altering behaviour otherwise:

```bash
sudo tee /etc/dcgm-exporter/config.yaml >/dev/null <<'YAML'
labels:
  static:
    - name: STUDIO
      value: ""
  env:
    - name: PROJECT
server:
  port: ":9400"
YAML
sudo systemctl restart dcgm-exporter
```

### Revert to upstream dcgm-exporter entirely

Stop the enhanced build, swap the binary, remove the custom unit:

```bash
sudo systemctl disable --now dcgm-exporter
sudo make uninstall     # removes binary + unit, keeps /etc/dcgm-exporter/
# …install the upstream dcgm-exporter binary and its own unit…
```

`make uninstall` intentionally preserves `/etc/dcgm-exporter/` so your
operator edits survive.

## 8. Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| `journalctl -u dcgm-exporter` shows `config.yaml not found at /etc/dcgm-exporter/config.yaml` | `make install` ran but the config was not placed, or it was deleted. Restore or write a fresh copy — see [Configure](#4-configure). |
| Log shows `libdcgm.so.4 library was not found. Install Data Center GPU Manager (DCGM)` | DCGM 4 is not installed, or DCGM 3 is installed instead. Upgrade: `sudo apt install datacenter-gpu-manager-4-cuda12`. |
| All `USER` come out as `uid:<n>` | That UID has no matching system account. If you expected a real username, check `/etc/passwd` / `sssd` / LDAP resolution on the host. |
| All env-driven labels come out as `none` | Exporter is not running as root, or the process really did not `export` that variable. Verify with `sudo cat /proc/<PID>/environ \| xargs -0 -n1 \| grep <KEY>=`. |
| Start fails with `unknown field "kubernetes"` (or similar) | `config.yaml` only accepts `labels:` and `server:` — remove the unknown key. |
| Start fails with `"USER" is a system-reserved label name` | Remove the custom label; `USER` is always injected by the exporter. |
| An env label suddenly shows `other` for many values | That label's value set exceeded 128 distinct entries in one scrape cycle (per-cycle cardinality cap, Clarification Q5). Excess values are merged into `other`. Investigate whether your users are using runtime-varied `PROJECT` strings (timestamps, UUIDs) and ask them to adopt a stable scheme. |
| Sum of UTIL splits disagrees with `nvidia-smi` UTIL by more than a couple of points | Graphics processes are not counted (only NVML compute processes participate). If a large graphics workload is running, the exporter's total will legitimately lag `nvidia-smi`. Also check `/proc/<pid>/environ` permissions on the offending PIDs. |
| Service flaps (start → fail → restart) in a tight loop | `StartLimitBurst=5 / StartLimitIntervalSec=30s` will eventually stop trying. Check the log for the underlying error (usually a config-validation failure). |
