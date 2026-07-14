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
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/marker"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/mig"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

var containerRemoteDCGMURIScenario = scenario.MustFind(scenario.Catalog, scenario.SuiteContainer, "remoteDcgmUri")

// runK8sSuite handles the optional two-pass default run that keeps MIG mutation isolated.
func runK8sSuite(ctx context.Context, stdout io.Writer, root string, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	if splitDefaultMIGRun(cfg.Tests) {
		fmt.Fprintln(stdout, "[e2e] k8s maximum-coverage phase: full-GPU baseline")
		nonMIG := cfg
		nonMIG.Tests.MIGConfigure = "false"
		nonMIG.Tests.SkipScenarios = append(append([]string{}, nonMIG.Tests.SkipScenarios...), migScenarioSelectors()...)
		if err := runK8sSuiteOnce(ctx, stdout, root, manager, runner, nonMIG); err != nil {
			return err
		}

		fmt.Fprintln(stdout, "[e2e] k8s maximum-coverage phase: MIG")
		mig := cfg
		mig.Tests.SkipScenarios = append(append([]string{}, mig.Tests.SkipScenarios...), nonMIGScenarioSelectors()...)
		return runK8sSuiteOnce(ctx, stdout, root, manager, runner, mig)
	}
	return runK8sSuiteOnce(ctx, stdout, root, manager, runner, cfg)
}

// splitDefaultMIGRun reports whether the default k8s selection should run non-MIG and MIG scenarios separately.
func splitDefaultMIGRun(opts config.Tests) bool {
	return len(opts.Suites) == 0 && len(opts.Scenarios) == 0 && mig.MutationRequested(opts.MIGConfigure)
}

// migScenarioSelectors returns every k8s catalog selector that requires MIG.
func migScenarioSelectors() []string {
	return scenario.SelectorsMatching(scenario.Catalog, func(entry scenario.Scenario) bool {
		return entry.Suite == scenario.SuiteK8s &&
			entry.HasAnyCapability("host:mig", "host:mixed_mig", "host:mig_instance_entity")
	})
}

// nonMIGScenarioSelectors returns every k8s catalog selector that does not require MIG.
func nonMIGScenarioSelectors() []string {
	migScenarios := map[string]struct{}{}
	for _, selector := range migScenarioSelectors() {
		migScenarios[selector] = struct{}{}
	}
	var selectors []string
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteK8s {
			continue
		}
		if _, ok := migScenarios[entry.Selector()]; ok {
			continue
		}
		selectors = append(selectors, entry.Selector())
	}
	return selectors
}

// runK8sSuiteOnce executes one k8s plan from setup marker through selected scenario groups.
func runK8sSuiteOnce(ctx context.Context, stdout io.Writer, root string, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	if len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, cfg)) == 0 {
		return nil
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteK8s, false)
	if err != nil {
		return setupFailure(err)
	}
	writeSection(stdout, "Kubernetes setup")
	fmt.Fprintf(stdout, "[e2e] binary: %s\n", binary)
	fmt.Fprintln(stdout)

	run := newK8sSuiteRun(ctx, stdout, root, runner, cfg, binary)
	defer run.cleanup(ctx)

	if err := run.emitSetupRunning(); err != nil {
		return err
	}
	if run.resultMarkers {
		fmt.Fprintln(stdout)
	}
	writeStep(stdout, "setup", "preparing cluster")
	if err := run.prepareCluster(); err != nil {
		return run.failSetup(err)
	}
	writeStep(stdout, "setup", "probing capabilities")
	probeInputs, err := run.prepareProbeInputs()
	if err != nil {
		return run.failSetup(err)
	}
	plan, err := run.buildPlan(probeInputs)
	if err != nil {
		return err
	}
	return run.executePlan(plan)
}

// k8sProbeInputs carries host and cluster probe evidence into scenario planning.
type k8sProbeInputs struct {
	dcgmImage           string
	expectedDCGMVersion string
}

// k8sSuiteRun keeps the mutable state for one k8s suite attempt together.
type k8sSuiteRun struct {
	ctx      context.Context
	stdout   io.Writer
	root     string
	runner   e2eexec.Runner
	cfg      config.Config
	binary   string
	reporter marker.Reporter

	clusterCfg cluster.Config
	featureCfg cluster.FeatureConfig

	resultMarkers      bool
	suiteResultMarkers bool
	cleanupFuncs       []func(context.Context)
}

