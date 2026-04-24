# Validation Log: DCGM_FI_DEV_GPU_UTIL Multi-User Attribution

**Feature**: `001-multi-user-gpu-util`
**Phase**: US1 MVP + US2 weighted split (Setup + Foundational + User Stories 1 and 2)
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

### Multi-user weighted split (US2 Acceptance Scenario #1)

Four concurrent CUDA workloads: 3 × root(PROJECT=proj-a), 1 × ubuntu(PROJECT=proj-b):

```
$ nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv
pid, process_name, used_gpu_memory [MiB]
161923, /tmp/hold, 102 MiB     # root  / proj-a
161924, /tmp/hold, 102 MiB     # root  / proj-a
161925, /tmp/hold, 102 MiB     # root  / proj-a
161932, /tmp/hold, 102 MiB     # ubuntu/ proj-b

$ curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
DCGM_FI_DEV_GPU_UTIL{...,PROJECT="proj-a",STUDIO="ai-lab",USER="root"}   23
DCGM_FI_DEV_GPU_UTIL{...,PROJECT="proj-b",STUDIO="ai-lab",USER="ubuntu"}  8
```

- Two output rows, one per `(USER, PROJECT)` group.
- Sum = 23 + 8 = 31, the GPU's true utilization (verified via dcgmi).
- Distribution ratio 23:8 ≈ 74%:26%, matching the 3:1 process split
  (expected 75%:25%; single-integer rounding residue absorbed by the
  closure compensation on the last group in canonical sort order).
- `USER=root` for UID 0; `USER=ubuntu` for UID 1000.
- SC-002 invariant: `sum(splits) == util_total` — satisfied exactly (tighter
  than the ≤1 tolerance required by spec).

### Non-UTIL counters untouched (SC-007, FR-001)

During every workload (single- and multi-user):

```
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-f68e64a6-...",...,DCGM_FI_DRIVER_VERSION="570.158.01"} 41
```

No `USER`, `STUDIO`, or `PROJECT` labels added; byte-identical to upstream
dcgm-exporter output.

## Unit Test Summary

| Package | Tests | Result |
|---------|-------|--------|
| `internal/pkg/appconfig` | 15 | ✅ pass |
| `internal/pkg/transformation` | 50+ (30 new for this feature) | ✅ pass |
| `pkg/cmd` | existing + adapted | ✅ pass |

New US2 tests worth highlighting:
- `TestBareMetalUserMapper_US2_WeightedSplit_AliceBob` — 3:1 ratio → {60, 20}.
- `TestBareMetalUserMapper_US2_ThreeEqualGroupsClosure` — {33, 33, 34}.
- `TestBareMetalUserMapper_US2_FuzzInvariantSumEqualsTotal` — 100 random
  iterations: `sum(splits) == total` exactly.
- `TestBareMetalUserMapper_US2_MultiEnvAggregateKey` — aggregation across
  PROJECT + EXPERIMENT labels.
- `TestBareMetalUserMapper_US2_EnvCardinalityCap` — 200 distinct PROJECT
  values → 128 survive + `other` bucket.
- `TestSplitUtil` / `TestCapEnvCardinality_*` — algorithmic unit coverage.

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
- Invariant breaches (sum ≠ total) are NOT published as business metrics; a
  package-level atomic counter (`transformation.InvariantBreaches`) will be
  exposed via the exporter self-health series in T037.

## Not Yet Validated (Deferred to Later Phases)

- US3: systemd unit + package install (tasks T031–T035).
- Polish: self-health metrics, cross-version benchmark, documentation runbook
  (tasks T036–T043).

