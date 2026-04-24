# Feature Specification: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Feature Branch**: `001-multi-user-gpu-util`
**Created**: 2026-04-24
**Status**: Draft
**Input**: User description: "请参考 new_feature.txt 中的概述，实现DCGM_FI_DEV_GPU_UTIL的多用户使用率统计功能。要求最终可以在裸金属 GPU 服务器上以 systemctl service 的模式部署，要求有 config.yaml 文件"

## Clarifications

### Session 2026-04-24

- Q: `config.yaml` 的顶层结构应当遵从哪一种方案？ → A: 完全采用项目根目录 `config.yaml` 的结构——顶层 `labels:`（含 `static: []` 与 `env: []` 两个数组）+ `server:`（端口与超时）；`config.yaml` **不包括**要采集的 metrics 种类（由既有 counters CSV 机制负责），也不含 `collectors_file`、`collect_interval_ms`、`log_level`、`kubernetes` 等运行时调参项；它是一份"只管 label 的"配置文件。
- Q: 部署形态是否需要为 Kubernetes 场景做任何额外优化？ → A: 不需要。本项目 **只针对裸金属 GPU 服务器** 的 systemd 部署做设计与测试；不做容器化/K8s 的功能扩展、兼容路径、pod 标签联动或 DaemonSet 打包；`config.yaml` 中不提供 `kubernetes` 字段；遗留代码里任何 K8s 专属 transformer 对本特性的默认部署均为关闭态。
- Q: 多用户加权分摊的"聚合键"应如何构成？ → A: 聚合键 = `USER` + `labels.env[]` 中 **所有** 标签的取值组成的有序元组；`labels.static[]` 在同一台机器上恒定，不参与聚合键；`static` 标签统一附加到每个分组输出的 metric 上。
- Q: 当 `--config` 未显式指定且默认路径 `/etc/dcgm-exporter/config.yaml` 不存在时，exporter 应如何启动？ → A: 硬要求配置文件。`config.yaml` 未找到（默认路径不存在，且未通过 `--config` 指定）时，MUST 以非零退出码退出并在日志中给出 "config.yaml not found at /etc/dcgm-exporter/config.yaml" 的明确提示；不使用内置默认、不备选路径、不静默降级。用户若需要上游原生 dcgm-exporter 行为，应改用上游未集成本特性的二进制或分支。
- Q: `config.yaml` 中的 `server.port` 与现有命令行 flag（上游 `-a/--address`）同时提供时，以哪个为准？ → A: CLI flag 覆盖 YAML。若启动命令显式指定了 `-a/--address`（通过 `cli.Context.IsSet` 判定），它覆盖 `server.port`；未显式指定时采用 YAML `server.port`；YAML 未提供 `server.port` 时使用内置默认 `:9400`。优先级链：内置默认 < `config.yaml` < CLI flag。相同规则适用于未来任何"既有 CLI flag 同时出现在 YAML"的字段。
- Q: 对 `labels.env[]` 标签值的基数控制策略应采用哪种？ → A: 字符集 `[A-Za-z0-9_.-]{1,64}`，非法字符替换为 `_`，超长截断到 64；每次采集时同一 env 标签的取值集合不得超过 128 个；按字典序保留前 128 个值，其余整批归并为 `other`。配置文件中不提供白名单/阈值字段；该约束在代码中硬编码，默认即生效。

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 独占 GPU 的用户利用率归属 (Priority: P1)

在多人共享的裸金属 GPU 服务器上，当某位用户（例如 `alice`）独自占用一张 GPU 运行训练任务时，平台管理员与用户本人都希望在监控系统中直接看到"这张 GPU 的利用率是谁在使用、属于哪个项目"。系统需要自动识别正在使用 GPU 的进程，并在 `DCGM_FI_DEV_GPU_UTIL` 指标上附加用户名（`USER`）、所属工作室/服务器标签（`STUDIO`）以及项目标签（`PROJECT`），使指标可以在 Prometheus/Grafana 中按用户或项目维度聚合。

