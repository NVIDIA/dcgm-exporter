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
	"strings"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/installdeps"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

const (
	testsCategoryDependencies = "Dependencies"
	testsCategoryExecution    = "Execution"
	testsCategoryFeatures     = "Feature setup"
	testsCategoryImages       = "Images and registry authentication"
	testsCategoryKubernetes   = "Kubernetes"
)

// runTests parses environment-backed flags and dispatches the selected suites.
func runTests(ctx context.Context, args []string, stdout, stderr io.Writer, root string, runner e2eexec.Runner) error {
	if helpRequested(args) {
		writeTestsHelp(stdout)
		return nil
	}
	opts, err := parseTests(args, stderr)
	if err != nil {
		return err
	}
	runner = newOutputRunner(stdout, runner, opts.Verbose)
	restorePath, err := installdeps.ConfigureToolPath(root, opts)
	if err != nil {
		return err
	}
	defer restorePath()
	if opts.ListScenarios {
		return scenario.WriteList(stdout)
	}
	mode, err := detectArtifactMode(root)
	if err != nil {
		return setupFailure(err)
	}
	writeSection(stdout, "Run configuration")
	fmt.Fprintf(stdout, "[e2e] artifact mode: %s\n", mode)
	writeVerbose(stdout, opts.Verbose, "enabled")
	if err := prepareNVMLInjection(&opts, !opts.BuildOnly && !opts.DryRun); err != nil {
		return setupFailure(err)
	}
	if opts.DryRun {
		return runDryRun(ctx, stdout, root, runner, opts)
	}
	if err := validateLiveImageConfig(root, mode, opts); err != nil {
		return setupFailure(err)
	}
	if err := installdeps.IfRequested(ctx, stdout, root, runner, &opts); err != nil {
		return setupFailure(err)
	}
	return runSelectedSuitesContext(ctx, stdout, root, runner, config.Config{Tests: opts})
}

// writeTestsHelp renders help from the same urfave command definition used for parsing.
func writeTestsHelp(w io.Writer) {
	opts := defaultTestsConfig()
	command := newTestsCLICommand(&opts)
	command.Flags = append(command.Flags, &urfavecli.BoolFlag{
		Name:     "help",
		Aliases:  []string{"h"},
		Usage:    "show help",
		Category: testsCategoryExecution,
	})
	urfavecli.HelpPrinter(w, urfavecli.CommandHelpTemplate, command)
}

// parseTests merges environment defaults with explicit tests command flags.
func parseTests(args []string, stderr io.Writer) (config.Tests, error) {
	opts, err := testsFromEnv()
	if err != nil {
		return opts, err
	}
	command := newTestsCLICommand(&opts)
	args = normalizeBoolArgs(args, map[string]struct{}{
		"build-only":        {},
		"install-deps":      {},
		"dry-run":           {},
		"verbose":           {},
		"list-scenarios":    {},
		"result-markers":    {},
		"no-result-markers": {},
	})
	app := urfavecli.NewApp()
	app.Name = "e2e"
	app.Writer = io.Discard
	app.ErrWriter = stderr
	app.HideHelp = true
	app.Commands = []*urfavecli.Command{command}
	if err := app.Run(append([]string{"e2e", "tests"}, args...)); err != nil {
		return opts, err
	}
	return opts, nil
}

