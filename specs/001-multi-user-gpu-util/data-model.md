# Phase 1 Data Model: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Feature**: `001-multi-user-gpu-util`
**Date**: 2026-04-24 (rewritten post-clarify)

本文件描述本特性引入或扩展的核心数据结构、字段含义、取值来源、校验规则与不变量。与 `spec.md` 的 Clarifications 和 `research.md` 的决策保持同步。

---

## 1. `Config`（YAML → Go 结构）

`config.yaml` 顶层仅两节：`labels` 与 `server`。对应 Go 结构（位于 `internal/pkg/appconfig/types.go`）：

```text
Config
├── Labels  LabelsConfig    # yaml: labels
└── Server  ServerConfig    # yaml: server

LabelsConfig
├── Static []StaticLabel    # yaml: static；默认空切片
└── Env    []EnvLabel       # yaml: env；默认空切片

StaticLabel
├── Name  string            # yaml: name  — 必填
└── Value string            # yaml: value — 可为空

EnvLabel
├── Name    string          # yaml: name    — 必填
└── EnvVar  string          # yaml: env_var — 可选；默认等于 Name

ServerConfig
├── Port         string     # yaml: port          — 默认 ":9400"；/metrics 监听地址
├── Timeout      Duration   # yaml: timeout       — 默认 "10s"；单次采集链路整体超时
├── ReadTimeout  Duration   # yaml: read_timeout  — 默认 "5s"； http.Server.ReadTimeout
└── WriteTimeout Duration   # yaml: write_timeout — 默认 "10s"；http.Server.WriteTimeout
```

各 timeout 字段的作用权威定义见 `contracts/config.yaml.schema.md` §`server` 节。

注意：不包含上一版 plan 的 `bare_metal_user_attribution.*` / `collectors_file` / `log_level` / `kubernetes` 等字段（Clarifications Q1 明确排除）。

### 字段校验规则

| 字段 | 必填 | 校验 |
|------|------|------|
| 顶层未知键 | — | 严格模式：出现即启动失败（由 `yaml.v3` + `KnownFields(true)` 保证） |
| `Labels.Static[].Name` / `Labels.Env[].Name` | 是 | 符合 Prometheus 标签命名规范 `^[a-zA-Z_][a-zA-Z0-9_]*$`；不可与系统保留标签同名（见 R8） |
| 标签名唯一性 | — | `Labels.Static` 内 Name 互不重复、`Labels.Env` 内 Name 互不重复、且两组之间 Name 互不重复 |
| `StaticLabel.Value` | 否 | 可为空；空时走 FR-003 回退链：同名环境变量 → `unknown` |
| `EnvLabel.EnvVar` | 否 | 符合 shell 变量名规则 `^[A-Za-z_][A-Za-z0-9_]*$`；默认等于 `Name` |
| `Server.Port` | 否 | 可解析为 `:port` 或 `host:port`，端口 ∈ [1, 65535] |
| `Server.*Timeout` | 否 | Go duration 格式；必须 > 0 |

### 运行时派生值（非 YAML 字段）

下列值由代码在启动时派生，**不**写入 `config.yaml`：
- `UserNameCacheTTL = 300s`（R3，硬编码）
- `EnvValueMaxLen = 64`（R4/FR-004）
- `EnvValueCharset = [A-Za-z0-9_.-]`（R4/FR-004）
- `MaxEnvCardinalityPerCycle = 128`（Clarifications Q5）
- `FallbackNone = "none"`、`FallbackUnknown = "unknown"`、`FallbackOther = "other"`

### 不变量

- 成功 `LoadYAMLConfig` 返回的 `*Config` 满足：`Labels.Static` 与 `Labels.Env` 的 Name 集合互不相交、不含系统保留标签、全部符合命名规范。
- CLI flag 的"显式指定"（通过 `cli.Context.IsSet`）覆盖 YAML 同名字段；其优先级链：内置默认 < YAML < CLI（Clarifications Q4）。
- `config.yaml` 未找到（默认路径不存在且未传 `--config`）→ 启动失败（Clarifications Q4）。
- `config.yaml` 存在但 `Labels` 两数组均为空 → 启动时合成默认标签集：`Static=[{Name:"STUDIO",Value:""}]` + `Env=[{Name:"PROJECT",EnvVar:"PROJECT"}]`（FR-012）。

---

## 2. `GPUProcess`（运行时实体，不持久化）

单次采集周期内一条"某 GPU 上的某计算进程"的记录。

```text
GPUProcess
├── PID      uint32
├── GPUIndex uint
├── GPUUUID  string
├── UID      uint32     # 读 /proc/<PID>/status 失败 → math.MaxUint32，记 WARN
├── Username string     # LookupId 成功 → 用户名；失败 → "uid:<UID>"
└── EnvVals  []string   # 与 Config.Labels.Env 同长度、同顺序的取值；读取失败的位置为 "none"
```

### 生命周期

- 创建：`bareMetalUserMapper.Process()` 内部，每轮对每 GPU 列出计算进程 PID 列表后逐个构造。
- 销毁：本轮采集结束即释放。

### 不变量

