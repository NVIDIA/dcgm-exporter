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
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	localCDISpecPath       = "/etc/cdi/nvidia-e2e.yaml"
	localRuntimeConfig     = "/etc/nvidia-container-runtime/config.toml"
	localRuntimeModeCDI    = `s/^mode = .*/mode = "cdi"/`
	localRuntimeModeLegacy = `s/^mode = .*/mode = "legacy"/`
)

// PrepareNVSwitchRuntime temporarily exposes the host CDI device edits to
// workloads in an owned k3d cluster. The nested runtime injects the GPU driver
// but not host-only NVSwitch libraries. When DCGM uses NVSDM on this host,
// setup also mounts the NVSDM and InfiniBand libraries. Other hosts, such as
// Hopper systems that use NSCQ, get no extra libraries. Other groups stay in
// legacy mode because local MIG setup relies on it.
func PrepareNVSwitchRuntime(
	ctx context.Context,
	runner e2eexec.Runner,
	w io.Writer,
	cfg Config,
	local LocalConfig,
	useNVSDM bool,
) (func(context.Context) error, error) {
	if cfg.ClusterName != "" && local.ClusterName != "" && cfg.ClusterName != local.ClusterName {
		return noLocalCleanup, fmt.Errorf(
			"local cluster name mismatch: config=%q local=%q",
			cfg.ClusterName,
			local.ClusterName,
		)
	}
	hostSpec, err := createNVSwitchCDISpec(ctx, runner, w, local.HostRootPath, useNVSDM)
	if err != nil {
		return noLocalCleanup, err
	}
	defer os.Remove(hostSpec)

	nodes, err := localClusterDockerNodes(ctx, runner, local.ClusterName)
	if err != nil {
		return noLocalCleanup, err
	}

	configured := make([]string, 0, len(nodes))
	cleanup := func(cleanupCtx context.Context) error {
		var errs []error
		for _, node := range configured {
			errs = append(errs, run(
				w,
				runner,
				cleanupCtx,
				"docker_restore_legacy_runtime_"+node,
				dockerExec(node, "sed", "-i", localRuntimeModeLegacy, localRuntimeConfig),
			))
			errs = append(errs, run(
				w,
				runner,
				cleanupCtx,
				"docker_remove_e2e_cdi_spec_"+node,
				dockerExec(node, "rm", "-f", localCDISpecPath),
			))
		}
		return errors.Join(errs...)
	}

	for _, node := range nodes {
		if runner.Run(ctx, dockerExec(node, "test", "-e", "/dev/nvidiactl")).ExitCode != 0 {
			continue
		}
		configured = append(configured, node)

		relativeSpec, err := filepath.Rel(local.HostRootPath, hostSpec)
		if err != nil || strings.HasPrefix(relativeSpec, "..") {
			cleanupErr := cleanup(context.WithoutCancel(ctx))
			return noLocalCleanup, errors.Join(
				fmt.Errorf("temporary CDI spec is outside the mounted host root"),
				cleanupErr,
			)
		}
		source := filepath.Join(local.HostRootMount, relativeSpec)
		commands := []struct {
			name string
			args []string
		}{
			{name: "create_cdi_directory", args: []string{"mkdir", "-p", filepath.Dir(localCDISpecPath)}},
			{name: "copy_host_cdi_spec", args: []string{"cp", source, localCDISpecPath}},
			{
				name: "rebase_host_cdi_paths",
				args: []string{
					"sed", "-i",
					`/hostPath: \//s#hostPath: /#hostPath: ` + strings.TrimRight(local.HostRootMount, "/") + `/#`,
					localCDISpecPath,
				},
			},
			{name: "enable_cdi_runtime", args: []string{"sed", "-i", localRuntimeModeCDI, localRuntimeConfig}},
			{name: "verify_cdi_runtime", args: []string{"grep", "-q", `mode = "cdi"`, localRuntimeConfig}},
			{
				name: "verify_rebased_cdi_spec",
				args: []string{
					"grep",
					"-q",
					"hostPath: " + strings.TrimRight(local.HostRootMount, "/") + "/",
					localCDISpecPath,
				},
			},
		}
		for _, command := range commands {
			err := runRequired(
				ctx,
				runner,
				w,
				"docker_"+command.name+"_"+node,
				dockerExec(node, command.args...),
			)
			if err != nil {
				cleanupErr := cleanup(context.WithoutCancel(ctx))
				return noLocalCleanup, errors.Join(err, cleanupErr)
			}
		}
	}

	if len(configured) == 0 {
		return noLocalCleanup, fmt.Errorf(
			"local k3d cluster %s has no Docker node with /dev/nvidiactl",
			local.ClusterName,
		)
	}
	nodeLabel := "nodes"
	if len(configured) == 1 {
		nodeLabel = "node"
	}
	fmt.Fprintf(
		w,
		"[e2e] enabled host CDI device edits on %d local GPU %s for NVSwitch validation\n",
		len(configured),
		nodeLabel,
	)
	return cleanup, nil
}

