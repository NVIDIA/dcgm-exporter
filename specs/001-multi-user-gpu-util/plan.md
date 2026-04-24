# Implementation Plan: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Branch**: `001-multi-user-gpu-util` | **Date**: 2026-04-24 (revised after /speckit.clarify) | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/001-multi-user-gpu-util/spec.md`

**Note**: This plan is the Phase 0/1 output of `/speckit.plan`. It has been rewritten after the `/speckit.clarify` session on 2026-04-24 to align with the finalized `config.yaml` schema (`labels: {static[], env[]}` + `server:`), bare-metal-only scope, and hard-coded cardinality rules.

## Summary

在现有的 dcgm-exporter（NVIDIA 官方 fork）基础上增量扩展，仅改动 `DCGM_FI_DEV_GPU_UTIL` 一个指标：使其额外携带 (a) 系统自动注入的 `USER` 标签（PID→UID→用户名；失败退化为 `uid:<n>`），以及 (b) 用户在 `config.yaml` `labels:` 节中声明的任意数量的自定义标签（每条来自 `labels.static[]` 或 `labels.env[]`）。当多个计算进程共享同一张 GPU 时，按「聚合键 = `USER` + `labels.env[]` 所有取值组成的有序元组」分组，按进程数加权把原本单条的 `DCGM_FI_DEV_GPU_UTIL` 拆成多条分摊记录；`labels.static[]` 在单机恒定，不参与聚合键，附加到每条输出上。其余 DCGM 指标保持原样。

`config.yaml` 仅承载"与标签相关的配置" + `server:` 节；不承载 metrics 种类（继续由 DCGM counters CSV 负责）、不承载 `log_level/kubernetes/collectors_file` 等无关字段。`--config` 未指定且 `/etc/dcgm-exporter/config.yaml` 不存在时硬退出；YAML 与 CLI flag 的优先级为：内置默认 < YAML < CLI。`labels.env[]` 标签值的合法化（字符集、64 字符截断）与基数上限（每轮 128 个取值，超限归 `other`）在代码中硬编码，不暴露为配置字段。

部署形态 **仅** 为 Linux 裸金属 GPU 服务器 + systemd service；不做 K8s/容器化适配。

## Technical Context

**Language/Version**: Go 1.24（`go.mod` 声明 `go 1.24.0`，`toolchain 1.24.13`）

**Primary Dependencies**:
- `github.com/NVIDIA/go-dcgm`（DCGM 采集，既有）
- `github.com/NVIDIA/go-nvml`（计算进程 PID 列表，既有）
- `github.com/urfave/cli/v2`（既有 CLI）
- `gopkg.in/yaml.v3`（新增直接依赖：`config.yaml` 解析）
- `github.com/prometheus/client_model` / `exporter-toolkit`（既有）
- Go 标准库 `os/user`（UID→用户名解析）
- `log/slog`（项目新代码约定）

**Storage**: 无持久化存储；运行时读取：
- `/etc/dcgm-exporter/config.yaml`（新增，仅含 `labels` + `server` 两节）
- `/proc/<PID>/status`（UID）
- `/proc/<PID>/environ`（任意用户声明的 env 变量）

**Testing**: `go test` + `stretchr/testify`；复用项目既有 `go.uber.org/mock` 生成的 Collector mock。本特性新增模块（YAML 加载、`/proc` 读取、标签解析、加权分摊）一律附单测；`/proc` 读取通过可注入的 `ProcFS` 接口（默认 `/proc`，测试时指向临时目录）。

**Target Platform**: **Linux 裸金属 GPU 服务器 + systemd service 运行**。不支持 / 不适配容器化或 Kubernetes 场景。

**Project Type**: Go 单体服务（`cmd/dcgm-exporter` 入口 + `internal/pkg/**` 库）。

**Performance Goals**:
- 单轮采集耗时 P95 ≤ 上游原生 dcgm-exporter 的 2 倍（SC-003）。
- 8 GPU × 约 100 计算进程规模下，单轮标签解析与分摊 ≤ 50 ms（不阻塞采集循环）。

**Constraints**:
- 仅对 `DCGM_FI_DEV_GPU_UTIL` 改造标签集；其他指标 100% 保持原输出（SC-007）。
- 分摊后同一 GPU 的分片之和 **精确等于** 真实 GPU 利用率（整数百分比），四舍五入 ±1 靠闭合补偿归零。
- 不引入热加载；配置变更通过 `systemctl restart` 生效。
- 单进程 `/proc` 读取失败不得中断整轮采集。
- 不为 K8s/容器做任何兼容/打包/优化；Helm/DaemonSet 资产保持上游原样，不在本特性更新范围。

**Scale/Scope**:
- 代码改动集中在 4 个新文件 + 2 个现有文件（Config 结构 + CLI 参数装配），预计新增代码 < 1200 LOC（含测试）。
- 可选标签数：`labels.static[]` 和 `labels.env[]` 合计上限由 Prometheus TSDB 基数决定，实现层对每个 env 标签强制 128 取值上限。

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

`.specify/memory/constitution.md` 仍为未填充的模板（全部 `[PRINCIPLE_*]`/`[SECTION_*]` 占位符），没有建立生效的原则性门禁。

**判断**：无成文约束 → 无违反；以"合理默认"通过——