- `Username` 不为空；要么系统用户名，要么 `uid:<n>`。
- `len(EnvVals) == len(Config.Labels.Env)`，顺序严格对齐。
- `EnvVals[i]` 经过 R4 合法化（字符集 + 64 截断），不直接暴露原始值给 Prometheus。
- 单个 `GPUProcess` 构造失败不影响其他 PID 的采集。

---

## 3. `GPUProcessGroup`（派生聚合）

对单 GPU 上的 `GPUProcess` 按聚合键聚合的结果；聚合键 = `Username` + `EnvVals` 元组（Clarifications Q3）。

```text
GPUProcessGroup
├── GPUUUID    string
├── Username   string
├── EnvVals    []string   # 与 Config.Labels.Env 同长同序
├── ProcessCnt uint       # 本组进程数
└── Weight     float64    # ProcessCnt / 该 GPU 的 total
```

### 基数保护（Q5 决策）

在 `Process()` 内对 **每个 env 标签独立** 做基数裁剪：
- 对每个 env 标签 `E_i`，收集本轮所有 groups 的 `EnvVals[i]`；
- 若该集合基数 > `MaxEnvCardinalityPerCycle (128)`，按字典序保留前 128，其余整组的 `EnvVals[i]` 改写为 `"other"`；
- 改写后可能出现键冲突（多组 EnvVals 相同），进一步合并为单组（`ProcessCnt` 累加、`Weight` 重算）。

### 不变量

- 对同一 GPUUUID：`sum(ProcessCnt) == totalProcesses(GPU)`。
- 对同一 GPUUUID：`sum(Weight) == 1.0`（浮点）。
- `totalProcesses(GPU) == 0` 时退化为单一占位组：`Username="none"`；每个 `EnvVals[i]="none"`；`ProcessCnt=0`；`Weight=1.0`。

---

## 4. `Metric.Labels` 扩展

复用现有 `internal/pkg/collector/types.go:Metric.Labels map[string]string`；本 transformer 仅在输出 `DCGM_FI_DEV_GPU_UTIL` 的分摊记录时注入以下键：

| Label | 来源 | 示例 | 必存在 |
|-------|------|------|--------|
| `USER` | `GPUProcessGroup.Username` | `alice` / `uid:1105` / `none` | 是（硬编码注入） |
| 每个 `Config.Labels.Static[i].Name` | 启动时解析（值 → 同名环境变量 → `unknown`） | `STUDIO="ai-lab"` | 是（每条输出都附加） |
| 每个 `Config.Labels.Env[j].Name` | `GPUProcessGroup.EnvVals[j]`（可能为 `none` / 被截断 / `other`） | `PROJECT="llm-training"` | 是 |

### 标签基数承诺

- `USER`：受限于系统用户数 + `uid:<n>` 可能数；每轮输出 `unique_users` 指标便于监控。
- 每个 `Static[i]`：恒为 1。
- 每个 `Env[j]`：上限 128（超限归并为 `other`）。

---

## 5. `Metric` 分摊派生规则

输入：`DCGM_FI_DEV_GPU_UTIL` 某 GPU 的一条原始 `Metric`，值为 `utilTotal`（整数，[0,100]）。

输出：

- `totalProcesses(GPU) == 0` → 1 条占位 Metric：`USER="none"`、每个 env 标签 `"none"`、static 标签取启动时确定值、`Value="0"`。
- 否则：按聚合键稳定排序的 N 组：
  - 前 N-1 组：`Value = round(Weight_i * utilTotal)`；
  - 第 N 组：`Value = utilTotal - Σ Value_i (i<N)`（闭合补偿）；
  - 每条继承原 Metric 的 `GPU`/`GPUUUID`/`Counter`；`Labels` 按第 4 节规则注入。

### 不变量

- **分摊和**：同一 GPU 的所有分摊 `Value` 之和 **精确等于** `utilTotal`（闭合补偿保证）。
- **标签隔离**：非 `DCGM_FI_DEV_GPU_UTIL` 的 counter 本 transformer 一律原样透传，不加任何标签、不复制 Metric。
- **空闲占位**：保证时序连续，不因 0 进程断点。

---

## 6. 实体关系图

```text
         (1)┌───────────────────────┐
        owns│        Config         │
            │ (LabelsConfig,        │
            │  ServerConfig)        │
            └──────────┬────────────┘
                       │ drives schema
                       ▼
         ┌───────────────────────────────┐
         │   bareMetalUserMapper         │
         │   (transformer, per-cycle)    │
         └──┬───────────────┬───────────┬┘
            │               │           │
    queries │        reads  │           │ applies cardinality cap
 NVML PID   │   /proc/<PID>/│           │ (MaxEnvCardinalityPerCycle)
 list per   │   {status,    │           │
 GPU        ▼   environ}    ▼           ▼
 ┌───────────────────┐  ┌───────────────────┐
 │    GPUProcess     │  │   sanitize &      │
 │ (N per GPU cycle) │  │   cap env values  │
 └────────┬──────────┘  └───────────────────┘
          │ group by (Username, EnvVals…)
          ▼
 ┌───────────────────────┐
 │   GPUProcessGroup     │ ───────> produces ───────> ┌────────────┐
 │   (per GPU cycle)     │                             │  Metric    │
 └───────────────────────┘                             │ (UTIL split)│
                                                      └────────────┘
```
