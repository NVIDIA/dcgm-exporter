# Phase 0 Research: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Feature**: `001-multi-user-gpu-util`
**Date**: 2026-04-24 (rewritten post-clarify)

本文件为 Phase 0 的决策归档。原研究已在 `/speckit.clarify` 会话后重写以匹配澄清结论。

---

## R1. `config.yaml` 的解析库

**Decision**: `gopkg.in/yaml.v3`，直接依赖，严格模式 `Decoder.KnownFields(true)`。

**Rationale**:
- 解析内容 **仅** 涉及 `labels: {static[], env[]}` + `server:` 两节（由 Clarification Q1 固定），字段数量极少，无需 viper/koanf 的丰富特性。
- `KnownFields(true)` 能在出现拼写错误（例如把 `labels.env` 写成 `labels.envs`）时立即失败；符合 FR-010 要求。
- 已经以间接依赖形式存在于 `go.mod`，提升为直接依赖零成本。

**Alternatives considered**:
- viper / koanf：引入数十个间接依赖；不需要多格式/环境变量叠加；拒绝。
- 手写反射解析：重复造轮子；拒绝。

---

## R2. GPU 计算进程 PID 列表来源

**Decision**: 复用既有 `internal/pkg/nvmlprovider/provider.go:GetDeviceProcessMemory`（调用 NVML `GetComputeRunningProcesses`），仅取 PID 字段；对 MIG 调用既有 `GetAllMIGDevicesProcessMemory`。

**Rationale**:
- `GetComputeRunningProcesses` 天然只返回计算进程，满足 FR-008"不应把图形/显示类进程纳入统计"。
- 仓库已有 NVML/MIG 封装，包括对 `ERROR_NOT_SUPPORTED` / `ERROR_NOT_FOUND` 的降级，直接复用避免二次实现。
- DCGM JobStats 要求显式 job 生命周期（`jobStartStats/jobStopStats`），在裸金属无调度器的场景无人触发，不适用。

**Alternatives considered**:
- `nvidia-smi` 子进程 + 文本解析：慢且脆弱，拒绝。
- cgroup v2 PID 列表：无法区分计算/图形进程，拒绝。

---

## R3. UID → 用户名解析 & 缓存

**Decision**: Go 标准库 `os/user.LookupId(strconv.Itoa(uid))` + 进程内 TTL 缓存（默认 300s，**不**暴露为配置字段，硬编码）。解析失败兜底 `uid:<数字>`。

**Rationale**:
- 标准库走 NSS / libc getpwuid，自动处理 `/etc/passwd`、`sssd`、LDAP 等；零新增依赖。
- 每轮采集期间同一 UID 可能出现多次，用 `sync.Map` 缓存值 + 时间戳大幅减少 syscall。
- 把 TTL 硬编码是因为用户在 Q5 确认"基数保护类参数不暴露为配置"的原则；TTL 是同性质的内部调优项。

**Alternatives considered**:
- 每次 syscall：在 100+ 进程规模下多次重复调用浪费。
- `getent passwd` 子进程：更慢更脆弱。

---

## R4. `/proc/<PID>/environ` 读取与安全模型

**Decision**:
- exporter 以 `root` 运行（systemd unit `User=root`）。读 `os.ReadFile("/proc/<PID>/environ")`，按 `\x00` 切分，查找每个被 `labels.env[]` 声明的 `env_var` 对应 token。
- 单 PID 读取失败（进程退出、权限、ENOENT）→ 将该 PID 的 env 标签全部记为 `none`，继续本轮采集；记 WARN。
- 合法化规则：保留 `[A-Za-z0-9_.-]`；其余替换为 `_`；截断到 64 字符。
- 每轮采集同一 env 标签的取值集合上限 128（按字典序保留前 128），超出整批归并为 `other`（Q5 决策）。

**Rationale**:
- 非 root 读不到其他用户的 environ；裸金属场景下以 root 运行是常规约束（并发写入 `docs/bare-metal-deployment.md`）。
- 合法化防御 `PROJECT=$(date +%s)` 或带空格/shell 字符的环境变量，保护 Prometheus 标签合规与 TSDB 基数；硬编码而非配置项简化部署。

**Alternatives considered**:
- 要求 `CAP_SYS_PTRACE` 代替 root：可选方案，在部署文档中作为降权建议列出，但默认仍 root（减少运维困惑）。
- 白名单机制（Q5 Option C）：裸金属无法事先穷举所有合法 PROJECT 值，拒绝。

---

## R5. 加权分摊算法与数值稳定性

