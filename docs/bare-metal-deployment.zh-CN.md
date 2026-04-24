# 裸金属服务器部署指南：DCGM_FI_DEV_GPU_UTIL 多用户使用率统计

> 英文版本：[bare-metal-deployment.md](./bare-metal-deployment.md)
> 特性来源：`specs/001-multi-user-gpu-util/`

本文档介绍如何在一台 **裸金属 Linux GPU 服务器** 上以 systemd service
模式部署本特性增强版的 dcgm-exporter，从而让 `DCGM_FI_DEV_GPU_UTIL`
指标自动携带"哪个用户、哪个项目"的标签，并在多用户共享同一张 GPU 时
按进程数加权拆分到每位用户。

> **范围说明**：本特性 **只针对裸金属 + systemd 部署** 设计与测试，**不适用于
> Kubernetes / 容器化** 场景。如果您在 K8s 集群里运行 GPU 工作负载，请继
> 续使用上游原生 dcgm-exporter 的 Helm Chart 或 DaemonSet。

---

## 目录

1. [它能做什么](#1-它能做什么)
2. [前置条件](#2-前置条件)
3. [构建二进制](#3-构建二进制)
4. [一键安装](#4-一键安装)
5. [配置 config.yaml](#5-配置-configyaml)
6. [启动 systemd 服务](#6-启动-systemd-服务)
7. [验证效果](#7-验证效果)
8. [常见运维操作](#8-常见运维操作)
9. [回滚与卸载](#9-回滚与卸载)
10. [故障排查](#10-故障排查)
11. [设计取舍 FAQ](#11-设计取舍-faq)

---

## 1. 它能做什么

**只增强一个指标**：`DCGM_FI_DEV_GPU_UTIL`。

### 1.1 单用户独占 GPU 时

```
DCGM_FI_DEV_GPU_UTIL{gpu="0", USER="alice", STUDIO="ai-lab", PROJECT="llm-training", ...} 85
```

自动附加：
- `USER` —— 通过 PID → UID → 用户名解析（系统自动注入，**不可关闭**）
- `STUDIO` 等 —— 您在 `config.yaml` 里声明的"静态标签"
- `PROJECT` 等 —— 您在 `config.yaml` 里声明的"环境变量标签"

### 1.2 多用户共享 GPU 时（按进程数加权分摊）

GPU 0 的真实 UTIL = 80%；alice 跑 3 个进程（PROJECT=proj-a），bob 跑 1 个进程（PROJECT=proj-b）：

```
DCGM_FI_DEV_GPU_UTIL{..., USER="alice", PROJECT="proj-a"} 60   # 3/4 × 80
DCGM_FI_DEV_GPU_UTIL{..., USER="bob",   PROJECT="proj-b"} 20   # 1/4 × 80
```

**两条之和 = 80**（与真实 GPU UTIL 严格相等，由"末位闭合补偿"算法保证，
不会因取整少一个百分点）。

### 1.3 GPU 空闲时

```
DCGM_FI_DEV_GPU_UTIL{..., USER="none", STUDIO="ai-lab", PROJECT="none"} 0
```

输出占位记录，避免 Prometheus 时间序列断点。

### 1.4 其他 DCGM 指标完全不受影响

`DCGM_FI_DEV_GPU_TEMP`、`DCGM_FI_DEV_POWER_USAGE`、`DCGM_FI_DEV_FB_USED`
等所有非 UTIL 指标 **逐字节** 保持上游原生 dcgm-exporter 的输出，您原有
的 Grafana 看板、Prometheus 告警规则**无需改动**。

---

## 2. 前置条件

### 2.1 操作系统

- Linux（推荐 Ubuntu 22.04 / 24.04，或 RHEL 9）
- 内核 ≥ 5.10
- root 权限（exporter 必须以 root 运行才能读取其他用户的 `/proc/<PID>/environ`）

### 2.2 NVIDIA 驱动 + CUDA

```bash
nvidia-smi   # 应能列出 GPU
```

### 2.3 NVIDIA DCGM 4.x（**必须是 4.x**，3.x 不兼容）

```bash
# 添加 NVIDIA CUDA 仓库（Ubuntu 24.04 amd64 示例）
cd /tmp
wget https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb
sudo dpkg -i cuda-keyring_1.1-1_all.deb
sudo apt-get update

# 安装 DCGM 4
sudo apt-get install -y datacenter-gpu-manager-4-cuda12

# 启动 DCGM 后台服务
sudo systemctl enable --now nvidia-dcgm
sudo systemctl status nvidia-dcgm

# 验证
dcgmi discovery -l    # 应能列出 GPU
ldconfig -p | grep libdcgm.so.4   # 应有 libdcgm.so.4
```

> ⚠️ 如果您之前装的是 DCGM 3.x（`datacenter-gpu-manager` 不带版本号的包），
> 请先 `sudo apt remove datacenter-gpu-manager`，再装 DCGM 4，否则 exporter
> 启动会报 `the libdcgm.so.4 library was not found`。

### 2.4 Go 1.24+（仅构建主机需要，目标主机不需要）

```bash
go version
```

---

## 3. 构建二进制

```bash
git clone https://github.com/<您的-fork>/dcgm-exporter.git
cd dcgm-exporter
git checkout 001-multi-user-gpu-util
make binary
# 产物：./cmd/dcgm-exporter/dcgm-exporter
```

---

## 4. 一键安装

```bash
sudo GO=$(which go) make install
```

> **注意**：`sudo` 默认会清空 `PATH`，所以需要显式传 `GO=$(which go)`，
> 否则 `make install` 内部的 `go build` 找不到 go。

`make install` 做了哪些事：

| 来源 | 安装到 | 行为 |
|------|--------|------|
| `cmd/dcgm-exporter/dcgm-exporter` | `/usr/bin/dcgm-exporter` | 每次覆盖 |
| `etc/default-counters.csv` | `/etc/dcgm-exporter/default-counters.csv` | 每次覆盖 |
| `config.yaml`（仓库根） | `/etc/dcgm-exporter/config.yaml` | **仅当目标不存在时安装**——保护运维已编辑的配置 |
| `packaging/config-files/systemd/nvidia-dcgm-exporter.service` | `/etc/systemd/system/dcgm-exporter.service` | 每次覆盖 |

---

## 5. 配置 config.yaml

编辑 `/etc/dcgm-exporter/config.yaml`，**只允许两个顶层节**：`labels` 和 `server`。

### 5.1 最小可用示例

```yaml
labels:
  static:
    - name: STUDIO
      value: "ai-lab"          # 替换为您服务器/工作室的名称

  env:
    - name: PROJECT             # 从进程的 PROJECT 环境变量读取

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
```

### 5.2 多标签示例

```yaml
labels:
  static:
    - name: STUDIO
      value: "ai-lab"
    - name: CLUSTER
      value: ""                 # 留空 → 启动时读 $CLUSTER 环境变量 → 仍空则取 "unknown"

  env:
    - name: PROJECT
    - name: EXPERIMENT          # 用户的进程需 export EXPERIMENT
      env_var: EXPERIMENT_NAME  # 标签名是 EXPERIMENT，但读取的是 $EXPERIMENT_NAME

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
```

### 5.3 关键规则速查

| 规则 | 说明 |
|------|------|
| 顶层只允许 `labels` 和 `server` | 出现 `kubernetes:`、`log_level:` 等其他键 → 启动失败 |
| `labels.static[].name` / `labels.env[].name` 命名 | 必须符合 Prometheus 规则 `[a-zA-Z_][a-zA-Z0-9_]*` |
| 不可与系统保留标签同名 | `USER`、`gpu`、`UUID`、`device`、`modelName`、`Hostname`、`container`、`namespace`、`pod`、`exported_*` 等 |
| `static.value` 为空时的回退链 | `value` 空 → 读同名 OS 环境变量 → 仍空 → 取 `"unknown"`（启动时确定一次，运行期不变） |
| `env.env_var` 省略时 | 默认等于 `name` |
| env 标签值的合法化 | 仅保留 `[A-Za-z0-9_.-]`，其他字符替换为 `_`；超过 64 字符截断 |
| 单标签每轮基数上限 | 128（按字典序保留前 128，其余整批归并为 `"other"`，防止 TSDB 基数爆炸） |
| **`config.yaml` 缺失** | **服务硬退出**，日志输出 `config.yaml not found at <path>`；不会静默回退到内置默认 |

### 5.4 Static 与 Env 标签的差异

| 维度 | `labels.static[]` | `labels.env[]` |
|------|-------------------|----------------|
| 取值时机 | 启动时一次 | 每次采集，按 PID 动态读 `/proc/<PID>/environ` |
| 同主机各 GPU/各用户 | **同值** | **可能不同** |
| 是否参与"多用户分摊聚合键" | **否**（单机恒定） | **是**（USER + 所有 env 标签的组合） |
| 修改生效方式 | `systemctl restart` | 用户重启进程时新 `export` 即生效 |

---

## 6. 启动 systemd 服务

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dcgm-exporter
sudo systemctl status dcgm-exporter --no-pager
# Active: active (running)
```

日志位置：
- `/var/log/dcgm-exporter.log`（unit 文件中 `StandardOutput=append:` 指向此处）
- `journalctl -u dcgm-exporter -n 200`（systemd 视角）

systemd unit 关键参数：

| 参数 | 值 | 含义 |
|------|---|------|
| `Restart` | `on-failure` | 异常退出自动拉起 |
| `RestartSec` | `3s` | 拉起前等待 3 秒 |
| `StartLimitBurst` / `StartLimitIntervalSec` | `5` / `30s` | 30 秒内最多重启 5 次，防崩溃循环 |
| `Requires` | `nvidia-dcgm.service` | DCGM 未就绪不启动 |
| `User` | `root` | 必须 root，否则读不到其他用户的 environ |

---

## 7. 验证效果

### 7.1 GPU 空闲（无任何 CUDA 进程）

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
```

**预期**：

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="none",STUDIO="ai-lab",PROJECT="none"} 0
```

### 7.2 单用户负载

启动一个使用 GPU 的训练进程（任何 CUDA 程序均可），并在启动前 `export PROJECT=...`：

```bash
export PROJECT=llm-training
python train.py &
```

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL'
```

**预期**：

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="<您的用户名>",STUDIO="ai-lab",PROJECT="llm-training"} <利用率>
```

### 7.3 多用户共享负载

在多个 shell（不同 UID 或同 UID 不同 PROJECT）里同时启动多个训练进程：

```bash
# Shell 1（root）
PROJECT=proj-a ./your-trainer &
PROJECT=proj-a ./your-trainer &
PROJECT=proj-a ./your-trainer &

# Shell 2（切换到普通用户）
sudo -u ubuntu env PROJECT=proj-b ./your-trainer &
```

`DCGM_FI_DEV_GPU_UTIL` 会按 (USER, PROJECT) 分组并加权拆分，例如：

```
DCGM_FI_DEV_GPU_UTIL{...,USER="root",   PROJECT="proj-a"} 27
DCGM_FI_DEV_GPU_UTIL{...,USER="ubuntu", PROJECT="proj-b"}  9
```

两条之和 = 该 GPU 当时的真实 UTIL（这里是 ~36%）。

### 7.4 非 UTIL 指标完全未变

```bash
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_TEMP'
# DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-...",...} 41
```

无 USER/STUDIO/PROJECT 标签 —— 与上游原生 dcgm-exporter 一字不差。

### 7.5 崩溃自愈

```bash
PID=$(systemctl show -p MainPID dcgm-exporter | cut -d= -f2)
sudo kill -9 $PID
sleep 5
systemctl status dcgm-exporter --no-pager | head -5
# 预期：3 秒内已重启，PID 是新值，状态 active (running)
```

### 7.6 配置变更生效

```bash
sudo sed -i 's/ai-lab/ai-lab-phase2/' /etc/dcgm-exporter/config.yaml
sudo systemctl restart dcgm-exporter
sleep 3
curl -s http://localhost:9400/metrics | grep '^DCGM_FI_DEV_GPU_UTIL{' | head -1
# 现在 STUDIO 显示 ai-lab-phase2
```

---

## 8. 常见运维操作

| 任务 | 命令 |
|------|------|
| 查看实时日志 | `sudo journalctl -u dcgm-exporter -f` |
| 查看最近错误 | `sudo journalctl -u dcgm-exporter -p err -n 50` |
| 修改配置后生效 | `sudo systemctl restart dcgm-exporter` |
| 临时停服 | `sudo systemctl stop dcgm-exporter` |
| 取消开机自启 | `sudo systemctl disable dcgm-exporter` |
| 用不同端口临时调试 | `sudo /usr/bin/dcgm-exporter --config /etc/dcgm-exporter/config.yaml -f /etc/dcgm-exporter/default-counters.csv -a :19400` |
| 升级二进制 | 重新 `sudo GO=$(which go) make install` 后 `systemctl restart dcgm-exporter`（您的 `config.yaml` 会被保留） |

### CLI 参数与 YAML 的优先级

> 内置默认 < `config.yaml` < 命令行参数

例如：

```bash
# /etc/dcgm-exporter/config.yaml 写的是 server.port: ":9400"
# 但临时想在 :19400 跑：
sudo /usr/bin/dcgm-exporter --config /etc/dcgm-exporter/config.yaml \
    -f /etc/dcgm-exporter/default-counters.csv -a :19400
# CLI 的 -a 会覆盖 YAML 的 server.port
```

注意：**只有显式传了 `-a`** 才会覆盖；只是写默认值不算"显式"。

---

## 9. 回滚与卸载

### 9.1 仅"关闭多用户标签"，保留二进制

把 `config.yaml` 改成最小默认形式（只剩 `STUDIO`+`PROJECT`），重启：

```bash
sudo tee /etc/dcgm-exporter/config.yaml >/dev/null <<'YAML'
labels:
  static:
    - name: STUDIO
      value: ""
  env:
    - name: PROJECT
server:
  port: ":9400"
YAML
sudo systemctl restart dcgm-exporter
```

> ⚠️ 本特性 **不支持** 通过 `config.yaml` 完全关闭"多用户标签"功能 —— 只
> 要服务在跑，`DCGM_FI_DEV_GPU_UTIL` 上就一定带 `USER` + 您声明的 static
> /env 标签。如需完全回到上游原生输出，请走下一节。

### 9.2 完全卸载本特性、回到上游原生 exporter

```bash
sudo systemctl disable --now dcgm-exporter
sudo make -C /path/to/dcgm-exporter uninstall
# 然后安装上游原生 dcgm-exporter 二进制及其 unit 文件
```

`make uninstall` **故意保留** `/etc/dcgm-exporter/`（含您编辑过的
`config.yaml` 与 `default-counters.csv`），避免运维成果丢失。

---

## 10. 故障排查

| 症状 | 可能原因 / 解决方案 |
|------|---------------------|
| 启动失败，日志显示 `config.yaml not found at /etc/dcgm-exporter/config.yaml` | 文件被删或路径不对。重新 `make install`，或检查 `--config` 参数。 |
| 启动失败，日志显示 `the libdcgm.so.4 library was not found` | 装的是 DCGM 3.x，请按 [§2.3](#23-nvidia-dcgm-4x必须是-4x3x-不兼容) 升级到 DCGM 4。 |
| 启动失败，日志显示 `unknown field "kubernetes"`（或类似） | `config.yaml` 顶层只允许 `labels` 和 `server`，删除多余键。 |
| 启动失败，日志显示 `"USER" is a system-reserved label name` | 不要在 `config.yaml` 里声明 `USER`、`gpu`、`UUID` 等系统保留名。 |
| 所有 `USER` 都是 `uid:<n>` 形式 | 该 UID 在 `/etc/passwd`/sssd/LDAP 里查不到用户名。属于系统设置，不是 exporter bug。 |
| 所有 env 标签都是 `none` | (1) exporter 是否以 root 运行？(2) 用户的进程是否真的 `export` 了变量？用 `sudo cat /proc/<PID>/environ \| xargs -0 -n1 \| grep <KEY>=` 自查。 |
| 某个 env 标签值出现 `"other"` | 该标签当前轮取值数已超过 128，多出的整批被合并到 `other`。检查是否有用户在 `PROJECT` 里塞时间戳/UUID 之类高基数字符串。 |
| GPU UTIL 分摊之和与 `nvidia-smi` 显示偏差较大 | (1) 本特性只统计**计算进程**，纯图形进程不计；(2) `nvidia-smi` 与 DCGM 采样窗口不同，瞬时值会有几个百分点差异；(3) `/proc/<PID>/environ` 读不到的 PID 会被跳过。 |
| 服务启动后立即 fail → restart 循环 | `journalctl -u dcgm-exporter -n 50` 看根本原因，最常见是 `config.yaml` 校验失败。`StartLimitBurst=5/StartLimitIntervalSec=30s` 触发后会停止重启，避免占用 CPU。 |
| Prometheus 抓取超时 | 调大 `server.read_timeout` / `server.write_timeout`，并核查 `nvidia-dcgm.service` 是否健康。 |

---

## 11. 设计取舍 FAQ

**Q1：为什么 config.yaml 不能控制采集哪些指标？**
**A**：要采哪些 DCGM 指标继续由原有的 `default-counters.csv` 负责，
本特性的 `config.yaml` 只承担"标签设计"职责。这样两个关注点解耦：
您可以独立调整"采什么指标"和"打什么标签"，不会互相影响，也不会破坏
上游既有运维流程。

**Q2：为什么不允许在 config.yaml 关闭本特性？**
**A**：本特性是这个 fork 的核心增量，"成功加载 config.yaml 就视为
启用"是最小化配置面的设计。如果运维想要回到上游原生输出，应该改用
上游 binary（[§9.2](#92-完全卸载本特性回到上游原生-exporter)）。

**Q3：为什么单标签每轮基数限制写死成 128？**
**A**：一来上游运维不会频繁碰这个值，暴露成配置反而增加误用面；
二来 128 是 Prometheus TSDB 在常见场景下的安全阈值，超过 128 个 PROJECT
基本可以判断是用户在 env 里塞了时间戳/UUID 之类反模式，应当通过教育
用户解决而非放大阈值。如果您的场景确实需要更高基数，源码常量在
`internal/pkg/appconfig/const.go:MaxEnvCardinalityPerCycle`，重新构建即可。

**Q4：为什么必须以 root 运行？**
**A**：读取其他用户的 `/proc/<PID>/environ` 在 Linux 上需要权限。
若您有强烈降权需求，可以给 exporter 进程授予 `CAP_SYS_PTRACE`
能力（修改 systemd unit `User=` + `AmbientCapabilities=`），但默认
unit 选择简单稳妥的 `User=root`。

**Q5：如何在 Prometheus / Grafana 里聚合？**
**A**：本特性输出的标签可以直接被 Prometheus 函数聚合，例如：

```promql
# 各用户瞬时 GPU 利用率（跨 GPU 求和）
sum by (USER) (DCGM_FI_DEV_GPU_UTIL)

# 各项目过去 1 小时的平均利用率
avg_over_time(
  sum by (PROJECT) (DCGM_FI_DEV_GPU_UTIL)[1h:]
)

# 某 STUDIO 下各 (用户, 项目) 组合的累计 GPU 时长（小时）
sum by (USER, PROJECT) (
  rate(DCGM_FI_DEV_GPU_UTIL{STUDIO="ai-lab"}[5m])
) * 3600 / 100
```

---

## 附：相关文件位置速查

| 角色 | 路径 |
|------|------|
| 二进制 | `/usr/bin/dcgm-exporter` |
| 配置文件 | `/etc/dcgm-exporter/config.yaml` |
| 计数器定义 | `/etc/dcgm-exporter/default-counters.csv` |
| systemd unit | `/etc/systemd/system/dcgm-exporter.service` |
| 标准日志 | `/var/log/dcgm-exporter.log` |
| 指标端点 | `http://<host>:9400/metrics` |
| 仓库内规格 | `specs/001-multi-user-gpu-util/spec.md` |
| 仓库内契约 | `specs/001-multi-user-gpu-util/contracts/` |
| 端到端验证证据 | `specs/001-multi-user-gpu-util/validation-log.md` |
| 英文版 runbook | `docs/bare-metal-deployment.md` |
