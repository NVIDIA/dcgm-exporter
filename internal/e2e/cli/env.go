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
func testsFromEnv() (config.TestOptions, error) {
	return testOptionsFromEnv()
}

// clusterLifecycleOptionsFromEnv excludes bootstrap-only controls that cluster commands do not expose.
func clusterLifecycleOptionsFromEnv() (config.TestOptions, error) {
	return testOptionsFromEnv()
}

// testOptionsFromEnv builds test options from E2E environment variables and configured defaults.
func testOptionsFromEnv() (config.TestOptions, error) {
	defaults := defaultTestsConfig()
	runName := envStringDefault("E2E_RUN_NAME", defaults.RunName)
	runConfig := cluster.DefaultConfig()
	opts := config.TestOptions{
		PrebuiltBinaries:          defaults.PrebuiltBinaries,
		ToolsDir:                  os.Getenv("E2E_TOOLS_DIR"),
		ExporterBinary:            os.Getenv("E2E_EXPORTER_BINARY"),
		BootstrapOnly:             defaults.BootstrapOnly,
		DryRun:                    defaults.DryRun,
		Verbose:                   defaults.Verbose,
		ResultMarkers:             defaults.ResultMarkers,
		E2EResultMarkers:          defaults.E2EResultMarkers,
		Suites:                    envList("E2E_SUITE"),
		SkipSuites:                envList("E2E_SKIP_SUITE"),
		Scenarios:                 envList("E2E_SCENARIO"),
		SkipScenarios:             envList("E2E_SKIP_SCENARIO"),
		Kubeconfig:                os.Getenv("E2E_KUBECONFIG"),
		Cleanup:                   defaults.Cleanup,
		KeepCluster:               defaults.KeepCluster,
		RunName:                   runName,
		ClusterName:               envStringDefault("E2E_K3D_CLUSTER_NAME", runConfig.ClusterName),
		Namespace:                 envStringDefault("E2E_HELM_NAMESPACE", runConfig.Namespace),
		ReleaseName:               envStringDefault("E2E_HELM_RELEASE_NAME", runConfig.ReleaseName),
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
		WaitTimeout:               envStringDefault("E2E_WAIT_TIMEOUT", defaults.WaitTimeout),
		Timeout:                   envStringDefault("E2E_TIMEOUT", defaults.Timeout),
		K8sRuntimeClass:           os.Getenv("E2E_K8S_RUNTIME_CLASS"),
		K8sNodeSelectorKey:        os.Getenv("E2E_K8S_NODE_SELECTOR_KEY"),
		K8sNodeSelectorValue:      os.Getenv("E2E_K8S_NODE_SELECTOR_VALUE"),
		K3dIPFamily:               envStringDefault("E2E_K3D_IP_FAMILY", defaults.K3dIPFamily),
		SharedGPUReplicas:         os.Getenv("E2E_SHARED_GPU_REPLICAS"),
		MIGConfigure:              envStringDefault("E2E_MIG_CONFIGURE", defaults.MIGConfigure),
	}

	// An invalid mode is kept as written, so an explicit flag can still replace it;
	// the winning value is validated when the flags are applied.
	opts.K3dCgroupV1 = envFeatureMode("E2E_K3D_CGROUP_V1", defaults.K3dCgroupV1)
	opts.SharedGPUConfigure = envFeatureMode("E2E_SHARED_GPU_CONFIGURE", defaults.SharedGPUConfigure)
	opts.DRAConfigure = envFeatureMode("E2E_DRA_CONFIGURE", defaults.DRAConfigure)
	opts.GPUOperator = envGPUOperatorMode("E2E_GPU_OPERATOR", defaults.GPUOperator)

	return opts, nil
}

// envFeatureMode reads an auto, true or false mode variable, normalized when it is
// valid and kept as written otherwise so flag validation can report it. A blank
// value keeps the fallback.
func envFeatureMode(name string, fallback config.FeatureMode) config.FeatureMode {
	value := envModeDefault(name, string(fallback))
	if mode, err := config.ParseFeatureMode(value); err == nil {
		return mode
	}

	return config.FeatureMode(value)
}

// envGPUOperatorMode reads E2E_GPU_OPERATOR, normalized when it is valid and kept as
// written otherwise so flag validation can report it. A blank value keeps the
// fallback.
func envGPUOperatorMode(name string, fallback config.GPUOperatorMode) config.GPUOperatorMode {
	value := envModeDefault(name, string(fallback))
	if mode, err := config.ParseGPUOperatorMode(value); err == nil {
		return mode
	}

	return config.GPUOperatorMode(value)
}

// bootstrapFromEnvironment reads the bootstrap mode from the environment.
func bootstrapFromEnvironment() (config.BootstrapMode, config.ValueSource, error) {
	value := strings.TrimSpace(os.Getenv("E2E_BOOTSTRAP"))
	if value == "" {
		return "", config.ValueDefault, nil
	}
	mode, err := parseBootstrapMode(value)
	if err != nil {
		return "", "", fmt.Errorf("E2E_BOOTSTRAP: %w", err)
	}
	return mode, config.ValueEnvironment, nil
}

// parseBootstrapMode validates a bootstrap mode after CLI and environment parsing.
func parseBootstrapMode(value string) (config.BootstrapMode, error) {
	return config.ParseBootstrapMode(value)
}

// defaultTestsConfig returns defaults that are independent of the environment and package metadata.
func defaultTestsConfig() config.TestOptions {
	clusterDefaults := cluster.DefaultConfig()
	return config.TestOptions{
		RunName:            clusterDefaults.RunName,
		ResultMarkers:      false,
		E2EResultMarkers:   false,
		Cleanup:            true,
		ClusterName:        clusterDefaults.ClusterName,
		Namespace:          clusterDefaults.Namespace,
		ReleaseName:        clusterDefaults.ReleaseName,
		WaitTimeout:        cluster.DefaultWaitTimeout(),
		K3dIPFamily:        "auto",
		K3dCgroupV1:        config.FeatureAuto,
		SharedGPUConfigure: config.FeatureOn,
		DRAConfigure:       config.FeatureOn,
		MIGConfigure:       "auto",
		GPUOperator:        config.GPUOperatorInstall,
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

// envModeDefault reads a mode variable, treating a value of only spaces as unset
// so it cannot reach the options as an invalid mode.
func envModeDefault(name, fallback string) string {
	if value := os.Getenv(name); strings.TrimSpace(value) != "" {
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

// envBoolFlagDefault preserves urfave's strconv.ParseBool input contract for boolean flags.
func envBoolFlagDefault(name string, fallback bool) (bool, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("could not parse %q as bool value from environment variable %q: %w", value, name, err)
	}
	return parsed, nil
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
