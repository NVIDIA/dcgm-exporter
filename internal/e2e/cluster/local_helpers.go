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
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// runRequired logs a setup step and fails when a cluster command exits non-zero.
func runRequired(ctx context.Context, runner e2eexec.Runner, w io.Writer, name string, command e2eexec.Command) error {
	fmt.Fprintf(w, "[e2e] step: %s\n", name)
	command.Stdout = w
	command.Stderr = w
	command.LogName = name
	command.QuietOnSuccess = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return resultError(commandString(command), result)
	}
	return nil
}

// kubectlWithStdin builds a kubectl command that applies manifest bytes through stdin.
func kubectlWithStdin(cfg Config, stdin []byte, args ...string) e2eexec.Command {
	command := kubectl(cfg, args...)
	command.Stdin = stdin
	return command
}

// kubeEnv returns KUBECONFIG for commands that honor only environment configuration.
func kubeEnv(cfg Config) []string {
	if kubeconfig := cfg.EffectiveKubeconfig(); kubeconfig != "" {
		return []string{"KUBECONFIG=" + kubeconfig}
	}
	return nil
}

// text returns stdout or stderr text from a cluster probe command.
func text(ctx context.Context, runner e2eexec.Runner, command e2eexec.Command) string {
	result := runner.Run(ctx, command)
	if len(result.Stdout) != 0 {
		return string(result.Stdout)
	}
	return string(result.Stderr)
}

// timeoutFor returns the feature-specific wait timeout or the cluster default.
func timeoutFor(featureCfg FeatureConfig) string {
	if featureCfg.WaitTimeout != "" {
		return featureCfg.WaitTimeout
	}
	return defaultK3dWaitTimeout
}

// parseDuration returns fallback when a duration option is empty or invalid.
func parseDuration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}

// resultError formats command failures with captured stdout and stderr.
func resultError(label string, result e2eexec.Result) error {
	if result.Err != nil {
		return fmt.Errorf("%s: %w\n%s%s", label, result.Err, result.Stdout, result.Stderr)
	}
	return fmt.Errorf("%s: exit code %d\n%s%s", label, result.ExitCode, result.Stdout, result.Stderr)
}
