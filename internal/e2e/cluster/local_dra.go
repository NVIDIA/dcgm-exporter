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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	nvidiaDRADriverName   = "gpu.nvidia.com"
	draDriverReleaseName  = "nvidia-dra-driver-gpu"
	draDriverNamespace    = "nvidia-dra-driver-gpu"
	draNodePluginLabelKey = "nvidia.com/dra-kubelet-plugin"
)

// PrepareDRA labels the nodes and installs the NVIDIA DRA driver when this run may set up DRA and the
// cluster publishes no NVIDIA GPU ResourceSlices. The returned cleanup undoes what PrepareDRA changed.
// It is returned with a setup error too, because a failed install can leave a partial release.
func PrepareDRA(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context) error, error) {
	needed, err := draSetupNeeded(ctx, runner, cfg, featureCfg)
	if err != nil || !needed {
		return noLocalCleanup, err
	}
	if _, err := draDriverVersion(featureCfg); err != nil {
		return noLocalCleanup, err
	}

	unlabel, err := labelDRANodes(ctx, runner, w, cfg)
	if err != nil {
		return unlabel, err
	}

	uninstall, err := installDRADriver(ctx, runner, w, cfg, featureCfg)
	return joinCleanups(uninstall, unlabel), err
}

// prepareDRADriver installs the NVIDIA DRA driver like PrepareDRA, on nodes the caller has already labeled.
func prepareDRADriver(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context) error, error) {
	needed, err := draSetupNeeded(ctx, runner, cfg, featureCfg)
	if err != nil || !needed {
		return noLocalCleanup, err
	}
	return installDRADriver(ctx, runner, w, cfg, featureCfg)
}

// draDriverVersion returns the NVIDIA DRA driver chart version to install, or an error when none is set.
// Setup checks it before changing the cluster, so a missing version leaves the nodes untouched.
func draDriverVersion(featureCfg FeatureConfig) (string, error) {
	version := strings.TrimSpace(featureCfg.Dependencies.DRADriverVersion)
	if version == "" {
		return "", errors.New("NVIDIA_DRA_DRIVER_VERSION is required")
	}
	return version, nil
}

// requireDRADriverVersionIfInstalling returns an error when DRA setup will install the driver chart but no
// version is set. When the cluster already publishes NVIDIA GPU ResourceSlices, no chart is installed and
// no version is needed.
func requireDRADriverVersionIfInstalling(ctx context.Context, runner e2eexec.Runner, cfg Config, featureCfg FeatureConfig) error {
	needed, err := draSetupNeeded(ctx, runner, cfg, featureCfg)
	if err != nil || !needed {
		return err
	}

	_, err = draDriverVersion(featureCfg)
	return err
}

// draSetupNeeded reports whether this run may set up DRA and the cluster lacks NVIDIA GPU ResourceSlices.
// A failed or unreadable ResourceSlice lookup is an error, so setup never changes the cluster on a guess.
func draSetupNeeded(ctx context.Context, runner e2eexec.Runner, cfg Config, featureCfg FeatureConfig) (bool, error) {
	if !draConfigurationEnabled(featureCfg, cfg) {
		return false, nil
	}

	available, err := draResourcesAvailable(ctx, runner, cfg)
	if err != nil {
		return false, fmt.Errorf("check NVIDIA DRA ResourceSlices before setup: %w", err)
	}
	return !available, nil
}

// labelDRANodes sets the DRA kubelet plugin label on every node and returns the cleanup that removes it.
// A node that already has the label key, with any value, stops setup: labels carry no run ownership, so
// removing it later could undo a setting this run did not make. The cleanup is returned with a labeling
// error too, because some nodes may already carry the label.
func labelDRANodes(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) (func(context.Context) error, error) {
	lookup := kubectl(cfg, "get", "nodes", "-l", draNodePluginLabelKey, "-o", "name")
	result := runner.Run(ctx, lookup)
	if result.ExitCode != 0 {
		return noLocalCleanup, fmt.Errorf("check nodes for label %q: %w", draNodePluginLabelKey, resultError(commandString(lookup), result))
	}
	if labeled := strings.Fields(string(result.Stdout)); len(labeled) != 0 {
		return noLocalCleanup, fmt.Errorf("label %q is already set on %s; remove it so this run can set up NVIDIA DRA", draNodePluginLabelKey, strings.Join(labeled, ", "))
	}

	unlabel := func(cleanupCtx context.Context) error {
		return removeDRANodeLabel(cleanupCtx, runner, w, cfg)
	}
	label := kubectl(cfg, "label", "nodes", "--all", draNodePluginLabelKey+"=true", "--overwrite")
	if err := runRequired(ctx, runner, w, "kubectl_label_dra_nodes", label); err != nil {
		return unlabel, fmt.Errorf("label nodes for the NVIDIA DRA driver: %w", err)
	}
	return unlabel, nil
}

// removeDRANodeLabel removes the DRA kubelet plugin label from every node.
func removeDRANodeLabel(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	unlabel := kubectl(cfg, "label", "nodes", "--all", draNodePluginLabelKey+"-", "--overwrite")
	if err := runRequired(ctx, runner, w, "kubectl_unlabel_dra_nodes", unlabel); err != nil {
		return fmt.Errorf("remove NVIDIA DRA node labels: %w", err)
	}
	return nil
}

