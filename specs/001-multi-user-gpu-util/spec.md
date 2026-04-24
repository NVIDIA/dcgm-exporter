# Feature Specification: DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

**Feature Branch**: `001-multi-user-gpu-util`
**Created**: 2026-04-24
**Status**: Draft
**Input**: User description: "请参考 new_feature.txt 中的概述，实现DCGM_FI_DEV_GPU_UTIL的多用户使用率统计功能。要求最终可以在裸金属 GPU 服务器上以 systemctl service 的模式部署，要求有 config.yaml 文件"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - 独占 GPU 的用户利用率归属 (Priority: P1)

在多人共享的裸金属 GPU 服务器上，当某位用户（例如 `alice`）独自占用一张 GPU 运行训练任务时，平台管理员与用户本人都希望在监控系统中直接看到"这张 GPU 的利用率是谁在使用、属于哪个项目"。系统需要自动识别正在使用 GPU 的进程，并在 `DCGM_FI_DEV_GPU_UTIL` 指标上附加用户名（`USER`）、所属工作室/服务器标签（`STUDIO`）以及项目标签（`PROJECT`），使指标可以在 Prometheus/Grafana 中按用户或项目维度聚合。

**Why this priority**: 这是整个功能的基础能力。如果无法为单用户场景正确归因 GPU 利用率，后续的多用户分摊、项目维度统计都无从谈起；没有这一步，平台就失去了"谁用了多少 GPU"这一最核心的问题答案。

**Independent Test**: 在一台 GPU 服务器上，由 `alice` 用户启动一个占用 GPU 的进程，并导出环境变量 `PROJECT=llm-training`；查询指标端点后应看到 `DCGM_FI_DEV_GPU_UTIL{gpu="0",USER="alice",PROJECT="llm-training",STUDIO="<配置值>"}` 且数值等于该 GPU 当前的真实利用率。

**Acceptance Scenarios**:

1. **Given** 一台 GPU 服务器已部署本功能、配置文件中设置了 `STUDIO="studio-a"`，**When** 用户 `alice` 以 `PROJECT=llm-training` 运行一个占用 GPU 0 的进程、且该 GPU 当前利用率为 85%，**Then** 指标端点返回一条 `DCGM_FI_DEV_GPU_UTIL{gpu="0",USER="alice",PROJECT="llm-training",STUDIO="studio-a",...} 85`。
2. **Given** GPU 0 上没有任何用户进程，**When** 采集一次指标，**Then** 返回 `DCGM_FI_DEV_GPU_UTIL{...,USER="none",PROJECT="none",...} 0`，同时其他非利用率指标（例如温度、功耗）不受标签改造影响。
3. **Given** 某个 GPU 进程未设置 `PROJECT` 环境变量，**When** 采集该 GPU 的利用率指标，**Then** 指标的 `PROJECT` 标签取值为 `none`（或配置中定义的默认值），`USER` 仍能根据进程 UID 正确解析。

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

运维人员需要能够把这个增强版 exporter 作为一个后台服务常驻在 GPU 服务器上，开机自动启动、崩溃自动重启，并通过一个集中管理的 `config.yaml` 配置文件来调整标签、端口、采集间隔等，而不需要每次改配置都去改命令行参数或重新编译。

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
- **UID 无法解析为用户名**：当 UID 没有对应的系统账户（例如容器场景下的临时 UID）时，`USER` 标签应当取值为 `uid:<数字>` 的形式，避免标签为空或 `unknown` 造成聚合歧义。
- **进程环境变量读取受限**：当 `/proc/<PID>/environ` 因权限问题不可读时（非 root 部署场景），`PROJECT` 标签应当退化为默认值（`none`），而不是让整条指标消失。
- **GPU 支持 MIG 切分**：当 GPU 工作在 MIG 模式下被切成多个实例时，本次特性仅需保证标签逻辑不破坏原指标输出；多实例下的用户分摊精细化留待后续版本。
- **指标标签基数爆炸**：如果服务器上长期运行大量用户/项目，配置文件需要支持限制 `PROJECT` 标签的白名单或最大基数，防止 Prometheus TSDB 爆炸。
- **非 DCGM_FI_DEV_GPU_UTIL 指标不应被改造**：温度、功耗、显存等指标必须保持原有标签输出不变，避免影响现有看板和告警。
- **采集周期内 GPU 进程列表抖动**：采集过程中进程新增/消失，不应造成某一时刻用户分摊之和显著偏离真实 GPU 利用率（允许四舍五入带来的小误差）。

