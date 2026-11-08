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
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/bootstrap"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

const (
	testsCategoryDependencies   = "Dependencies"
	testsCategoryExecution      = "Execution"
	testsCategoryFeatures       = "Feature setup"
	testsCategoryImages         = "Images and registry authentication"
	testsCategoryKubernetes     = "Kubernetes"
	testsFlagResultMarkers      = "result-markers"
	testsFlagHelmNamespace      = "helm-namespace"
	testsFlagDCGMNamespace      = "dcgm-namespace"
	testsFlagBootstrapComponent = "bootstrap-component"
)

// runTests dispatches the suites selected by the parser-owned tests command.
func runTests(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.TestOptions) (resultErr error) {
	runner = newCommandOutputRunner(stdout, runner, opts.Verbose)

	if opts.ListScenarios {
		return writeScenarioList(stdout, opts.Verbose)
	}

	ctx, cancel, err := timeoutContext(ctx, opts.Timeout)
	if err != nil {
		return setupFailure(err)
	}
	defer cancel()

	root, err = resolveE2ERoot(root)
	if err != nil {
		return setupFailure(err)
	}

	if err := rejectEmptySelection(opts); err != nil {
		return err
	}

	restorePath, err := bootstrap.ConfigureToolPath(root, opts)
	if err != nil {
		return err
	}
	defer restorePath()

	if err := prepareRunInputs(stdout, root, &opts); err != nil {
		return setupFailure(err)
	}

	selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts})
	requirements := scenario.RequirementsFor(selected, runConditions(opts))
	if opts.DryRun {
		return runDryRun(stdout, opts, selected)
	}

	if err := validateRunImages(stdout, opts, requirements); err != nil {
		return setupFailure(err)
	}

	dockerClientRequired := !opts.BuildOnly && (scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDocker}) ||
		scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDockerCLI}))
	runner, loginDockerSession, cleanupDockerSession, err := prepareDockerRunSession(ctx, stdout, runner, &opts, dockerClientRequired)
	if err != nil {
		return setupFailure(err)
	}

	defer func() {
		if cleanupErr := cleanupDockerSession(); cleanupErr != nil {
			resultErr = errors.Join(resultErr, setupFailure(cleanupErr))
		}
	}()

	if err := prepareRunPrerequisites(ctx, stdout, root, runner, &opts, requirements, loginDockerSession); err != nil {
		return setupFailure(err)
	}

	if opts.BootstrapOnly {
		return nil
	}
	return runSelectedSuitesContext(ctx, stdout, root, runner, config.Config{TestOptions: opts})
}

// writeScenarioList prints the catalog selectors and, when verbose, their direct Go commands.
func writeScenarioList(stdout io.Writer, verbose bool) error {
	if err := scenario.WriteList(stdout); err != nil {
		return err
	}
	if verbose {
		return writeDirectGoCommands(stdout)
	}
	return nil
}

// rejectEmptySelection fails a run whose selectors choose no catalog scenario.
// Only a bootstrap-only run without explicit selectors may prepare tools without scenarios.
func rejectEmptySelection(opts config.TestOptions) error {
	selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts})
	explicitSelection := len(opts.Suites) != 0 || len(opts.Scenarios) != 0
	if len(selected) == 0 && (explicitSelection || !opts.BootstrapOnly || opts.DryRun) {
		return setupFailure(fmt.Errorf("suite and scenario selectors choose no runnable catalog scenarios"))
	}
	return nil
}

// prepareRunInputs detects the artifact mode, reports the run configuration, and
// resolves optional fixture inputs before scenarios are selected for execution.
func prepareRunInputs(stdout io.Writer, root string, opts *config.TestOptions) error {
	mode, err := detectArtifactMode(root)
	if err != nil {
		return err
	}
	if err := normalizeBootstrapForArtifact(opts, mode); err != nil {
		return err
	}
	writeSection(stdout, "Run configuration")
	fmt.Fprintf(stdout, "[e2e] artifact mode: %s\n", mode)
	fmt.Fprintf(stdout, "[e2e] bootstrap: %s (%s)\n", opts.BootstrapMode, opts.BootstrapSource)
	writeVerbose(stdout, opts.Verbose, "enabled")
	prepareFixtures := !opts.BuildOnly && !opts.DryRun
	if err := prepareNVMLInjection(opts, prepareFixtures); err != nil {
		return err
	}
	return prepareNVSDMMock(opts, prepareFixtures)
}

