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
	"strings"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	runtimePreflightPodName            = "dcgm-exporter-e2e-runtime-preflight"
	runtimePreflightTimeout            = 2 * time.Minute
	runtimePreflightDiagnosticsTimeout = 30 * time.Second
)

// preflightLocalGPURuntime verifies the NVIDIA runtime inside a k3d node before
// the device plugin makes GPU-dependent setup wait on it.
func preflightLocalGPURuntime(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, local LocalConfig) (resultErr error) {
	image, err := ResolveLocalK3DImage(local.RuntimePreflightImage)
	if err != nil {
		return err
	}
	if err := TagLocalK3DImage(ctx, runner, w, image, "docker_tag_runtime_preflight_image", nil); err != nil {
		return err
	}
	imported, err := ImportImageIfPresentWithResult(ctx, runner, w, cfg, image.Local, "k3d_import_runtime_preflight_image")
	if err != nil {
		return err
	}
	if !imported {
		return fmt.Errorf("nested GPU runtime preflight requires host-local image %s; run the GPU runtime bootstrap before local k3d setup", image.Local)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := run(w, runner, cleanupCtx, "kubectl_delete_runtime_preflight", kubectl(cfg, "delete", "pod", runtimePreflightPodName, "--ignore-not-found=true", "--wait=false")); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("clean nested GPU runtime preflight: %w", err))
		}
	}()

	if err := runRequired(ctx, runner, w, "kubectl_apply_runtime_preflight", kubectlWithStdin(cfg, []byte(runtimePreflightManifest(local, image.Local)), "apply", "-f", "-")); err != nil {
		return err
	}
	timeout := localRuntimePreflightTimeout(local.WaitTimeout)
	if err := runRequired(ctx, runner, w, "kubectl_wait_runtime_preflight", kubectl(cfg,
		"wait",
		"--for=jsonpath={.status.phase}=Succeeded",
		"pod/"+runtimePreflightPodName,
		"--timeout="+timeout.String(),
	)); err != nil {
		diagnosticsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimePreflightDiagnosticsTimeout)
		defer cancel()
		runtimePreflightDiagnostics(diagnosticsCtx, runner, w, cfg)
		return fmt.Errorf("nested GPU runtime preflight failed: %w", err)
	}
	return nil
}

// runtimePreflightManifest creates the isolated nested-runtime validation pod.
// The pod runs only nvidia-smi, so it turns off the image's CUDA version
// requirement: in legacy mode the runtime refuses a CUDA image newer than the
// driver's CUDA version on GPUs, such as GeForce, that the image does not list
// by driver branch.
func runtimePreflightManifest(local LocalConfig, image string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  labels:
    app.kubernetes.io/managed-by: dcgm-exporter-e2e
spec:
  restartPolicy: Never
  runtimeClassName: %s
  nodeSelector:
    %s: %s
  containers:
  - name: gpu-runtime
    image: %s
    imagePullPolicy: Never
    env:
    - name: NVIDIA_VISIBLE_DEVICES
      value: all
    - name: NVIDIA_DRIVER_CAPABILITIES
      value: utility
    - name: NVIDIA_DISABLE_REQUIRE
      value: "true"
    command: ["nvidia-smi", "-L"]
`, runtimePreflightPodName, local.RuntimeClass, local.NodeSelectorKey, local.NodeSelectorValue, image)
}

// localRuntimePreflightTimeout bounds the diagnostic check below the general setup timeout.
func localRuntimePreflightTimeout(value string) time.Duration {
	timeout := parseDuration(value, runtimePreflightTimeout)
	if timeout > runtimePreflightTimeout {
		return runtimePreflightTimeout
	}
	return timeout
}

// runtimePreflightDiagnostics retains pod and k3d-node evidence when the nested hook cannot start.
func runtimePreflightDiagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) {
	commands := []struct {
		name    string
		command e2eexec.Command
	}{
		{"kubectl_describe_runtime_preflight", kubectl(cfg, "describe", "pod", runtimePreflightPodName)},
		{"kubectl_runtime_preflight_logs", kubectl(cfg, "logs", "pod/"+runtimePreflightPodName, "--all-containers=true", "--ignore-errors=true", "--tail=200")},
		{"kubectl_runtime_preflight_events", kubectl(cfg, "get", "events", "--field-selector=involvedObject.kind=Pod,involvedObject.name="+runtimePreflightPodName, "--sort-by=.lastTimestamp")},
	}
	for _, diagnostic := range commands {
		_ = run(w, runner, ctx, diagnostic.name, diagnostic.command)
	}
	for _, node := range localRuntimePreflightNodes(ctx, runner, cfg.ClusterName) {
		_ = run(w, runner, ctx, "docker_runtime_preflight_node_logs_"+strings.TrimPrefix(node, "k3d-"), e2eexec.Command{
			Name: "docker", Args: []string{"logs", "--tail=200", node},
		})
	}
}

// localRuntimePreflightNodes returns owned k3d nodes without turning best-effort diagnostics into setup failures.
func localRuntimePreflightNodes(ctx context.Context, runner e2eexec.Runner, clusterName string) []string {
	nodes, err := localClusterDockerNodes(ctx, runner, clusterName)
	if err != nil {
		return nil
	}
	return nodes
}