## Requirements *(mandatory)*

### Functional Requirements

#### 标签解析与归属

- **FR-001**: 系统 MUST 在 `DCGM_FI_DEV_GPU_UTIL` 指标上附加三个额外标签：`USER`、`STUDIO`、`PROJECT`；其他非 `DCGM_FI_DEV_GPU_UTIL` 的指标 MUST 保持原输出格式不变，不额外添加这三个标签。
- **FR-002**: 系统 MUST 通过读取 `/proc/<PID>/status` 的 UID 字段并解析为系统用户名得到 `USER` 标签；若无法解析为用户名则 MUST 使用 `uid:<数字 UID>` 作为兜底值。
- **FR-003**: 系统 MUST 将配置文件中指定的静态字符串作为 `STUDIO` 标签的值，供所有 `DCGM_FI_DEV_GPU_UTIL` 指标共用。
- **FR-004**: 系统 MUST 通过读取 `/proc/<PID>/environ` 中的 `PROJECT` 环境变量得到 `PROJECT` 标签；若进程未设置、读取失败或值为空，MUST 退化为配置中定义的默认值（默认 `none`）。
- **FR-005**: 当某张 GPU 上不存在任何用户进程时，系统 MUST 输出一条 `USER="none"`、`PROJECT="none"`、`STUDIO=<配置值>` 且利用率值为 0 的占位记录，确保时序连续性。

#### 多用户加权分摊

- **FR-006**: 当同一 GPU 有多个进程时，系统 MUST 按公式「某 (USER, PROJECT) 组合的进程数 ÷ 该 GPU 上的总进程数 × GPU 实际利用率」输出一条指标；即以 (USER, PROJECT) 作为聚合键而非单纯以 USER 作聚合键。
- **FR-007**: 分摊后所有同一 GPU 下的 `DCGM_FI_DEV_GPU_UTIL` 记录数值之和，MUST 等于该 GPU 的真实利用率（允许由整数舍入导致的 ±1 的误差）。
- **FR-008**: 系统 MUST 能够识别"计算类"的 GPU 进程以参与分摊；实现时可参考 DCGM 或 NVML 已有的进程列表接口（例如查询 GPU 上的计算进程 PID），而不应把图形/显示类进程也纳入统计。

#### 配置与运行

- **FR-009**: 系统 MUST 支持通过 `config.yaml` 文件提供运行时配置，至少涵盖：监听地址/端口、`STUDIO` 静态标签值、`PROJECT` 默认值、采集间隔、日志级别、DCGM 采集字段/收集器路径。
- **FR-010**: 系统 MUST 在启动时校验配置文件存在与格式正确性；若校验失败，MUST 以非零退出码退出并在日志中打印清晰错误信息。
- **FR-011**: 系统 MUST 允许通过命令行参数 `--config <path>` 指定配置文件位置，若未指定则使用约定的默认路径（例如 `/etc/dcgm-exporter/config.yaml`）。
- **FR-012**: 系统 SHOULD 在配置文件中提供一个开关，用于启用/禁用多用户加权分摊特性；在禁用时，保持与上游 dcgm-exporter 的输出一致以便兼容回退。

#### systemd 部署

- **FR-013**: 系统 MUST 提供一份可直接使用的 systemd unit 文件（例如 `dcgm-exporter.service`），包含 `ExecStart`、`Restart=on-failure`、合适的 `User`/`Group`（能读取 `/proc/<PID>/*` 和调用 DCGM 接口）等配置。
- **FR-014**: 系统 MUST 附带部署说明文档，覆盖：二进制安装路径、配置文件路径、systemd 单元文件安装命令、服务启停与日志查询命令。
- **FR-015**: systemd 服务 MUST 在异常退出后被自动拉起（例如通过 `Restart=on-failure` 和重启次数/时间窗口约束），以保证监控数据连续性。

#### 可靠性与可观测性

- **FR-016**: 单个进程信息读取失败（例如 `/proc/<PID>/status` 消失、权限不足）MUST NOT 中断本轮整体采集；系统 MUST 跳过该进程并在日志中记录可定位问题的警告。
- **FR-017**: 系统 MUST 在日志中暴露关键运行状态：配置加载结果、每轮采集到的 GPU 数量与进程数、异常/降级情况，日志级别可通过配置文件调整。
- **FR-018**: 系统 SHOULD 暴露自身运行健康度的指标（例如采集耗时、采集失败次数），便于对 exporter 自身做监控告警。