// installDRADriver creates the run-owned DRA namespace, installs the NVIDIA DRA driver chart into it,
// and waits for its ResourceSlices. A namespace that another run owns stops the install. The returned
// cleanup removes the release and the namespace; it is returned with a later setup error too, because
// Helm can leave a partial release.
func installDRADriver(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context) error, error) {
	version, err := draDriverVersion(featureCfg)
	if err != nil {
		return noLocalCleanup, err
	}
	if _, err := ensureManagedNamespace(ctx, runner, w, cfg, draDriverNamespace); err != nil {
		return noLocalCleanup, fmt.Errorf("prepare NVIDIA DRA driver namespace: %w", err)
	}

	timeout := timeoutFor(featureCfg)
	uninstall := func(cleanupCtx context.Context) error {
		return cleanupOwnedRelease(cleanupCtx, runner, w, cfg, "dra", draDriverReleaseName, draDriverNamespace, timeout)
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_repo_dra", []string{"repo", "add", "nvidia", "https://helm.ngc.nvidia.com/nvidia", "--force-update"}); err != nil {
		return uninstall, err
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_repo_update_dra", []string{"repo", "update"}); err != nil {
		return uninstall, err
	}

	args := []string{
		"upgrade", "--install", draDriverReleaseName, "nvidia/nvidia-dra-driver-gpu",
		"--namespace", draDriverNamespace,
		"--version", version,
		"--set", "resources.gpus.enabled=true",
		"--set", "resources.computeDomains.enabled=false",
		"--set", "gpuResourcesEnabledOverride=true",
		"--set", "nvidiaDriverRoot=/",
		"--wait",
		"--timeout", timeout,
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_install_dra", args); err != nil {
		return uninstall, err
	}
	return uninstall, WaitForDRAResources(ctx, runner, cfg, timeout)
}

// WaitForDRAResources waits until NVIDIA GPU ResourceSlices are published. A lookup can fail while the
// driver starts, so it keeps polling; the timeout error names the last lookup's failure, if it failed.
func WaitForDRAResources(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	var lastErr error
	available := func() bool {
		var found bool
		found, lastErr = draResourcesAvailable(ctx, runner, cfg)
		return found
	}
	return cfg.poll(ctx, timeout, available, func() error {
		if lastErr != nil {
			return fmt.Errorf("timed out waiting for NVIDIA DRA resources; last lookup failed: %w", lastErr)
		}
		return errors.New("timed out waiting for NVIDIA DRA resources")
	})
}

// draConfigurationEnabled reports whether validation may install DRA resources.
func draConfigurationEnabled(featureCfg FeatureConfig, cfg Config) bool {
	return featureCfg.DRAConfigure.SetUpOn(cfg.LocalK3D())
}

// draResourcesAvailable reports whether NVIDIA GPU ResourceSlices exist. It returns an error when the
// lookup fails or its output is not a ResourceSlice list, which is different from a list without them.
func draResourcesAvailable(ctx context.Context, runner e2eexec.Runner, cfg Config) (bool, error) {
	lookup := kubectl(cfg, "get", "resourceslices", "-o", "json")
	result := runner.Run(ctx, lookup)
	if result.ExitCode != 0 {
		return false, resultErrorForCommand(lookup, result)
	}

	list, ok := parseNVIDIAResourceSlices(result.Stdout)
	if !ok {
		return false, fmt.Errorf("%s returned output that is not a ResourceSlice list", commandString(lookup))
	}
	return list.hasFullGPU(), nil
}

type nvidiaResourceSliceDevice struct {
	Attributes map[string]map[string]any `json:"attributes"`
	Basic      *struct {
		Attributes map[string]map[string]any `json:"attributes"`
	} `json:"basic"`
}

type nvidiaResourceSliceList struct {
	Items []struct {
		Spec struct {
			Driver  string                      `json:"driver"`
			Devices []nvidiaResourceSliceDevice `json:"devices"`
		} `json:"spec"`
	} `json:"items"`
}

func parseNVIDIAResourceSlices(data []byte) (nvidiaResourceSliceList, bool) {
	var list nvidiaResourceSliceList
	if err := json.Unmarshal(data, &list); err != nil {
		return nvidiaResourceSliceList{}, false
	}
	return list, true
}

// hasNVIDIAFullGPUResourceSlice reports whether data is a ResourceSlice list with a full NVIDIA GPU.
func hasNVIDIAFullGPUResourceSlice(data []byte) bool {
	list, ok := parseNVIDIAResourceSlices(data)
	return ok && list.hasFullGPU()
}

// hasFullGPU reports whether the NVIDIA DRA driver publishes at least one full GPU device.
func (list nvidiaResourceSliceList) hasFullGPU() bool {
	for _, item := range list.Items {
		if item.Spec.Driver != nvidiaDRADriverName {
			continue
		}
		for _, device := range item.Spec.Devices {
			if deviceType(deviceAttributes(device)) == "gpu" {
				return true
			}
		}
	}
	return false
}

func deviceAttributes(device nvidiaResourceSliceDevice) map[string]map[string]any {
	if len(device.Attributes) != 0 {
		return device.Attributes
	}
	if device.Basic != nil {
		return device.Basic.Attributes
	}
	return nil
}

func deviceType(attributes map[string]map[string]any) string {
	for name, value := range attributes {
		if name != "type" && !strings.HasSuffix(name, "/type") {
			continue
		}
		for _, field := range []string{"string", "stringValue"} {
			if text, ok := value[field].(string); ok {
				return strings.ToLower(strings.TrimSpace(text))
			}
		}
	}
	return ""
}
