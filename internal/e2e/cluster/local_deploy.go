/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// waitForGPUAllocatable polls until Kubernetes reports allocatable nvidia.com/gpu resources.
func waitForGPUAllocatable(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, timeout string) error {
	duration, err := time.ParseDuration(timeout)
	if err != nil || duration <= 0 {
		duration, _ = time.ParseDuration(defaultK3dWaitTimeout)
	}
	seconds := int(duration.Seconds())
	if seconds < 1 {
		seconds = 1
	}

	script := fmt.Sprintf(`elapsed=0
while :; do
  output="$(kubectl get nodes -o 'jsonpath={range .items[*]}{.status.allocatable.nvidia\.com/gpu}{"\n"}{end}' 2>/dev/null || true)"
  if printf '%%s\n' "$output" | awk 'BEGIN { sum = 0 } /^[[:space:]]*[0-9]+[[:space:]]*$/ { sum += $1 } END { exit !(sum > 0) }'; then
    printf 'cluster reports allocatable nvidia.com/gpu resources\n'
    exit 0
  fi
  if [ "$elapsed" -ge %d ]; then
    printf 'cluster did not report allocatable nvidia.com/gpu resources before timeout\n' >&2
    exit 1
  fi
  sleep 2
  elapsed=$((elapsed + 2))
done`, seconds)

	return runRequired(ctx, runner, w, "kubectl_wait_gpu_allocatable", e2eexec.Command{Name: "sh", Args: []string{"-c", script}, Env: kubeEnv(cfg)})
}

// ensureRuntimeClass applies the NVIDIA RuntimeClass expected by local validation pods.
func ensureRuntimeClass(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, name string) error {
	manifest := fmt.Sprintf(`apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: %s
handler: nvidia
`, name)
	return runRequired(ctx, runner, w, "kubectl_runtime_class", kubectlWithStdin(cfg, []byte(manifest), "apply", "-f", "-"))
}

// labelFirstNode marks the first node as the default scheduling target for validation workloads.
func labelFirstNode(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig) error {
	result := runner.Run(ctx, kubectl(cfg, "get", "nodes", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}"))
	if result.ExitCode != 0 {
		return resultError("kubectl get nodes", result)
	}
	node := firstLine(string(result.Stdout))
	if node == "" {
		return errors.New("no Kubernetes nodes were returned by kubectl")
	}
	if err := runRequired(ctx, runner, w, "kubectl_label_gpu_node", kubectl(cfg, "label", "node", node, "nvidia.com/gpu.present=true", "--overwrite")); err != nil {
		return err
	}
	return runRequired(ctx, runner, w, "kubectl_label_exporter_node", kubectl(cfg, "label", "node", node, local.NodeSelectorKey+"="+local.NodeSelectorValue, "--overwrite"))
}