- 简单性：复用现有 transformer 管道，不引入新服务/新存储/新依赖（yaml.v3 已在 go.mod 间接存在）。
- 可测试性：新模块均以纯函数 / 接口注入（`ProcFS`、NVML）方式暴露，便于单测。
- 向后兼容：未启用本特性（不使用 `--config`）时上游 CLI 二进制行为不变；启用后 `DCGM_FI_DEV_GPU_UTIL` 外的所有指标一字节不改。
- 范围控制：spec 中 Q2 明确"不做 K8s 适配"，plan 层随之锁定 systemd 单一部署形态，避免多部署路径的复杂度。

Phase 1 完成后复检通过（见末尾 Post-Design Re-check）。

## Project Structure

### Documentation (this feature)

```text
specs/001-multi-user-gpu-util/
├── plan.md              # 本文件（Phase 0/1 输出，已按澄清重写）
├── spec.md              # /speckit.specify + /speckit.clarify 输出
├── research.md          # Phase 0 输出
├── data-model.md        # Phase 1 输出
├── quickstart.md        # Phase 1 输出
├── contracts/           # Phase 1 输出
│   ├── config.yaml.schema.md    # config.yaml 字段契约
│   ├── metrics.contract.md      # DCGM_FI_DEV_GPU_UTIL 标签 & 数值契约
│   └── dcgm-exporter.service    # systemd unit 契约
├── tasks.md             # /speckit.tasks 输出（需要在 plan 更新后重跑）
└── checklists/
    └── requirements.md
```

### Source Code (repository root)

Go 单体项目内增量扩展，新增/修改文件：

```text
dcgm-exporter/
├── cmd/
│   └── dcgm-exporter/
│       └── main.go                        # 不变
├── pkg/
│   └── cmd/
│       └── app.go                         # 修改：新增 --config flag；加载 YAML；CLI 覆盖 YAML
├── internal/
│   └── pkg/
│       ├── appconfig/
│       │   ├── types.go                   # 修改：新增 LabelsConfig / ServerConfig 结构
│       │   ├── const.go                   # 修改：加入新的默认值常量（:9400、128 cardinality、"unknown"、"none"、"other"）
│       │   └── yamlconfig.go              # 新增：LoadYAMLConfig(path) + Validate()
│       ├── transformation/
│       │   ├── bare_metal_user_mapper.go  # 新增：标签解析 + 加权分摊 transformer
│       │   ├── bare_metal_user_mapper_test.go
│       │   ├── procfs.go                  # 新增：/proc 读取抽象（status / environ）
│       │   ├── procfs_test.go
│       │   └── transformer.go             # 修改：当 --config 提供时注册 BareMetalUserMapper
│       └── nvmlprovider/
│           └── provider.go                # 不变（复用 GetComputeRunningProcesses）
├── etc/
│   └── default-counters.csv               # 不变（DCGM_FI_DEV_GPU_UTIL 保持启用）
├── packaging/
│   └── config-files/
│       └── systemd/
│           └── nvidia-dcgm-exporter.service # 修改：ExecStart --config；Restart=on-failure
├── config.yaml                            # 仓库已存在一份示例；作为 install 目标模板
├── docs/
│   └── bare-metal-deployment.md           # 新增：裸金属部署 runbook
└── specs/001-multi-user-gpu-util/         # 本次规划产物
```

**Structure Decision**: Option 1（单项目 Go 增量扩展）。澄清会话确认只做裸金属部署，不新增前后端/独立模块/容器映像，原因同 spec Clarification Q2 & Q5：最小化改动面、最大化可测性，与现有 transformer 管道范式一致。

## Complexity Tracking

无条款被违反，无需填写偏差表。

---

## Phase 0 – Outline & Research（已完成）

产物：`research.md`（按 Clarifications 重写）。7 项决策已收敛：

1. YAML 库 → `gopkg.in/yaml.v3`（严格 `KnownFields(true)`）。
2. 计算进程 PID 来源 → 既有 NVML `GetComputeRunningProcesses`。
3. UID→用户名 → Go `os/user.LookupId` + TTL 缓存（300s，非配置项）。
4. `/proc/<PID>/environ` → root 身份读取；值合法化 + 长度截断 + 每轮基数上限。
5. 加权分摊 → `round(weight × util)` + 末位闭合补偿；聚合键 = `USER` + 所有 env 标签。
6. Transformer 作用域 → 仅 `DCGM_FI_DEV_GPU_UTIL`；启用由"是否读到 config.yaml"决定，无独立开关。
7. systemd 策略 → `Restart=on-failure` + `RestartSec=3s` + `StartLimitBurst/IntervalSec` 组合；`User=root`。

## Phase 1 – Design & Contracts（已完成）

产物：`data-model.md`、`contracts/config.yaml.schema.md`、`contracts/metrics.contract.md`、`contracts/dcgm-exporter.service`、`quickstart.md`。`CODEBUDDY.md` 的 `<!-- SPECKIT -->` 区块指向本 plan 路径。

### Post-Design Constitution Re-check

设计阶段无引入新服务 / 新持久化 / 不可测试模块；transformer 仍为旁路作用于末端，失败隔离，满足向后兼容。门禁通过。

### Agent Context Update

`/root/go/src/dcgm-exporter/CODEBUDDY.md` `<!-- SPECKIT START --> ... <!-- SPECKIT END -->` 区块已指向 `specs/001-multi-user-gpu-util/plan.md`（本文件）。