// newK8sSuiteRun derives all local cluster configuration shared by one k8s suite attempt.
func newK8sSuiteRun(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, cfg config.Config, binary string) *k8sSuiteRun {
	resultMarkers := cfg.Tests.ResultMarkers && !cfg.Tests.NoResultMarkers
	suiteResultMarkers := specMarkersEnabled(cfg.Tests)
	clusterCfg := clusterConfig(root, cfg.Tests)
	featureCfg := clusterFeatureConfig(root, cfg.Tests)
	return &k8sSuiteRun{
		ctx:                ctx,
		stdout:             stdout,
		root:               root,
		runner:             runner,
		cfg:                cfg,
		binary:             binary,
		reporter:           marker.NewReporter(stdout),
		clusterCfg:         clusterCfg,
		featureCfg:         featureCfg,
		resultMarkers:      resultMarkers,
		suiteResultMarkers: suiteResultMarkers,
	}
}

// emitSetupRunning announces k8s setup before cluster mutation starts.
func (r *k8sSuiteRun) emitSetupRunning() error {
	if !r.resultMarkers {
		return nil
	}
	return r.reporter.Emit(marker.StatusRunning, "dcgm_exporter_e2e_setup")
}

// failSetup emits the setup failure marker and wraps the error with the setup exit code.
func (r *k8sSuiteRun) failSetup(err error) error {
	if r.resultMarkers {
		_ = r.reporter.Emit(marker.StatusFailed, "dcgm_exporter_e2e_setup")
	}
	return setupFailure(err)
}

// cleanup removes owned cluster resources after a k8s suite attempt.
func (r *k8sSuiteRun) cleanup(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	for i := len(r.cleanupFuncs) - 1; i >= 0; i-- {
		r.cleanupFuncs[i](cleanupCtx)
	}
	if r.clusterCfg.LocalK3D() && !r.cfg.Tests.KeepCluster {
		_ = cluster.Cleanup(cleanupCtx, r.runner, r.stdout, r.clusterCfg)
	}
}

// prepareCluster installs prerequisites, prepares MIG, and ensures the target cluster is usable.
func (r *k8sSuiteRun) prepareCluster() error {
	if !r.clusterCfg.LocalK3D() {
		writeStep(r.stdout, "setup", "using external Kubernetes cluster")
		r.cleanupFuncs = append(r.cleanupFuncs, func(cleanupCtx context.Context) {
			if err := cluster.Cleanup(cleanupCtx, r.runner, r.stdout, r.clusterCfg); err != nil {
				fmt.Fprintf(r.stdout, "[e2e] WARN external cluster cleanup failed: %v\n", err)
			}
		})
		return nil
	}

	migCleanup, err := mig.PrepareHostBeforeCluster(r.ctx, r.stdout, r.runner, r.cfg.Tests)
	if err != nil {
		return err
	}
	r.cleanupFuncs = append(r.cleanupFuncs, migCleanup)

	localCfg, err := localClusterConfig(r.root, r.clusterCfg, r.cfg.Tests)
	if err != nil {
		return err
	}
	writeStep(r.stdout, "setup", "starting local k3d cluster "+r.clusterCfg.ClusterName)
	if _, err := cluster.EnsureLocal(r.ctx, r.runner, r.stdout, localCfg); err != nil {
		return err
	}
	writeStep(r.stdout, "setup", "preparing registry auth")
	cleanupDockerConfig, _, err := prepareDockerRegistryLogin(r.ctx, r.stdout, r.runner, &r.cfg.Tests)
	if err != nil {
		return err
	}
	r.cleanupFuncs = append(r.cleanupFuncs, func(context.Context) { cleanupDockerConfig() })
	writeStep(r.stdout, "setup", "importing exporter image")
	if err := ensureDockerImageAvailable(r.ctx, r.stdout, r.runner, r.cfg.Tests, exporterImage(r.cfg.Tests)); err != nil {
		return err
	}
	if err := tagLocalK3dImage(r.ctx, r.runner, r.stdout, r.cfg.Tests); err != nil {
		return err
	}
	return cluster.ImportImageIfPresent(r.ctx, r.runner, r.stdout, r.clusterCfg, localK3dExporterImage(r.cfg.Tests))
}

