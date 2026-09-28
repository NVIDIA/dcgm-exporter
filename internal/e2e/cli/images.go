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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// exporterImage returns the immutable exporter image reference when a digest is configured.
func exporterImage(opts config.Tests) string {
	return pullImage(opts.ExporterImage)
}

// exporterUbuntuImage returns the Ubuntu exporter image reference for container tests.
func exporterUbuntuImage(opts config.Tests) string {
	return pullImage(opts.ExporterUbuntuImage)
}

// localK3dExporterImage returns a tag-form image reference that k3d can import by name.
func localK3dExporterImage(opts config.Tests) string {
	return taggedImage(opts.ExporterImage)
}

func pullImage(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	ref, err := e2eimage.Parse(value)
	if err != nil {
		return value
	}
	return ref.Pull()
}

func taggedImage(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	ref, err := e2eimage.Parse(value)
	if err != nil {
		return value
	}
	return ref.Tagged()
}

// tagLocalK3dImage creates the tag-form alias needed when the primary image is digest-pinned.
func tagLocalK3dImage(ctx context.Context, runner e2eexec.Runner, stdout io.Writer, opts config.Tests) error {
	source := exporterImage(opts)
	target := localK3dExporterImage(opts)
	if source == "" || target == "" || source == target {
		return nil
	}
	writeStep(stdout, "step", "docker_tag_exporter_image")
	result := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"tag", source, target}, Env: dockerConfigEnv(opts), Stdout: stdout, Stderr: stdout, LogName: "docker_tag_exporter_image", QuietOnSuccess: true})
	if result.ExitCode != 0 {
		output := strings.TrimSpace(string(result.Stderr))
		if output == "" {
			output = strings.TrimSpace(string(result.Stdout))
		}
		return fmt.Errorf("docker tag %s %s failed: %s", source, target, output)
	}
	return nil
}

// ensureDockerImageAvailable pulls an image only when it is not already present locally.
func ensureDockerImageAvailable(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests, image string) error {
	if image == "" {
		return nil
	}
	if inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", image}, Env: dockerConfigEnv(opts)}); inspect.ExitCode == 0 {
		return nil
	}
	writeStep(stdout, "step", "docker_pull_exporter_image")
	result := runner.Run(ctx, e2eexec.Command{
		Name:           "docker",
		Args:           []string{"pull", image},
		Env:            dockerConfigEnv(opts),
		Stdout:         stdout,
		Stderr:         stdout,
		LogName:        "docker_pull_exporter_image",
		QuietOnSuccess: true,
	})
	if result.ExitCode != 0 {
		output := strings.TrimSpace(string(result.Stderr))
		if output == "" {
			output = strings.TrimSpace(string(result.Stdout))
		}
		return fmt.Errorf("docker pull %s failed: %s", image, output)
	}
	return nil
}

// dcgmImage returns the immutable standalone DCGM image reference when a digest is configured.
func dcgmImage(opts config.Tests) string {
	return pullImage(opts.DCGMImage)
}

// detectArtifactMode distinguishes source checkout runs from prebuilt test roots.
func detectArtifactMode(root string) (artifactMode, error) {
	if fileExists(filepath.Join(root, "go.mod")) {
		return artifactModeSource, nil
	}
	if dirExists(filepath.Join(root, "bin")) {
		return artifactModePackage, nil
	}
	return "", fmt.Errorf("e2e tests must be run from a repo root with go.mod or a package root with bin/")
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ensureDCGMImageAvailable verifies the standalone DCGM image is accessible before planning required probes.
func ensureDCGMImageAvailable(ctx context.Context, runner e2eexec.Runner, opts config.Tests) error {
	image := dcgmImage(opts)
	if image == "" {
		return fmt.Errorf("selected DCGM-probed scenarios require --dcgm-image or E2E_DCGM_IMAGE")
	}
	result := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"manifest", "inspect", image}, Env: dockerConfigEnv(opts)})
	if result.ExitCode == 0 {
		return nil
	}
	localResult := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", image}, Env: dockerConfigEnv(opts)})
	if localResult.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("selected DCGM-probed scenarios require accessible DCGM image %s: %s", image, resultText(result))
}

// resultText extracts the most useful failure text from a command result.
func resultText(result e2eexec.Result) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	if len(result.Stderr) != 0 {
		return strings.TrimSpace(string(result.Stderr))
	}
	if len(result.Stdout) != 0 {
		return strings.TrimSpace(string(result.Stdout))
	}
	return fmt.Sprintf("command exited with status %d", result.ExitCode)
}

// validateLiveImageConfig ensures image-backed suites have a concrete exporter image before execution.
func validateLiveImageConfig(_ string, mode artifactMode, opts config.Tests) error {
	if opts.BuildOnly {
		return nil
	}
	if err := validateOptionalImage("--exporter-ubuntu-image", opts.ExporterUbuntuImage); err != nil {
		return err
	}
	cfg := config.Config{Tests: opts}
	imageRequired := len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteContainer, cfg)) != 0 ||
		len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, cfg)) != 0
	if !imageRequired {
		return nil
	}
	if opts.ExporterImage == "" {
		if mode == artifactModePackage {
			return fmt.Errorf("prebuilt test environment does not define E2E_EXPORTER_IMAGE")
		}
		return fmt.Errorf("container and Kubernetes validation require --exporter-image or E2E_EXPORTER_IMAGE; source runs should use make test-e2e")
	}
	return validateRequiredImage("--exporter-image", opts.ExporterImage)
}

func validateRequiredImage(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if _, err := e2eimage.Parse(value); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func validateOptionalImage(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return validateRequiredImage(name, value)
}