// newTestsCLICommand defines the tests flags, environment variables, help, and validation in one place.
func newTestsCLICommand(opts *config.Tests) *urfavecli.Command {
	suites := urfavecli.NewStringSlice(opts.Suites...)
	skipSuites := urfavecli.NewStringSlice(opts.SkipSuites...)
	scenarios := urfavecli.NewStringSlice(opts.Scenarios...)
	skipScenarios := urfavecli.NewStringSlice(opts.SkipScenarios...)
	registryLogins := (*stringList)(&opts.DockerRegistryLogins)
	return &urfavecli.Command{
		Name:      "tests",
		HelpName:  "e2e tests",
		Usage:     "Run dcgm-exporter e2e validation suites",
		UsageText: "e2e tests [options]",
		Description: `Select suites or scenarios, inspect the plan with --dry-run, and use --verbose to include successful command output and capability evidence.

Use --list-scenarios to discover selectors and --dry-run to inspect a plan.
Result markers are stable machine-readable output and are enabled only when requested. Exit 10 means setup failure; exit 20 means test failure.`,
		Flags: []urfavecli.Flag{
			&urfavecli.BoolFlag{Name: "build-only", EnvVars: []string{"E2E_BUILD_ONLY"}, Value: opts.BuildOnly, Destination: &opts.BuildOnly, Usage: "build or locate selected suite binaries without running them", Category: testsCategoryExecution},
			&urfavecli.StringFlag{Name: "tools-dir", EnvVars: []string{"E2E_TOOLS_DIR"}, Destination: &opts.ToolsDir, Usage: "directory for downloaded helper tools and source-tree suite binaries", Category: testsCategoryDependencies},
			&urfavecli.BoolFlag{Name: "install-deps", EnvVars: []string{"E2E_INSTALL_DEPS"}, Value: opts.InstallDeps, Destination: &opts.InstallDeps, Usage: "install or configure missing host prerequisites; set false to verify only", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "install-deps-docker", EnvVars: []string{"E2E_INSTALL_DEPS_DOCKER"}, Destination: &opts.InstallDepsDocker, Usage: "Docker installation mode: `true|false`", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "install-deps-nvidia-container-toolkit", EnvVars: []string{"E2E_INSTALL_DEPS_NVIDIA_CONTAINER_TOOLKIT"}, Destination: &opts.InstallDepsNvidiaToolkit, Usage: "NVIDIA Container Toolkit installation mode: `true|false`", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "install-deps-dcgm", EnvVars: []string{"E2E_INSTALL_DEPS_DCGM"}, Destination: &opts.InstallDepsDCGM, Usage: "host DCGM installation mode: `true|false`", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "install-deps-vsock", EnvVars: []string{"E2E_INSTALL_DEPS_VSOCK"}, Destination: &opts.InstallDepsVSOCK, Usage: "VSOCK module configuration mode: `true|false`", Category: testsCategoryDependencies},
			&urfavecli.BoolFlag{Name: "dry-run", EnvVars: []string{"E2E_DRY_RUN"}, Destination: &opts.DryRun, Usage: "print capability and scenario decisions without running tests", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, EnvVars: []string{"E2E_VERBOSE"}, Destination: &opts.Verbose, Usage: "print all command output and capability evidence", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "list-scenarios", EnvVars: []string{"E2E_LIST_SCENARIOS"}, Value: opts.ListScenarios, Destination: &opts.ListScenarios, Usage: "list available suite/scenario selectors and exit", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "result-markers", Destination: &opts.ResultMarkers, Value: opts.ResultMarkers, Usage: "emit reserved &&&& result marker lines; environment: E2E_RESULT_MARKERS", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "no-result-markers", Destination: &opts.NoResultMarkers, Usage: "disable result markers; equivalent to E2E_RESULT_MARKERS=false", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "suite", Value: suites, Usage: "run only `suite`; repeat or set E2E_SUITE for multiple suites", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "skip-suite", Value: skipSuites, Usage: "skip `suite`; repeat or set E2E_SKIP_SUITE for multiple suites", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "scenario", Value: scenarios, Usage: "run only `suite/name`; repeat or set E2E_SCENARIO for multiple scenarios", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "skip-scenario", Value: skipScenarios, Usage: "skip `suite/name`; repeat or set E2E_SKIP_SCENARIO for multiple scenarios", Category: testsCategoryExecution},
			&urfavecli.StringFlag{Name: "kubeconfig", EnvVars: []string{"E2E_KUBECONFIG"}, Destination: &opts.Kubeconfig, Usage: "use an existing Kubernetes cluster through `path`", Category: testsCategoryKubernetes},
			&urfavecli.BoolFlag{Name: "k3d-keep-cluster", EnvVars: []string{"E2E_K3D_KEEP_CLUSTER"}, Destination: &opts.KeepCluster, Usage: "keep e2e-managed k3d resources after the run", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k3d-cluster-name", EnvVars: []string{"E2E_K3D_CLUSTER_NAME"}, Value: opts.ClusterName, Destination: &opts.ClusterName, Usage: "owned k3d cluster `name`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "helm-namespace", EnvVars: []string{"E2E_HELM_NAMESPACE"}, Value: opts.Namespace, Destination: &opts.Namespace, Usage: "dcgm-exporter Kubernetes `namespace`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "helm-release-name", Aliases: []string{"helm-release"}, EnvVars: []string{"E2E_HELM_RELEASE_NAME"}, Value: opts.ReleaseName, Destination: &opts.ReleaseName, Usage: "dcgm-exporter Helm release `name`", Category: testsCategoryKubernetes},
			&urfavecli.GenericFlag{Name: "docker-registry-login", EnvVars: []string{"E2E_DOCKER_REGISTRY_LOGIN"}, Value: registryLogins, Usage: "Docker login as `REGISTRY,USERNAME,PASSWORD_FILE`; repeat for multiple registries", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "docker-registry-login-file", EnvVars: []string{"E2E_DOCKER_REGISTRY_LOGIN_FILE"}, Destination: &opts.DockerRegistryLoginFile, Usage: "read newline-separated Docker login specs from `path`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "docker-config-dir", EnvVars: []string{"E2E_DOCKER_CONFIG_DIR"}, Destination: &opts.DockerConfigDir, Usage: "Docker configuration `directory` used for authenticated pulls", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k8s-image-pull-secret", EnvVars: []string{"E2E_K8S_IMAGE_PULL_SECRET"}, Destination: &opts.K8sImagePullSecret, Usage: "existing Kubernetes image pull secret `name`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "exporter-image", EnvVars: []string{"E2E_EXPORTER_IMAGE"}, Value: opts.ExporterImage, Destination: &opts.ExporterImage, Usage: "complete dcgm-exporter image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "exporter-ubuntu-image", EnvVars: []string{"E2E_EXPORTER_UBUNTU_IMAGE"}, Value: opts.ExporterUbuntuImage, Destination: &opts.ExporterUbuntuImage, Usage: "optional complete Ubuntu dcgm-exporter image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "dcgm-image", EnvVars: []string{"E2E_DCGM_IMAGE"}, Value: opts.DCGMImage, Destination: &opts.DCGMImage, Usage: "complete standalone DCGM image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "dcgm-version", EnvVars: []string{"E2E_DCGM_VERSION", "DCGM_VERSION"}, Value: opts.Dependencies.DCGMVersion, Destination: &opts.Dependencies.DCGMVersion, Usage: "semantic DCGM `version` required from nv-hostengine", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k3s-image", EnvVars: []string{"E2E_K3S_IMAGE"}, Value: opts.K3SImage, Destination: &opts.K3SImage, Usage: "complete K3S base image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k3d-node-base-image", EnvVars: []string{"E2E_K3D_NODE_BASE_IMAGE"}, Value: opts.K3DNodeBaseImage, Destination: &opts.K3DNodeBaseImage, Usage: "CUDA base image `reference` used to build the K3D node", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k3d-node-output-image", EnvVars: []string{"E2E_K3D_NODE_OUTPUT_IMAGE"}, Value: opts.K3DNodeOutputImage, Destination: &opts.K3DNodeOutputImage, Usage: "image `reference` assigned to the built K3D node", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "busybox-image", EnvVars: []string{"E2E_BUSYBOX_IMAGE"}, Value: opts.BusyboxImage, Destination: &opts.BusyboxImage, Usage: "complete BusyBox helper image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "container-toolkit-test-image", EnvVars: []string{"E2E_CONTAINER_TOOLKIT_TEST_IMAGE"}, Value: opts.ContainerToolkitTestImage, Destination: &opts.ContainerToolkitTestImage, Usage: "CUDA image `reference` used to test Container Toolkit GPU access", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "cuda-workload-image", EnvVars: []string{"E2E_CUDA_WORKLOAD_IMAGE"}, Value: opts.CUDAWorkloadImage, Destination: &opts.CUDAWorkloadImage, Usage: "CUDA workload pod image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k3d-version", EnvVars: []string{"K3D_VERSION"}, Value: opts.Dependencies.K3DVersion, Destination: &opts.Dependencies.K3DVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "k3s-version", EnvVars: []string{"K3S_VERSION"}, Value: opts.Dependencies.K3SVersion, Destination: &opts.Dependencies.K3SVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "helm-version", EnvVars: []string{"HELM_VERSION"}, Value: opts.Dependencies.HelmVersion, Destination: &opts.Dependencies.HelmVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "device-plugin-version", EnvVars: []string{"NVIDIA_DEVICE_PLUGIN_VERSION"}, Value: opts.Dependencies.DevicePluginVersion, Destination: &opts.Dependencies.DevicePluginVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "gpu-operator-version", EnvVars: []string{"GPU_OPERATOR_VERSION"}, Value: opts.Dependencies.GPUOperatorVersion, Destination: &opts.Dependencies.GPUOperatorVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "dra-driver-version", EnvVars: []string{"NVIDIA_DRA_DRIVER_VERSION"}, Value: opts.Dependencies.DRADriverVersion, Destination: &opts.Dependencies.DRADriverVersion, Hidden: true},
			&urfavecli.StringFlag{Name: "k3d-amd64-sha256", EnvVars: []string{"K3D_LINUX_AMD64_SHA256"}, Value: opts.Dependencies.K3DAMD64SHA256, Destination: &opts.Dependencies.K3DAMD64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "k3d-arm64-sha256", EnvVars: []string{"K3D_LINUX_ARM64_SHA256"}, Value: opts.Dependencies.K3DARM64SHA256, Destination: &opts.Dependencies.K3DARM64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "kubectl-amd64-sha256", EnvVars: []string{"KUBECTL_LINUX_AMD64_SHA256"}, Value: opts.Dependencies.KubectlAMD64SHA256, Destination: &opts.Dependencies.KubectlAMD64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "kubectl-arm64-sha256", EnvVars: []string{"KUBECTL_LINUX_ARM64_SHA256"}, Value: opts.Dependencies.KubectlARM64SHA256, Destination: &opts.Dependencies.KubectlARM64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "helm-amd64-sha256", EnvVars: []string{"HELM_LINUX_AMD64_SHA256"}, Value: opts.Dependencies.HelmAMD64SHA256, Destination: &opts.Dependencies.HelmAMD64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "helm-arm64-sha256", EnvVars: []string{"HELM_LINUX_ARM64_SHA256"}, Value: opts.Dependencies.HelmARM64SHA256, Destination: &opts.Dependencies.HelmARM64SHA256, Hidden: true},
			&urfavecli.StringFlag{Name: "k8s-runtime-class", EnvVars: []string{"E2E_K8S_RUNTIME_CLASS"}, Destination: &opts.K8sRuntimeClass, Usage: "Kubernetes RuntimeClass `name` for GPU pods", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k8s-node-selector-key", EnvVars: []string{"E2E_K8S_NODE_SELECTOR_KEY"}, Destination: &opts.K8sNodeSelectorKey, Usage: "node selector `key` for GPU pods", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k8s-node-selector-value", EnvVars: []string{"E2E_K8S_NODE_SELECTOR_VALUE"}, Destination: &opts.K8sNodeSelectorValue, Usage: "node selector `value` for GPU pods", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k3d-ip-family", EnvVars: []string{"E2E_K3D_IP_FAMILY"}, Value: opts.K3dIPFamily, Destination: &opts.K3dIPFamily, Usage: "owned k3d network mode: `ipv4|ipv6|dualstack`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "shared-gpu-configure", EnvVars: []string{"E2E_SHARED_GPU_CONFIGURE"}, Value: opts.SharedGPUConfigure, Destination: &opts.SharedGPUConfigure, Usage: "time-sliced GPU setup mode: `auto|true|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "shared-gpu-replicas", EnvVars: []string{"E2E_SHARED_GPU_REPLICAS"}, Destination: &opts.SharedGPUReplicas, Usage: "time-sliced GPU replica `count`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dra-configure", EnvVars: []string{"E2E_DRA_CONFIGURE"}, Value: opts.DRAConfigure, Destination: &opts.DRAConfigure, Usage: "NVIDIA DRA driver setup mode: `auto|true|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "mig-configure", EnvVars: []string{"E2E_MIG_CONFIGURE"}, Value: opts.MIGConfigure, Destination: &opts.MIGConfigure, Usage: "MIG setup mode: `auto|true|false|profile`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dcgm-failure-injection", EnvVars: []string{"E2E_DCGM_FAILURE_INJECTION"}, Value: opts.DCGMFailureInjection, Destination: &opts.DCGMFailureInjection, Usage: "DCGM failure injection mode: `auto|true|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dcgm-nvml-injection-yaml", EnvVars: []string{"E2E_DCGM_NVML_INJECTION_YAML"}, Destination: &opts.DCGMNVMLInjectionYAML, Usage: "run compatible host scenarios with a DCGM NVML injection YAML `path`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "gpu-operator", EnvVars: []string{"E2E_GPU_OPERATOR"}, Value: opts.GPUOperator, Destination: &opts.GPUOperator, Usage: "GPU Operator mode: `auto|existing|install|false`", Category: testsCategoryFeatures},
		},
		Action: func(c *urfavecli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected e2e tests arguments: %v", c.Args().Slice())
			}
			opts.Suites = c.StringSlice("suite")
			opts.SkipSuites = c.StringSlice("skip-suite")
			opts.Scenarios = c.StringSlice("scenario")
			opts.SkipScenarios = c.StringSlice("skip-scenario")
			if c.IsSet("result-markers") {
				opts.E2EResultMarkers = c.Bool("result-markers")
			}
			if opts.NoResultMarkers {
				opts.ResultMarkers = false
				opts.E2EResultMarkers = false
			}
			return validateTestsConfig(*opts)
		},
	}
}

// normalizeBoolArgs accepts both "--flag value" and "--flag=value" for bool flags.
func normalizeBoolArgs(args []string, boolFlags map[string]struct{}) []string {
	normalized := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, ok := boolFlagName(arg)
		if !ok {
			normalized = append(normalized, arg)
			continue
		}
		if _, isBool := boolFlags[name]; !isBool || strings.Contains(arg, "=") || i+1 >= len(args) || !isBoolLiteral(args[i+1]) {
			normalized = append(normalized, arg)
			continue
		}
		normalized = append(normalized, arg+"="+args[i+1])
		i++
	}
	return normalized
}

// boolFlagName extracts a flag name from short or long flag syntax.
func boolFlagName(arg string) (string, bool) {
	switch {
	case strings.HasPrefix(arg, "--"):
		return strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--"), true
	case strings.HasPrefix(arg, "-"):
		return strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "-"), true
	default:
		return "", false
	}
}

// isBoolLiteral reports whether value is one of urfave/cli's accepted bool spellings.
func isBoolLiteral(value string) bool {
	switch value {
	case "true", "false", "1", "0", "t", "f", "T", "F", "TRUE", "FALSE":
		return true
	default:
		return false
	}
}

// stringList lets repeated registry login flags append into config.Tests.
type stringList []string

// Set appends one flag value.
func (s *stringList) Set(value string) error {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			*s = append(*s, line)
		}
	}
	return nil
}

// String returns the comma-separated flag display form.
func (s *stringList) String() string {
	return strings.Join(*s, ",")
}