**Why this priority**: 这是整个功能的基础能力。如果无法为单用户场景正确归因 GPU 利用率，后续的多用户分摊、项目维度统计都无从谈起；没有这一步，平台就失去了"谁用了多少 GPU"这一最核心的问题答案。

**Independent Test**: 在一台 GPU 服务器上，由 `alice` 用户启动一个占用 GPU 的进程，并导出环境变量 `PROJECT=llm-training`；查询指标端点后应看到 `DCGM_FI_DEV_GPU_UTIL{gpu="0",USER="alice",PROJECT="llm-training",STUDIO="<配置值>"}` 且数值等于该 GPU 当前的真实利用率。

**Acceptance Scenarios**:

1. **Given** 一台 GPU 服务器已部署本功能、配置文件中设置了 `STUDIO="ai-lab"`，**When** 用户 `alice` 以 `PROJECT=llm-training` 运行一个占用 GPU 0 的进程、且该 GPU 当前利用率为 85%，**Then** 指标端点返回一条 `DCGM_FI_DEV_GPU_UTIL{gpu="0",USER="alice",PROJECT="llm-training",STUDIO="ai-lab",...} 85`。
2. **Given** GPU 0 上没有任何用户进程，**When** 采集一次指标，**Then** 返回 `DCGM_FI_DEV_GPU_UTIL{...,USER="none",PROJECT="none",...} 0`，同时其他非利用率指标（例如温度、功耗）不受标签改造影响。
3. **Given** 某个 GPU 进程未设置 `PROJECT` 环境变量，**When** 采集该 GPU 的利用率指标，**Then** 指标的 `PROJECT` 标签取值为 `none`（FR-004 硬编码兜底），`USER` 仍能根据进程 UID 正确解析。

---

### User Story 2 - 多用户共享 GPU 时的按进程数加权分摊 (Priority: P1)

当同一张 GPU 被多位用户同时使用时，平台需要把这张 GPU 的总利用率按"每个用户占用的进程数"公平地分摊到每位用户，以便运维/计费/配额管理模块能够看到每个用户实际吃掉了多少 GPU 份额，而不是把整张 GPU 的利用率重复记到多个用户头上。

**Why this priority**: 多用户共享是裸金属 GPU 服务器最常见的争议来源（"到底谁在跑任务、跑了多少"），必须与 P1 的归属能力一同交付才能算 MVP。如果没有按进程数加权，监控指标就会出现"两个用户都显示 80% 利用率"这种令人误解的结果。

**Independent Test**: 构造一个场景：`alice` 在 GPU 0 上有 3 个进程、`bob` 有 1 个进程，GPU 0 当前真实利用率为 80%。采集指标后应得到两条记录：`DCGM_FI_DEV_GPU_UTIL{...,USER="alice",...} 60` 和 `DCGM_FI_DEV_GPU_UTIL{...,USER="bob",...} 20`，且两条之和等于 80（允许四舍五入带来的 ±1 误差）。

**Acceptance Scenarios**:

1. **Given** GPU 0 的总利用率为 80%，`alice` 有 3 个进程、`bob` 有 1 个进程，**When** 采集 `DCGM_FI_DEV_GPU_UTIL`，**Then** 返回两条记录：`USER="alice"` 值为 60（= 3/4 × 80），`USER="bob"` 值为 20（= 1/4 × 80）。
2. **Given** 同一位用户 `alice` 在 GPU 0 上运行了 2 个不同 `PROJECT` 环境变量的进程（`proj-a` 和 `proj-b` 各 1 个），**When** 采集指标，**Then** 输出两条分别标记 `PROJECT="proj-a"` 和 `PROJECT="proj-b"` 的记录，按进程数加权分摊利用率。
3. **Given** GPU 0 上 3 个用户共享，**When** 所有用户分摊值求和，**Then** 等于该 GPU 的真实利用率（允许四舍五入带来的 ±1 误差）。

---

### User Story 3 - 以 systemd service 模式在裸金属服务器上部署 (Priority: P1)

