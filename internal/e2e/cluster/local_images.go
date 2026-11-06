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
	"os"
	"strings"

	distributionreference "github.com/distribution/reference"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

// LocalK3DImageReference pairs the immutable host pull reference with the
// tag-bearing name k3d imports and Kubernetes deploys locally.
type LocalK3DImageReference struct {
	Source string
	Local  string
}

// ResolveLocalK3DImage returns a deterministic tag for a digest-only image.
func ResolveLocalK3DImage(value string) (LocalK3DImageReference, error) {
	ref, err := e2eimage.Parse(value)
	if err != nil {
		return LocalK3DImageReference{}, fmt.Errorf("local k3d image: %w", err)
	}
	local := ref.Tagged()
	if ref.Tag() == "" {
		_, digest, found := strings.Cut(ref.Digest(), ":")
		if !found {
			return LocalK3DImageReference{}, fmt.Errorf("local k3d image %q has an invalid digest", value)
		}
		if len(digest) < 12 {
			return LocalK3DImageReference{}, fmt.Errorf("local k3d image %q has an invalid digest", value)
		}
		local = ref.Repository() + ":dcgm-exporter-e2e-" + digest[:12]
	}
	return LocalK3DImageReference{Source: ref.Pull(), Local: local}, nil
}

// TagLocalK3DImage materializes the local tag when a digest-pinned source needs one.
func TagLocalK3DImage(ctx context.Context, runner e2eexec.Runner, w io.Writer, image LocalK3DImageReference, stepName string, env []string) error {
	if image.Source == image.Local {
		return nil
	}
	if stepName == "" {
		stepName = "docker_tag_k3d_image"
	}
	command := e2eexec.Command{Name: "docker", Args: []string{"tag", image.Source, image.Local}, Env: env, Stdout: w, Stderr: w, LogName: stepName, QuietOnSuccess: true}
	fmt.Fprintf(w, "[e2e] step: %s\n", stepName)
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return resultError(commandString(command), result)
	}
	return nil
}

// ImportImageIfPresent imports a host-local Docker image into an owned k3d cluster.
func ImportImageIfPresent(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, image string) error {
	_, err := ImportImageIfPresentWithResult(ctx, runner, w, cfg, image, "k3d_import_exporter_image")
	return err
}

// ImportImageIfPresentWithStep imports a host-local Docker image using the named e2e step.
// ImportImageIfPresentWithResult verifies imported image content on each k3d node.
func ImportImageIfPresentWithStep(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, image, stepName string) error {
	_, err := ImportImageIfPresentWithResult(ctx, runner, w, cfg, image, stepName)
	return err
}

// ImportImageIfPresentWithResult imports a host-local Docker image and reports whether import was attempted.
func ImportImageIfPresentWithResult(
	ctx context.Context,
	runner e2eexec.Runner,
	w io.Writer,
	cfg Config,
	image, stepName string,
) (imported bool, returnErr error) {
	if image == "" || !cfg.LocalK3D() {
		return false, nil
	}
	inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", image}})
	if inspect.ExitCode != 0 {
		return false, nil
	}
	platform, isImageIndex, err := dockerImageIndexPlatform(inspect.Stdout)
	if err != nil {
		return false, fmt.Errorf("inspect host-local image %s: %w", image, err)
	}
	if stepName == "" {
		stepName = "k3d_import_image"
	}
	importSource := image
	if isImageIndex {
		archivePath, err := savePlatformImageArchive(ctx, runner, w, image, platform, stepName)
		if err != nil {
			return false, err
		}
		defer func() {
			if err := removeTemporaryK3dImageArchive(archivePath); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}()
		importSource = archivePath
	}

	command := e2eexec.Command{Name: "k3d", Args: []string{"image", "import", importSource, "-c", cfg.ClusterName}}
	fmt.Fprintf(w, "[e2e] step: %s\n", stepName)
	command.Stdout = w
	command.Stderr = w
	command.LogName = stepName
	command.QuietOnSuccess = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return true, resultError(commandString(command), result)
	}
	output := strings.ToLower(string(result.Stdout) + string(result.Stderr))
	if k3dImageImportFailed(output) {
		return true, fmt.Errorf("%s reported image import errors for %s", commandString(command), image)
	}
	if err := verifyImageOnNodes(ctx, runner, w, cfg.ClusterName, image); err != nil {
		return true, err
	}
	return true, nil
}

