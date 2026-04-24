# Validation Log: DCGM_FI_DEV_GPU_UTIL Multi-User Attribution

**Feature**: `001-multi-user-gpu-util`
**Phase**: US1 MVP (Setup + Foundational + User Story 1)
**Date**: 2026-04-24
**Platform**: NVIDIA Tesla T4, driver 570.158.01, CUDA 12.8, DCGM 4.5.3, Ubuntu 24.04

## Evidence

### Idle GPU (SC-006, FR-005)

Baseline with no compute processes:

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-f68e64a6-3221-aea9-ad86-ac7a540dbea7",...,
    PROJECT="none",STUDIO="ai-lab",USER="none"} 0
```

`USER=none`, env label `PROJECT=none`, static label `STUDIO=ai-lab` (resolved
from `config.yaml`).

### Single-user workload (US1 Acceptance Scenario #1)

A root-owned CUDA workload `/tmp/hold` running with `PROJECT=llm-training`:

```
$ nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv
pid, process_name, used_gpu_memory [MiB]
154642, /tmp/hold, 102 MiB

$ curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-f68e64a6-...",...,
    PROJECT="llm-training",STUDIO="ai-lab",USER="root"} 8
```

- `USER=root` — UID 0 resolved via `os/user.LookupId`.
- `PROJECT=llm-training` — read from `/proc/154642/environ`.
- `STUDIO=ai-lab` — startup-resolved from `config.yaml` static section.
- Non-zero GPU utilization observed (`8`).

### Non-UTIL counters untouched (SC-007, FR-001)

During the same workload:

```
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-f68e64a6-...",...,DCGM_FI_DRIVER_VERSION="570.158.01"} 41
```

No `USER`, `STUDIO`, or `PROJECT` labels added; byte-identical to upstream
dcgm-exporter output.

## Unit Test Summary

| Package | Tests | Result |
|---------|-------|--------|
| `internal/pkg/appconfig` | 15 | ✅ pass |
| `internal/pkg/transformation` | 50 (14 new) | ✅ pass |
| `pkg/cmd` | existing | ✅ pass |

## Deviations / Notes

- **NVML auto-init**: Upstream code only initialises NVML when
  `--kubernetes` is set (`pkg/cmd/app.go:487`). For this feature, we now
  also initialise when `config.yaml` declares labels (bare-metal path). The
  condition is widened to `Kubernetes || len(Labels.Static)>0 || len(Labels.Env)>0`.
- **Log line drift**: The single log line
  `NVML provider successfully initialized for Kubernetes MIG support` has been
  generalised to `NVML provider successfully initialized` since it now
  supports both paths.
- The installed DCGM version must be 4.x (`libdcgm.so.4`). DCGM 3.x is not
  compatible with the upstream `github.com/NVIDIA/go-dcgm` version pinned in
  `go.mod`.

## Not Yet Validated (Deferred to Later Phases)

- US2: Multi-user weighted split (tasks T024–T029).
- US3: systemd unit + package install (tasks T031–T035).
- Polish: self-health metrics, cross-version benchmark, documentation runbook
  (tasks T036–T043).
