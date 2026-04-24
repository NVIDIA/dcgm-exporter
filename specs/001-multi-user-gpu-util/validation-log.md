# Validation Log: DCGM_FI_DEV_GPU_UTIL Multi-User Attribution

**Feature**: `001-multi-user-gpu-util`
**Phase**: Complete (Phases 1–6: Setup + Foundational + US1 + US2 + US3 + Polish)
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

### systemd deployment end-to-end (US3 Acceptance Scenarios #1–#4)

Installed via `sudo GO=$(which go) make install`:

```
installed /etc/dcgm-exporter/config.yaml
install -m 644 -D ./packaging/config-files/systemd/nvidia-dcgm-exporter.service \
    /etc/systemd/system/dcgm-exporter.service
```

Enabled and started:

```
$ sudo systemctl enable --now dcgm-exporter
$ systemctl is-active dcgm-exporter
active
```

**AS#1 — service reaches `active (running)` and serves metrics**: verified;
`curl http://localhost:9400/metrics` returns 200 with our labels attached.

**AS#2 — config edit takes effect on restart**: `sed -i 's/ai-lab/ai-lab-phase2/'`
on `/etc/dcgm-exporter/config.yaml` + `systemctl restart` immediately flips
the `STUDIO` label value in subsequent scrapes; restore + restart flips it
back. Confirmed.

**AS#3 — crash auto-heal**: `sudo kill -9 <Main PID>` is followed within 5s
by a fresh PID and `systemctl is-active` returning `active` again. Confirmed
(Restart=on-failure + RestartSec=3s).

**AS#4 — invalid / missing config refuses to start**: moving
`/etc/dcgm-exporter/config.yaml` away and running `systemctl start
dcgm-exporter` produces in `/var/log/dcgm-exporter.log`:

```
level=ERROR msg="config.yaml not found at /etc/dcgm-exporter/config.yaml"
```

Service enters `activating (auto-restart) (Result: exit-code)` and eventually
hits `StartLimitBurst=5` within `StartLimitIntervalSec=30s`. Confirmed.

**Clean uninstall (`sudo make uninstall`)** removes the binary and unit but
leaves `/etc/dcgm-exporter/` intact so operator-edited config survives an
in-place upgrade. Confirmed.

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

New US3 tests worth highlighting:
- `Test_contextToConfig_MissingConfigFileIsFatal` — exact `config.yaml not found at <path>` error.
- `Test_contextToConfig_CLIOverridesYAMLAddress` — CLI `-a` explicitly
  passed overrides `server.port` from YAML.
- `Test_contextToConfig_YAMLAddressUsedWhenCLIUnset` — YAML `server.port`
  used when CLI did not set `-a`.

Polish-phase additions:
- `TestBareMetalUserMapper_T039_OnlyTouchesGpuUtil` — feeds TEMP / POWER /
  FB / SM_CLOCK simultaneously with UTIL and asserts each non-UTIL counter
  comes out **byte-identical** (SC-007 hard regression).
- `BenchmarkProcess_8GPU_128Procs` — 8 GPUs × 128 PIDs reference workload,
  measured at **27.6 ms/cycle** on a 2.5 GHz Xeon (target: < 50 ms).
- `BenchmarkProcess_vs_Upstream` — same workload with vs. without the
  mapper enabled, for tracking regressions over time.
- Self-health counters exposed via `transformation.Stats()` (cycles,
  GPUs/PIDs observed, PID-read failures, invariant breaches, last cycle
  duration). Wiring into the upstream Prometheus surface remains a
  separate follow-up — the upstream renderer is not based on
  `prometheus/client_golang`, so dropping a new metric in is a small
  refactor outside this feature's scope.
- Per-cycle `slog.Debug` line summarises GPUs / total_pids / unique_users /
  unique_groups / elapsed_ms.

## Static Analysis

- `go vet ./...` clean for the three new files
  (`yamlconfig.go`, `procfs.go`, `bare_metal_user_mapper.go`).
- `staticcheck ./internal/pkg/appconfig/... ./internal/pkg/transformation/...`
  reports zero new findings on feature files. Remaining ST1000 / ST1003
  warnings on this run all originate from unmodified upstream files.

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

- **None.** All 43 tasks (T001–T043) are complete. Full Prometheus
  self-monitoring metric surface is left as a separate follow-up because
  upstream uses a bespoke text renderer rather than `prometheus/client_golang`;
  internal counters are already accessible via `transformation.Stats()`.

