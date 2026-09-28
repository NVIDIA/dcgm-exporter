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

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// runClusterUp creates or repairs the owned local k3d cluster without deploying workloads.
func runClusterUp(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.Tests) error {
	if _, err := detectArtifactMode(root); err != nil {
		return setupFailure(err)
	}
	cfg := clusterConfig(root, opts)
	if !cfg.LocalK3D() {
		return setupFailure(errors.New("e2e cluster up manages local k3d only; omit --kubeconfig"))
	}
	local, err := localClusterConfig(root, cfg, opts)
	if err != nil {
		return setupFailure(err)
	}
	_, err = cluster.EnsureLocal(ctx, runner, stdout, local)
	return setupFailure(err)
}

// runClusterDeploy ensures the target cluster exists and deploys dcgm-exporter to it.
func runClusterDeploy(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.Tests) error {
	mode, err := detectArtifactMode(root)
	if err != nil {
		return setupFailure(err)
	}
	if err := validateRequiredImage("--exporter-image", opts.ExporterImage); err != nil {
		if mode == artifactModePackage {
			return setupFailure(fmt.Errorf("package environment does not define a valid E2E_EXPORTER_IMAGE: %w", err))
		}
		return setupFailure(fmt.Errorf("e2e cluster deploy requires --exporter-image or E2E_EXPORTER_IMAGE; source runs should use make e2e-k3d-deploy: %w", err))
	}
	cfg := clusterConfig(root, opts)
	if cfg.LocalK3D() {
		local, err := localClusterConfig(root, cfg, opts)
		if err != nil {
			return setupFailure(err)
		}
		if _, err := cluster.EnsureLocal(ctx, runner, stdout, local); err != nil {
			return setupFailure(err)
		}
		cleanupDockerConfig, _, err := prepareDockerRegistryLogin(ctx, stdout, runner, &opts)
		if err != nil {
			return setupFailure(err)
		}
		defer cleanupDockerConfig()
		if err := ensureDockerImageAvailable(ctx, stdout, runner, opts, exporterImage(opts)); err != nil {
			return setupFailure(err)
		}
		if err := cluster.ImportImageIfPresent(ctx, runner, stdout, cfg, exporterImage(opts)); err != nil {
			return setupFailure(err)
		}
	}
	if err := prepareRegistryAuthForNamespaces(ctx, stdout, runner, cfg, &opts, []string{cfg.Namespace}); err != nil {
		return setupFailure(err)
	}
	return setupFailure(cluster.DeployExporter(ctx, runner, stdout, cfg, cluster.ExporterConfig{
		Chart:             filepath.Join(root, "deployment"),
		Image:             opts.ExporterImage,
		ImagePullSecret:   opts.K8sImagePullSecret,
		RuntimeClass:      runtimeClassFor(opts, cfg),
		NodeSelectorKey:   nodeSelectorKeyFor(opts, cfg),
		NodeSelectorValue: nodeSelectorValueFor(opts, cfg),
		WaitTimeout:       opts.WaitTimeout,
	}))
}

// runClusterDeployDCGM deploys standalone DCGM for remote-DCGM validation scenarios.
func runClusterDeployDCGM(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.Tests) error {
	if _, err := detectArtifactMode(root); err != nil {
		return setupFailure(err)
	}
	dcgmImageRef := dcgmImage(opts)
	if err := validateRequiredImage("--dcgm-image", dcgmImageRef); err != nil {
		return setupFailure(err)
	}
	cleanupDockerConfig, _, err := prepareDockerRegistryLogin(ctx, stdout, runner, &opts)
	if err != nil {
		return setupFailure(err)
	}
	defer cleanupDockerConfig()
	if err := ensureDCGMImageAvailable(ctx, runner, opts); err != nil {
		return setupFailure(err)
	}
	cfg := clusterConfig(root, opts)
	if cfg.LocalK3D() {
		local, err := localClusterConfig(root, cfg, opts)
		if err != nil {
			return setupFailure(err)
		}
		if _, err := cluster.EnsureLocal(ctx, runner, stdout, local); err != nil {
			return setupFailure(err)
		}
	}
	if err := cluster.EnsureManagedNamespace(ctx, runner, stdout, cfg, cfg.DCGMNamespace); err != nil {
		return setupFailure(err)
	}
	if err := prepareRegistryAuthForNamespaces(ctx, stdout, runner, cfg, &opts, []string{cfg.DCGMNamespace}); err != nil {
		return setupFailure(err)
	}
	dcgm := cluster.DefaultDCGMConfig()
	dcgm.Image = dcgmImageRef
	dcgm.RuntimeClass = runtimeClassFor(opts, cfg)
	dcgm.NodeSelectorKey = nodeSelectorKeyFor(opts, cfg)
	dcgm.NodeSelectorValue = nodeSelectorValueFor(opts, cfg)
	dcgm.ImagePullSecret = opts.K8sImagePullSecret
	dcgm.Name = dcgmNameFor(opts)
	dcgm.Port = dcgmPortFor(opts)
	dcgm.WaitTimeout = opts.WaitTimeout
	return setupFailure(cluster.DeployDCGM(ctx, runner, stdout, cfg, dcgm))
}