// prepareProbeInputs gathers host and cluster capabilities needed to decide scenario outcomes.
func (r *k8sSuiteRun) prepareProbeInputs() (k8sProbeInputs, error) {
	if err := prepareRegistryAuth(r.ctx, r.stdout, r.runner, r.clusterCfg, &r.cfg.Tests); err != nil {
		return k8sProbeInputs{}, err
	}

	inputs := k8sProbeInputs{}
	if dcgmImageNeeded(r.cfg.Tests) {
		writeStep(r.stdout, "setup", "checking DCGM image")
		if err := validateRequiredImage("--dcgm-image", r.cfg.Tests.DCGMImage); err != nil {
			return k8sProbeInputs{}, err
		}
		if err := ensureDCGMImageAvailable(r.ctx, r.runner, r.cfg.Tests); err != nil {
			return k8sProbeInputs{}, err
		}
		version := strings.TrimSpace(r.cfg.Tests.Dependencies.DCGMVersion)
		if version == "" {
			return k8sProbeInputs{}, fmt.Errorf("selected DCGM-probed scenarios require --dcgm-version, E2E_DCGM_VERSION, or DCGM_VERSION")
		}
		inputs.dcgmImage = dcgmImage(r.cfg.Tests)
		inputs.expectedDCGMVersion = version
	}

	if draProbeNeeded(r.cfg.Tests) {
		writeStep(r.stdout, "setup", "probing DRA resources")
		cleanup, err := cluster.PrepareDRAForProbe(r.ctx, r.runner, r.stdout, r.clusterCfg, r.featureCfg)
		if err != nil {
			return k8sProbeInputs{}, err
		}
		r.cleanupFuncs = append(r.cleanupFuncs, cleanup)
	}
	return inputs, nil
}

// buildPlan converts probe evidence and selected scenarios into runnable and skipped groups.
func (r *k8sSuiteRun) buildPlan(inputs k8sProbeInputs) (scenario.Plan, error) {
	writeStep(r.stdout, "setup", "probing host capabilities")
	hostProbe := capability.ProbeHost(r.ctx, r.runner, capability.ProbeOptions{
		FailureInjection:    featureModeEnabled(r.cfg.Tests.DCGMFailureInjection),
		MIGConfigure:        r.cfg.Tests.MIGConfigure,
		DCGMImage:           inputs.dcgmImage,
		DockerEnv:           dockerConfigEnv(r.cfg.Tests),
		ExpectedDCGMVersion: inputs.expectedDCGMVersion,
	})
	applyHostProbeOutputs(&r.cfg.Tests, hostProbe)
	caps := hostProbe.Snapshot
	writeStep(r.stdout, "setup", "probing cluster capabilities")
	clusterCaps, err := cluster.ProbeCapabilities(r.ctx, r.runner, r.clusterCfg, r.featureCfg)
	if err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	caps = caps.With(clusterCaps...)
	if r.clusterCfg.LocalK3D() && ipFamilyRequestsIPv6(r.cfg.Tests.K3dIPFamily) {
		caps = caps.With(capability.Capability{
			Name:     "cluster:ipv6",
			Status:   capability.StatusSupported,
			Source:   "local k3d configuration",
			Reason:   "local k3d was requested with dual-stack service networking",
			Evidence: r.cfg.Tests.K3dIPFamily,
		})
	}
	if standaloneDCGMImageNeeded(r.cfg.Tests) {
		caps = caps.With(managedFailureInjectionCapabilities(r.ctx, r.runner, r.cfg.Tests, caps)...)
	}
	writeSection(r.stdout, "Capabilities")
	writeCapabilitySummary(r.stdout, "Host", "host:", capability.HostNames(), caps, r.cfg.Tests.Verbose)
	writeCapabilitySummary(r.stdout, "Cluster", "cluster:", capability.ClusterNames(), caps, r.cfg.Tests.Verbose)
	writeCapabilitySummary(r.stdout, "Remote DCGM", "dcgm:", capability.DCGMNames(), caps, r.cfg.Tests.Verbose)
	if err := requireClusterGPUResources(caps, r.cfg.Tests); err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	plan, err := scenario.Select(scenario.Catalog, caps, r.cfg)
	if err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	writeSection(r.stdout, "Scenario plan")
	if err := plan.WriteSummary(r.stdout); err != nil {
		return scenario.Plan{}, err
	}
	writeVerbosePlanLabels(r.stdout, r.cfg.Tests.Verbose, plan)
	if len(plan.RemoteLabels) != 0 && standaloneDCGMImageNeeded(r.cfg.Tests) {
		if err := ensureLocalDCGMImageImport(r.ctx, r.stdout, r.runner, r.clusterCfg, r.cfg.Tests); err != nil {
			return scenario.Plan{}, r.failSetup(err)
		}
	}
	if r.resultMarkers {
		fmt.Fprintln(r.stdout)
		if err := r.reporter.Emit(marker.StatusPassed, "dcgm_exporter_e2e_setup"); err != nil {
			return scenario.Plan{}, err
		}
		if err := emitSkippedScenarioMarkers(r.reporter, plan); err != nil {
			return scenario.Plan{}, err
		}
		fmt.Fprintln(r.stdout)
	}
	return plan, nil
}

