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

// EnsureImagePullSecret creates or updates a managed image pull secret.
func EnsureImagePullSecret(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace, secretName, dockerConfigPath string) error {
	if secretName == "" && dockerConfigPath == "" {
		return nil
	}
	if secretName == "" || dockerConfigPath == "" {
		return fmt.Errorf("image pull secret requires both secret name and docker config path: secretName=%q dockerConfigPath=%q", secretName, dockerConfigPath)
	}
	create := kubectl(
		cfg,
		"-n", namespace,
		"create", "secret", "generic", secretName,
		"--type=kubernetes.io/dockerconfigjson",
		"--from-file=.dockerconfigjson="+dockerConfigPath,
		"--dry-run=client",
		"-o", "yaml",
	)
	create.SensitiveOutput = true
	secretYAML := runner.Run(ctx, create)
	if secretYAML.ExitCode != 0 {
		return resultErrorForCommand(create, secretYAML)
	}
	apply := kubectlWithStdin(cfg, secretYAML.Stdout, "apply", "-f", "-")
	apply.SensitiveOutput = true
	if err := runRequired(ctx, runner, w, "kubectl_apply_pull_secret", apply); err != nil {
		return err
	}
	return runRequired(ctx, runner, w, "kubectl_label_pull_secret", kubectl(
		cfg,
		"-n", namespace,
		"label", "secret", secretName,
		"app.kubernetes.io/managed-by=dcgm-exporter-e2e",
		"app.kubernetes.io/part-of=dcgm-exporter",
		"--overwrite",
	))
}

// RequireImagePullSecret verifies a named pull Secret immediately before a consuming Helm action.
func RequireImagePullSecret(ctx context.Context, runner e2eexec.Runner, cfg Config, namespace, secretName string) error {
	if secretName == "" {
		return nil
	}
	command := kubectl(
		cfg,
		"-n", namespace,
		"get", "secret", secretName,
		"-o", `jsonpath={.type}{"\t"}{.data.\.dockerconfigjson}`,
	)
	command.SensitiveOutput = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return fmt.Errorf("required image pull secret %s/%s is unavailable: %w", namespace, secretName, resultErrorForCommand(command, result))
	}
	out := strings.TrimRight(string(result.Stdout), "\r\n")
	parts := strings.SplitN(out, "\t", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) != "kubernetes.io/dockerconfigjson" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("required image pull secret %s/%s must be type kubernetes.io/dockerconfigjson with non-empty .dockerconfigjson data", namespace, secretName)
	}
	return nil
}
