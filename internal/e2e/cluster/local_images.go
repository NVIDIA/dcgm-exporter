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
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ImportImageIfPresent imports a host-local Docker image into an owned k3d cluster.
func ImportImageIfPresent(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, image string) error {
	_, err := ImportImageIfPresentWithResult(ctx, runner, w, cfg, image, "k3d_import_exporter_image")
	return err
}

// ImportImageIfPresentWithResult imports a host-local Docker image and reports whether import was attempted.
func ImportImageIfPresentWithResult(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, image, stepName string) (bool, error) {
	if image == "" || !cfg.LocalK3D() {
		return false, nil
	}
	inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", image}})
	if inspect.ExitCode != 0 {
		return false, nil
	}
	if stepName == "" {
		stepName = "k3d_import_image"
	}
	command := e2eexec.Command{Name: "k3d", Args: []string{"image", "import", image, "-c", cfg.ClusterName}}
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
	return true, nil
}

// k3dImageImportFailed detects k3d import failures that can appear in successful command output.
func k3dImageImportFailed(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "failed to import image") ||
		strings.Contains(output, "failed to import images") ||
		strings.Contains(output, "error importing image") ||
		strings.Contains(output, "error importing images")
}
