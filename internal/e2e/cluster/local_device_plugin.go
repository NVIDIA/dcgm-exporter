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
	"fmt"
	"io"
	"os"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ensureDevicePlugin installs the default NVIDIA device plugin configuration for local k3d.
func ensureDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig) error {
	return installDevicePlugin(ctx, runner, w, cfg, local, nil)
}

// ConfigureMIGDevicePlugin installs the NVIDIA device plugin in mixed MIG mode.
func ConfigureMIGDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig) error {
	return installDevicePlugin(ctx, runner, w, cfg, local, []string{"--set", "migStrategy=mixed"})
}

// ConfigureSharedGPUDevicePlugin installs a time-sliced NVIDIA device plugin config.
func ConfigureSharedGPUDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig, replicas string) error {
	if replicas == "" {
		replicas = "2"
	}
	configFile, err := os.CreateTemp("", "dcgm-exporter-shared-gpu-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(configFile.Name())
	_, writeErr := fmt.Fprintf(configFile, `version: v1
sharing:
  timeSlicing:
    failRequestsGreaterThanOne: true
    resources:
    - name: nvidia.com/gpu
      replicas: %s
`, replicas)
	closeErr := configFile.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return installDevicePlugin(ctx, runner, w, cfg, local, []string{"--set-file", "config.map.default=" + configFile.Name(), "--set", "config.default=default"})
}

// ResetDevicePlugin restores the default NVIDIA device plugin mode.
func ResetDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig) error {
	return installDevicePlugin(ctx, runner, w, cfg, local, nil)
}

// installDevicePlugin applies the NVIDIA device plugin Helm release with optional mode-specific values.
func installDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig, extraArgs []string) error {
	if err := ensureRuntimeClass(ctx, runner, w, cfg, local.RuntimeClass); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "helm_repo_device_plugin", e2eexec.Command{Name: "helm", Args: []string{"repo", "add", "nvidia-device-plugin", "https://nvidia.github.io/k8s-device-plugin", "--force-update"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "helm_repo_update_device_plugin", e2eexec.Command{Name: "helm", Args: []string{"repo", "update", "nvidia-device-plugin"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	args := []string{
		"upgrade", "--install", "nvidia-device-plugin", "nvidia-device-plugin/nvidia-device-plugin",
		"--namespace", "kube-system",
		"--version", local.DevicePluginVersion,
		"--set", "runtimeClassName=" + local.RuntimeClass,
		"--set-string", "nodeSelector." + strings.ReplaceAll(local.NodeSelectorKey, ".", "\\.") + "=" + local.NodeSelectorValue,
	}
	args = append(args, extraArgs...)
	args = append(args, "--wait", "--timeout", local.WaitTimeout)
	return runRequired(ctx, runner, w, "helm_install_device_plugin", e2eexec.Command{
		Name: "helm",
		Args: args,
		Env:  kubeEnv(cfg),
	})
}