// executePlan emits skip markers and runs each selected k8s scenario group.
func (r *k8sSuiteRun) executePlan(plan scenario.Plan) error {
	for _, group := range plan.Groups {
		if len(group.Labels) == 0 {
			continue
		}
		if err := runK8sGroup(r.ctx, r.stdout, r.runner, r.binary, r.root, r.clusterCfg, r.cfg.Tests, group, r.resultMarkers, r.suiteResultMarkers); err != nil {
			return scenarioFailure(err)
		}
	}
	return nil
}

// ensureLocalDCGMImageImport imports the standalone DCGM image into owned k3d clusters when required.
func ensureLocalDCGMImageImport(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, clusterCfg cluster.Config, opts config.Tests) error {
	if !clusterCfg.LocalK3D() {
		return nil
	}
	imported, err := cluster.ImportImageIfPresentWithResult(ctx, runner, stdout, clusterCfg, dcgmImage(opts), "k3d_import_dcgm_image")
	if err == nil {
		return nil
	}
	reason := "local k3d could not import the configured DCGM image"
	if !imported {
		reason = "local k3d could not inspect the configured DCGM image"
	}
	reason += "; standalone DCGM and failure injection scenarios require cluster image access"
	if opts.K8sImagePullSecret != "" {
		fmt.Fprintf(stdout, "[e2e] WARN %s; relying on Kubernetes image pull secret %s: %v\n", reason, opts.K8sImagePullSecret, err)
		return nil
	}
	return fmt.Errorf("%s; pass --docker-registry-login-file or pre-load the image: %w", reason, err)
}

// requireClusterGPUResources fails explicit GPU-backed k8s runs when the cluster exposes no GPU resources.
func requireClusterGPUResources(caps capability.Snapshot, opts config.Tests) error {
	if opts.DryRun || len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, config.Config{Tests: opts})) == 0 {
		return nil
	}
	cap := caps.Lookup("cluster:gpu_resources")
	if cap.Status == capability.StatusSupported {
		return nil
	}
	return fmt.Errorf("selected Kubernetes validation requires allocatable nvidia.com/gpu resources: %s", cap.Reason)
}

// managedFailureInjectionCapabilities probes capabilities that depend on a deployable standalone DCGM image.
func managedFailureInjectionCapabilities(ctx context.Context, runner e2eexec.Runner, opts config.Tests, caps capability.Snapshot) []capability.Capability {
	image := dcgmImage(opts)
	if !featureModeEnabled(opts.DCGMFailureInjection) || image == "" {
		return nil
	}
	if caps.Lookup("dcgm:failure_injection").Status == capability.StatusSupported {
		return nil
	}
	if caps.Lookup("dcgm:failure_injection").Status == capability.StatusUnsupported {
		return nil
	}
	if caps.Lookup("cluster:standalone_dcgm_resources").Status != capability.StatusSupported {
		return nil
	}
	if result := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"manifest", "inspect", image}}); result.ExitCode != 0 {
		return nil
	}
	reason := "validation can deploy standalone DCGM image " + image
	return []capability.Capability{
		{Name: "dcgm:remote_dcgm", Status: capability.StatusSupported, Source: "managed DCGM manifest", Reason: reason, Evidence: image},
		{Name: "dcgm:failure_injection", Status: capability.StatusSupported, Source: "managed DCGM manifest", Reason: reason, Evidence: image},
	}
}

