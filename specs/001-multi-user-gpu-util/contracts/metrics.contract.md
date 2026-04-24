# Contract: `DCGM_FI_DEV_GPU_UTIL` 标签与数值契约

**Feature**: `001-multi-user-gpu-util`
**Consumer**: Prometheus 抓取端 / Grafana 看板 / 用量审计
**Producer**: dcgm-exporter（启用本特性的构建）

## 作用域

- **仅影响** `DCGM_FI_DEV_GPU_UTIL` 一个指标。
- **不影响** `DCGM_FI_DEV_GPU_TEMP`、`DCGM_FI_DEV_POWER_USAGE`、`DCGM_FI_DEV_FB_USED`、以及 counters CSV 中所有其他指标，它们保持上游 dcgm-exporter 的原样输出（SC-007）。
- 启用条件：成功加载 `config.yaml`（默认路径或 `--config`）。

## 标签集合

`DCGM_FI_DEV_GPU_UTIL` 输出的 Prometheus 样本，在原有上游标签之上额外包含：

| 标签 | 类型 | 必存在 | 取值规则 |
|------|------|--------|---------|
| `USER` | string | 是 | 系统用户名；UID 无对应账户 → `uid:<n>`；GPU 空闲 → `none` |
| 每个 `Config.Labels.Static[i].Name` | string | 是 | 启动时确定：`value` 非空 → 原值；否则 `os.Getenv(name)` → `unknown` |
| 每个 `Config.Labels.Env[j].Name` | string | 是 | 逐 PID 读 `/proc/<PID>/environ[env_var]`；缺失 → `none`；超长/非法字符 → 合法化后值；基数超限 → `other` |

原有上游标签（`gpu`、`UUID`、`device`、`modelName`、`Hostname`、`pci_bus_id` 等）保留不变。

## 场景示例

假设 `config.yaml`：

```yaml
labels:
  static:
    - name: STUDIO
      value: ai-lab
  env:
    - name: PROJECT
```

### 1) 单用户独占

```
alice 独占 GPU 0，PROJECT=llm-training，GPU_UTIL=85
  ↓
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="alice",STUDIO="ai-lab",PROJECT="llm-training"} 85
```

### 2) 多用户共享（按进程数加权）

```
GPU 0 UTIL=80；alice×3 (PROJECT=proj-a)，bob×1 (PROJECT=proj-b)
  ↓
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",STUDIO="ai-lab",PROJECT="proj-a"} 60
DCGM_FI_DEV_GPU_UTIL{...,USER="bob",  STUDIO="ai-lab",PROJECT="proj-b"} 20
```

**不变量**：同一 `gpu` / `UUID` 下所有样本的 `value` 之和 **严格等于** GPU 真实 UTIL（整数百分比），由末位闭合补偿保证。

### 3) 同用户多项目（按聚合键拆分）

```
alice 在 GPU 0 上开 2 个进程：1 个 PROJECT=proj-a，1 个 PROJECT=proj-b；UTIL=80
聚合键 = (USER, PROJECT)，得两组
  ↓
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",STUDIO="ai-lab",PROJECT="proj-a"} 40
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",STUDIO="ai-lab",PROJECT="proj-b"} 40
```

### 4) GPU 空闲

```
GPU 0 上无计算进程
  ↓
DCGM_FI_DEV_GPU_UTIL{...,USER="none",STUDIO="ai-lab",PROJECT="none"} 0
```

### 5) 进程未 export PROJECT

```
alice 的进程没设 PROJECT；UTIL=50
  ↓
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",STUDIO="ai-lab",PROJECT="none"} 50
```

### 6) UID 无对应账户

```
PID 12345 的 UID=70000，/etc/passwd 无该 UID
  ↓
DCGM_FI_DEV_GPU_UTIL{...,USER="uid:70000",STUDIO="ai-lab",PROJECT="<分摊值>"}
```

### 7) STUDIO 配置为空值 + 环境变量回退

```yaml
labels:
  static:
    - name: STUDIO
      value: ""            # 空值
```

启动时读 `os.Getenv("STUDIO")`；若环境变量为 "dev-host"：

```
DCGM_FI_DEV_GPU_UTIL{...,STUDIO="dev-host",...}
```

若环境变量也为空：

```
DCGM_FI_DEV_GPU_UTIL{...,STUDIO="unknown",...}
```

### 8) 多 env 标签（EXPERIMENT）

```yaml
labels:
  env:
    - name: PROJECT
    - name: EXPERIMENT
      env_var: EXPERIMENT_NAME
```

聚合键 = `USER` + `PROJECT` + `EXPERIMENT` 三元组。同 GPU 上 alice/proj-a/exp1×2 与 alice/proj-a/exp2×2，UTIL=80：

```
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",PROJECT="proj-a",EXPERIMENT="exp1"} 40
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",PROJECT="proj-a",EXPERIMENT="exp2"} 40
```

### 9) 基数超限（Q5）

某 env 标签一轮内出现 200 个不同取值：保留字典序前 128 个；其余 72 个值的 PID 在该轮的对应标签位改写为 `other`，可能导致多组合并。

## 关闭特性时的契约

"特性关闭"只可能通过 **不提供 `config.yaml`** 的方式实现，但该方式会 **使程序拒绝启动**（FR-011）。因此在发布本特性后：
- 用户要么运行带 `--config` 的本分支 exporter → 指标带 USER/static/env 标签；
- 要么使用上游原版二进制（不走本分支）→ 指标按上游原样输出。

本分支不提供"保留二进制但关闭多用户标签"的运行模式。

## 基数保护契约（Q5）

- `static[].Name`：每条基数 = 1。
- `env[].Name`：每轮单标签取值数 ≤ 128；超出归 `other`。
- 标签值字符集：`[A-Za-z0-9_.-]`；超过 64 字符截断；非 UTF-8 字节剔除（防御 `/proc/environ` 中的二进制垃圾）。

## 标签稳定性

- 标签名一旦在 `config.yaml` 中声明，运行期不变；修改需 `systemctl restart`。
- `USER` 由 exporter 注入，位置与取值规则固定；用户无法重命名或屏蔽。
- `DCGM_FI_DEV_GPU_UTIL` 的标签值 **不会** 被用于上报内部错误（例如 `USER="invariant_error"` 之类的特殊取值）。当实现层侦测到"分摊和 ≠ 真实 UTIL"的不变式违例时，仅通过日志与 exporter 自身健康指标（`dcgm_exporter_bare_metal_mapper_invariant_breaches_total`）上报，不污染业务指标的标签空间。
