# Quickstart: 裸金属 GPU 服务器多用户利用率统计

**Feature**: `001-multi-user-gpu-util`
**目标读者**: 集群/服务器运维
**目标**: 10 分钟内在一台裸金属 GPU 服务器上，从源码构建并以 systemd service 形式运行带 USER + 自定义标签的 dcgm-exporter。

> 本方案 **仅** 针对裸金属 + systemd 场景。K8s / 容器化不在支持范围（Clarification Q2）。

---

## 前置条件

- Linux 主机（Ubuntu 22.04 / RHEL 9，内核 ≥ 5.10）。
- NVIDIA 驱动 + DCGM（`nvidia-dcgm.service` active）：
  ```bash
  systemctl status nvidia-dcgm
  nvidia-smi
  dcgmi discovery -l
  ```
- Go 工具链 ≥ 1.24。
- root 权限（读取 `/proc/<PID>/environ` 与 systemd 操作）。

---

## 1. 构建二进制

```bash
cd /opt/src
git clone https://github.com/<your-fork>/dcgm-exporter.git
cd dcgm-exporter
git checkout 001-multi-user-gpu-util

make binary            # 产物：./cmd/dcgm-exporter/dcgm-exporter
```

---

## 2. 安装二进制、counters CSV

```bash
sudo install -m 0755 ./cmd/dcgm-exporter/dcgm-exporter /usr/bin/dcgm-exporter
sudo install -d /etc/dcgm-exporter
sudo install -m 0644 ./etc/default-counters.csv /etc/dcgm-exporter/default-counters.csv
```

`default-counters.csv` 继续由上游既有机制负责"要采集哪些 DCGM 指标"，**不进入** `config.yaml`（Clarification Q1）。

---

## 3. 准备 `config.yaml`

```bash
sudo tee /etc/dcgm-exporter/config.yaml > /dev/null <<'YAML'
labels:
  static:
    - name: STUDIO
      value: "ai-lab"

  env:
    - name: PROJECT             # env_var 默认 = name
    # 若想再追加 EXPERIMENT 标签，把下面两行去掉注释：
    # - name: EXPERIMENT
    #   env_var: EXPERIMENT_NAME

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
YAML
sudo chmod 0644 /etc/dcgm-exporter/config.yaml
```

**提醒**：
- `labels` 与 `server` 是仅有的两个顶层键，写其他键会让 exporter 启动失败。
- 若 `labels` 节整体缺失或两数组都为空，系统自动合成默认标签集：`static: [STUDIO(value="")]` + `env: [PROJECT]`（FR-012）。
- `static` 的 `value` 为空会回退到 **同名环境变量**，再空为 `unknown`（FR-003）。

---

## 4. 安装 systemd unit

```bash
sudo cp specs/001-multi-user-gpu-util/contracts/dcgm-exporter.service \
        /etc/systemd/system/dcgm-exporter.service

sudo systemctl daemon-reload
sudo systemctl enable --now dcgm-exporter
sudo systemctl status dcgm-exporter --no-pager
# Expected: active (running)
```

如果忘记放 `config.yaml` 就启动，服务会立刻失败（Clarification Q4）；`journalctl -u dcgm-exporter -n 20` 会看到：

```
config.yaml not found at /etc/dcgm-exporter/config.yaml
```

---

## 5. 验证

### 5.1 空闲状态

```bash
curl -s http://localhost:9400/metrics | grep DCGM_FI_DEV_GPU_UTIL
```

预期（`STUDIO` 取 `value` 或环境变量或 `unknown`）：

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="none",STUDIO="ai-lab",PROJECT="none"} 0
```

### 5.2 单用户场景

```bash
export PROJECT=llm-training
python train.py &
```

```
DCGM_FI_DEV_GPU_UTIL{gpu="0",...,USER="alice",STUDIO="ai-lab",PROJECT="llm-training"} <util>
```

同时非 UTIL 指标保持原形：

```bash
curl -s http://localhost:9400/metrics | grep DCGM_FI_DEV_GPU_TEMP
# → 不含 USER / STUDIO / PROJECT 标签
```

### 5.3 多用户场景

`alice×3 (PROJECT=proj-a)` + `bob×1 (PROJECT=proj-b)`，GPU_UTIL=80：

```
DCGM_FI_DEV_GPU_UTIL{...,USER="alice",STUDIO="ai-lab",PROJECT="proj-a"} 60
DCGM_FI_DEV_GPU_UTIL{...,USER="bob",  STUDIO="ai-lab",PROJECT="proj-b"} 20
```

两条之和 = 80（严格相等）。

### 5.4 故障自愈

```bash
sudo systemctl kill --signal=SIGKILL dcgm-exporter
sudo systemctl status dcgm-exporter --no-pager
# 3 秒内应回到 active (running)
```

### 5.5 配置变更生效

```bash
sudo sed -i 's/ai-lab/ai-lab-phase2/' /etc/dcgm-exporter/config.yaml
sudo systemctl restart dcgm-exporter
curl -s http://localhost:9400/metrics | grep 'DCGM_FI_DEV_GPU_UTIL{' | head -1
# STUDIO 新值已生效
```

---

## 6. 故障排查

| 症状 | 排查方向 |
|------|---------|
| 服务启动即失败，日志显示 `config.yaml not found` | 确认 `/etc/dcgm-exporter/config.yaml` 存在，或通过 `--config` 指定其他路径 |
| 所有 `USER` 都是 `uid:<n>` | 检查 `/etc/passwd` / `sssd` 能否解析该 UID |
| 所有 `env` 标签都是 `none` | 确认 exporter 以 root 运行；`cat /proc/<PID>/environ \| xargs -0 -n1 \| grep <ENV_VAR>=` 验证目标进程确实 export 了变量 |
| 启动报 "unknown field ..." | `config.yaml` 顶层只允许 `labels` 和 `server`；检查拼写 |
| 启动报 "label name conflicts with system-reserved" | `labels.*.name` 与上游/内置标签重名；改名 |
| `env` 标签出现 `other` | 该标签本轮取值超过 128 个，属基数保护行为，正常 |
| 分摊之和与 `nvidia-smi` 的 UTIL 偏差 ≥ 1 | 检查是否存在图形类进程（本特性只统计计算进程）；也可能是读 `/proc/environ` 权限受限，对应 PID 被降级 |