// runK8sGroup prepares one scenario group, executes the k8s suite binary, and captures diagnostics on failure.
func runK8sGroup(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, binary, root string, clusterCfg cluster.Config, opts config.Tests, group scenario.PlanGroup, resultMarkers, suiteResultMarkers bool) error {
	writeSection(stdout, "Running k8s group: "+group.Name)
	fmt.Fprintf(stdout, "[e2e] scenarios: %d\n", len(group.Labels))
	writeStep(stdout, "running", strings.Join(group.Labels, ", "))
	writeVerbose(stdout, opts.Verbose, "%s labels: %s", group.Name, strings.Join(group.Labels, " || "))
	groupName := scenarioGroupResultName(group.Name)
	reporter := marker.NewReporter(stdout)
	groupMarkers := resultMarkers && !suiteResultMarkers
	failureMarkers := resultMarkers
	groupRunningEmitted := false
	emitGroupRunning := func() error {
		if groupRunningEmitted {
			return nil
		}
		groupRunningEmitted = true
		return reporter.Emit(marker.StatusRunning, groupName)
	}
	if groupMarkers {
		if err := emitGroupRunning(); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
	}
	cleanup, err := prepareK8sGroup(ctx, stdout, runner, root, clusterCfg, opts, group)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
		defer cancel()
		_ = cluster.Diagnostics(cleanupCtx, runner, stdout, clusterCfg)
		if failureMarkers {
			_ = emitGroupRunning()
			_ = reporter.Emit(marker.StatusFailed, groupName)
		}
		return err
	}

	exporterImage := k8sExporterImage(clusterCfg, opts)
	args := []string{
		"-test.v",
		"--ginkgo.v",
		"--ginkgo.no-color",
		"--ginkgo.label-filter=" + strings.Join(group.Labels, " || "),
		"-kubeconfig=" + clusterCfg.EffectiveKubeconfig(),
		"-namespace=" + clusterCfg.Namespace,
		"-chart=" + filepath.Join(root, "deployment"),
		"-exporter-image=" + exporterImage,
		"-gpu-operator-exporter-image=" + exporterImage,
		"-k8s-image-pull-secret=" + opts.K8sImagePullSecret,
		"-runtime-class=" + runtimeClassFor(opts, clusterCfg),
		"-node-selector-key=" + nodeSelectorKeyFor(opts, clusterCfg),
		"-node-selector-value=" + nodeSelectorValueFor(opts, clusterCfg),
		"-remote-dcgm=" + dcgmAddressFor(opts, clusterCfg),
		"-dcgm-image=" + dcgmImage(opts),
		"-busybox-image=" + opts.BusyboxImage,
		"-cuda-workload-image=" + opts.CUDAWorkloadImage,
		"-dcgm-namespace=" + clusterCfg.DCGMNamespace,
		"-dcgm-name=" + dcgmNameFor(opts),
		"-dcgm-port=" + dcgmPortFor(opts),
		"-mig-instance-entity-id=" + opts.MIGInstanceEntityID,
		"-mig-instance-nvml-id=" + opts.MIGInstanceNVMLID,
		"-unsupported-field-candidate=" + opts.UnsupportedFieldCandidate,
		fmt.Sprintf("-result-markers=%t", suiteResultMarkers),
		"-arguments={-f=/etc/dcgm-exporter/default-counters.csv,-c=1000}",
	}
	result := runner.Run(ctx, e2eexec.Command{Name: binary, Args: args, Dir: root, Stdout: stdout, Stderr: stdout})
	fmt.Fprintln(stdout)
	if result.ExitCode != 0 {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
		defer cancel()
		_ = cluster.Diagnostics(cleanupCtx, runner, stdout, clusterCfg)
		cleanup(cleanupCtx)
		if failureMarkers {
			_ = emitGroupRunning()
			_ = reporter.Emit(marker.StatusFailed, groupName)
		}
		return fmt.Errorf("k8s group %s failed: %s", group.Name, result.Stderr)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	writeStep(stdout, "cleanup", "finished k8s group "+group.Name)
	cleanup(cleanupCtx)
	if groupMarkers {
		if err := reporter.Emit(marker.StatusPassed, groupName); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
	}
	return nil
}