运维人员需要能够把这个增强版 exporter 作为一个后台服务常驻在 GPU 服务器上，开机自动启动、崩溃自动重启，并通过一个集中管理的 `config.yaml` 配置文件来调整标签（`labels.static[]` / `labels.env[]`）与 HTTP 服务器参数（`server.port` / `server.*_timeout`），而不需要每次改配置都去改命令行参数或重新编译。

**Why this priority**: 无法以 systemd service 形式稳定部署就意味着这个功能无法真正落地到生产的裸金属环境；配置文件是运维日常操作的入口，是部署体验的核心。因此与 P1 并列。

**Independent Test**: 在一台裸金属 GPU 服务器上执行安装脚本/手动步骤，将 exporter 安装为 systemd service，启动后通过 `systemctl status` 查看服务处于 `active (running)` 状态，修改 `config.yaml` 中的 `STUDIO` 值后重启服务，再次查询指标时新的 `STUDIO` 值已生效。

**Acceptance Scenarios**:

1. **Given** 管理员已将二进制与 `config.yaml` 放置到服务器规定路径，**When** 执行 `systemctl enable --now dcgm-exporter`，**Then** 服务启动成功，`systemctl status` 显示 `active (running)`，且指标端点能返回数据。
2. **Given** 服务已运行，管理员在 `config.yaml` 中修改 `studio` 字段，**When** 执行 `systemctl restart dcgm-exporter`，**Then** 再次采集到的 `DCGM_FI_DEV_GPU_UTIL` 指标上的 `STUDIO` 标签为新值。
3. **Given** 服务运行过程中进程因异常退出，**When** systemd 检测到进程退出，**Then** 服务根据 Restart 策略自动重启并恢复指标暴露。
4. **Given** `config.yaml` 内容格式错误（例如 YAML 语法错误或必填字段缺失），**When** 启动服务，**Then** 服务在日志中输出清晰的错误提示并拒绝启动，而不是使用未知默认值静默运行。

---

### Edge Cases

- **GPU 上存在僵尸/无主进程**：如果 `/proc/<PID>/status` 读取失败（进程已退出、权限不足），该进程不应导致整条采集链路失败，应当在本轮采集中跳过这一进程并记录警告日志。
- **UID 无法解析为用户名**：当 UID 没有对应的系统账户（例如集群账号变更后的遗留临时 UID）时，`USER` 标签应当取值为 `uid:<数字>` 的形式，避免标签为空或把不同 UID 合并到同一聚合键。
- **进程环境变量读取受限**：当 `/proc/<PID>/environ` 因权限问题不可读时（非 root 部署场景），该 PID 上所有 `labels.env[]` 标签的取值均退化为 `none`，而不是让整条指标消失。
- **GPU 支持 MIG 切分**：当 GPU 工作在 MIG 模式下被切成多个实例时，本次特性仅需保证标签逻辑不破坏原指标输出；多实例下的用户分摊精细化留待后续版本。
- **指标标签基数爆炸**：`labels.env[]` 标签的值完全来自用户进程的环境变量。系统硬编码基数保护规则（字符集合法化、64 字符截断、每轮 128 取值上限，超限归并为 `other`，详见 FR-004），无需在 `config.yaml` 中为此项提供任何开关或白名单配置。
- **非 DCGM_FI_DEV_GPU_UTIL 指标不应被改造**：温度、功耗、显存等指标必须保持原有标签输出不变，避免影响现有看板和告警。
- **采集周期内 GPU 进程列表抖动**：采集过程中进程新增/消失，不应造成某一时刻用户分摊之和显著偏离真实 GPU 利用率（允许四舍五入带来的小误差）。

## Requirements *(mandatory)*

### Functional Requirements

#### 标签解析与归属