// validateRunImages checks and prints the image references for runs that execute scenarios.
func validateRunImages(stdout io.Writer, opts config.TestOptions, requirements []scenario.SelectedRequirement) error {
	if opts.BuildOnly || opts.BootstrapOnly {
		return nil
	}
	if err := validateSelectedContainerImages(opts, requirements); err != nil {
		return err
	}
	return writeSelectedImageReferences(stdout, opts, requirements)
}

// prepareRunPrerequisites bootstraps selected dependencies, logs in to registries,
// and checks images and the GPU runtime before any suite starts.
func prepareRunPrerequisites(
	ctx context.Context,
	stdout io.Writer,
	root string,
	runner e2eexec.Runner,
	opts *config.TestOptions,
	requirements []scenario.SelectedRequirement,
	loginDockerSession func() error,
) error {
	prepared, err := bootstrap.RunIfEnabled(ctx, stdout, root, runner, opts, requirements)
	if err != nil {
		return err
	}
	if err := loginDockerSession(); err != nil {
		return err
	}
	if !opts.BuildOnly && !opts.BootstrapOnly {
		if err := checkSelectedContainerImages(ctx, runner, *opts, requirements); err != nil {
			return err
		}
	}
	return bootstrap.CheckGPURuntime(ctx, stdout, runner, *opts, prepared)
}