// prepareK8sGroup applies only the cluster mutations required by the selected scenario group.
func prepareK8sGroup(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, root string, clusterCfg cluster.Config, opts config.Tests, group scenario.PlanGroup) (func(context.Context), error) {
	featureCfg := clusterFeatureConfig(root, opts)
	switch group.Name {
	case "embedded-mig":
		if !clusterCfg.LocalK3D() {
			return func(context.Context) {}, nil
		}
		local, err := localClusterConfig(root, clusterCfg, opts)
		if err != nil {
			return func(context.Context) {}, err
		}
		if err := cluster.ConfigureMIGDevicePlugin(ctx, runner, stdout, clusterCfg, local); err != nil {
			return func(context.Context) {}, err
		}
		return func(cleanupCtx context.Context) {
			_ = cluster.ResetDevicePlugin(cleanupCtx, runner, stdout, clusterCfg, local)
		}, nil
	case "embedded-dra":
		return cluster.PrepareDRA(ctx, runner, stdout, clusterCfg, featureCfg)
	case "embedded-shared-gpu":
		if !clusterCfg.LocalK3D() && !featureModeRequired(opts.SharedGPUConfigure) {
			return func(context.Context) {}, nil
		}
		if !clusterCfg.LocalK3D() {
			return func(context.Context) {}, fmt.Errorf("shared GPU configuration for external clusters is not implemented; configure the cluster before running sharedGpu")
		}
		local, err := localClusterConfig(root, clusterCfg, opts)
		if err != nil {
			return func(context.Context) {}, err
		}
		if err := cluster.ConfigureSharedGPUDevicePlugin(ctx, runner, stdout, clusterCfg, local, opts.SharedGPUReplicas); err != nil {
			return func(context.Context) {}, err
		}
		return func(cleanupCtx context.Context) {
			_ = cluster.ResetDevicePlugin(cleanupCtx, runner, stdout, clusterCfg, local)
		}, nil
	case "standalone-baseline", "standalone-failure-injection":
		if err := prepareRegistryAuthForNamespaces(ctx, stdout, runner, clusterCfg, &opts, []string{clusterCfg.DCGMNamespace}); err != nil {
			return func(context.Context) {}, err
		}
		dcgm := cluster.DefaultDCGMConfig()
		dcgm.Image = dcgmImage(opts)
		dcgm.RuntimeClass = runtimeClassFor(opts, clusterCfg)
		dcgm.NodeSelectorKey = nodeSelectorKeyFor(opts, clusterCfg)
		dcgm.NodeSelectorValue = nodeSelectorValueFor(opts, clusterCfg)
		dcgm.ImagePullSecret = opts.K8sImagePullSecret
		dcgm.Name = dcgmNameFor(opts)
		dcgm.Port = dcgmPortFor(opts)
		dcgm.WaitTimeout = opts.WaitTimeout
		if err := cluster.DeployDCGM(ctx, runner, stdout, clusterCfg, dcgm); err != nil {
			return func(context.Context) {}, err
		}
		return func(cleanupCtx context.Context) { cluster.DeleteDCGM(cleanupCtx, runner, stdout, clusterCfg) }, nil
	case "gpu-operator-baseline":
		name := "gpuOperatorChart"
		if groupHasLabel(group, "gpuOperatorIPv6") {
			name = "gpuOperatorIPv6"
		}
		return cluster.PrepareGPUOperator(ctx, runner, stdout, clusterCfg, featureCfg, name)
	case "gpu-operator-shared-gpu":
		return cluster.PrepareGPUOperator(ctx, runner, stdout, clusterCfg, featureCfg, "gpuOperatorSharedGpu")
	case "gpu-operator-mig":
		return cluster.PrepareGPUOperator(ctx, runner, stdout, clusterCfg, featureCfg, "gpuOperatorMig")
	case "gpu-operator-dra":
		return cluster.PrepareGPUOperator(ctx, runner, stdout, clusterCfg, featureCfg, "gpuOperatorDRA")
	default:
		return func(context.Context) {}, nil
	}
}

// draProbeNeeded reports whether DRA configuration or selected scenarios require a DRA probe.
func draProbeNeeded(opts config.Tests) bool {
	return selectedScenarioUsesCapability(opts, "cluster:dra")
}

// dcgmImageNeeded reports whether any selected coverage needs a standalone DCGM image.
func dcgmImageNeeded(opts config.Tests) bool {
	for _, capabilityName := range []string{
		"host:profiling",
		"host:nvswitch",
		"host:grace_cpu",
		"host:c2c",
		"host:mig_instance_entity",
		"host:unsupported_field",
		"dcgm:remote_dcgm",
		"dcgm:failure_injection",
		"dcgm:failure_injection_nvlink_crc",
	} {
		if selectedScenarioUsesCapability(opts, capabilityName) {
			return true
		}
	}
	return false
}

// standaloneDCGMImageNeeded reports whether Kubernetes coverage needs DCGM deployed beside the exporter.
func standaloneDCGMImageNeeded(opts config.Tests) bool {
	return selectedScenarioUsesCapability(opts, "dcgm:remote_dcgm") ||
		selectedScenarioUsesCapability(opts, "dcgm:failure_injection") ||
		selectedScenarioUsesCapability(opts, "dcgm:failure_injection_nvlink_crc")
}