- **FR-001**: 系统 MUST 在 `DCGM_FI_DEV_GPU_UTIL` 指标上附加以下两类额外标签：(a) 系统内置的 `USER` 标签（不可由用户关闭或重命名）；(b) 用户在 `config.yaml` 的 `labels:` 节中声明的任意数量的自定义标签（每条来自 `labels.static[]` 或 `labels.env[]`）。其他非 `DCGM_FI_DEV_GPU_UTIL` 的指标 MUST 保持原输出格式不变，不附加上述任何标签。
- **FR-002**: 系统 MUST 通过读取 `/proc/<PID>/status` 的 UID 字段并解析为系统用户名得到 `USER` 标签；若无法解析为用户名则 MUST 使用 `uid:<数字 UID>` 作为兜底值。`USER` 由系统自动注入，不经过 `config.yaml`；配置中重复声明 `USER` 视为配置错误。
- **FR-003**: 对 `labels.static[]` 中每一项，系统 MUST 将其 `value` 作为该标签在 `DCGM_FI_DEV_GPU_UTIL` 上的固定值；若 `value` 为空字符串，则 MUST 回退到同名操作系统环境变量；若环境变量也未设置，MUST 使用 `unknown` 作为最终兜底值。`value` 与环境变量回退均在 exporter 启动时确定一次，运行过程中不再变化。
- **FR-004**: 对 `labels.env[]` 中每一项，系统 MUST 从每个 GPU 进程的 `/proc/<PID>/environ` 中读取 `env_var` 指定的环境变量（未显式指定 `env_var` 时默认等于 `name`），将其值作为该标签取值；若进程未设置该变量、读取失败或值为空，MUST 使用 `none` 作为兜底值。对读入的值 MUST 做以下合法化：仅保留 `[A-Za-z0-9_.-]`，其余字符替换为 `_`；超过 64 字符部分截断。在单次采集内对同一 env 标签 MUST 强制 128 取值上限：按字典序保留前 128 个取值，其余整批归并为 `other`，以防御 TSDB 基数爆炸。该合法化与基数上限规则在代码中硬编码，不通过 `config.yaml` 暴露。
- **FR-005**: 当某张 GPU 上不存在任何用户进程时，系统 MUST 输出一条占位记录，其数值为 0，其上标签取值规则为：`USER="none"`；每条 `labels.static[]` 标签仍取启动时确定的静态值；每条 `labels.env[]` 标签取 `none`。

#### 多用户加权分摊

- **FR-006**: 当同一 GPU 有多个进程时，系统 MUST 按公式「某「聚合键」组合的进程数 ÷ 该 GPU 上的总进程数 × GPU 实际利用率」输出一条指标。聚合键定义为：`USER` + `labels.env[]` 中所有标签取值组成的有序元组（`labels.static[]` 标签在同一台机器上恒为同值，不参与聚合键）。
- **FR-007**: 分摊后所有同一 GPU 下的 `DCGM_FI_DEV_GPU_UTIL` 记录数值之和，MUST 等于该 GPU 的真实利用率（允许由整数舍入导致的 ±1 的误差）。
- **FR-008**: 系统 MUST 能够识别"计算类"的 GPU 进程以参与分摊；实现时可参考 DCGM 或 NVML 已有的进程列表接口（例如查询 GPU 上的计算进程 PID），而不应把图形/显示类进程也纳入统计。

#### 配置与运行

- **FR-009**: 系统 MUST 支持通过 `config.yaml` 文件提供「仅与标签相关」的运行时配置，顶层结构固定为：
    - `labels.static[]`：每项包含 `name`（必填，符合 Prometheus 标签命名规范 `[a-zA-Z_][a-zA-Z0-9_]*`）与 `value`（可为空，空时回退到同名环境变量，再空则 `unknown`）；
    - `labels.env[]`：每项包含 `name`（必填，同上命名规范）与可选 `env_var`（默认等于 `name`）；
    - `server.port`（监听端口）、`server.timeout`、`server.read_timeout`、`server.write_timeout`（HTTP/采集超时相关）。
  `config.yaml` MUST NOT 包含要采集的 metric 种类（这仍由既有的 DCGM counters CSV 机制负责），也不包含 `collectors_file`、`collect_interval`、`log_level`、`kubernetes` 等运行参数。
