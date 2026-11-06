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
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// runRequired logs a setup step and fails when a cluster command exits non-zero.
func runRequired(ctx context.Context, runner e2eexec.Runner, w io.Writer, name string, command e2eexec.Command) error {
	fmt.Fprintf(w, "[e2e] step: %s\n", name)
	if command.SensitiveOutput {
		command.Stdout = nil
		command.Stderr = nil
	} else {
		command.Stdout = w
		command.Stderr = w
	}
	command.LogName = name
	command.QuietOnSuccess = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return resultErrorForCommand(command, result)
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

// runHelm validates the owned state path before running any Helm operation.
func runHelm(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, name string, args []string) error {
	if err := validateHelmState(cfg); err != nil {
		return err
	}
	return runRequired(ctx, runner, w, name, e2eexec.Command{Name: "helm", Args: args, Env: helmEnv(cfg)})
}

// uninstallHelmRelease removes a release and waits for its resources to be
// deleted. A release that is already absent is not an error.
func uninstallHelmRelease(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, name, release, namespace, timeout string) error {
	return runHelm(ctx, runner, w, cfg, name, []string{"uninstall", release, "--namespace", namespace, "--ignore-not-found", "--wait", "--timeout", timeout})
}

// noLocalCleanup is returned when local feature setup made no changes.
func noLocalCleanup(context.Context) error {
	return nil
}

// joinCleanups returns one cleanup that runs each cleanup in order, even after a failure, and
// reports every failure.
func joinCleanups(cleanups ...func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		errs := make([]error, 0, len(cleanups))
		for _, cleanup := range cleanups {
			errs = append(errs, cleanup(ctx))
		}
		return errors.Join(errs...)
	}
}

// runHelmDiagnostic prints successful diagnostic output as well as failures.
func runHelmDiagnostic(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, name string, args []string) error {
	if err := validateHelmState(cfg); err != nil {
		return err
	}
	return run(w, runner, ctx, name, e2eexec.Command{Name: "helm", Args: args, Env: helmEnv(cfg)})
}

// cleanupHelmState removes only this run's repository metadata and cache.
func cleanupHelmState(cfg Config) error {
	if err := validateHelmState(cfg); err != nil {
		return err
	}
	if err := os.RemoveAll(helmStateDir(cfg)); err != nil {
		return fmt.Errorf("remove run-owned Helm state: %w", err)
	}
	return nil
}

// validateHelmState requires an explicit artifact root and rejects links below
// it so an old local-state link cannot redirect writes to shared Helm files.
func validateHelmState(cfg Config) error {
	if !filepath.IsAbs(cfg.Root) {
		return fmt.Errorf("helm state requires an absolute artifact root, got %q", cfg.Root)
	}
	if !validRunName(cfg.runName()) {
		return fmt.Errorf("invalid Helm state run name %q", cfg.runName())
	}
	parent := filepath.Join(cfg.Root, ".local-e2e")
	if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("helm state directory %q is a symbolic link", parent)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect Helm state directory: %w", err)
	}
	return filepath.WalkDir(helmStateDir(cfg), func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Helm state: %w", err)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("helm state path %q is a symbolic link", path)
		}
		return nil
	})
}

// helmStateDir is independent of caller-owned kubeconfig and Helm paths.
func helmStateDir(cfg Config) string {
	return filepath.Join(cfg.Root, ".local-e2e", "helm-"+cfg.runName())
}

// helmEnv keeps repository metadata and downloaded charts in this run's local
// state directory. Leave registry credentials, plugins and Kubernetes auth in
// the caller's environment; these commands do not manage those files.
func helmEnv(cfg Config) []string {
	state := helmStateDir(cfg)
	cache := filepath.Join(state, "cache")
	return append(kubeEnv(cfg),
		"HELM_REPOSITORY_CONFIG="+filepath.Join(state, "repositories.yaml"),
		"HELM_REPOSITORY_CACHE="+filepath.Join(cache, "repository"),
		"HELM_CACHE_HOME="+cache,
		"HELM_CONTENT_CACHE="+filepath.Join(cache, "content"),
	)
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

// resultErrorForCommand omits private output and runner error text from sensitive failures.
func resultErrorForCommand(command e2eexec.Command, result e2eexec.Result) error {
	label := commandString(command)
	if command.SensitiveOutput {
		switch {
		case errors.Is(result.Err, context.Canceled):
			return fmt.Errorf("%s: command failed with exit code %d: %w", label, result.ExitCode, context.Canceled)
		case errors.Is(result.Err, context.DeadlineExceeded):
			return fmt.Errorf("%s: command failed with exit code %d: %w", label, result.ExitCode, context.DeadlineExceeded)
		}
		return fmt.Errorf("%s: command failed with exit code %d", label, result.ExitCode)
	}
	return resultError(label, result)
}