// containerRemoteDCGMImageNeeded reports whether container coverage needs a DCGM image for remote probes.
func containerRemoteDCGMImageNeeded(opts config.Tests) bool {
	for _, entry := range selectedScenariosForPrerequisites(opts, scenario.SuiteContainer) {
		if entry.Selector() == containerRemoteDCGMURIScenario.Selector() {
			return true
		}
	}
	return false
}

// selectedScenarioUsesCapability checks selected scenarios for a required capability before planning.
func selectedScenarioUsesCapability(opts config.Tests, capabilityName string) bool {
	for _, entry := range selectedScenariosForPrerequisites(opts, scenario.SuiteK8s) {
		for _, gate := range append(append([]string{}, entry.Capabilities...), entry.ExtraGates...) {
			if gate == capabilityName {
				return true
			}
		}
	}
	return false
}

// selectedScenariosForPrerequisites ignores skip filters so setup can satisfy explicitly selected scenarios.
func selectedScenariosForPrerequisites(opts config.Tests, suite scenario.Suite) []scenario.Scenario {
	if len(opts.Scenarios) == 0 {
		return scenario.SelectedForSuite(scenario.Catalog, suite, config.Config{Tests: opts})
	}
	selected := make([]scenario.Scenario, 0, len(opts.Scenarios))
	for _, entry := range scenario.Catalog {
		if entry.Suite != suite {
			continue
		}
		if !containsString(opts.Scenarios, entry.Selector()) {
			continue
		}
		if containsString(opts.SkipSuites, "all") || containsString(opts.SkipSuites, string(entry.Suite)) || containsString(opts.SkipScenarios, entry.Selector()) {
			continue
		}
		if len(opts.Suites) != 0 && !containsString(opts.Suites, "all") && !containsString(opts.Suites, string(entry.Suite)) {
			continue
		}
		selected = append(selected, entry)
	}
	return selected
}

// featureModeRequired reports whether a feature mode asks setup to fail if unavailable.
func featureModeRequired(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// featureModeEnabled reports whether a feature mode permits setup to attempt configuration.
func featureModeEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// ipFamilyRequestsIPv6 reports whether a k3d IP-family option needs IPv6 cluster support.
func ipFamilyRequestsIPv6(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ipv6", "dualstack":
		return true
	default:
		return false
	}
}

// groupHasLabel reports whether any scenario in a plan group carries the given catalog label.
func groupHasLabel(group scenario.PlanGroup, label string) bool {
	for _, candidate := range group.Labels {
		if candidate == label {
			return true
		}
	}
	return false
}

// emitSkippedScenarioMarkers records terminal markers for scenarios that planning skipped.
func emitSkippedScenarioMarkers(reporter marker.Reporter, plan scenario.Plan) error {
	for _, item := range plan.Scenarios {
		if item.Outcome != scenario.OutcomeSkipped {
			continue
		}
		name, ok := item.Scenario.MarkerBaseName()
		if !ok {
			continue
		}
		if err := reporter.Emit(marker.StatusRunning, name); err != nil {
			return err
		}
		if err := reporter.Emit(marker.StatusSkipped, name); err != nil {
			return err
		}
	}
	return nil
}

// applyHostProbeOutputs forwards host probe evidence to the k8s suite through environment variables.
func applyHostProbeOutputs(opts *config.Tests, probe capability.ProbeResult) {
	if probe.MIGInstanceEntityID != "" {
		opts.MIGInstanceEntityID = probe.MIGInstanceEntityID
	}
	if probe.MIGInstanceNVMLID != "" {
		opts.MIGInstanceNVMLID = probe.MIGInstanceNVMLID
	}
	if probe.UnsupportedFieldCandidate != "" {
		opts.UnsupportedFieldCandidate = probe.UnsupportedFieldCandidate
	}
	if probe.UnsupportedFieldEvidence != "" {
		opts.UnsupportedFieldEvidence = probe.UnsupportedFieldEvidence
	}
}

// clusterConfig derives the cluster package configuration from test command options.
func clusterConfig(root string, opts config.Tests) cluster.Config {
	cfg := cluster.DefaultConfig()
	if opts.ClusterName != "" {
		cfg.ClusterName = opts.ClusterName
	}
	if opts.Namespace != "" {
		cfg.Namespace = opts.Namespace
		cfg.DCGMNamespace = opts.Namespace + "-dcgm"
	}
	if opts.DCGMNamespace != "" {
		cfg.DCGMNamespace = opts.DCGMNamespace
	}
	if opts.ReleaseName != "" {
		cfg.ReleaseName = opts.ReleaseName
	}
	cfg.Kubeconfig = opts.Kubeconfig
	if cfg.LocalK3D() {
		cfg.LocalKubeconfig = filepath.Join(root, ".local-e2e", "kubeconfig-"+cfg.ClusterName+".yaml")
	}
	return cfg
}