### Key Entities *(include if feature involves data)*

- **GPU**: 一张物理 GPU 设备，关键属性包括设备索引（gpu）、UUID、型号、当前总利用率；与其上运行的进程构成 1:N 关系。
- **GPU Process**: 正在使用 GPU 的一个进程，关键属性为 PID、所在 GPU 索引、发起用户 UID、`PROJECT` 环境变量值；是 User 与 GPU 之间的关联实体。
- **User**: 系统用户，属性为用户名、UID；通过 GPU Process 与 GPU 建立使用关系。
- **Studio**: 一台服务器级别的静态归属标签，由运维在配置文件中配置，在一台主机上对所有 GPU 与所有用户的 `DCGM_FI_DEV_GPU_UTIL` 指标统一生效。
- **Project**: 一个业务/实验项目标识，由用户在启动进程时通过环境变量 `PROJECT` 指定；用于在监控系统中按项目维度聚合 GPU 用量。
- **Config**: `config.yaml` 中定义的运行时配置对象，包括监听端点、标签默认值、静态标签值、采集开关、日志级别等字段。

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 在单用户独占 GPU 场景下，运维人员仅查看 Prometheus/Grafana 即可在 30 秒内回答"这张 GPU 正在被谁、哪个项目使用"的问题，无需再登录服务器执行 `nvidia-smi`。
- **SC-002**: 在多用户共享 GPU 场景下，同一时刻同一 GPU 的所有 `DCGM_FI_DEV_GPU_UTIL` 指标数值之和与真实 GPU 利用率的偏差 ≤ 1（百分比单位，由整数舍入导致）。
- **SC-003**: 在一台至少 8 张 GPU、并发 100+ 用户进程的服务器上，exporter 完成一轮采集的耗时 P95 不高于上游原生 dcgm-exporter 的 2 倍。
- **SC-004**: 运维人员按部署文档操作，能够在 10 分钟内完成一台裸金属 GPU 服务器上本服务的安装、配置与 systemd 启用，并看到第一条带 `USER/STUDIO/PROJECT` 标签的指标。
- **SC-005**: 当 exporter 进程因异常退出时，systemd 在 10 秒内将其重新拉起；单次采集异常不会导致服务退出或指标长期中断。
- **SC-006**: 在 `USER` 无法解析、`PROJECT` 未提供等异常输入下，系统 100% 以约定的兜底值输出（`uid:<n>` 或 `none`），不会出现缺失标签或空标签值的指标行。
- **SC-007**: 针对非 `DCGM_FI_DEV_GPU_UTIL` 的指标，100% 保持与上游 dcgm-exporter 相同的标签集合，现有看板与告警规则无需改动即可继续工作。

## Assumptions

- 假设部署目标是 Linux 裸金属 GPU 服务器（非容器/Kubernetes 环境），因此可以直接读取宿主机 `/proc/<PID>/status` 与 `/proc/<PID>/environ`。
- 假设 exporter 进程以有权限读取所有用户 `/proc/<PID>/environ` 的身份运行（通常是 `root`，或具备 `CAP_SYS_PTRACE` 能力的专用账户）；否则 `PROJECT` 标签降级为默认值是可接受的行为。
- 假设服务器上已安装与运行 NVIDIA DCGM（libdcgm 可用），并具备通过 NVML/DCGM API 查询 GPU 上计算进程 PID 列表的能力。
- 假设每张物理服务器上只使用一个 `STUDIO` 标签取值，因此在配置文件中以单值静态字符串呈现即可。
- 假设 `PROJECT` 环境变量名固定为 `PROJECT`（全大写），约定由用户在启动训练脚本时显式 `export`。
- 假设 MIG 多实例 GPU 在首版中仍按物理 GPU 聚合统计，精细化 MIG 分摊不在本特性范围内。
- 假设配置管理采用"修改 `config.yaml` → `systemctl restart` 生效"的方式，不要求运行时热加载。
- 假设部署包形态是「二进制 + `config.yaml` + `dcgm-exporter.service` + 安装说明」，不引入容器化分发。