**Decision**:
- 聚合键 = `USER` + `labels.env[]` 中所有标签按声明顺序排列的取值元组（Q3 决策）。
- 分摊算法：
  1. 对同一 GPU 的进程按聚合键聚合，得每组进程数 `count_i`；
  2. 前 N-1 组值 `v_i = round(count_i / total × util_total)`；
  3. 第 N 组值 `v_N = util_total - Σ v_i (i<N)`（闭合补偿）；
  4. 排序策略：按聚合键字典序稳定排序，使末位补偿始终落在同一组，减少跨采集周期跳动。

**Rationale**:
- 闭合补偿 → 同 GPU 各分摊严格求和等于 `util_total`，SC-002 允许 ±1 但实际归 0。
- 稳定排序 → 观测曲线平滑，不会每个周期在不同组间"随机 +1/-1"。

**Alternatives considered**:
- `floor`：之和永远 ≤ utilTotal，Grafana 累加看板低估；拒绝。
- 浮点非取整：`DCGM_FI_DEV_GPU_UTIL` 是整数百分比 gauge，保持上游语义。

---

## R6. Transformer 作用域与启用策略

**Decision**:
- 新增 `bareMetalUserMapper` 置于 transformer 管道 **末端**，仅处理 `DCGM_FI_DEV_GPU_UTIL`。
- 启用条件：成功加载了 `config.yaml`（无论是默认路径还是 `--config`）。**不引入独立开关字段** —— Q4 决策要求"config 缺失即硬退出"，故"config 存在"就等价于"启用"。
- K8s/HPC/DRA 等既有 transformer 不因本特性改变；裸金属场景下它们自然因缺少 K8s 环境/HPC 目录而旁路。
- 配置文件存在但 `labels` 节为空/缺失时，使用内置默认标签集 `static: [STUDIO]` + `env: [PROJECT]`（FR-012，保持与 `new_feature.txt` 示例语义兼容）。

**Rationale**:
- 单一入口点（成功读到 config 即启用）最小化配置面；用户只需回答一个问题："要不要自定义标签？"。
- 末端位置隔离性最好，对其他 transformer 零影响。

**Alternatives considered**:
- 独立 `enabled: true/false` 开关：与"config 缺失硬退出"语义冲突；拒绝。
- 对每个指标 per-counter 启用：过度工程；spec 明确只改 UTIL。

---

## R7. systemd 单元策略

**Decision**:

```ini
[Unit]
Description=NVIDIA DCGM-exporter with multi-user GPU utilization attribution
After=nvidia-dcgm.service network-online.target
Requires=nvidia-dcgm.service

[Service]
Type=simple
User=root
ExecStart=/usr/bin/dcgm-exporter --config /etc/dcgm-exporter/config.yaml
Restart=on-failure
RestartSec=3s
StartLimitBurst=5
StartLimitIntervalSec=30s
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

**Rationale**:
- `Restart=on-failure` + `RestartSec=3s` → 10 s 内自愈（SC-005）。
- `User=root` → 满足跨用户 `/proc/<PID>/environ` 读取。
- `ProtectKernelTunables` / `ProtectProc=invisible` 等选项 **不启用**，因为会干扰 DCGM/NVML 与 `/proc` 访问。

**Alternatives considered**:
- `DynamicUser=yes`：禁用跨 UID 的 `/proc` 访问，与 R4 冲突。
- `Restart=always`：崩溃循环下无上限重启会把 CPU 吃光；`on-failure` + `StartLimit*` 组合更稳妥。

---

## R8. 标签名冲突与系统内置标签

**Decision**: 启动时校验 `labels.static[].name` / `labels.env[].name` 全部两两互不重复，且不得等于下列系统保留标签（大小写敏感）：
- `USER`（由 exporter 自动注入）
- `gpu`、`UUID`、`device`、`modelName`、`Hostname`、`container`、`namespace`、`pod`、`exported_container`、`exported_namespace`、`exported_pod`、`exported_job`（上游既有标签）

冲突时启动失败，错误信息定位到冲突标签名。

**Rationale**: 避免 Prometheus 在渲染时静默覆盖或附加 `exported_` 前缀，导致看板难以理解的"看起来正常但实际错了"的指标。

**Alternatives considered**:
- 允许同名覆盖：调试风险大，拒绝。
- 自动加前缀：违反"用户写啥就输出啥"的可预测性，拒绝。

---

## 研究结论

8 项决策全部收敛，互不冲突，符合 spec 的 Clarifications 条目。进入 Phase 1 Design & Contracts。