// localClusterConfig derives k3d-specific configuration for owned local clusters.
func localClusterConfig(root string, cfg cluster.Config, opts config.Tests) (cluster.LocalConfig, error) {
	local, err := cluster.DefaultLocalConfig(root, opts)
	if err != nil {
		return cluster.LocalConfig{}, err
	}
	local.ClusterName = cfg.ClusterName
	local.Kubeconfig = cfg.LocalKubeconfig
	local.IPFamily = opts.K3dIPFamily
	if opts.K8sRuntimeClass != "" {
		local.RuntimeClass = opts.K8sRuntimeClass
	}
	if opts.K8sNodeSelectorKey != "" {
		local.NodeSelectorKey = opts.K8sNodeSelectorKey
	}
	if opts.K8sNodeSelectorValue != "" {
		local.NodeSelectorValue = opts.K8sNodeSelectorValue
	}
	return local, nil
}

// clusterFeatureConfig derives optional GPU feature setup from test command options.
func clusterFeatureConfig(root string, opts config.Tests) cluster.FeatureConfig {
	return cluster.FeatureConfig{
		Root:               root,
		DRAConfigure:       opts.DRAConfigure,
		GPUOperator:        opts.GPUOperator,
		SharedGPUConfigure: opts.SharedGPUConfigure,
		SharedGPUReplicas:  opts.SharedGPUReplicas,
		WaitTimeout:        opts.WaitTimeout,
		ExporterImage:      opts.ExporterImage,
		BusyboxImage:       opts.BusyboxImage,
		Dependencies:       opts.Dependencies,
		ImagePullSecret:    opts.K8sImagePullSecret,
	}
}

// scenarioGroupResultName converts a scenario group name to its suite-level result marker name.
func scenarioGroupResultName(group string) string {
	return "dcgm_exporter_e2e_group_" + strings.ReplaceAll(group, "-", "_")
}

// runtimeClassFor prefers explicit test options over cluster defaults.
func runtimeClassFor(opts config.Tests, cfg cluster.Config) string {
	if opts.K8sRuntimeClass != "" {
		return opts.K8sRuntimeClass
	}
	if cfg.LocalK3D() {
		return cluster.DefaultRuntimeClass()
	}
	return ""
}

// nodeSelectorKeyFor prefers explicit test options over cluster defaults.
func nodeSelectorKeyFor(opts config.Tests, cfg cluster.Config) string {
	if opts.K8sNodeSelectorKey != "" {
		return opts.K8sNodeSelectorKey
	}
	if cfg.LocalK3D() {
		return cluster.DefaultNodeSelectorKey()
	}
	return ""
}

// nodeSelectorValueFor prefers explicit test options over cluster defaults.
func nodeSelectorValueFor(opts config.Tests, cfg cluster.Config) string {
	if opts.K8sNodeSelectorValue != "" {
		return opts.K8sNodeSelectorValue
	}
	if cfg.LocalK3D() {
		return cluster.DefaultNodeSelectorValue()
	}
	return ""
}

// dcgmAddressFor chooses the remote DCGM service address visible to validation pods.
func dcgmAddressFor(opts config.Tests, cfg cluster.Config) string {
	if opts.RemoteDCGM != "" {
		return opts.RemoteDCGM
	}
	return dcgmNameFor(opts) + "." + cfg.DCGMNamespace + ".svc.cluster.local:" + dcgmPortFor(opts)
}

// dcgmNameFor returns the expected standalone DCGM service name.
func dcgmNameFor(opts config.Tests) string {
	if opts.DCGMName != "" {
		return opts.DCGMName
	}
	return "dcgm"
}

// dcgmPortFor returns the expected standalone DCGM service port.
func dcgmPortFor(opts config.Tests) string {
	if opts.DCGMPort != "" {
		return opts.DCGMPort
	}
	return "5555"
}

// k8sExporterImage returns the reference deployed inside the cluster.
func k8sExporterImage(clusterCfg cluster.Config, opts config.Tests) string {
	if clusterCfg.LocalK3D() {
		return localK3dExporterImage(opts)
	}
	return exporterImage(opts)
}
