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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

var (
	dcgmVersionRE                   = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`)
	errDCGMImagePlatformUnavailable = errors.New("configured DCGM image is not published for the host architecture")
)

// containerImageManifestIndex keeps the raw manifest list so a missing list can be told apart from an empty one.
type containerImageManifestIndex struct {
	Manifests json.RawMessage `json:"manifests"`
}

// containerImageManifest describes one image published by a Docker manifest index.
type containerImageManifest struct {
	Platform containerImagePlatform `json:"platform"`
}

// containerImagePlatform names the operating system and processor architecture for one image.
type containerImagePlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

// exporterImage returns the immutable exporter image reference when a digest is configured.
func exporterImage(opts config.TestOptions) string {
	return pullImage(opts.ExporterImage)
}

// completeImage preserves repository, tag, and digest for consumers that require a tag-bearing digest reference.
func completeImage(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	ref, err := e2eimage.Parse(value)
	if err != nil {
		return value
	}
	return ref.String()
}

// localK3dExporterImage returns a tag-form image reference that k3d can import by name.
func localK3dExporterImage(opts config.TestOptions) string {
	image, err := cluster.ResolveLocalK3DImage(opts.ExporterImage)
	if err != nil {
		return opts.ExporterImage
	}
	return image.Local
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
func tagLocalK3dImage(ctx context.Context, runner e2eexec.Runner, stdout io.Writer, opts config.TestOptions) error {
	image, err := cluster.ResolveLocalK3DImage(opts.ExporterImage)
	if err != nil {
		return fmt.Errorf("local exporter image: %w", err)
	}
	return cluster.TagLocalK3DImage(ctx, runner, stdout, image, "docker_tag_exporter_image", dockerConfigEnv(opts))
}

// ensureDockerImageAvailable pulls an image only when it is not already present locally.
func ensureDockerImageAvailable(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, image string) error {
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
func dcgmImage(opts config.TestOptions) string {
	return pullImage(opts.DCGMImage)
}

func gpuOperatorDCGMImage(opts config.TestOptions) string {
	image := strings.TrimSpace(opts.GPUOperatorDCGMImage)
	if image == "" {
		image = cluster.DefaultGPUOperatorMainDCGMImage
	}
	return pullImage(image)
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
func ensureDCGMImageAvailable(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions) error {
	image := dcgmImage(opts)
	if image == "" {
		return fmt.Errorf("selected DCGM-probed scenarios require --dcgm-image or E2E_DCGM_IMAGE")
	}
	if err := checkContainerImageAvailable(ctx, runner, opts, image); err != nil {
		return fmt.Errorf("selected DCGM-probed scenarios require accessible DCGM image %s: %w", image, err)
	}
	return nil
}

// ensureDCGMImageVersion verifies that the selected image provides the pinned nv-hostengine version.
func ensureDCGMImageVersion(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions) error {
	expected := strings.TrimSpace(opts.Dependencies.DCGMVersion)
	if expected == "" {
		return fmt.Errorf("selected remote DCGM scenarios require --dcgm-version, E2E_DCGM_VERSION, or DCGM_VERSION")
	}

	image := dcgmImage(opts)
	actual, err := dcgmImageVersion(ctx, runner, image, dockerConfigEnv(opts))
	if err != nil {
		return err
	}

	if actual != expected {
		return fmt.Errorf("configured DCGM image %s provides nv-hostengine %s, want %s", image, actual, expected)
	}
	return nil
}

// dcgmImageVersion runs nv-hostengine --version in a DCGM image and returns
// the DCGM version it reports. An image not published for this platform
// returns errDCGMImagePlatformUnavailable.
func dcgmImageVersion(ctx context.Context, runner e2eexec.Runner, image string, dockerEnv []string) (string, error) {
	result := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{"run", "--rm", "--entrypoint", "/usr/bin/nv-hostengine", image, "--version"},
		Env:  dockerEnv,
	})
	if result.Err != nil || result.ExitCode != 0 {
		output := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
		if strings.Contains(output, "no matching manifest for linux/") {
			return "", fmt.Errorf("%w: %s", errDCGMImagePlatformUnavailable, image)
		}
		return "", fmt.Errorf("configured DCGM image %s could not run nv-hostengine --version: %s", image, resultText(result))
	}

	output := versionOutput(result)
	version := dcgmVersionRE.FindString(output)
	if version == "" {
		return "", fmt.Errorf("configured DCGM image %s did not report a parseable nv-hostengine version: %s", image, output)
	}
	return version, nil
}

// ensureHostDCGMVersion verifies that fallback dcgmi matches the pinned DCGM version.
func ensureHostDCGMVersion(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions) error {
	expected := strings.TrimSpace(opts.Dependencies.DCGMVersion)
	if expected == "" {
		return fmt.Errorf("host DCGM probe fallback requires --dcgm-version, E2E_DCGM_VERSION, or DCGM_VERSION")
	}
	actual, err := hostDCGMVersion(ctx, runner)
	if err != nil {
		return fmt.Errorf("host DCGM probe fallback: %w", err)
	}

	if actual != expected {
		return fmt.Errorf("host dcgmi provides DCGM %s, want %s", actual, expected)
	}
	return nil
}

// hostDCGMVersion runs the host's dcgmi --version and returns the DCGM version
// it reports.
func hostDCGMVersion(ctx context.Context, runner e2eexec.Runner) (string, error) {
	result := runner.Run(ctx, e2eexec.Command{Name: "dcgmi", Args: []string{"--version"}})
	if result.Err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("could not run dcgmi --version: %s", resultText(result))
	}

	output := versionOutput(result)
	version := dcgmVersionRE.FindString(output)
	if version == "" {
		return "", fmt.Errorf("host dcgmi did not report a parseable version: %s", output)
	}
	return version, nil
}

// versionOutput returns a successful command's stdout, then its stderr. A first
// docker run writes image pull progress to stderr, so the version on stdout
// must be searched first.
func versionOutput(result e2eexec.Result) string {
	return strings.TrimSpace(string(result.Stdout) + "\n" + string(result.Stderr))
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

// validateSelectedContainerImages checks selected image settings before E2E checks or changes the host.
func validateSelectedContainerImages(opts config.TestOptions, requirements []scenario.SelectedRequirement) error {
	checked := map[scenario.ContainerImage]struct{}{}
	for _, selected := range requirements {
		if !scenario.IsImageRequirement(selected.Requirement) {
			continue
		}
		if _, ok := checked[selected.Requirement.Image]; ok {
			continue
		}
		checked[selected.Requirement.Image] = struct{}{}
		_, err := configuredContainerImage(opts, selected.Requirement.Image)
		if err != nil {
			return fmt.Errorf(
				"container image %s required by %s: %w",
				selected.Requirement.Image,
				strings.Join(selected.RequiredBy, ", "),
				err,
			)
		}
	}
	return nil
}

// checkSelectedContainerImages verifies controller-local images required by selected scenarios without pulling them.
// It runs after Docker login so registry checks use the credentials selected for this test run; external Kubernetes images are left to the target cluster.
func checkSelectedContainerImages(
	ctx context.Context,
	runner e2eexec.Runner,
	opts config.TestOptions,
	requirements []scenario.SelectedRequirement,
) error {
	checked := map[scenario.ContainerImage]struct{}{}
	for _, selected := range requirements {
		if !scenario.RequiresLocalImageCheck(selected.Requirement) {
			continue
		}
		if _, ok := checked[selected.Requirement.Image]; ok {
			continue
		}
		checked[selected.Requirement.Image] = struct{}{}
		image, err := configuredContainerImage(opts, selected.Requirement.Image)
		if err != nil {
			return fmt.Errorf(
				"container image %s required by %s: %w",
				selected.Requirement.Image,
				strings.Join(selected.RequiredBy, ", "),
				err,
			)
		}
		if err := checkContainerImageAvailable(ctx, runner, opts, image); err != nil {
			return fmt.Errorf(
				"container image %s required by %s is unavailable: %w",
				image,
				strings.Join(selected.RequiredBy, ", "),
				err,
			)
		}
	}
	return nil
}

// configuredContainerImage reads one named scenario image from the test options and validates it.
func configuredContainerImage(opts config.TestOptions, name scenario.ContainerImage) (string, error) {
	var flag, value string
	switch name {
	case scenario.ContainerImageDCGM:
		flag, value = "--dcgm-image", opts.DCGMImage
	case scenario.ContainerImageExporter:
		flag, value = "--exporter-image", opts.ExporterImage
	case scenario.ContainerImageBusybox:
		flag, value = "--busybox-image", opts.BusyboxImage
	case scenario.ContainerImageCUDAWorkload:
		flag, value = "--cuda-workload-image", opts.CUDAWorkloadImage
	case scenario.ContainerImageGPUOperatorDCGM:
		flag, value = "--gpu-operator-dcgm-image", gpuOperatorDCGMImage(opts)
	default:
		return "", fmt.Errorf("unknown scenario image %q", name)
	}
	if err := validateRequiredImage(flag, value); err != nil {
		return "", err
	}
	return pullImage(value), nil
}

// writeSelectedImageReferences records the resolved images required by this run.
func writeSelectedImageReferences(w io.Writer, opts config.TestOptions, requirements []scenario.SelectedRequirement) error {
	printed := map[scenario.ContainerImage]struct{}{}
	for _, selected := range requirements {
		if !scenario.IsImageRequirement(selected.Requirement) {
			continue
		}
		if _, ok := printed[selected.Requirement.Image]; ok {
			continue
		}
		printed[selected.Requirement.Image] = struct{}{}
		image, err := configuredContainerImage(opts, selected.Requirement.Image)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "[e2e] image %s: %s\n", selected.Requirement.Image, image); err != nil {
			return err
		}
	}
	return nil
}

// checkContainerImageAvailable checks local Docker storage first, then the registry manifest for this host platform.
// It never pulls an image; suite setup remains responsible for any image it must import or run.
func checkContainerImageAvailable(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions, image string) error {
	local := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{"image", "inspect", image},
		Env:  dockerConfigEnv(opts),
	})
	if local.ExitCode == 0 {
		return nil
	}
	remote := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{"manifest", "inspect", image},
		Env:  dockerConfigEnv(opts),
	})
	if remote.ExitCode != 0 {
		return fmt.Errorf(
			"docker image inspect: %s; docker manifest inspect: %s",
			resultText(local),
			resultText(remote),
		)
	}
	if err := manifestSupportsHostPlatform(remote.Stdout); err != nil {
		return fmt.Errorf(
			"docker image inspect: %s; docker manifest inspect: %s; check manifest platform: %w",
			resultText(local),
			resultText(remote),
			err,
		)
	}
	return nil
}

// manifestSupportsHostPlatform rejects a manifest index that does not publish an image for this host.
// A single-image manifest has no manifest list, while an empty list has no image to select.
func manifestSupportsHostPlatform(manifest []byte) error {
	var index containerImageManifestIndex
	if err := json.Unmarshal(manifest, &index); err != nil {
		return fmt.Errorf("decode manifest JSON: %w", err)
	}
	if index.Manifests == nil {
		return nil
	}
	var entries []containerImageManifest
	if err := json.Unmarshal(index.Manifests, &entries); err != nil {
		return fmt.Errorf("decode manifest list: %w", err)
	}
	if len(entries) == 0 {
		return errors.New("image manifest list contains no platform entries")
	}
	for _, item := range entries {
		if item.Platform.OS == runtime.GOOS && item.Platform.Architecture == runtime.GOARCH {
			return nil
		}
	}
	return fmt.Errorf("image is not published for %s/%s", runtime.GOOS, runtime.GOARCH)
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
