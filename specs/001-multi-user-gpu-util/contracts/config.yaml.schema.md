# Contract: `config.yaml` Schema

**Feature**: `001-multi-user-gpu-util`
**Consumer**: 运维 / systemd service
**Producer**: dcgm-exporter 启动时加载并校验

## 解析规则

- 使用 `gopkg.in/yaml.v3`，`Decoder.KnownFields(true)` —— 出现顶层或子节未知字段立即启动失败。
- 顶层只允许两个键：`labels` 和 `server`。**不得**出现 `collectors_file`、`collect_interval_ms`、`log_level`、`kubernetes`、`bare_metal_user_attribution`、`studio`、`project_env_var` 等字段（Clarification Q1）。
- `--config` 未指定时默认路径 `/etc/dcgm-exporter/config.yaml`；文件不存在 → 硬退出（Clarification Q4）。
- CLI flag 优先级高于 YAML（Clarification Q5）。

## 字段表

### `labels` 节

| 键 | 类型 | 默认 | 校验 |
|----|------|------|------|
| `labels.static` | array of objects | `[]`（空时合成默认 `[{name: STUDIO, value: ""}]`） | 每项必填 `name`；可选 `value` |
| `labels.static[].name` | string | — | `^[a-zA-Z_][a-zA-Z0-9_]*$`；不得与系统保留标签冲突 |
| `labels.static[].value` | string | `""` | 不做字符集限制（用户承担责任）；但渲染前会做 UTF-8 校验 |
| `labels.env` | array of objects | `[]`（空时合成默认 `[{name: PROJECT}]`） | 每项必填 `name`；可选 `env_var` |
| `labels.env[].name` | string | — | 同 `static[].name` 规则 |
| `labels.env[].env_var` | string | 同 `name` | `^[A-Za-z_][A-Za-z0-9_]*$` |

**系统保留标签名（大小写敏感）**，不允许出现在 `static[].name` 或 `env[].name`：

`USER`、`gpu`、`UUID`、`device`、`modelName`、`Hostname`、`container`、`namespace`、`pod`、`exported_container`、`exported_namespace`、`exported_pod`、`exported_job`。

### `server` 节

| 键 | 类型 | 默认 | 校验 | 作用 |
|----|------|------|------|------|
| `server.port` | string | `":9400"` | 可解析为 `[host]:port`；端口 ∈ [1,65535] | `/metrics` HTTP 端点的监听地址 |
| `server.timeout` | Go duration | `"10s"` | `> 0` | 单次 DCGM 采集链路（含 transformer）的整体超时；超时即本轮作废并记错误日志 |
| `server.read_timeout` | Go duration | `"5s"` | `> 0` | `http.Server.ReadTimeout`：接受 Prom 抓取请求时读取请求的最大耗时 |
| `server.write_timeout` | Go duration | `"10s"` | `> 0` | `http.Server.WriteTimeout`：向 Prom 抓取端写响应的最大耗时 |

### 空 `labels` 的默认行为（FR-012）

若 `labels` 节被整体省略 **或** `labels.static` 与 `labels.env` 同时为空数组，系统合成：

```yaml
labels:
  static:
    - name: STUDIO
      value: ""           # 空值 → 回退到 STUDIO 环境变量 → "unknown"
  env:
    - name: PROJECT       # env_var 默认 = name
```

## `static[].value` 回退链（FR-003）

启动时对每个 `static[i]` 按如下顺序求值一次，之后运行期不变：

1. 若 `value` 非空 → 直接使用；
2. 否则尝试 `os.Getenv(name)`（例如 `name=STUDIO` → 读 `STUDIO` 环境变量）；
3. 仍为空 → `unknown`。

## `env[].value` 运行期规则（FR-004）

每轮采集对每 PID：

1. 从 `/proc/<PID>/environ` 读取 `env_var` 指定的变量；
2. 读不到/值为空 → `none`；
3. 合法化：只保留 `[A-Za-z0-9_.-]`，其余替换为 `_`；
4. 截断到 64 字符；
5. 每轮每 env 标签独立做基数裁剪，取值数 > 128 时按字典序保留前 128，其余整批改写为 `other`（Clarification Q5）。

## CLI 覆盖 YAML（Clarification Q4）

| CLI flag | 覆盖的 YAML 字段 |
|----------|-----------------|
| `-a` / `--address` | `server.port` |
| `--config` | 本身只决定配置文件路径，不参与字段覆盖 |

其他既有上游 CLI flag（`-f/--collectors`、`--collect-interval` 等）与本 `config.yaml` 无交集，继续走上游逻辑。

## 失败处理契约

| 场景 | 退出码 | 日志要求 |
|------|--------|---------|
| `--config` 指向的路径不存在或不可读 | 非 0 | 完整绝对路径 + 系统 error |
| 默认路径 `/etc/dcgm-exporter/config.yaml` 不存在且未指定 `--config` | 非 0 | `config.yaml not found at /etc/dcgm-exporter/config.yaml` |
| YAML 语法错误 | 非 0 | 行/列信息 |
| 顶层或子节出现未知字段 | 非 0 | 列出字段名 |
| 字段类型错误 | 非 0 | 字段路径 + 期望类型 |
| 标签命名违规/与系统保留名冲突/重名 | 非 0 | 字段路径 + 冲突原因 |
| `server.port` 不可解析 | 非 0 | 字段路径 + 原始值 |

## 完整样例（规约范例，与仓库 `config.yaml` 一致）

```yaml
labels:
  static:
    - name: STUDIO
      value: "ai-lab"
    # - name: CLUSTER
    #   value: ""           # 留空 → 读 CLUSTER 环境变量 → 再空为 "unknown"

  env:
    - name: PROJECT         # env_var 默认 = name
    # - name: EXPERIMENT
    #   env_var: EXPERIMENT_NAME

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
```

## 兼容性承诺

- 本契约定义的两节（`labels`、`server`）及其字段名在 feature 首发版本后冻结。
- 新增字段必须向后兼容（有合理默认），删除/重命名字段属 breaking change。
- 如需增加"非标签类"运行时配置（例如日志级别、采集间隔），应置于新的顶层节（例如 `runtime:`）而非混入 `labels`，以保持 Q1 的语义边界。