// createNVSwitchCDISpec generates a current host CDI spec and, when useNVSDM is
// set, adds the NVSDM and InfiniBand libraries.
func createNVSwitchCDISpec(ctx context.Context, runner e2eexec.Runner, w io.Writer, hostRoot string, useNVSDM bool) (string, error) {
	file, err := os.CreateTemp(filepath.Join(hostRoot, "tmp"), "dcgm-exporter-e2e-cdi-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create temporary CDI spec: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temporary CDI spec: %w", err)
	}

	command := e2eexec.Command{
		Name: "nvidia-ctk",
		Args: []string{"cdi", "generate", "--driver-root=" + hostRoot, "--dev-root=/dev", "--output=" + path},
	}
	if err := runRequired(ctx, runner, w, "generate_host_cdi_spec", command); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if useNVSDM {
		if err := addNVSDMLibraries(path, hostRoot); err != nil {
			_ = os.Remove(path)
			return "", err
		}
	}
	return path, nil
}

// addNVSDMLibraries adds the NVSDM and InfiniBand libraries that DCGM NVSwitch
// discovery needs on NVSDM hosts.
func addNVSDMLibraries(specPath, hostRoot string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read generated CDI spec: %w", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse generated CDI spec: %w", err)
	}
	if spec == nil {
		return fmt.Errorf("parse generated CDI spec: document is empty")
	}
	edits, ok := spec["containerEdits"].(map[string]any)
	if !ok {
		edits = make(map[string]any)
		spec["containerEdits"] = edits
	}
	mounts, _ := edits["mounts"].([]any)
	existing := make(map[string]bool, len(mounts))
	existingNames := make(map[string]bool, len(mounts))
	for _, entry := range mounts {
		mount, _ := entry.(map[string]any)
		containerPath, _ := mount["containerPath"].(string)
		existing[containerPath] = true
		existingNames[filepath.Base(containerPath)] = true
	}

	for _, name := range []string{"libnvsdm.so.1", "libibmad.so.5", "libibumad.so.3", "libmnl.so.0"} {
		if existingNames[name] {
			continue
		}
		containerPath, err := findNVSwitchLibrary(hostRoot, name)
		if err != nil {
			return err
		}
		if existing[containerPath] {
			continue
		}
		mounts = append(mounts, map[string]any{
			"hostPath":      containerPath,
			"containerPath": containerPath,
			"options":       []string{"ro", "nosuid", "nodev", "rbind", "rprivate"},
		})
	}

	edits["mounts"] = mounts

	updated, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("render generated CDI spec: %w", err)
	}
	//nolint:gosec // CDI specs are system-readable configuration files.
	if err := os.WriteFile(specPath, updated, 0o644); err != nil {
		return fmt.Errorf("write generated CDI spec: %w", err)
	}
	return nil
}

// findNVSwitchLibrary resolves one Ubuntu or RHEL library path for the current architecture.
func findNVSwitchLibrary(hostRoot, name string) (string, error) {
	var ubuntuDirectory string
	switch runtime.GOARCH {
	case "amd64":
		ubuntuDirectory = "x86_64-linux-gnu"
	case "arm64":
		ubuntuDirectory = "aarch64-linux-gnu"
	default:
		return "", fmt.Errorf("NVSwitch library lookup does not support architecture %s", runtime.GOARCH)
	}

	candidates := []string{
		filepath.Join(hostRoot, "usr", "lib", ubuntuDirectory, name),
		filepath.Join(hostRoot, "usr", "lib64", name),
		filepath.Join(hostRoot, "lib64", name),
	}
	foundPath := ""
	foundTarget := ""
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("resolve NVSwitch library %s: %w", name, err)
		}
		if foundTarget == "" {
			foundPath = candidate
			foundTarget = resolved
			continue
		}
		if resolved != foundTarget {
			return "", fmt.Errorf("required NVSwitch library %s has multiple distinct matches under %s", name, hostRoot)
		}
	}
	if foundPath == "" {
		return "", fmt.Errorf("required NVSwitch library %s was not found under %s", name, hostRoot)
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(foundPath, hostRoot), "/"), nil
}

// dockerExec builds a command that runs inside the named Docker node.
func dockerExec(node string, args ...string) e2eexec.Command {
	return e2eexec.Command{Name: "docker", Args: append([]string{"exec", node}, args...)}
}
