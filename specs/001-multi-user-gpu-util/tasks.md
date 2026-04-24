---

description: "Task list for feature 001-multi-user-gpu-util (post-clarify revision)"
---

# Tasks: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Input**: Design documents from `/specs/001-multi-user-gpu-util/`
**Prerequisites**: plan.md (required), spec.md (required, incl. Clarifications 2026-04-24), research.md, data-model.md, contracts/, quickstart.md

**Tests**: 本特性新增的核心模块（YAML 加载、procfs 解析、标签解析、加权分摊算法、新 transformer、启动时标签名冲突校验）一律附单元测试（非 TDD，但必做）；端到端走 quickstart.md。

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1 = 单用户归属；US2 = 多用户加权分摊；US3 = systemd 部署
- All file paths are repository-relative (root = `/root/go/src/dcgm-exporter/`)

## Path Conventions

- Go 单体项目：源码在 `cmd/`、`pkg/`、`internal/pkg/`；测试与被测源文件同目录。
- 打包与部署资产：`packaging/`、`examples/`、`docs/`、仓库根 `config.yaml`。

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: 准备依赖、目录骨架与示例占位，但不触及业务逻辑。

- [X] T001 Confirm Go toolchain ≥ 1.24 at repository root by running `go version`; if mismatch, abort and ask operator to upgrade. No code change.
- [X] T002 [P] Promote `gopkg.in/yaml.v3` from indirect to direct dependency: add an explicit `import "gopkg.in/yaml.v3"` in a new stub file `internal/pkg/appconfig/yaml_import.go`; run `go mod tidy`; commit updated `go.mod`/`go.sum`.
- [X] T003 [P] Create empty package files with only package headers (no logic) to reserve layout: `internal/pkg/appconfig/yamlconfig.go`, `internal/pkg/transformation/bare_metal_user_mapper.go`, `internal/pkg/transformation/procfs.go`.
- [X] T004 [P] Create deployment asset skeletons to be populated by later tasks: `docs/bare-metal-deployment.md` (empty file with H1 only). The canonical sample `config.yaml` already exists at repository root and is the authoritative template — reference it, do not duplicate it elsewhere.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: 配置结构、YAML 加载、命名/冲突校验、`--config` flag、CLI 覆盖规则；US1/US2/US3 共同依赖。

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T005 Extend `internal/pkg/appconfig/types.go` per `data-model.md §1`: add `LabelsConfig`, `StaticLabel`, `EnvLabel`, `ServerConfig`, and a feature-level `Labels LabelsConfig` + `Server ServerConfig` on `Config` — with `yaml:"..."` tags strictly matching the schema in `contracts/config.yaml.schema.md`. Do NOT add `collectors_file`, `collect_interval_ms`, `log_level`, or `kubernetes` fields.
- [X] T006 Add runtime-derived constants in `internal/pkg/appconfig/const.go`: `UserNameCacheTTL = 300*time.Second`, `EnvValueMaxLen = 64`, `MaxEnvCardinalityPerCycle = 128`, `FallbackNone = "none"`, `FallbackUnknown = "unknown"`, `FallbackOther = "other"`, default server port `":9400"`, default timeouts. Also declare `SystemReservedLabelNames = map[string]struct{}{ "USER": {}, "gpu": {}, "UUID": {}, "device": {}, "modelName": {}, "Hostname": {}, "container": {}, "namespace": {}, "pod": {}, "exported_container": {}, "exported_namespace": {}, "exported_pod": {}, "exported_job": {} }`.
- [X] T007 Implement `LoadYAMLConfig(path string) (*Config, error)` in `internal/pkg/appconfig/yamlconfig.go`: open file, `yaml.NewDecoder(f).KnownFields(true).Decode(&cfg)`; if file does not exist return a typed sentinel `ErrConfigNotFound` wrapping the path so callers can produce the exact Q4 log line `"config.yaml not found at <path>"`.
- [X] T008 Implement `func (c *Config) Validate() error` in the same file covering every rule from `contracts/config.yaml.schema.md`: (a) every label `name` matches `^[a-zA-Z_][a-zA-Z0-9_]*$`; (b) no `name` is in `SystemReservedLabelNames`; (c) names are unique across `Labels.Static` ∪ `Labels.Env`; (d) each `EnvLabel.EnvVar` (or `Name` when EnvVar empty) matches `^[A-Za-z_][A-Za-z0-9_]*$`; (e) `Server.Port` parses via `net.SplitHostPort` with port ∈ [1,65535]; (f) all timeouts > 0.
- [X] T009 Implement `func (c *Config) ApplyDefaults()` in the same file: if `Labels.Static` and `Labels.Env` are both empty/nil, synthesize `Static = [{Name:"STUDIO", Value:""}]` and `Env = [{Name:"PROJECT", EnvVar:"PROJECT"}]` (FR-012). Fill missing `Server.*` values with the constants from T006. For each `EnvLabel` with empty `EnvVar`, set `EnvVar = Name`. For each `StaticLabel` whose `Value` is empty, resolve the startup fallback chain: `os.Getenv(Name)` → `FallbackUnknown` (FR-003). Order: `ApplyDefaults()` is called inside `LoadYAMLConfig` **after** decode but **before** `Validate`.
- [X] T010 [P] Unit tests for the YAML pipeline in `internal/pkg/appconfig/yamlconfig_test.go`: table-driven cases using `gopkg.in/yaml.v3` fixtures embedded inline — (a) happy path mirroring repo-root `config.yaml`; (b) file missing → `ErrConfigNotFound`; (c) malformed YAML → error with line info; (d) top-level unknown key `kubernetes:` → error mentioning the key; (e) nested unknown key `labels.unknown:` → error; (f) every Validate rule failing individually (bad label name char, reserved name `USER`, duplicate name across static+env, bad env_var, bad port, zero timeout); (g) empty `labels` → defaults synthesized to `[STUDIO]` + `[PROJECT]`; (h) `StaticLabel.Value` fallback to `os.Getenv` then to `"unknown"` (use `t.Setenv`).
- [X] T011 Register new `--config <path>` CLI flag in `pkg/cmd/app.go` alongside existing flags. In the `buildConfig` closure, resolution order (Clarification Q4): (1) if user passed `--config` → that path; (2) else default `/etc/dcgm-exporter/config.yaml`; (3) call `appconfig.LoadYAMLConfig`; on `ErrConfigNotFound` print `"config.yaml not found at <path>"` to stderr/slog and return non-zero exit. Then apply CLI overrides only for flags where `c.IsSet(...)` is true: specifically `-a/--address` overrides `cfg.Server.Port`. Other upstream flags (`-f/--collectors`, `--collect-interval`, etc.) are orthogonal — leave them as-is and not mirrored in YAML.
- [X] T012 Wire the resolved `*Config` into the existing transformer pipeline registration (`internal/pkg/transformation/transformer.go`). For now, register **no new transformer** — leave an explicitly commented placeholder `// BareMetalUserMapper will be registered in US1 (T021)`. Ensure `go build ./...` + `go test ./...` still pass (pre-existing behavior preserved when `--config` points at a valid file with default labels).
- [X] T013 [P] Flesh out `docs/bare-metal-deployment.md` with a "Prerequisites" + "Configure" section matching `quickstart.md §1–3`; leave placeholders for "Install", "Verify", "Troubleshooting" (filled in US3).

