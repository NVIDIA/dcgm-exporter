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

// PrepareDRAForProbe installs DRA resources before capability planning when configured.
func PrepareDRAForProbe(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context), error) {
	if !draConfigurationEnabled(featureCfg, cfg) || draResourcesAvailable(ctx, runner, cfg) {
		return func(context.Context) {}, nil
	}
	if err := InstallDRA(ctx, runner, w, cfg, featureCfg); err != nil {
		if draConfigurationRequired(featureCfg, cfg) {
			return func(context.Context) {}, err
		}
		fmt.Fprintf(w, "[e2e] WARN NVIDIA DRA driver installation failed; DRA scenarios will be skipped unless resources already exist: %v\n", err)
		return func(context.Context) {}, nil
	}
	if err := WaitForDRAResources(ctx, runner, cfg, timeoutFor(featureCfg)); err != nil {
		if draConfigurationRequired(featureCfg, cfg) {
			return func(context.Context) {}, err
		}
		fmt.Fprintf(w, "[e2e] WARN NVIDIA DRA resources were not published; DRA scenarios will be skipped: %v\n", err)
	}
	return func(cleanupCtx context.Context) { _ = CleanupDRA(cleanupCtx, runner, w, cfg, featureCfg) }, nil
}

// PrepareDRA installs DRA resources for a selected DRA scenario.
func PrepareDRA(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context), error) {
	if !draConfigurationEnabled(featureCfg, cfg) || draResourcesAvailable(ctx, runner, cfg) {
		return func(context.Context) {}, nil
	}
	if err := InstallDRA(ctx, runner, w, cfg, featureCfg); err != nil {
		return func(context.Context) {}, err
	}
	if err := WaitForDRAResources(ctx, runner, cfg, timeoutFor(featureCfg)); err != nil {
		return func(cleanupCtx context.Context) { _ = CleanupDRA(cleanupCtx, runner, w, cfg, featureCfg) }, err
	}
	return func(cleanupCtx context.Context) { _ = CleanupDRA(cleanupCtx, runner, w, cfg, featureCfg) }, nil
}

// InstallDRA installs the NVIDIA DRA driver chart.
func InstallDRA(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	version := strings.TrimSpace(featureCfg.Dependencies.DRADriverVersion)
	if version == "" {
		return fmt.Errorf("NVIDIA_DRA_DRIVER_VERSION is required")
	}
	timeout := timeoutFor(featureCfg)
	if err := runRequired(ctx, runner, w, "kubectl_label_dra_nodes", kubectl(cfg, "label", "nodes", "--all", "nvidia.com/dra-kubelet-plugin=true", "--overwrite")); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "helm_repo_dra", e2eexec.Command{Name: "helm", Args: []string{"repo", "add", "nvidia", "https://helm.ngc.nvidia.com/nvidia", "--force-update"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "helm_repo_update_dra", e2eexec.Command{Name: "helm", Args: []string{"repo", "update"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	args := []string{
		"upgrade", "--install", "nvidia-dra-driver-gpu", "nvidia/nvidia-dra-driver-gpu",
		"--namespace", "nvidia-dra-driver-gpu",
		"--create-namespace",
		"--version", version,
		"--set", "resources.gpus.enabled=true",
		"--set", "resources.computeDomains.enabled=false",
		"--set", "gpuResourcesEnabledOverride=true",
		"--set", "nvidiaDriverRoot=/",
		"--wait",
		"--timeout", timeout,
	}
	return runRequired(ctx, runner, w, "helm_install_dra", e2eexec.Command{Name: "helm", Args: args, Env: kubeEnv(cfg)})
}

// CleanupDRA removes DRA resources installed by validation.
func CleanupDRA(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	timeout := timeoutFor(featureCfg)
	_ = runRequired(ctx, runner, w, "helm_uninstall_dra", e2eexec.Command{Name: "helm", Args: []string{"uninstall", "nvidia-dra-driver-gpu", "--namespace", "nvidia-dra-driver-gpu", "--ignore-not-found", "--wait", "--timeout", timeout}, Env: kubeEnv(cfg)})
	_ = runRequired(ctx, runner, w, "kubectl_delete_dra_namespace", kubectl(cfg, "delete", "namespace", "nvidia-dra-driver-gpu", "--ignore-not-found=true"))
	_ = runRequired(ctx, runner, w, "kubectl_unlabel_dra_nodes", kubectl(cfg, "label", "nodes", "--all", "nvidia.com/dra-kubelet-plugin-", "--overwrite"))
	return nil
}

// WaitForDRAResources waits until ResourceSlices are published.
func WaitForDRAResources(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	for {
		if draResourcesAvailable(ctx, runner, cfg) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for NVIDIA DRA resources")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// draConfigurationEnabled reports whether validation may install DRA resources.
func draConfigurationEnabled(featureCfg FeatureConfig, cfg Config) bool {
	switch strings.ToLower(strings.TrimSpace(featureCfg.DRAConfigure)) {
	case "true":
		return true
	case "auto", "":
		return cfg.LocalK3D()
	default:
		return false
	}
}

// draConfigurationRequired reports whether DRA setup failure should fail the run.
func draConfigurationRequired(featureCfg FeatureConfig, cfg Config) bool {
	switch strings.ToLower(strings.TrimSpace(featureCfg.DRAConfigure)) {
	case "true":
		return true
	default:
		return false
	}
}

// draResourcesAvailable reports whether ResourceSlice objects already exist in the cluster.
func draResourcesAvailable(ctx context.Context, runner e2eexec.Runner, cfg Config) bool {
	result := runner.Run(ctx, kubectl(cfg, "get", "resourceslices", "-A", "-o", "name"))
	return result.ExitCode == 0 && strings.Contains(strings.ToLower(string(result.Stdout)), "resourceslice")
}
