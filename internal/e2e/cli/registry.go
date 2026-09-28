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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// registryLogin is one Docker registry credential source.
type registryLogin struct {
	registry     string
	username     string
	passwordFile string
}

// prepareRegistryAuth creates pull secrets in every namespace selected k8s scenarios may use.
func prepareRegistryAuth(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, clusterCfg cluster.Config, opts *config.Tests) error {
	namespaces := []string{clusterCfg.Namespace, clusterCfg.DCGMNamespace}
	if selectedScenarioUsesCapability(*opts, "cluster:gpu_operator") {
		namespaces = append(namespaces, "gpu-operator")
	}
	return prepareRegistryAuthForNamespaces(ctx, stdout, runner, clusterCfg, opts, namespaces)
}

// prepareRegistryAuthForNamespaces logs in locally and mirrors Docker credentials into named namespaces.
func prepareRegistryAuthForNamespaces(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, clusterCfg cluster.Config, opts *config.Tests, namespaces []string) error {
	logins, err := registryLogins(*opts)
	if err != nil {
		return err
	}
	if len(namespaces) == 0 || len(logins) == 0 {
		return nil
	}
	for _, namespace := range namespaces {
		if err := cluster.EnsureManagedNamespace(ctx, runner, stdout, clusterCfg, namespace); err != nil {
			return err
		}
	}
	cleanupDockerConfig, err := runDockerRegistryLogins(ctx, stdout, runner, opts, logins)
	if err != nil {
		return err
	}
	defer cleanupDockerConfig()
	if opts.K8sImagePullSecret == "" {
		opts.K8sImagePullSecret = "dcgm-exporter-e2e-registry-auth"
	}
	dockerConfig, err := dockerConfigJSONPath(*opts)
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		if err := cluster.EnsureImagePullSecret(ctx, runner, stdout, clusterCfg, namespace, opts.K8sImagePullSecret, dockerConfig); err != nil {
			return err
		}
	}
	return nil
}

// prepareDockerRegistryLogin performs local Docker logins needed by Docker pulls or manifest checks.
func prepareDockerRegistryLogin(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts *config.Tests) (func(), bool, error) {
	logins, err := registryLogins(*opts)
	if err != nil {
		return nil, false, err
	}
	if len(logins) == 0 {
		return func() {}, false, nil
	}
	cleanupDockerConfig, err := runDockerRegistryLogins(ctx, stdout, runner, opts, logins)
	if err != nil {
		return nil, false, err
	}
	if opts.K8sImagePullSecret == "" {
		opts.K8sImagePullSecret = "dcgm-exporter-e2e-registry-auth"
	}
	return cleanupDockerConfig, true, nil
}

// runDockerRegistryLogins executes docker login commands using password files and an isolated config when needed.
func runDockerRegistryLogins(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts *config.Tests, logins []registryLogin) (func(), error) {
	cleanupDockerConfig, err := ensureDockerConfigDir(opts)
	if err != nil {
		return nil, err
	}
	for _, login := range logins {
		password, err := os.ReadFile(login.passwordFile)
		if err != nil {
			cleanupDockerConfig()
			return nil, fmt.Errorf("read Docker registry password file %s: %w", login.passwordFile, err)
		}
		writeStep(stdout, "step", "docker_login")
		result := runner.Run(ctx, e2eexec.Command{
			Name:           "docker",
			Args:           []string{"login", login.registry, "-u", login.username, "--password-stdin"},
			Env:            dockerConfigEnv(*opts),
			Stdin:          password,
			Stdout:         stdout,
			Stderr:         stdout,
			LogName:        "docker_login",
			QuietOnSuccess: true,
		})
		if result.ExitCode != 0 {
			cleanupDockerConfig()
			return nil, fmt.Errorf("docker login %s failed: %s", login.registry, result.Stderr)
		}
	}
	return cleanupDockerConfig, nil
}

// ensureDockerConfigDir creates a temporary Docker config unless the caller supplied one.
func ensureDockerConfigDir(opts *config.Tests) (func(), error) {
	if opts.DockerConfigDir != "" {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "dcgm-exporter-e2e-docker-config-")
	if err != nil {
		return nil, err
	}
	opts.DockerConfigDir = dir
	return func() {
		_ = os.RemoveAll(dir)
		opts.DockerConfigDir = ""
	}, nil
}

// registryLogins parses registry login specs from flags and optional login files.
func registryLogins(opts config.Tests) ([]registryLogin, error) {
	specs := append([]string{}, opts.DockerRegistryLogins...)
	if opts.DockerRegistryLoginFile != "" {
		file, err := os.Open(opts.DockerRegistryLoginFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			specs = append(specs, line)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	logins := make([]registryLogin, 0, len(specs))
	for _, spec := range specs {
		parts := strings.Split(spec, ",")
		if len(parts) != 3 {
			return nil, fmt.Errorf("docker registry login spec must be REGISTRY,USERNAME,PASSWORD_FILE")
		}
		login := registryLogin{
			registry:     strings.TrimSpace(parts[0]),
			username:     strings.TrimSpace(parts[1]),
			passwordFile: strings.TrimSpace(parts[2]),
		}
		if login.registry == "" || login.username == "" || login.passwordFile == "" {
			return nil, fmt.Errorf("docker registry login spec must be REGISTRY,USERNAME,PASSWORD_FILE")
		}
		logins = append(logins, login)
	}
	return logins, nil
}

// dockerConfigEnv returns the DOCKER_CONFIG environment for Docker subcommands.
func dockerConfigEnv(opts config.Tests) []string {
	if opts.DockerConfigDir != "" {
		return []string{"DOCKER_CONFIG=" + opts.DockerConfigDir}
	}
	return nil
}

// dockerConfigJSONPath resolves the config.json path that Kubernetes pull-secret creation should read.
func dockerConfigJSONPath(opts config.Tests) (string, error) {
	if opts.DockerConfigDir != "" {
		if err := os.MkdirAll(opts.DockerConfigDir, 0o700); err != nil {
			return "", err
		}
		return filepath.Join(opts.DockerConfigDir, "config.json"), nil
	}
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "config.json"), nil
}