- **FR-010**: 系统 MUST 在启动时校验 `config.yaml` 存在与格式正确性，至少包括：(a) 顶层只允许 `labels` 与 `server` 两个键，出现未知键 MUST 拒绝启动；(b) 每个标签 `name` 必须唯一，且不得与系统保留标签冲突——完整保留集合为 `USER`、`gpu`、`UUID`、`device`、`modelName`、`Hostname`、`container`、`namespace`、`pod`、`exported_container`、`exported_namespace`、`exported_pod`、`exported_job`，权威清单定义在 `contracts/config.yaml.schema.md` §系统保留标签名；(c) 校验失败时 MUST 以非零退出码退出并在日志中打印字段路径级别的错误信息。
- **FR-011**: 系统 MUST 允许通过命令行参数 `--config <path>` 指定配置文件位置，若未指定则使用约定的默认路径 `/etc/dcgm-exporter/config.yaml`。若默认路径下文件亦不存在，MUST 以非零退出码退出并输出 "config.yaml not found at <path>"；不得静默回退到内置默认值，不得尝试额外备选路径。对所有"同时存在 CLI flag 与 YAML 字段"的配置项（例如 `-a/--address` 与 `server.port`），生效优先级链为：内置默认 < `config.yaml` < 命令行 flag；CLI flag 的"显式指定"由 `cli.Context.IsSet` 判定，未被用户显式设置的 flag 不视为覆盖。
- **FR-012**: 系统 MUST 在 `config.yaml` 缺失 `labels` 节（或 `labels` 内两个数组均为空）时，采用内置默认 Label 集：`static: [STUDIO]`（`value` 为空，回退到 `STUDIO` 环境变量，再空为 `unknown`）+ `env: [PROJECT]`（从进程 `PROJECT` 环境变量读取），以保持与概述示例的向后兼容语义。该内置默认 **仅** 在配置文件存在且 `labels` 节明确为空时生效；配置文件本身缺失的行为见 FR-011。

#### systemd 部署

- **FR-013**: 系统 MUST 提供一份可直接使用的 systemd unit 文件（例如 `dcgm-exporter.service`），包含 `ExecStart`、`Restart=on-failure`、合适的 `User`/`Group`（能读取 `/proc/<PID>/*` 和调用 DCGM 接口）等配置。
- **FR-014**: 系统 MUST 附带部署说明文档，覆盖：二进制安装路径、配置文件路径、systemd 单元文件安装命令、服务启停与日志查询命令。
- **FR-015**: systemd 服务 MUST 在异常退出后被自动拉起（例如通过 `Restart=on-failure` 和重启次数/时间窗口约束），以保证监控数据连续性。

#### 可靠性与可观测性

- **FR-016**: 单个进程信息读取失败（例如 `/proc/<PID>/status` 消失、权限不足）MUST NOT 中断本轮整体采集；系统 MUST 跳过该进程并在日志中记录可定位问题的警告。
- **FR-017**: 系统 MUST 在日志中暴露关键运行状态：配置加载结果、每轮采集到的 GPU 数量与进程数、异常/降级情况。日志级别通过上游既有的命令行参数或环境变量控制，`config.yaml` 本身不承载 `log_level` 字段（见 Clarification Q1）。
- **FR-018**: 系统 SHOULD 暴露自身运行健康度的指标（例如采集耗时、采集失败次数），便于对 exporter 自身做监控告警。

### Key Entities *(include if feature involves data)*