// timeoutContext applies the optional execution timeout after CLI parsing.
// Cleanup and diagnostics use their separately bounded grace period so an
// interrupted run can still release resources safely.
func timeoutContext(parent context.Context, value string) (context.Context, context.CancelFunc, error) {
	duration := time.Duration(0)
	var err error
	if strings.TrimSpace(value) != "" {
		duration, err = time.ParseDuration(value)
	}
	if err != nil || duration < 0 {
		return nil, nil, fmt.Errorf("--timeout must be a non-negative Go duration, got %q", value)
	}
	if duration == 0 {
		return parent, func() {
			// This no-op lets callers defer cancellation unconditionally.
		}, nil
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	return ctx, cancel, nil
}

// runConditions records CLI configuration as plain run facts for scenario requirement collection.
func runConditions(opts config.TestOptions) scenario.RunConditions {
	return scenario.RunConditions{
		LocalKubernetes:         opts.Kubeconfig == "",
		RegistryLoginConfigured: len(opts.DockerRegistryLogins) != 0 || opts.DockerRegistryLoginFile != "",
		NVMLInjectionConfigured: opts.DCGMNVMLInjectionYAML != "",
		HostDCGMIsMocked:        opts.DCGMNVMLInjectionYAML != "" || opts.DCGMNVSDMMockYAML != "",
	}
}

// writeDirectGoCommands publishes a focused source-tree command for every catalog scenario.
func writeDirectGoCommands(w io.Writer) error {
	fmt.Fprintln(w, "\nDirect Go commands:")
	for _, entry := range scenario.Catalog {
		command, err := suite.DirectGoCommand(entry)
		if err != nil {
			return err
		}
		words := make([]string, 0, len(command.Env)+len(command.Args)+1)
		words = append(words, command.Env...)
		words = append(words, command.Name)
		words = append(words, command.Args...)
		for index := range words {
			words[index] = shellWord(words[index])
		}
		if _, err := fmt.Fprintf(w, "%-22s %s\n", entry.Selector(), strings.Join(words, " ")); err != nil {
			return err
		}
	}
	return nil
}

// applyBoolFlagValue applies an explicit flag before validating its environment default.
func applyBoolFlagValue(c *urfavecli.Context, flagName, envName string, destination *bool, parseEnv func(string, bool) (bool, error)) error {
	if c.IsSet(flagName) {
		*destination = c.Bool(flagName)
		return nil
	}
	value, err := parseEnv(envName, *destination)
	if err != nil {
		return err
	}
	*destination = value
	return nil
}

// shellWord quotes a command word only when shell metacharacters require it.
func shellWord(value string) string {
	if value != "" && strings.IndexFunc(value, shellWordNeedsQuoting) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellWordNeedsQuoting(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return false
	case r >= 'A' && r <= 'Z':
		return false
	case r >= '0' && r <= '9':
		return false
	default:
		return !strings.ContainsRune("_@%+=:,./-", r)
	}
}

// parseTests merges environment defaults with explicit tests command flags.
func parseTests(args []string, stderr io.Writer) (config.TestOptions, error) {
	opts, err := testsFromEnv()
	if err != nil {
		return opts, err
	}
	command := newTestsCLICommand(&opts, true)
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
func newTestsCLICommand(opts *config.TestOptions, includeBootstrap bool) *urfavecli.Command {
	suites := urfavecli.NewStringSlice(opts.Suites...)
	skipSuites := urfavecli.NewStringSlice(opts.SkipSuites...)
	scenarios := urfavecli.NewStringSlice(opts.Scenarios...)
	skipScenarios := urfavecli.NewStringSlice(opts.SkipScenarios...)
	registryLogins := (*stringList)(&opts.DockerRegistryLogins)
	bootstrapMode := string(opts.BootstrapMode)
	bootstrapComponents := urfavecli.NewStringSlice()
	modes := featureModeFlags{
		k3dCgroupV1:        string(opts.K3dCgroupV1),
		sharedGPUConfigure: string(opts.SharedGPUConfigure),
		draConfigure:       string(opts.DRAConfigure),
		gpuOperator:        string(opts.GPUOperator),
	}
	return &urfavecli.Command{
		Name:      "tests",
		Usage:     "Run dcgm-exporter e2e validation suites",
		UsageText: "e2e tests [options]",
		Description: `Select suites or scenarios, use --dry-run to see the plan, and use --verbose to show command output and check details; sensitive output is withheld.

Use --list-scenarios to list selectors and --dry-run to see the planned scenarios.
Result markers are stable machine-readable output and are enabled only when requested. Exit 10 means setup failure; exit 20 means test failure.`,
		Flags: []urfavecli.Flag{
			&urfavecli.BoolFlag{Name: "build-only", Value: opts.BuildOnly, Destination: &opts.BuildOnly, Usage: "build or locate selected suite binaries without running them; environment: E2E_BUILD_ONLY", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "prebuilt-binaries", Value: opts.PrebuiltBinaries, Destination: &opts.PrebuiltBinaries, Usage: "require prebuilt suite binaries instead of compiling from the source tree; environment: E2E_PREBUILT_BINARIES", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "tools-dir", EnvVars: []string{"E2E_TOOLS_DIR"}, Destination: &opts.ToolsDir, Usage: "directory for downloaded helper tools and source-tree suite binaries", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "exporter-binary", EnvVars: []string{"E2E_EXPORTER_BINARY"}, Destination: &opts.ExporterBinary, Usage: "prebuilt dcgm-exporter binary `path` used by host validation", Category: testsCategoryDependencies},
			&urfavecli.StringFlag{Name: "bootstrap", Value: bootstrapMode, Destination: &bootstrapMode, Usage: "mode for host dependencies: `check|install|skip`; environment: E2E_BOOTSTRAP", Category: testsCategoryDependencies},
			&urfavecli.StringSliceFlag{Name: testsFlagBootstrapComponent, Value: bootstrapComponents, Usage: "set one dependency mode as `name=check|install|skip`; repeat; environment: E2E_BOOTSTRAP_COMPONENTS", Category: testsCategoryDependencies},
			&urfavecli.BoolFlag{Name: "bootstrap-only", Value: opts.BootstrapOnly, Destination: &opts.BootstrapOnly, Usage: "stop after bootstrap checks and any authorized installation; environment: E2E_BOOTSTRAP_ONLY", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "dry-run", Destination: &opts.DryRun, Usage: "list selected scenarios and their required artifacts without probing, building, or running tests; environment: E2E_DRY_RUN", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Destination: &opts.Verbose, Usage: "print command output and host check details, with sensitive output withheld; environment: E2E_VERBOSE", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "list-scenarios", Value: opts.ListScenarios, Destination: &opts.ListScenarios, Usage: "list available suite/scenario selectors and exit; environment: E2E_LIST_SCENARIOS", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: testsFlagResultMarkers, Destination: &opts.ResultMarkers, Value: opts.ResultMarkers, Usage: "emit reserved &&&& result marker lines; environment: E2E_RESULT_MARKERS", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "no-result-markers", Destination: &opts.NoResultMarkers, Usage: "disable result markers; equivalent to E2E_RESULT_MARKERS=false", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "suite", Value: suites, Usage: "run only `suite`; repeat or set E2E_SUITE for multiple suites", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "skip-suite", Value: skipSuites, Usage: "skip `suite`; repeat or set E2E_SKIP_SUITE for multiple suites", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "scenario", Value: scenarios, Usage: "run only `suite/name`; repeat or set E2E_SCENARIO for multiple scenarios", Category: testsCategoryExecution},
			&urfavecli.StringSliceFlag{Name: "skip-scenario", Value: skipScenarios, Usage: "skip `suite/name`; repeat or set E2E_SKIP_SCENARIO for multiple scenarios", Category: testsCategoryExecution},
			&urfavecli.BoolFlag{Name: "cleanup", Value: opts.Cleanup, Destination: &opts.Cleanup, Usage: "clean retainable E2E-managed resources after the run; environment: E2E_CLEANUP", Category: testsCategoryExecution},
			&urfavecli.StringFlag{Name: "kubeconfig", EnvVars: []string{"E2E_KUBECONFIG"}, Destination: &opts.Kubeconfig, Usage: "use an existing Kubernetes cluster through `path`", Category: testsCategoryKubernetes},
			&urfavecli.BoolFlag{Name: "k3d-keep-cluster", Destination: &opts.KeepCluster, Usage: "keep e2e-managed k3d resources after the run; environment: E2E_K3D_KEEP_CLUSTER", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "run-name", EnvVars: []string{"E2E_RUN_NAME"}, Value: opts.RunName, Destination: &opts.RunName, Usage: "name used to derive and own resources for this test run", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k3d-cluster-name", EnvVars: []string{"E2E_K3D_CLUSTER_NAME"}, Value: opts.ClusterName, Destination: &opts.ClusterName, Usage: "owned k3d cluster `name`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: testsFlagHelmNamespace, EnvVars: []string{"E2E_HELM_NAMESPACE"}, Value: opts.Namespace, Destination: &opts.Namespace, Usage: "dcgm-exporter Kubernetes `namespace`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "helm-release-name", Aliases: []string{"helm-release"}, EnvVars: []string{"E2E_HELM_RELEASE_NAME"}, Value: opts.ReleaseName, Destination: &opts.ReleaseName, Usage: "dcgm-exporter Helm release `name`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: testsFlagDCGMNamespace, EnvVars: []string{"E2E_DCGM_NAMESPACE"}, Value: opts.DCGMNamespace, Destination: &opts.DCGMNamespace, Usage: "standalone DCGM Kubernetes `namespace`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "wait-timeout", EnvVars: []string{"E2E_WAIT_TIMEOUT"}, Value: opts.WaitTimeout, Destination: &opts.WaitTimeout, Usage: "timeout for Kubernetes readiness waits as a Go `duration`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "timeout", EnvVars: []string{"E2E_TIMEOUT"}, Value: opts.Timeout, Destination: &opts.Timeout, Usage: "bound test execution to a Go `duration` (zero means unlimited); cleanup and diagnostics have a two-minute grace period", Category: testsCategoryExecution},
			&urfavecli.GenericFlag{Name: "docker-registry-login", EnvVars: []string{"E2E_DOCKER_REGISTRY_LOGIN"}, Value: registryLogins, Usage: "Docker login as `REGISTRY,USERNAME,PASSWORD_FILE`; repeat for multiple registries", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "docker-registry-login-file", EnvVars: []string{"E2E_DOCKER_REGISTRY_LOGIN_FILE"}, Destination: &opts.DockerRegistryLoginFile, Usage: "read newline-separated Docker login specs from `path`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "docker-config-dir", EnvVars: []string{"E2E_DOCKER_CONFIG_DIR"}, Destination: &opts.DockerConfigDir, Usage: "Docker configuration `directory` used for authenticated pulls", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "k8s-image-pull-secret", EnvVars: []string{"E2E_K8S_IMAGE_PULL_SECRET"}, Destination: &opts.K8sImagePullSecret, Usage: "existing Kubernetes image pull secret `name`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "exporter-image", EnvVars: []string{"E2E_EXPORTER_IMAGE"}, Value: opts.ExporterImage, Destination: &opts.ExporterImage, Usage: "complete dcgm-exporter image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "dcgm-image", EnvVars: []string{"E2E_DCGM_IMAGE"}, Value: opts.DCGMImage, Destination: &opts.DCGMImage, Usage: "complete standalone DCGM image `reference`", Category: testsCategoryImages},
			&urfavecli.StringFlag{Name: "dcgm-version", EnvVars: []string{"E2E_DCGM_VERSION", "DCGM_VERSION"}, Value: opts.Dependencies.DCGMVersion, Destination: &opts.Dependencies.DCGMVersion, Usage: "DCGM package `version` required from nv-hostengine", Category: testsCategoryImages},
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
			&urfavecli.StringFlag{Name: "container-toolkit-version", EnvVars: []string{"NVIDIA_CONTAINER_TOOLKIT_VERSION"}, Value: opts.Dependencies.ContainerToolkitVersion, Destination: &opts.Dependencies.ContainerToolkitVersion, Hidden: true},
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
			&urfavecli.StringFlag{Name: "k3d-ip-family", EnvVars: []string{"E2E_K3D_IP_FAMILY"}, Value: opts.K3dIPFamily, Destination: &opts.K3dIPFamily, Usage: "owned k3d network mode: `auto|ipv4|ipv6|dualstack`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "k3d-cgroup-v1", EnvVars: []string{"E2E_K3D_CGROUP_V1"}, Value: modes.k3dCgroupV1, Destination: &modes.k3dCgroupV1, Usage: "owned k3d cgroup v1 mode: `auto|true|false`", Category: testsCategoryKubernetes},
			&urfavecli.StringFlag{Name: "shared-gpu-configure", EnvVars: []string{"E2E_SHARED_GPU_CONFIGURE"}, Value: modes.sharedGPUConfigure, Destination: &modes.sharedGPUConfigure, Usage: "time-sliced GPU setup mode: `auto|true|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "shared-gpu-replicas", EnvVars: []string{"E2E_SHARED_GPU_REPLICAS"}, Destination: &opts.SharedGPUReplicas, Usage: "time-sliced GPU replica `count`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dra-configure", EnvVars: []string{"E2E_DRA_CONFIGURE"}, Value: modes.draConfigure, Destination: &modes.draConfigure, Usage: "NVIDIA DRA driver setup mode: `auto|true|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "mig-configure", EnvVars: []string{"E2E_MIG_CONFIGURE"}, Value: opts.MIGConfigure, Destination: &opts.MIGConfigure, Usage: "MIG setup mode: `auto|true|false|profile ID`; auto and false use MIG as it is, true or a profile ID changes MIG for the run and restores it afterward", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dcgm-nvml-injection-yaml", EnvVars: []string{"E2E_DCGM_NVML_INJECTION_YAML"}, Destination: &opts.DCGMNVMLInjectionYAML, Usage: "DCGM NVML injection YAML `path`; required by host/nvmlInjectionMetrics, host/migXidCounters and host/configServesMetrics, and used by the other host-config-injection scenarios when given", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "dcgm-nvsdm-mock-yaml", EnvVars: []string{"E2E_DCGM_NVSDM_MOCK_YAML"}, Destination: &opts.DCGMNVSDMMockYAML, Usage: "configure explicit host/nvsdmMockMetrics with a DCGM NVSDM mock YAML `path`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "gpu-operator", EnvVars: []string{"E2E_GPU_OPERATOR"}, Value: modes.gpuOperator, Destination: &modes.gpuOperator, Usage: "GPU Operator mode: `auto|existing|install|false`", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "gpu-operator-chart", EnvVars: []string{"E2E_GPU_OPERATOR_CHART"}, Destination: &opts.GPUOperatorChart, Usage: "local GPU Operator main chart `path` for latest-main scenarios", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "gpu-operator-image", EnvVars: []string{"E2E_GPU_OPERATOR_IMAGE"}, Destination: &opts.GPUOperatorImage, Usage: "optional controller image `reference` for the local GPU Operator chart", Category: testsCategoryFeatures},
			&urfavecli.StringFlag{Name: "gpu-operator-dcgm-image", EnvVars: []string{"E2E_GPU_OPERATOR_DCGM_IMAGE"}, Destination: &opts.GPUOperatorDCGMImage, Usage: "optional DCGM image `reference` for remote-hostengine GPU Operator scenarios", Category: testsCategoryFeatures},
		},
		Action: func(c *urfavecli.Context) error {
			return applyTestsFlags(c, opts, bootstrapMode, modes, includeBootstrap)
		},
	}
}

// applyTestsFlags resolves parsed flags and E2E_* environment values into opts:
// an explicit flag wins, then the environment, then the default. It validates the result.
func applyTestsFlags(c *urfavecli.Context, opts *config.TestOptions, bootstrapMode string, modes featureModeFlags, includeBootstrap bool) error {
	if c.NArg() != 0 {
		return fmt.Errorf("unexpected e2e tests arguments: %v", c.Args().Slice())
	}
	if err := applyBoolFlags(c, opts); err != nil {
		return err
	}
	opts.Suites = c.StringSlice("suite")
	opts.SkipSuites = c.StringSlice("skip-suite")
	opts.Scenarios = c.StringSlice("scenario")
	opts.SkipScenarios = c.StringSlice("skip-scenario")
	if includeBootstrap {
		if err := applyBootstrapFlags(c, opts, bootstrapMode); err != nil {
			return err
		}
	}
	if err := applyResultMarkerFlags(c, opts); err != nil {
		return err
	}
	if err := applyFeatureModeFlags(opts, modes); err != nil {
		return err
	}
	if err := applyRunNameDefaults(c, opts.RunName, runResourceNames{
		cluster:       &opts.ClusterName,
		namespace:     &opts.Namespace,
		release:       &opts.ReleaseName,
		dcgmNamespace: &opts.DCGMNamespace,
	}); err != nil {
		return err
	}
	return validateTestsConfig(*opts)
}

// applyBoolFlags resolves boolean flags whose environment values use urfave's boolean rule.
func applyBoolFlags(c *urfavecli.Context, opts *config.TestOptions) error {
	for _, option := range []struct {
		flagName string
		envName  string
		value    *bool
	}{
		{flagName: "build-only", envName: "E2E_BUILD_ONLY", value: &opts.BuildOnly},
		{flagName: "prebuilt-binaries", envName: "E2E_PREBUILT_BINARIES", value: &opts.PrebuiltBinaries},
		{flagName: "dry-run", envName: "E2E_DRY_RUN", value: &opts.DryRun},
		{flagName: "verbose", envName: "E2E_VERBOSE", value: &opts.Verbose},
		{flagName: "list-scenarios", envName: "E2E_LIST_SCENARIOS", value: &opts.ListScenarios},
		{flagName: "k3d-keep-cluster", envName: "E2E_K3D_KEEP_CLUSTER", value: &opts.KeepCluster},
		{flagName: "cleanup", envName: "E2E_CLEANUP", value: &opts.Cleanup},
	} {
		if err := applyBoolFlagValue(c, option.flagName, option.envName, option.value, envBoolFlagDefault); err != nil {
			return err
		}
	}
	return nil
}

// applyBootstrapFlags resolves the bootstrap-only flag, bootstrap mode, and per-component modes.
func applyBootstrapFlags(c *urfavecli.Context, opts *config.TestOptions, bootstrapMode string) error {
	if err := applyBoolFlagValue(c, "bootstrap-only", "E2E_BOOTSTRAP_ONLY", &opts.BootstrapOnly, envBoolDefault); err != nil {
		return err
	}
	if err := normalizeBootstrapFlag(opts, bootstrapMode, c.IsSet("bootstrap")); err != nil {
		return err
	}
	return normalizeBootstrapComponents(opts, c.StringSlice(testsFlagBootstrapComponent), c.IsSet(testsFlagBootstrapComponent))
}

// applyResultMarkerFlags resolves CLI and suite result markers from --result-markers,
// then E2E_RESULT_MARKERS and E2E_SUITE_RESULT_MARKERS. --no-result-markers always
// disables both, as it did before these flags honored the environment.
func applyResultMarkerFlags(c *urfavecli.Context, opts *config.TestOptions) error {
	switch {
	case c.IsSet(testsFlagResultMarkers):
		opts.ResultMarkers = c.Bool(testsFlagResultMarkers)
		opts.E2EResultMarkers = c.Bool(testsFlagResultMarkers)
	case c.Bool("no-result-markers"):
		opts.ResultMarkers = false
		opts.E2EResultMarkers = false
	default:
		resultMarkers, err := envBoolDefault("E2E_RESULT_MARKERS", opts.ResultMarkers)
		if err != nil {
			return err
		}
		opts.ResultMarkers = resultMarkers
		suiteResultMarkers, err := envBoolDefault("E2E_SUITE_RESULT_MARKERS", resultMarkers)
		if err != nil {
			return err
		}
		opts.E2EResultMarkers = suiteResultMarkers
	}
	if opts.NoResultMarkers {
		opts.ResultMarkers = false
		opts.E2EResultMarkers = false
	}
	return nil
}

// stringList lets repeated registry login flags append into config.TestOptions.
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