**Checkpoint**: Foundation ready — `dcgm-exporter --config <path>` starts successfully; with default `labels:` ({STUDIO, PROJECT}) it should behave identically to pre-feature behavior (no transformer registered yet).

---

## Phase 3: User Story 1 - 独占 GPU 的用户利用率归属 (Priority: P1) 🎯 MVP

**Goal**: 单进程独占 GPU 时，`DCGM_FI_DEV_GPU_UTIL` 额外携带 `USER` + `config.yaml` 声明的所有 static/env 标签；其他指标标签完全不变。

**Independent Test**: 以 `alice` 运行设置了 `PROJECT=llm-training` 的 CUDA 进程；使用 repo-root `config.yaml`（`static: [STUDIO=ai-lab]` + `env: [PROJECT]`）启动；查询指标端点应返回 `DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="alice",STUDIO="ai-lab",PROJECT="llm-training"}`；同时 `DCGM_FI_DEV_GPU_TEMP` 等指标标签未变。

### Implementation for User Story 1

- [X] T014 [P] [US1] Implement `/proc` reader abstraction in `internal/pkg/transformation/procfs.go`: define `type ProcFS interface { ReadStatus(pid uint32) (uid uint32, err error); ReadEnviron(pid uint32, key string) (value string, err error) }` and a default implementation `type realProcFS struct { root string /* "/proc" */ }`. Parse `/proc/<pid>/status` scanning for a line starting with `Uid:`. Parse `/proc/<pid>/environ` as NUL-separated tokens; find token prefixed with `key + "="` and return the suffix (handle `=` inside value correctly, e.g. `PROJECT=a=b` → `a=b`).
- [X] T015 [P] [US1] Unit tests for `procfs.go` in `internal/pkg/transformation/procfs_test.go`: create a temp directory mimicking `/proc/<pid>/{status,environ}`, point `realProcFS.root` at it; cases: (a) normal parse; (b) status missing → error; (c) environ missing → error; (d) permission denied simulated by `chmod 0000`; (e) env key not present → empty + no error (so callers can apply `FallbackNone`); (f) env value with `=` inside.
- [X] T016 [US1] Implement UID → username resolver in `internal/pkg/transformation/bare_metal_user_mapper.go`: function `resolveUsername(uid uint32, cache *userCache) string` using `os/user.LookupId(strconv.Itoa(int(uid)))`; on error returns `fmt.Sprintf("uid:%d", uid)`. The `userCache` wraps a `sync.Map` keyed by uid holding `{name string, expiresAt time.Time}`; TTL from `appconfig.UserNameCacheTTL`.
- [X] T017 [US1] Implement env value sanitizer in `internal/pkg/transformation/bare_metal_user_mapper.go`: function `sanitizeEnvValue(raw string) string` — if empty return `appconfig.FallbackNone`; else replace every rune not in `[A-Za-z0-9_.-]` with `_`; truncate to `appconfig.EnvValueMaxLen` bytes (respecting UTF-8 byte boundary, fallback to 64 bytes raw slice if decode fails).
- [X] T018 [US1] Implement `bareMetalUserMapper` struct in `internal/pkg/transformation/bare_metal_user_mapper.go` with constructor `NewBareMetalUserMapper(cfg *appconfig.Config, nvml nvmlprovider.NVML, procFS ProcFS) *bareMetalUserMapper`. Store references only; do not precompute anything per-cycle-specific.
- [X] T019 [US1] Implement `func (m *bareMetalUserMapper) Process(metrics collector.MetricsByCounter, _ devicemonitoring.Info) error` — US1 scope: for each GPU, enumerate compute PIDs via `m.nvml.GetDeviceProcessMemory(gpuUUID)`; for each PID build a `GPUProcess`: resolve `Username` via T016, read each `cfg.Labels.Env[j].EnvVar` via `procFS.ReadEnviron` then sanitize via T017. Only handle the **single-process** branch here: if a GPU has exactly 1 process, inject `Labels["USER"]`, then for each `StaticLabel` inject the precomputed startup value (from T009), then for each `EnvLabel` inject that process's sanitized env value — mutating the single `DCGM_FI_DEV_GPU_UTIL` metric in-place for that GPU. Explicitly skip any counter whose `FieldName != "DCGM_FI_DEV_GPU_UTIL"`. Leave multi-process handling to US2 (T022).
- [X] T020 [US1] Handle the GPU-idle case in the same `Process` function: when a GPU has zero compute processes, overwrite (not duplicate) the `DCGM_FI_DEV_GPU_UTIL` metric: set `Labels["USER"]="none"`, each static label to its startup value, each env label to `"none"`, `Value="0"`.
- [X] T021 [US1] Register `bareMetalUserMapper` at the end of the transformer chain in `internal/pkg/transformation/transformer.go` whenever `cfg` has been successfully loaded (Clarification Q4: successful load == feature enabled; there is no separate on/off switch). Leave existing Kubernetes / HPC / DRA transformers untouched and unconditional (they'll naturally bypass on a bare-metal host with no K8s env / no HPC dir).
- [X] T022 [US1] Unit tests in `internal/pkg/transformation/bare_metal_user_mapper_test.go` for US1 scenarios: (a) single-process alice/llm-training → `USER=alice`, `STUDIO=<startup>`, `PROJECT=llm-training`, value preserved; (b) UID not in passwd → `USER="uid:<n>"`; (c) environ unreadable → `PROJECT="none"`; (d) GPU idle → `USER="none"`, `PROJECT="none"`, value=0; (e) non-UTIL counter passed in (e.g., `DCGM_FI_DEV_GPU_TEMP`) → labels untouched byte-for-byte; (f) static label with empty `Value` and env var fallback path verified end-to-end (use `t.Setenv("STUDIO", "dev")` and expect `STUDIO="dev"`). Use fake `ProcFS` and fake NVML returning deterministic PID lists.
- [X] T023 [US1] Verify or update the repository-root `config.yaml` content to match the canonical US1 demo values (`STUDIO=ai-lab`, `PROJECT` env label, `server.port: ":9400"`). Do not add unrelated fields. Update `docs/bare-metal-deployment.md` "Configure" section to point operators at this file.

**Checkpoint**: User Story 1 complete — single-user attribution works end-to-end on a bare-metal host with one process per GPU; no other metrics affected.

---

## Phase 4: User Story 2 - 多用户共享 GPU 按进程数加权分摊 (Priority: P1)

**Goal**: 同一 GPU 多进程时，按「聚合键 = `USER` + `labels.env[]` 所有取值元组」分组并按进程数加权拆分 `DCGM_FI_DEV_GPU_UTIL`；分摊之和严格等于原 UTIL。

**Independent Test**: 构造 `alice` 3 进程（PROJECT=proj-a）、`bob` 1 进程（PROJECT=proj-b），GPU_UTIL=80；预期两条记录 60 + 20 = 80；另构造多 env 标签场景验证多维聚合键正确工作。

### Implementation for User Story 2

- [ ] T024 [US2] Add group key + weight split types in `internal/pkg/transformation/bare_metal_user_mapper.go`: `type groupKey struct { Username string; EnvVals []string }` (with a `String()` method joining with `"\x1f"` for map keying and deterministic sort); `type gpuProcessGroup struct { GPUUUID string; Key groupKey; ProcessCnt uint; Weight float64 }`.
- [ ] T025 [US2] Implement per-cycle env cardinality cap helper `capEnvCardinality(groups []gpuProcessGroup, envLabelCount int, max int) []gpuProcessGroup` in the same file. For each env label index `i`: collect the unique values; if count > `max (=128)`, keep the lexicographically smallest `max` and rewrite the i-th value of every remaining group to `appconfig.FallbackOther`. After rewriting, re-merge groups whose keys became identical (sum their `ProcessCnt`, recompute `Weight`). Preserve stable sort after cap.
- [ ] T026 [US2] Implement pure helper `splitUtil(total int, groups []gpuProcessGroup) []int` in the same file: produce per-group integer values via `round(Weight * total)` for the first `N-1` groups; compute the final group's value as `total - sum(previous)` to close rounding error exactly. Input `groups` must be pre-sorted by `Key` for determinism.
- [ ] T027 [US2] Replace T019's single-process branch in `Process()` with the full split logic: group PIDs by `groupKey`, apply `capEnvCardinality`, stable-sort by `Key`, compute `splitUtil`, then for each group clone the original `Metric` via `utils.DeepCopy` (same pattern as `internal/pkg/transformation/hpc.go:95-130`), set each clone's `Labels` for USER/static/env accordingly, set its `Value`, and replace the original GPU's metric list in `metrics[counter]` with the clones.
- [ ] T028 [US2] Guard the "sum equals total" invariant inside `Process()` with an internal const `debugInvariants = true` for the first release. On violation: (1) log at error level with the full context (`gpu_uuid`, `util_total`, `split_sum`, per-group counts); (2) increment a dedicated self-health counter `dcgm_exporter_bare_metal_mapper_invariant_breaches_total` registered in T037 so that the breakage is observable in Prometheus via the exporter's own self-monitoring surface. **Do not** emit a business-labelled metric (no `USER="invariant_error"` etc.) — business metrics must not encode error channels (keeps `contracts/metrics.contract.md` surface stable).
- [ ] T029 [US2] Append US2 tests to `internal/pkg/transformation/bare_metal_user_mapper_test.go`: (a) `alice×3 proj-a + bob×1 proj-b @ UTIL=80 → {60, 20}`; (b) `alice/proj-a ×1 + alice/proj-b ×1 @ UTIL=80 → {40, 40}`; (c) `3 users equal @ UTIL=100 → {33, 33, 34}` (closure falls on last by sort order); (d) total=0 with multiple processes → all zeros; (e) fuzz: 100 iterations of random inputs with `math/rand.New(rand.NewSource(42))` — assert `sum(splits) == total` exactly; (f) multi-env aggregate key: `PROJECT` + `EXPERIMENT` two env labels, verify groups use both dimensions; (g) env cardinality cap: feed 200 distinct PROJECT values — verify lexicographically first 128 preserved, rest rewritten to `"other"` and merged.
- [ ] T030 [US2] Update `docs/bare-metal-deployment.md` "Verify" section to reflect Quickstart §5.3 multi-user expectation; no schema change.

**Checkpoint**: User Story 2 complete — multi-user attribution with exact sum-preserving split is live; env cardinality protected.

---

## Phase 5: User Story 3 - systemd service 部署 (Priority: P1)

**Goal**: 以 `systemctl enable --now` 在裸金属 GPU 服务器上跑起服务；config.yaml 驱动；崩溃自愈；`systemctl restart` 生效配置改动。

**Independent Test**: 按 `quickstart.md §1–5` 操作，10 分钟内完成安装；`systemctl status` 为 `active (running)`；`systemctl kill -s KILL` 后 10s 内回到 active；改 `config.yaml` 的 `static[STUDIO].value` 后 restart，指标上的 `STUDIO` 取新值；故意移走 `config.yaml` 后启动应硬退出并打印 `config.yaml not found at /etc/dcgm-exporter/config.yaml`。

### Implementation for User Story 3

- [ ] T031 [US3] Replace contents of `packaging/config-files/systemd/nvidia-dcgm-exporter.service` with the contract from `specs/001-multi-user-gpu-util/contracts/dcgm-exporter.service`. Keep the file name (packagers reference it).
- [ ] T032 [US3] Update `/root/go/src/dcgm-exporter/Makefile` `install` target (~lines 33-60): add steps that (a) copy repo-root `config.yaml` to `/etc/dcgm-exporter/config.yaml` only if the destination does not exist (preserve operator edits); (b) copy the systemd unit to `/etc/systemd/system/dcgm-exporter.service`. Add a corresponding `uninstall` target that removes binary, unit, but **NOT** `/etc/dcgm-exporter/config.yaml` (to avoid wiping ops edits).
- [ ] T033 [US3] Add integration test `pkg/cmd/app_test.go::TestApp_ConfigNotFound`: invoke the CLI with `--config /tmp/does-not-exist.yaml`; assert `app.Run` returns a non-nil error whose message contains `config.yaml not found at /tmp/does-not-exist.yaml` (covers FR-011 / Clarification Q4).
- [ ] T034 [US3] Add integration test `pkg/cmd/app_test.go::TestApp_CLIOverridesYAMLAddress`: write a temp `config.yaml` with `server.port: ":19400"`; run CLI with `--config <tmp>` + `-a :19500`; assert the resolved effective port is `:19500` (covers Clarification Q5 & FR-011 override chain).
- [ ] T035 [US3] Expand `docs/bare-metal-deployment.md` into a full runbook matching `quickstart.md`: "Prerequisites", "Build", "Install", "Configure", "Enable service", "Verify (idle / single-user / multi-user)", "Rollback", "Troubleshooting". Cross-reference `contracts/config.yaml.schema.md` for field details. Explicitly state "K8s/containerized deployment is out of scope" near the top.

**Checkpoint**: All three user stories independently functional; deployment path validated.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: 可观测性、性能、文档清理、发布就绪。

- [ ] T036 [P] Add per-cycle diagnostic logs (slog) in `internal/pkg/transformation/bare_metal_user_mapper.go`: at `Debug` level log `{gpus, total_pids, unique_users, unique_groups, elapsed_ms, capped_env_labels}`; at `Warn` level log per-PID read failures with `{pid, reason}`; never panic, never short-circuit the cycle.
- [ ] T037 [P] Expose exporter self-health metrics (FR-018) via the existing Prometheus registry in `internal/pkg/server/server.go`: three series — counters `dcgm_exporter_bare_metal_mapper_cycles_total` and `dcgm_exporter_bare_metal_mapper_errors_total`, a histogram `dcgm_exporter_bare_metal_mapper_duration_seconds`, and a counter `dcgm_exporter_bare_metal_mapper_invariant_breaches_total` (consumed by T028). Registration happens in the mapper's constructor (T018 extended).
- [ ] T038 [P] Performance sanity benchmark `internal/pkg/transformation/bare_metal_user_mapper_bench_test.go::BenchmarkProcess_8GPU_128Procs` using fake NVML + fake ProcFS; comment-document the 50 ms single-cycle target; assert with `b.ReportMetric` so regressions show in CI benchmark diffs.
- [ ] T039 [P] Add an explicit regression test that non-UTIL counters are unaffected: `internal/pkg/transformation/bare_metal_user_mapper_test.go::TestProcess_OnlyTouchesGpuUtil`: feed a `MetricsByCounter` containing `DCGM_FI_DEV_GPU_TEMP`, `DCGM_FI_DEV_POWER_USAGE`, `DCGM_FI_DEV_FB_USED` in addition to `DCGM_FI_DEV_GPU_UTIL`; verify those three counters' `Labels` maps are byte-identical pre and post-`Process`.
- [ ] T040 Update `RELEASE.md` (top of file) with a section for this feature: flag `--config`, config path `/etc/dcgm-exporter/config.yaml`, label schema summary, bare-metal-only scope, systemd deployment entry point.
- [ ] T041 Execute `quickstart.md §5.1–5.5` on a real GPU host (or document the execution trace on a single-GPU dev box) and capture the `curl :9400/metrics | grep DCGM_FI_DEV_GPU_UTIL` output as evidence in a new `specs/001-multi-user-gpu-util/validation-log.md`.
- [ ] T042 Run `go vet ./...`, `staticcheck ./...` (config at repo root), and `go test ./...`; fix any lint or test failures before marking the feature complete.
- [ ] T043 [P] Cross-version performance comparison against upstream (covers SC-003). In `internal/pkg/transformation/bare_metal_user_mapper_bench_test.go`, add `BenchmarkProcess_vs_Upstream` that runs the same 8-GPU × 128-proc workload (a) with `bareMetalUserMapper` registered and (b) with it disabled (simulating upstream), and asserts via `b.ReportMetric` that the P95 of (a) ≤ 2× P95 of (b). Document the baseline numbers in `specs/001-multi-user-gpu-util/validation-log.md` captured during T041.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Phase 1. **BLOCKS** all user stories (T005–T013 introduce Config/CLI plumbing every story builds on).
- **User Story 1 (Phase 3)**: Depends on Phase 2; delivers the MVP.
- **User Story 2 (Phase 4)**: Depends on Phase 2 + reuses mapper scaffolding from US1 (T018, T019). In practice US2 is serial after US1.
- **User Story 3 (Phase 5)**: Depends on Phase 2 only; purely packaging/deployment — can be developed in parallel with US1/US2 by a different person.
- **Polish (Phase 6)**: Depends on US1 + US2 + US3 functional completion.

### Within Each User Story

- Helpers/models (`procfs.go`, `sanitizeEnvValue`, `resolveUsername`, `splitUtil`, `capEnvCardinality`) come before the `Process` method integration.
- Unit tests come immediately after the module they cover (non-TDD ordering, but mandatory).
- T021 (transformer registration) is deliberately last in US1 and must not move earlier — otherwise a half-implemented mapper runs in the default pipeline.

### Parallel Opportunities

- **Phase 1**: T002, T003, T004 marked `[P]` — different files.
- **Phase 2**: T010 (tests) and T013 (docs) are `[P]` with each other. T005 → T006 → T007 → T008 → T009 → T011 → T012 are sequential (same file or dependency chain).
- **Phase 3 (US1)**: T014 and T015 are `[P]` (separate files for source and test). T016 / T017 / T018 / T019 / T020 touch the same `bare_metal_user_mapper.go` and must run sequentially.
- **Phase 5 (US3)**: T031, T032, T035 all touch distinct files and can be parallelized. T033/T034 both edit `pkg/cmd/app_test.go` — keep serial or merge.
- **Phase 6**: T036, T037, T038, T039, T043 all marked `[P]`.

---

## Parallel Example: User Story 1

```bash
# After Phase 2 is green, kick off US1 with:
Task: "T014 Implement ProcFS abstraction in internal/pkg/transformation/procfs.go"
Task: "T015 Unit tests for procfs.go in internal/pkg/transformation/procfs_test.go"

# Then serialise the mapper build-out (all in bare_metal_user_mapper.go):
Task: "T016 UID→username resolver + TTL cache"
Task: "T017 env value sanitizer"
Task: "T018 mapper struct + constructor"
Task: "T019 Process() — single-process branch"
Task: "T020 Process() — idle GPU branch"
Task: "T021 Register mapper in transformer.go"
Task: "T022 Unit tests for US1 scenarios"
Task: "T023 Align repo-root config.yaml + docs"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 (Setup).
2. Complete Phase 2 (Foundational) — `--config` + YAML load + Validate + ApplyDefaults.
3. Complete Phase 3 (US1) — single-user attribution.
4. **STOP and VALIDATE**: on a dev GPU with a single process, confirm `DCGM_FI_DEV_GPU_UTIL{USER,STUDIO,PROJECT}` appears and other counters untouched.
5. Demo — already useful for single-tenant machines.

### Incremental Delivery

1. Phase 1 + 2 → Foundation.
2. + Phase 3 (US1) → MVP; demo to a single-user tenant.
3. + Phase 4 (US2) → multi-user attribution.
4. + Phase 5 (US3) → systemd production deployment.
5. + Phase 6 (Polish) → 1.0.

### Parallel Team Strategy

- Dev A: Phase 2 T005–T013 (config plumbing), then Phase 3 T014–T023 (US1).
- Dev B: waits for Phase 2 checkpoint, then Phase 4 T024–T030 (US2).
- Dev C (packaging/ops): waits for Phase 2 checkpoint, then Phase 5 T031–T035 (US3) in parallel with US1/US2.

---

## Notes

- `[P]` tasks = different files, no dependencies on incomplete tasks.
- `[Story]` label maps task to a specific user story for traceability.
- Unit tests are mandatory per-module (non-TDD order, but tests must exist before the task is checked off).
- Commit after each task or logical group; keep commit subjects linked to TaskIDs.
- When in doubt about DCGM/NVML behavior, mirror patterns in `internal/pkg/transformation/hpc.go` (label injection via `utils.DeepCopy`) and `internal/pkg/nvmlprovider/provider.go` (process listing).
- Avoid: modifying shared `Metric` structs across stories, adding K8s-specific logic (out of scope per Clarification Q2), introducing configuration fields that exceed the `labels` + `server` contract (Q1).
