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
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// testsFromEnv builds the default tests configuration from E2E_* environment variables.
func testsFromEnv() (config.Tests, error) {
	defaults := defaultTestsConfig()
	resultMarkers, err := envBoolDefault("E2E_RESULT_MARKERS", defaults.ResultMarkers)
	if err != nil {
		return config.Tests{}, err
	}
	installDeps, err := envBoolDefault("E2E_INSTALL_DEPS", defaults.InstallDeps)
	if err != nil {
		return config.Tests{}, err
	}
	dryRun, err := envBoolDefault("E2E_DRY_RUN", false)
	if err != nil {
		return config.Tests{}, err
	}
	verbose, err := envBoolDefault("E2E_VERBOSE", false)
	if err != nil {
		return config.Tests{}, err
	}
	suiteResultMarkers, err := envBoolDefault("E2E_SUITE_RESULT_MARKERS", resultMarkers)
	if err != nil {
		return config.Tests{}, err
	}
	keepCluster, err := envBoolDefault("E2E_K3D_KEEP_CLUSTER", false)
	if err != nil {
		return config.Tests{}, err
	}
	return config.Tests{
		ToolsDir:                  os.Getenv("E2E_TOOLS_DIR"),
		InstallDeps:               installDeps,
		InstallDepsDocker:         os.Getenv("E2E_INSTALL_DEPS_DOCKER"),
		InstallDepsNvidiaToolkit:  os.Getenv("E2E_INSTALL_DEPS_NVIDIA_CONTAINER_TOOLKIT"),
		InstallDepsDCGM:           os.Getenv("E2E_INSTALL_DEPS_DCGM"),
		InstallDepsVSOCK:          os.Getenv("E2E_INSTALL_DEPS_VSOCK"),
		DryRun:                    dryRun,
		Verbose:                   verbose,
		ResultMarkers:             resultMarkers,
		E2EResultMarkers:          suiteResultMarkers,
		Suites:                    envList("E2E_SUITE"),
		SkipSuites:                envList("E2E_SKIP_SUITE"),
		Scenarios:                 envList("E2E_SCENARIO"),
		SkipScenarios:             envList("E2E_SKIP_SCENARIO"),
		Kubeconfig:                os.Getenv("E2E_KUBECONFIG"),
		KeepCluster:               keepCluster,
		ClusterName:               envStringDefault("E2E_K3D_CLUSTER_NAME", defaults.ClusterName),
		Namespace:                 envStringDefault("E2E_HELM_NAMESPACE", defaults.Namespace),
		ReleaseName:               envStringDefault("E2E_HELM_RELEASE_NAME", defaults.ReleaseName),
		DockerRegistryLoginFile:   os.Getenv("E2E_DOCKER_REGISTRY_LOGIN_FILE"),
		DockerConfigDir:           os.Getenv("E2E_DOCKER_CONFIG_DIR"),
		K8sImagePullSecret:        os.Getenv("E2E_K8S_IMAGE_PULL_SECRET"),
		MIGInstanceEntityID:       os.Getenv("E2E_MIG_INSTANCE_ENTITY_ID"),
		MIGInstanceNVMLID:         os.Getenv("E2E_MIG_INSTANCE_NVML_ID"),
		UnsupportedFieldCandidate: os.Getenv("E2E_UNSUPPORTED_FIELD_CANDIDATE"),
		UnsupportedFieldEvidence:  os.Getenv("E2E_UNSUPPORTED_FIELD_EVIDENCE"),
		RemoteDCGM:                os.Getenv("E2E_REMOTE_DCGM"),
		DCGMNamespace:             os.Getenv("E2E_DCGM_NAMESPACE"),
		DCGMName:                  os.Getenv("E2E_DCGM_NAME"),
		DCGMPort:                  os.Getenv("E2E_DCGM_PORT"),
		WaitTimeout:               os.Getenv("E2E_WAIT_TIMEOUT"),
		K8sRuntimeClass:           os.Getenv("E2E_K8S_RUNTIME_CLASS"),
		K8sNodeSelectorKey:        os.Getenv("E2E_K8S_NODE_SELECTOR_KEY"),
		K8sNodeSelectorValue:      os.Getenv("E2E_K8S_NODE_SELECTOR_VALUE"),
		K3dIPFamily:               envStringDefault("E2E_K3D_IP_FAMILY", defaults.K3dIPFamily),
		SharedGPUConfigure:        envStringDefault("E2E_SHARED_GPU_CONFIGURE", defaults.SharedGPUConfigure),
		SharedGPUReplicas:         os.Getenv("E2E_SHARED_GPU_REPLICAS"),
		DRAConfigure:              envStringDefault("E2E_DRA_CONFIGURE", defaults.DRAConfigure),
		MIGConfigure:              envStringDefault("E2E_MIG_CONFIGURE", defaults.MIGConfigure),
		DCGMFailureInjection:      envStringDefault("E2E_DCGM_FAILURE_INJECTION", defaults.DCGMFailureInjection),
		GPUOperator:               envStringDefault("E2E_GPU_OPERATOR", defaults.GPUOperator),
	}, nil
}

// defaultTestsConfig returns defaults that are independent of the environment and package metadata.
func defaultTestsConfig() config.Tests {
	clusterDefaults := cluster.DefaultConfig()
	return config.Tests{
		InstallDeps:          true,
		ResultMarkers:        false,
		E2EResultMarkers:     false,
		ClusterName:          clusterDefaults.ClusterName,
		Namespace:            clusterDefaults.Namespace,
		ReleaseName:          clusterDefaults.ReleaseName,
		K3dIPFamily:          "dualstack",
		SharedGPUConfigure:   "true",
		DRAConfigure:         "true",
		MIGConfigure:         "true",
		DCGMFailureInjection: "true",
		GPUOperator:          "install",
	}
}

// envList parses comma- or newline-separated selector lists.
func envList(name string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == ','
	})
	items := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

// envStringDefault returns the environment value or the supplied fallback.
func envStringDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// envBoolDefault parses an optional bool environment variable.
func envBoolDefault(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := parseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", name, value)
	}
	return parsed, nil
}

// validBoolLiteral reports whether value is accepted by parseBool.
func validBoolLiteral(value string) bool {
	_, err := parseBool(value)
	return err == nil
}

// parseBool accepts common shell-style boolean spellings.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "t", "yes", "y", "on":
		return true, nil
	case "false", "0", "f", "no", "n", "off":
		return false, nil
	default:
		return strconv.ParseBool(value)
	}
}

// oneOfFold compares a normalized value against an allowed set.
func oneOfFold(value string, allowed ...string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// containsString reports whether value appears exactly in values.
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