// savePlatformImageArchive saves one requested image platform to a temporary archive and removes it if saving fails.
func savePlatformImageArchive(
	ctx context.Context,
	runner e2eexec.Runner,
	w io.Writer,
	image, platform, stepName string,
) (string, error) {
	archive, err := os.CreateTemp("", "dcgm-exporter-e2e-k3d-image-*.tar")
	if err != nil {
		return "", fmt.Errorf("create platform-specific image archive: %w", err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		return "", errors.Join(
			fmt.Errorf("close temporary k3d image archive: %w", err),
			removeTemporaryK3dImageArchive(archivePath),
		)
	}

	saveStepName := stepName + "_save_platform_image"
	saveCommand := e2eexec.Command{
		Name: "docker",
		Args: []string{"image", "save", "--platform", platform, "--output", archivePath, image},
	}
	if err := runRequired(ctx, runner, w, saveStepName, saveCommand); err != nil {
		return "", errors.Join(
			fmt.Errorf("save platform-specific image archive: %w", err),
			removeTemporaryK3dImageArchive(archivePath),
		)
	}
	return archivePath, nil
}

// removeTemporaryK3dImageArchive removes a temporary image archive while treating an already-removed file as success.
func removeTemporaryK3dImageArchive(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove temporary k3d image archive: %w", err)
	}
	return nil
}

// dockerImageInspection holds the platform and descriptor fields read from Docker image inspect JSON.
type dockerImageInspection struct {
	OS           string `json:"Os"`
	Architecture string `json:"Architecture"`
	Variant      string `json:"Variant"`
	Descriptor   struct {
		MediaType string `json:"mediaType"`
	} `json:"Descriptor"`
}

// dockerImageIndexPlatform returns the selected platform only for indexed images.
func dockerImageIndexPlatform(inspectOutput []byte) (string, bool, error) {
	var images []dockerImageInspection
	if err := json.Unmarshal(inspectOutput, &images); err != nil {
		return "", false, fmt.Errorf("decode docker image inspect output: %w", err)
	}
	if len(images) != 1 {
		return "", false, fmt.Errorf("docker image inspect returned %d images, want 1", len(images))
	}

	image := images[0]
	mediaType := image.Descriptor.MediaType
	if mediaType != "application/vnd.oci.image.index.v1+json" &&
		mediaType != "application/vnd.docker.distribution.manifest.list.v2+json" {
		return "", false, nil
	}

	operatingSystem := strings.TrimSpace(image.OS)
	architecture := strings.TrimSpace(image.Architecture)
	if operatingSystem == "" || architecture == "" {
		return "", false, fmt.Errorf("indexed image is missing its OS or architecture")
	}
	platform := operatingSystem + "/" + architecture
	if variant := strings.TrimSpace(image.Variant); variant != "" {
		platform += "/" + variant
	}
	return platform, true, nil
}

// verifyImageOnNodes checks that every server and agent has usable local image content.
func verifyImageOnNodes(ctx context.Context, runner e2eexec.Runner, w io.Writer, clusterName, image string) error {
	named, err := distributionreference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("normalize imported image %q: %w", image, err)
	}
	normalizedImage := named.String()
	nodes, err := localClusterDockerNodes(ctx, runner, clusterName)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("k3d cluster %s has no server or agent nodes", clusterName)
	}

	for _, node := range nodes {
		command := e2eexec.Command{
			Name: "docker",
			Args: []string{
				"exec", node,
				"ctr", "--namespace", "k8s.io", "images", "check",
				"--quiet", "name==" + normalizedImage,
			},
		}
		result := runner.Run(ctx, command)
		if result.ExitCode != 0 {
			return resultError(commandString(command), result)
		}
		if strings.TrimSpace(string(result.Stdout)) == "" {
			return fmt.Errorf("k3d node %s does not have usable content for image %s", node, normalizedImage)
		}
		fmt.Fprintf(w, "[e2e] k3d node image ready: node=%s image=%s\n", node, normalizedImage)
	}
	return nil
}

// k3dImageImportFailed detects k3d import failures that can appear in successful command output.
func k3dImageImportFailed(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "failed to import image") ||
		strings.Contains(output, "failed to import images") ||
		strings.Contains(output, "error importing image") ||
		strings.Contains(output, "error importing images")
}