- **GPU**: 一张物理 GPU 设备，关键属性包括设备索引（gpu）、UUID、型号、当前总利用率；与其上运行的进程构成 1:N 关系。
- **GPU Process**: 正在使用 GPU 的一个进程，关键属性为 PID、所在 GPU 索引、发起用户 UID，以及每条 `labels.env[]` 对应环境变量的值；是 User 与 GPU 之间的关联实体。
- **User**: 系统用户，属性为用户名、UID；通过 GPU Process 与 GPU 建立使用关系。
- **StaticLabel**: `config.yaml` `labels.static[]` 中的一项；属性为 `name`（Prometheus 合法标签名）与 `value`（直接写入值，可空时回退到同名环境变量再回退到 `unknown`）。启动时求值，运行期不变。内置默认集包含 `STUDIO`。
- **EnvLabel**: `config.yaml` `labels.env[]` 中的一项；属性为 `name`（Prometheus 合法标签名）与可选 `env_var`（默认等于 `name`）。每次采集都会按 PID 从 `/proc/<PID>/environ` 动态解析；缺失值时取 `none`。内置默认集包含 `PROJECT`。
- **Config**: `config.yaml` 中定义的配置对象，仅包含 `labels` 与 `server` 两个顶层节，不涉及要采集的 metric 种类。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 在单用户独占 GPU 场景下，运维人员仅查看 Prometheus/Grafana 即可在 30 秒内回答"这张 GPU 正在被谁、哪个项目使用"的问题，无需再登录服务器执行 `nvidia-smi`。
- **SC-002**: 在多用户共享 GPU 场景下，同一时刻同一 GPU 的所有 `DCGM_FI_DEV_GPU_UTIL` 指标数值之和与真实 GPU 利用率的偏差 ≤ 1（百分比单位，由整数舍入导致）。
- **SC-003**: 在一台至少 8 张 GPU、并发 100+ 用户进程的服务器上，exporter 完成一轮采集的耗时 P95 不高于上游原生 dcgm-exporter 的 2 倍。
- **SC-004**: 运维人员按部署文档操作，能够在 10 分钟内完成一台裸金属 GPU 服务器上本服务的安装、配置与 systemd 启用，并看到第一条带 `USER` 以及 `config.yaml` 中声明的全部自定义标签（默认情况下即 `STUDIO` + `PROJECT`）的指标。
- **SC-005**: 当 exporter 进程因异常退出时，systemd 在 10 秒内将其重新拉起；单次采集异常不会导致服务退出或指标长期中断。
- **SC-006**: 在 `USER` 无法解析、`labels.env[]` 变量未提供、`labels.static[]` 未配置且同名环境变量亦缺失等异常输入下，系统 100% 以约定的兜底值输出（`USER` → `uid:<n>`；env 标签 → `none`；static 标签 → 同名环境变量，再空 → `unknown`），不会出现缺失标签或空标签值的指标行。
- **SC-007**: 针对非 `DCGM_FI_DEV_GPU_UTIL` 的指标，100% 保持与上游 dcgm-exporter 相同的标签集合，现有看板与告警规则无需改动即可继续工作。

## Assumptions

- 部署目标 **仅** 为 Linux 裸金属 GPU 服务器（非容器、非 Kubernetes）。本特性不对 K8s / 容器场景做任何专门优化或适配；上游代码中已有的 K8s 相关 transformer（pod 标签、DRA 等）在本特性的默认部署下处于关闭态，也不会被 `config.yaml` 重新启用。
- 假设 exporter 进程以有权限读取所有用户 `/proc/<PID>/environ` 的身份运行（通常是 `root`，或具备 `CAP_SYS_PTRACE` 能力的专用账户）；否则 `labels.env[]` 中的标签降级为 `none` 是可接受的行为。
- 假设服务器上已安装与运行 NVIDIA DCGM（libdcgm 可用），并具备通过 NVML/DCGM API 查询 GPU 上计算进程 PID 列表的能力。
- 假设每台物理服务器上一组 `labels.static[]` 取值恒定；运行期需要更新该组值时，通过修改 `config.yaml` 后 `systemctl restart` 生效（不提供热加载）。
- 假设 `labels.env[]` 中的环境变量名由运维通过 `config.yaml` 声明；约定用户在启动训练脚本时显式 `export` 对应变量，未 `export` 的变量取值回退为 `none`。
- 假设 MIG 多实例 GPU 在首版中仍按物理 GPU 聚合统计，精细化 MIG 分摊不在本特性范围内。
- 假设配置管理采用"修改 `config.yaml` → `systemctl restart` 生效"的方式，不要求运行时热加载。
- 假设部署包形态是「二进制 + `config.yaml` + `dcgm-exporter.service` + 安装说明」，不引入容器化/K8s 分发渠道。
