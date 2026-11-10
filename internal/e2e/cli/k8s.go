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
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/bootstrap"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/marker"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/mig"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

// GPU Operator k8s group names that setup handles specially.
const (
	gpuOperatorGroupPrefix       = "gpu-operator-"
	gpuOperatorMainEmbeddedGroup = "gpu-operator-main-embedded"
	gpuOperatorMainRemoteGroup   = "gpu-operator-main-remote"
	gpuOperatorMainDRAGroup      = "gpu-operator-main-dra"
)

const (
	embeddedNVSwitchGroup  = "embedded-nvswitch"
	embeddedMIGGroup       = "embedded-mig"
	embeddedSharedGPUGroup = "embedded-shared-gpu"
	// embeddedRenamedGPUGroup time-slices GPUs under the renamed resource
	// nvidia.com/gpu.shared.
	embeddedRenamedGPUGroup = "embedded-renamed-gpu"
	standaloneIPv6Group     = "standalone-ipv6"
	standaloneBaseGroup     = "standalone-baseline"
)

// runK8sSuite keeps static MIG mutation isolated from non-MIG scenarios.
func runK8sSuite(ctx context.Context, stdout io.Writer, root string, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	if splitMixedMIGRun(cfg.TestOptions) {
		var errs []error
		fmt.Fprintln(stdout, "[e2e] k8s maximum-coverage phase: full-GPU baseline")
		nonMIG := cfg
		nonMIG.TestOptions.MIGConfigure = "false"
		nonMIG.TestOptions.SkipScenarios = append(append([]string{}, nonMIG.TestOptions.SkipScenarios...), migScenarioSelectors()...)
		nonMIGExecuted, err := runK8sSuiteOnce(ctx, stdout, root, manager, runner, nonMIG, true)
		if err != nil {
			fullGPUError := fmt.Errorf("full-GPU phase: %w", err)
			errs = append(errs, fullGPUError)
			if code, ok := ExitCode(err); ok && code == exitSetupFailure {
				return errors.Join(errs...)
			}
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, fmt.Errorf("stop Kubernetes phase scheduling: %w", err))...)
		}

		fmt.Fprintln(stdout, "[e2e] k8s maximum-coverage phase: MIG")
		mig := cfg
		mig.TestOptions.SkipScenarios = append(append([]string{}, mig.TestOptions.SkipScenarios...), nonMIGScenarioSelectors()...)
		_, err = runK8sSuiteOnce(ctx, stdout, root, manager, runner, mig, nonMIGExecuted)
		if err != nil {
			errs = append(errs, fmt.Errorf("MIG phase: %w", err))
		}
		return errors.Join(errs...)
	}
	_, err := runK8sSuiteOnce(ctx, stdout, root, manager, runner, cfg, false)
	return err
}

// splitMixedMIGRun reports whether the selected k8s scenarios need separate
// non-MIG and static-MIG phases. Host MIG mutation happens before local cluster
// creation, so combining those scenario classes would expose the non-MIG phase
// through the device plugin's default migStrategy=none on a mixed-MIG host.
// Without a requested change the run uses MIG as it is, in one phase.
func splitMixedMIGRun(opts config.TestOptions) bool {
	if !mig.MutationRequested(opts.MIGConfigure) {
		return false
	}

	var hasMIG, hasNonMIG bool
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, config.Config{TestOptions: opts})
	for _, entry := range selected {
		if entry.UsesKubernetesAllocation(scenario.KubernetesAllocationMIG, scenario.KubernetesAllocationMixedMIG) {
			hasMIG = true
		} else {
			hasNonMIG = true
		}
	}
	return hasMIG && hasNonMIG
}

// migScenarioSelectors returns every k8s catalog selector that requires MIG.
func migScenarioSelectors() []string {
	return scenario.SelectorsMatching(scenario.Catalog, func(entry scenario.Scenario) bool {
		return entry.Suite == scenario.SuiteK8s &&
			entry.UsesKubernetesAllocation(scenario.KubernetesAllocationMIG, scenario.KubernetesAllocationMixedMIG)
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

// runK8sSuiteOnce executes one Kubernetes phase and reports whether it attempted any scenario groups.
func runK8sSuiteOnce(
	ctx context.Context,
	stdout io.Writer,
	root string,
	manager suite.TestBinaryManager,
	runner e2eexec.Runner,
	cfg config.Config,
	tolerateAllSkipped bool,
) (executed bool, resultErr error) {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, cfg)
	if len(selected) == 0 {
		return false, nil
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteK8s, false)
	if err != nil {
		return false, setupFailure(err)
	}
	var probeBinary string
	_, watchRetention := scenario.Find(selected, scenario.SuiteK8s, "watchRetention")
	_, remoteRestart := scenario.Find(selected, scenario.SuiteK8s, "remoteDcgmRestart")
	if watchRetention || remoteRestart {
		probeBinary, err = manager.EnsureDCGMProbeBinary(ctx)
		if err != nil {
			return false, setupFailure(err)
		}
	}
	writeSection(stdout, "Kubernetes setup")
	fmt.Fprintf(stdout, "[e2e] binary: %s\n", binary)
	if probeBinary != "" {
		fmt.Fprintf(stdout, "[e2e] DCGM probe: %s\n", probeBinary)
	}
	fmt.Fprintln(stdout)

	requirements := scenario.RequirementsFor(selected, runConditions(cfg.TestOptions))
	run := newK8sSuiteRun(ctx, stdout, root, runner, cfg, binary, probeBinary, selected, requirements)
	defer func() {
		if cleanupErr := run.cleanup(ctx); cleanupErr != nil {
			resultErr = errors.Join(resultErr, setupFailure(cleanupErr))
		}
	}()

	if err := run.emitSetupRunning(); err != nil {
		return false, err
	}
	if run.resultMarkers {
		fmt.Fprintln(stdout)
	}
	writeStep(stdout, "setup", "preparing cluster")
	if err := run.prepareCluster(); err != nil {
		return false, run.failSetup(err)
	}
	writeStep(stdout, "setup", "probing capabilities")
	probeInputs, err := run.prepareProbeInputs()
	if err != nil {
		return false, run.failSetup(err)
	}
	run.fieldSupport = run.fieldSupportEnv(probeInputs)
	plan, err := run.buildPlan(probeInputs)
	if err != nil {
		return false, err
	}
	return run.executePlanForPhase(plan, tolerateAllSkipped)
}

// k8sProbeInputs carries host and cluster probe evidence into scenario planning.
type k8sProbeInputs struct {
	dcgmImage                    string
	hostDCGMAddress              string
	dcgmImagePlatformUnavailable bool
}

// k8sSuiteRun keeps the mutable state for one k8s suite attempt together.
type k8sSuiteRun struct {
	ctx                  context.Context
	stdout               io.Writer
	root                 string
	runner               e2eexec.Runner
	cfg                  config.Config
	binary               string
	probe                string
	selected             []scenario.Scenario
	requirements         []scenario.SelectedRequirement
	reporter             marker.Reporter
	ensureHostDCGM       func(context.Context, io.Writer, string, e2eexec.Runner, config.TestOptions) error
	prepareHostDCGMProbe func(context.Context, io.Writer, e2eexec.Runner, config.TestOptions) (string, func(context.Context) error, error)

	clusterCfg cluster.Config
	featureCfg cluster.FeatureConfig

	// planningCaps is the capability snapshot used to plan this run; group setup reads host facts from it.
	planningCaps capability.Snapshot

	// skips collects the specs each group skipped, for the end-of-run listing.
	skips *skippedSpecs

	resultMarkers                bool
	suiteResultMarkers           bool
	cleanupFuncs                 []func(context.Context) error
	registryDockerConfigJSONPath string

	// fieldSupport is the E2E_DCGM_FIELD_SUPPORT entry for groups whose
	// exporter image carries fieldSupportDCGM, the probe's DCGM version.
	fieldSupport     []string
	fieldSupportDCGM string
}

// newK8sSuiteRun derives all local cluster configuration shared by one k8s suite attempt.
func newK8sSuiteRun(
	ctx context.Context,
	stdout io.Writer,
	root string,
	runner e2eexec.Runner,
	cfg config.Config,
	binary string,
	probe string,
	selected []scenario.Scenario,
	requirements []scenario.SelectedRequirement,
) *k8sSuiteRun {
	resultMarkers := cfg.TestOptions.ResultMarkers && !cfg.TestOptions.NoResultMarkers
	suiteResultMarkers := specMarkersEnabled(cfg.TestOptions)
	clusterCfg := clusterConfig(root, cfg.TestOptions)
	featureCfg := clusterFeatureConfig(root, clusterCfg, cfg.TestOptions, "")
	return &k8sSuiteRun{
		ctx:                  ctx,
		stdout:               stdout,
		root:                 root,
		runner:               runner,
		cfg:                  cfg,
		binary:               binary,
		probe:                probe,
		selected:             selected,
		requirements:         requirements,
		reporter:             marker.NewReporter(stdout),
		ensureHostDCGM:       bootstrap.EnsureHostDCGM,
		prepareHostDCGMProbe: bootstrap.PrepareHostDCGMProbe,
		clusterCfg:           clusterCfg,
		featureCfg:           featureCfg,
		resultMarkers:        resultMarkers,
		suiteResultMarkers:   suiteResultMarkers,
		skips:                skippedSpecsFrom(ctx),
	}
}

// emitSetupRunning announces k8s setup before cluster mutation starts.
func (r *k8sSuiteRun) emitSetupRunning() error {
	if !r.resultMarkers {
		return nil
	}
	return r.reporter.Emit(marker.StatusRunning, "dcgm_exporter_e2e_setup")
}

// failSetup captures cluster evidence, emits the setup failure marker, and wraps the error.
func (r *k8sSuiteRun) failSetup(err error) error {
	collectClusterDiagnostics(r.ctx, r.stdout, r.runner, r.clusterCfg)
	if r.resultMarkers {
		_ = r.reporter.Emit(marker.StatusFailed, "dcgm_exporter_e2e_setup")
	}
	return setupFailure(err)
}

// collectClusterDiagnostics preserves best-effort evidence before cleanup mutates the cluster.
func collectClusterDiagnostics(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, cfg cluster.Config) {
	diagnosticsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	writeSection(stdout, "Kubernetes failure diagnostics")
	_ = cluster.Diagnostics(diagnosticsCtx, runner, stdout, cfg)
}

// cleanup removes owned cluster resources after a k8s suite attempt.
func (r *k8sSuiteRun) cleanup(ctx context.Context) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	var errs []error
	for i := len(r.cleanupFuncs) - 1; i >= 0; i-- {
		errs = append(errs, r.cleanupFuncs[i](cleanupCtx))
	}
	if r.clusterCfg.LocalK3D() && r.cfg.TestOptions.Cleanup && !r.cfg.TestOptions.KeepCluster {
		errs = append(errs, cluster.Cleanup(cleanupCtx, r.runner, r.stdout, r.clusterCfg))
	}
	return errors.Join(errs...)
}

// prepareCluster prepares the selected cluster and checks its API before Docker image or registry setup.
func (r *k8sSuiteRun) prepareCluster() error {
	if !r.clusterCfg.LocalK3D() {
		writeStep(r.stdout, "setup", "using external Kubernetes cluster")
		if err := r.checkKubernetesAPIReady(); err != nil {
			return err
		}
		r.cleanupFuncs = append(r.cleanupFuncs, func(cleanupCtx context.Context) error {
			if !r.cfg.TestOptions.Cleanup {
				fmt.Fprintln(r.stdout, "[e2e] retaining CLI-managed Kubernetes resources (--cleanup=false)")
				return nil
			}
			if err := cluster.Cleanup(cleanupCtx, r.runner, r.stdout, r.clusterCfg); err != nil {
				return fmt.Errorf("clean external cluster resources: %w", err)
			}
			return nil
		})
		return nil
	}

	migCleanup, err := mig.PrepareHostBeforeCluster(r.ctx, r.stdout, r.runner, r.cfg.TestOptions)
	if err != nil {
		return err
	}
	r.cleanupFuncs = append(r.cleanupFuncs, migCleanup)

	localCfg, err := localClusterConfig(r.root, r.clusterCfg, r.cfg.TestOptions)
	if err != nil {
		return err
	}
	writeStep(r.stdout, "setup", "starting local k3d cluster "+r.clusterCfg.ClusterName)
	if _, err := cluster.EnsureLocal(r.ctx, r.runner, r.stdout, localCfg); err != nil {
		return err
	}
	if err := r.checkKubernetesAPIReady(); err != nil {
		return err
	}
	writeStep(r.stdout, "setup", "preparing registry auth")
	cleanupDockerConfig, _, err := prepareDockerRegistryLogin(r.ctx, r.stdout, r.runner, &r.cfg.TestOptions)
	if err != nil {
		return err
	}
	r.cleanupFuncs = append(r.cleanupFuncs, func(context.Context) error {
		cleanupDockerConfig()
		return nil
	})
	writeStep(r.stdout, "setup", "importing exporter image")
	if err := ensureDockerImageAvailable(r.ctx, r.stdout, r.runner, r.cfg.TestOptions, exporterImage(r.cfg.TestOptions)); err != nil {
		return err
	}
	if err := tagLocalK3dImage(r.ctx, r.runner, r.stdout, r.cfg.TestOptions); err != nil {
		return err
	}
	return cluster.ImportImageIfPresent(r.ctx, r.runner, r.stdout, r.clusterCfg, localK3dExporterImage(r.cfg.TestOptions))
}

// checkKubernetesAPIReady verifies the API only when the selected scenarios need it.
// The run receives requirements from the already selected scenarios, so it does not select again.
func (r *k8sSuiteRun) checkKubernetesAPIReady() error {
	if !scenario.HasRequirement(r.requirements, scenario.Requirement{Kind: scenario.RequirementKubernetesAPI}) {
		return nil
	}
	writeStep(r.stdout, "setup", "checking Kubernetes API readiness")
	return cluster.CheckAPIReady(r.ctx, r.runner, r.stdout, r.clusterCfg)
}

// prepareProbeInputs gathers controller-side DCGM data used to decide local Kubernetes scenarios.
// External targets return no controller evidence because target facts come from Kubernetes probes during planning.
func (r *k8sSuiteRun) prepareProbeInputs() (k8sProbeInputs, error) {
	if err := r.prepareRegistryCredentials(); err != nil {
		return k8sProbeInputs{}, err
	}
	return r.prepareDCGMProbeInputs()
}

// prepareDCGMProbeInputs gathers local DCGM image information and prepares host fallback inputs when required.
func (r *k8sSuiteRun) prepareDCGMProbeInputs() (k8sProbeInputs, error) {
	inputs := k8sProbeInputs{}
	if !r.clusterCfg.LocalK3D() || !scenario.HasImageRequirement(r.requirements, scenario.ContainerImageDCGM) {
		return inputs, nil
	}
	inputs.dcgmImage = dcgmImage(r.cfg.TestOptions)
	err := ensureDCGMImageVersion(r.ctx, r.runner, r.cfg.TestOptions)
	if err == nil {
		return inputs, nil
	}
	if !errors.Is(err, errDCGMImagePlatformUnavailable) {
		return k8sProbeInputs{}, err
	}
	inputs.dcgmImagePlatformUnavailable = true
	probeInputs := scenario.CapabilityProbeInputsFor(r.selected)
	probeNeedsDCGM := containsCapabilityProbeInput(probeInputs, scenario.CapabilityProbeInputDCGMImage) ||
		containsCapabilityProbeInput(probeInputs, scenario.CapabilityProbeInputMIGState)
	if !probeNeedsDCGM {
		inputs.dcgmImage = ""
		return inputs, nil
	}
	hostDCGMAddress, err := r.prepareHostDCGMFallback()
	if err != nil {
		return k8sProbeInputs{}, err
	}
	inputs.hostDCGMAddress = hostDCGMAddress
	return inputs, nil
}

// prepareRegistryCredentials prepares registry credentials and records their cleanup with the Kubernetes run.
func (r *k8sSuiteRun) prepareRegistryCredentials() error {
	registryConfigPath, cleanupRegistryConfig, err := prepareRegistryAuth(r.ctx, r.stdout, r.runner, r.clusterCfg, &r.cfg.TestOptions)
	if err != nil {
		return err
	}
	if cleanupRegistryConfig != nil {
		r.cleanupFuncs = append(r.cleanupFuncs, func(context.Context) error {
			cleanupRegistryConfig()
			return nil
		})
	}
	r.registryDockerConfigJSONPath = registryConfigPath
	return nil
}

// prepareHostDCGMFallback starts the exact host DCGM used to probe capabilities
// when the DCGM image is not published for this host architecture.
func (r *k8sSuiteRun) prepareHostDCGMFallback() (string, error) {
	writeStep(r.stdout, "setup", "checking exact host DCGM for image platform fallback")
	ensureHostDCGM := r.ensureHostDCGM
	if ensureHostDCGM == nil {
		ensureHostDCGM = bootstrap.EnsureHostDCGM
	}
	if err := ensureHostDCGM(r.ctx, r.stdout, r.root, r.runner, r.cfg.TestOptions); err != nil {
		return "", fmt.Errorf("prepare host DCGM probe fallback: %w", err)
	}
	if err := ensureHostDCGMVersion(r.ctx, r.runner, r.cfg.TestOptions); err != nil {
		return "", err
	}
	prepareHostDCGMProbe := r.prepareHostDCGMProbe
	if prepareHostDCGMProbe == nil {
		prepareHostDCGMProbe = bootstrap.PrepareHostDCGMProbe
	}
	hostDCGMAddress, cleanupHostDCGM, err := prepareHostDCGMProbe(r.ctx, r.stdout, r.runner, r.cfg.TestOptions)
	if err != nil {
		return "", fmt.Errorf("prepare host DCGM probe fallback: %w", err)
	}
	r.cleanupFuncs = append(r.cleanupFuncs, cleanupHostDCGM)
	return hostDCGMAddress, nil
}

// Capability name prefixes for controller host facts and Kubernetes target facts.
const (
	hostCapabilityPrefix    = "host:"
	clusterCapabilityPrefix = "cluster:"
)

// Host capabilities for the current MIG state.
const (
	hostMIGActive = hostCapabilityPrefix + "mig_active"
	hostMixedMIG  = hostCapabilityPrefix + "mixed_mig"
)

// buildPlan converts probe evidence and selected scenarios into runnable and skipped groups.
func (r *k8sSuiteRun) buildPlan(inputs k8sProbeInputs) (scenario.Plan, error) {
	caps, err := r.planningCapabilities(inputs)
	if err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	r.planningCaps = caps
	r.writePlanningCapabilities(caps)
	selection := scenario.SelectionOptions{}
	if !r.clusterCfg.LocalK3D() {
		selection.SkipUnknownCapabilityPrefixes = []string{hostCapabilityPrefix}
	}
	plan, err := scenario.SelectWithOptions(scenario.Catalog, caps, r.cfg, selection)
	if err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	if plan, err = r.applyTargetScenarioInputs(plan, inputs); err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	if plan, err = r.prepareRemoteDCGMImage(plan, inputs); err != nil {
		return scenario.Plan{}, r.failSetup(err)
	}
	writeSection(r.stdout, "Scenario plan")
	if err := plan.WriteSummary(r.stdout); err != nil {
		return scenario.Plan{}, err
	}
	writeVerbosePlanLabels(r.stdout, r.cfg.TestOptions.Verbose, plan)
	return plan, nil
}

// hostUsesNVSDM reports whether the host capability snapshot says DCGM reaches NVSwitches through NVSDM.
func hostUsesNVSDM(caps capability.Snapshot) bool {
	return caps.Lookup(hostCapabilityPrefix+"nvswitch_nvsdm").Status == capability.StatusSupported
}

// planningCapabilities probes the target cluster and adds local host facts for an
// owned k3d target, or the installable DRA projection for an external target.
func (r *k8sSuiteRun) planningCapabilities(inputs k8sProbeInputs) (capability.Snapshot, error) {
	writeStep(r.stdout, "setup", "probing cluster capabilities")
	clusterCaps, err := cluster.ProbeCapabilities(r.ctx, r.runner, r.clusterCfg)
	if err != nil {
		return capability.Snapshot{}, err
	}
	caps := capability.NewSnapshot(clusterCaps)
	if r.clusterCfg.LocalK3D() {
		writeStep(r.stdout, "setup", "probing local host capabilities")
		hostCaps := capability.ProbeHost(r.ctx, r.runner, r.hostProbeOptions(inputs))
		return localKubernetesPlanningCapabilities(hostCaps, caps, r.cfg.TestOptions.MIGConfigure), nil
	}
	if r.cfg.TestOptions.DRAConfigure.Required() {
		// Setup installs the NVIDIA DRA driver on a target that serves the DRA API.
		caps = caps.With(localTargetAllocationCapability("dra_resources", caps.Lookup("cluster:dra"), "setup can install NVIDIA DRA resources"))
	}
	return caps, nil
}

// fieldSupportEnv produces the field support evidence for an owned local k3d
// cluster, whose exporter reads this host's GPUs. On any other cluster the
// probe cannot reach the exporter's DCGM, so nothing is produced and the suite
// reports missing CSV rows as unqualified skips.
func (r *k8sSuiteRun) fieldSupportEnv(inputs k8sProbeInputs) []string {
	run := fieldSupportRun{
		stdout:   r.stdout,
		root:     r.root,
		toolsDir: bootstrap.TestsToolsDir(r.root, r.cfg.TestOptions),
		runner:   r.runner,
	}
	if !r.clusterCfg.LocalK3D() {
		writeSection(r.stdout, "DCGM field support")
		if !run.fromEnvironment() {
			fmt.Fprintln(r.stdout, "[e2e] not probed for a cluster the CLI did not create; missing CSV rows are reported as unqualified skips")
		}
		return nil
	}

	// Query the DCGM image the capability probes use, or the host DCGM they
	// fall back to; the exporter's DCGM in the cluster is the same version.
	probeOpts := r.hostProbeOptions(inputs)
	if probeOpts.DCGMImage == "" && probeOpts.HostDCGMAddress == "" {
		probeOpts.DCGMImage = dcgmImage(r.cfg.TestOptions)
		if probeOpts.DCGMImage == "" {
			writeSection(r.stdout, "DCGM field support")
			if !run.fromEnvironment() {
				fmt.Fprintln(r.stdout, "[e2e] no DCGM image or host DCGM is configured to probe; missing CSV rows are reported as unqualified skips")
			}
			return nil
		}
	}

	// The evidence holds only when the exporter image has the probe's DCGM
	// version; groups check their own image against the version kept here.
	exporterVersion := wantExporterDCGMVersion(r.stdout, r.runner, r.cfg.TestOptions,
		k8sExporterImage(r.clusterCfg, r.cfg.TestOptions))
	run.wantDCGMVersion = func(ctx context.Context) (string, error) {
		version, err := exporterVersion(ctx)
		r.fieldSupportDCGM = version
		return version, err
	}
	return run.env(r.ctx, probeOpts)
}

// groupFieldSupport returns the field support evidence for one group. Most
// groups read the probed DCGM and always get it. A GPU Operator group, or a
// standalone group on a user-supplied hostengine, reads a different DCGM, so it
// gets the evidence only when that DCGM has the probe's version; otherwise the
// output says why and the group gets none.
func (r *k8sSuiteRun) groupFieldSupport(group scenario.PlanGroup) []string {
	if len(r.fieldSupport) == 0 {
		return nil
	}

	source, version, err := r.groupDCGMVersion(group)
	switch {
	case source == "":
		return r.fieldSupport
	case err != nil:
		fmt.Fprintf(r.stdout, "[e2e] group %s gets no DCGM field support evidence: %v\n", group.Name, err)
		return nil
	case version != r.fieldSupportDCGM:
		fmt.Fprintf(r.stdout, "[e2e] group %s gets no DCGM field support evidence: %s carries DCGM %s, the probe's DCGM is %s\n",
			group.Name, source, version, r.fieldSupportDCGM)
		return nil
	default:
		return r.fieldSupport
	}
}

// groupDCGMVersion names the DCGM a GPU Operator group's exporter reads when
// it can differ from the probed one, and returns its version. The main remote
// and DRA groups read a hostengine from the GPU Operator DCGM image; other
// GPU Operator groups embed the DCGM of the GPU Operator exporter image. An
// empty name means the group uses the probed exporter image. A standalone
// group whose hostengine the user supplied with --remote-dcgm reads a DCGM of
// unknown version, so it returns an error and gets no evidence.
func (r *k8sSuiteRun) groupDCGMVersion(group scenario.PlanGroup) (string, string, error) {
	opts := r.cfg.TestOptions
	switch {
	case group.Name == gpuOperatorMainRemoteGroup || group.Name == gpuOperatorMainDRAGroup:
		image := gpuOperatorDCGMImage(opts)
		version, err := dcgmImageVersion(r.ctx, r.runner, image, dockerConfigEnv(opts))
		return "DCGM image " + image, version, err
	case strings.HasPrefix(group.Name, gpuOperatorGroupPrefix):
		image := k8sGPUOperatorExporterImage(r.clusterCfg, opts)
		if image == k8sExporterImage(r.clusterCfg, opts) {
			return "", "", nil
		}

		version, err := availableImageDCGMVersion(r.ctx, r.stdout, r.runner, opts, image)
		return "exporter image " + image, version, err
	case group.Name == standaloneBaseGroup && opts.RemoteDCGM != "":
		return "user-supplied --remote-dcgm hostengine", "", errors.New("the DCGM version of the user-supplied --remote-dcgm hostengine is unknown")
	default:
		return "", "", nil
	}
}

// hostProbeOptions returns the local host probe inputs for an owned k3d target.
func (r *k8sSuiteRun) hostProbeOptions(inputs k8sProbeInputs) capability.ProbeOptions {
	return capability.ProbeOptions{
		DryRun:          r.cfg.TestOptions.DryRun,
		DCGMImage:       inputs.dcgmImage,
		DockerEnv:       dockerConfigEnv(r.cfg.TestOptions),
		HostDCGMAddress: inputs.hostDCGMAddress,
	}
}

// writePlanningCapabilities prints the capability evidence used for planning. Host
// features and applicability are shown only for a local target the controller hosts.
func (r *k8sSuiteRun) writePlanningCapabilities(caps capability.Snapshot) {
	verbose := r.cfg.TestOptions.Verbose
	local := r.clusterCfg.LocalK3D()
	writeSection(r.stdout, "Capabilities")
	if local {
		writeCapabilitySummary(r.stdout, "Host features", hostCapabilityPrefix, capability.HostFeatureNames(), caps, verbose)
	} else {
		fmt.Fprintln(r.stdout, "Controller hardware is not probed for an external Kubernetes target.")
	}
	writeCapabilitySummary(r.stdout, "Cluster features", clusterCapabilityPrefix, capability.ClusterFeatureNames(), caps, verbose)
	if local {
		writeCapabilitySummary(r.stdout, "Scenario applicability", hostCapabilityPrefix, capability.ApplicabilityNames(), caps, verbose)
	}
}

// applyTargetScenarioInputs resolves scenario inputs from a local target, skipping an
// unsupported-field scenario without a candidate. On an external cluster it
// skips controller-probe scenarios and scenarios that need e2e-owned
// device-plugin setup.
func (r *k8sSuiteRun) applyTargetScenarioInputs(plan scenario.Plan, inputs k8sProbeInputs) (scenario.Plan, error) {
	if !r.clusterCfg.LocalK3D() {
		return skipExternalOwnedClusterScenarios(skipExternalControllerProbeScenarios(plan)), nil
	}
	scenarioInputs, err := capability.ResolveScenarioInputs(
		r.ctx,
		r.runner,
		r.hostProbeOptions(inputs),
		planHasAnyLabel(plan, "migSpecificInstanceSelection"),
		planHasAnyLabel(plan, "fieldUnsupported"),
	)
	if err != nil {
		return scenario.Plan{}, err
	}
	if planHasAnyLabel(plan, "fieldUnsupported") && scenarioInputs.UnsupportedFieldCandidate == "" {
		reason := scenarioInputs.UnsupportedFieldNotApplicableReason
		if reason == "" {
			reason = "no unsupported DCGM field candidate was found"
		}
		plan = scenario.SkipSelectedLabel(plan, "fieldUnsupported", "scenario not applicable: "+reason)
	}
	applyScenarioInputs(&r.cfg.TestOptions, scenarioInputs)
	return plan, nil
}

// prepareRemoteDCGMImage skips remote-DCGM scenarios when the DCGM image is not published
// for this host architecture, and otherwise makes the image available to the cluster.
func (r *k8sSuiteRun) prepareRemoteDCGMImage(plan scenario.Plan, inputs k8sProbeInputs) (scenario.Plan, error) {
	if len(plan.RemoteLabels) != 0 && inputs.dcgmImagePlatformUnavailable {
		for _, label := range append([]string(nil), plan.RemoteLabels...) {
			plan = scenario.SkipSelectedLabel(
				plan,
				label,
				"scenario not applicable: required DCGM image is not published for this host architecture",
			)
		}
	}
	if len(plan.RemoteLabels) == 0 {
		return plan, nil
	}
	image, err := ensureLocalDCGMImageImport(r.ctx, r.stdout, r.runner, r.clusterCfg, r.cfg.TestOptions)
	if err != nil {
		return scenario.Plan{}, err
	}
	r.cfg.TestOptions.DCGMImage = image
	return plan, nil
}

// skipExternalControllerProbeScenarios skips work that needs DCGM data Kubernetes does not expose.
// External targets never substitute controller probes for those target facts; a later target-aware probe can replace this skip.
func skipExternalControllerProbeScenarios(plan scenario.Plan) scenario.Plan {
	const reason = "scenario not applicable: requires DCGM data from the Kubernetes target, " +
		"but this e2e version cannot probe that data remotely"
	for _, item := range plan.Scenarios {
		if item.Outcome != scenario.OutcomeSelected || !containsCapabilityProbeInput(item.Scenario.CapabilityProbeInputs, scenario.CapabilityProbeInputDCGMImage) {
			continue
		}
		plan = scenario.SkipSelectedLabel(plan, item.Scenario.Name, reason)
	}
	return plan
}

// ownedClusterOnlyLabels lists scenarios whose device-plugin setup e2e makes
// only on its own local k3d cluster. customResourceNames needs GPUs renamed to
// nvidia.com/gpu.shared, which would also hide nvidia.com/gpu from the other
// scenarios of an external cluster.
var ownedClusterOnlyLabels = []string{"customResourceNames"}

// skipExternalOwnedClusterScenarios skips, on an external cluster, the
// scenarios that need device-plugin setup e2e makes only on its own cluster.
func skipExternalOwnedClusterScenarios(plan scenario.Plan) scenario.Plan {
	const reason = "scenario not applicable: needs the NVIDIA device plugin to advertise time-sliced " +
		"nvidia.com/gpu.shared, which e2e configures only on its own local k3d cluster"
	for _, label := range ownedClusterOnlyLabels {
		if planHasAnyLabel(plan, label) {
			plan = scenario.SkipSelectedLabel(plan, label, reason)
		}
	}
	return plan
}

// localKubernetesPlanningCapabilities combines local host evidence with the capabilities of the owned k3d target.
// MIG resources follow the MIG state the host has now, after any MIG change the run was allowed to make.
// Local setup can prepare DRA resources from that host; external clusters never use these projections.
func localKubernetesPlanningCapabilities(host, target capability.Snapshot, migConfigure string) capability.Snapshot {
	entries := target.Entries()
	entries = append(entries,
		localMIGAllocationCapability("mig_resources", host, hostMIGActive, migConfigure),
		localMIGAllocationCapability("mixed_mig_resources", host, hostMixedMIG, migConfigure),
		localTargetAllocationCapability("dra_resources", target.Lookup("cluster:dra"), "local cluster can install NVIDIA DRA resources"),
	)
	return host.With(entries...)
}

// localMIGAllocationCapability maps one current-state MIG capability to a cluster capability.
// When the run could change MIG, the host does not rule out MIG profiles, and MIG still has no usable state,
// the change failed. That is not a skip, so the capability becomes unknown and planning fails with the state's
// own reason. Otherwise an unsupported state keeps its reason, except that a host with no MIG profiles or a
// single GPU gets the plain cause. When the host lists MIG profiles and no GPU has MIG on with a DCGM GPU
// instance, a run that may not change MIG also says so and how to rerun.
func localMIGAllocationCapability(name string, host capability.Snapshot, stateName, migConfigure string) capability.Capability {
	state := host.Lookup(stateName)
	entry := capability.Capability{
		Name:     clusterCapabilityPrefix + name,
		Status:   state.Status,
		Source:   state.Source,
		Reason:   state.Reason,
		Evidence: state.Evidence,
	}
	if state.Status != capability.StatusUnsupported {
		return entry
	}
	profiles := host.Lookup("host:mig").Status
	if profiles == capability.StatusUnsupported {
		entry.Reason = "GPU does not advertise MIG profiles"
		return entry
	}
	if stateName == hostMixedMIG && host.Lookup("host:multi_gpu").Status == capability.StatusUnsupported {
		entry.Reason = "mixed MIG needs at least two GPUs"
		return entry
	}
	requested := mig.MutationRequested(migConfigure)
	if requested && host.Lookup(hostMIGActive).Status == capability.StatusUnsupported {
		entry.Status = capability.StatusUnknown
		entry.Reason = "MIG change requested; " + state.Reason
		return entry
	}
	if requested || profiles != capability.StatusSupported || host.Lookup(hostMIGActive).Status == capability.StatusSupported {
		return entry
	}
	value := strings.TrimSpace(migConfigure)
	if value == "" {
		value = "auto"
	}
	entry.Reason += "; --mig-configure=" + value + " leaves MIG as it is; rerun with --mig-configure=true or a " +
		"profile ID to configure MIG on GPU 0 and restore it afterward"
	return entry
}

// localTargetAllocationCapability maps setup status and evidence to a cluster capability for planning.
func localTargetAllocationCapability(name string, source capability.Capability, reason string) capability.Capability {
	return capability.Capability{
		Name:     clusterCapabilityPrefix + name,
		Status:   source.Status,
		Source:   source.Source,
		Reason:   reason + ": " + source.Reason,
		Evidence: source.Evidence,
	}
}

// executePlan preserves ordinary-run behavior for a selected plan with no runnable groups.
func (r *k8sSuiteRun) executePlan(plan scenario.Plan) error {
	_, err := r.executePlanForPhase(plan, false)
	return err
}

// executePlanForPhase reports whether it attempted any groups and can accept an all-skipped split phase.
func (r *k8sSuiteRun) executePlanForPhase(
	plan scenario.Plan,
	tolerateAllSkipped bool,
) (bool, error) {
	if len(plan.Scenarios) != 0 && len(plan.Groups) == 0 {
		return false, r.reportWhollySkippedPlan(plan, tolerateAllSkipped)
	}
	if err := r.reportSetupPassed(plan); err != nil {
		return false, err
	}
	return r.runPlanGroups(plan)
}

// reportWhollySkippedPlan emits skip markers for a plan with no runnable groups. It
// fails setup unless the caller tolerates an all-skipped phase, which is then SKIPPED.
func (r *k8sSuiteRun) reportWhollySkippedPlan(plan scenario.Plan, tolerateAllSkipped bool) error {
	if r.resultMarkers {
		if err := emitSkippedScenarioMarkers(r.reporter, plan); err != nil {
			return err
		}
	}
	if !tolerateAllSkipped {
		return r.failSetup(errors.New("all selected Kubernetes scenarios were skipped; no tests were executed"))
	}
	if !r.resultMarkers {
		return nil
	}
	return r.reporter.Emit(marker.StatusSkipped, "dcgm_exporter_e2e_setup")
}

// reportSetupPassed emits the passed setup marker and the plan's skipped scenario markers.
func (r *k8sSuiteRun) reportSetupPassed(plan scenario.Plan) error {
	if !r.resultMarkers {
		return nil
	}
	fmt.Fprintln(r.stdout)
	if err := r.reporter.Emit(marker.StatusPassed, "dcgm_exporter_e2e_setup"); err != nil {
		return err
	}
	if err := emitSkippedScenarioMarkers(r.reporter, plan); err != nil {
		return err
	}
	fmt.Fprintln(r.stdout)
	return nil
}

// runPlanGroups runs each non-empty group until the run context ends. It reports
// whether any group ran and joins group failures as a scenario failure.
func (r *k8sSuiteRun) runPlanGroups(plan scenario.Plan) (bool, error) {
	var errs []error
	executed := false
	for index, group := range plan.Groups {
		if err := r.ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stop Kubernetes group scheduling: %w", err))
			break
		}
		if len(group.Labels) == 0 {
			continue
		}
		executed = true
		opts := k8sGroupOptions(r.cfg.TestOptions, plan.Groups, index)
		if err := r.runK8sGroup(&opts, group); err != nil {
			errs = append(errs, fmt.Errorf("group %s: %w", group.Name, err))
		}
	}
	if len(errs) != 0 {
		return executed, scenarioFailure(errors.Join(errs...))
	}
	return executed, nil
}

// k8sGroupOptions clears an install before a later selected group of the same
// kind. The final standalone DCGM or GPU Operator install may be retained.
func k8sGroupOptions(opts config.TestOptions, groups []scenario.PlanGroup, index int) config.TestOptions {
	if opts.Cleanup {
		return opts
	}

	groupPrefix := ""
	switch {
	case strings.HasPrefix(groups[index].Name, gpuOperatorGroupPrefix):
		groupPrefix = gpuOperatorGroupPrefix
	case strings.HasPrefix(groups[index].Name, "standalone-"):
		groupPrefix = "standalone-"
	default:
		return opts
	}

	for _, later := range groups[index+1:] {
		if len(later.Labels) != 0 && strings.HasPrefix(later.Name, groupPrefix) {
			opts.Cleanup = true
			break
		}
	}
	return opts
}

// ensureLocalDCGMImageImport imports the standalone DCGM image into owned k3d clusters when required.
// It returns the local tag after import, or the source when a pull secret is available.
func ensureLocalDCGMImageImport(
	ctx context.Context,
	stdout io.Writer,
	runner e2eexec.Runner,
	clusterCfg cluster.Config,
	opts config.TestOptions,
) (string, error) {
	source := dcgmImage(opts)
	if !clusterCfg.LocalK3D() {
		return source, nil
	}
	image, err := cluster.ResolveLocalK3DImage(source)
	if err != nil {
		return "", fmt.Errorf("local DCGM image: %w", err)
	}
	inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", image.Source}, Env: dockerConfigEnv(opts)})
	inspected := inspect.ExitCode == 0
	if inspected {
		err = cluster.TagLocalK3DImage(ctx, runner, stdout, image, "docker_tag_dcgm_image", dockerConfigEnv(opts))
		if err == nil {
			_, err = cluster.ImportImageIfPresentWithResult(ctx, runner, stdout, clusterCfg, image.Local, "k3d_import_dcgm_image")
		}
	} else {
		err = fmt.Errorf("docker image inspect %s failed: %s", image.Source, resultText(inspect))
	}
	if err == nil {
		return image.Local, nil
	}
	reason := "local k3d could not import the configured DCGM image"
	if !inspected {
		reason = "local k3d could not inspect the configured DCGM image"
	}
	reason += "; standalone DCGM scenarios require cluster image access"
	if opts.K8sImagePullSecret != "" {
		fmt.Fprintf(stdout, "[e2e] WARN %s; relying on Kubernetes image pull secret %s: %v\n", reason, opts.K8sImagePullSecret, err)
		return source, nil
	}
	return "", fmt.Errorf("%s; pass --docker-registry-login-file or pre-load the image: %w", reason, err)
}

// ensureLocalGPUOperatorImageImport makes an overridden main-chart controller image available to k3d.
func ensureLocalGPUOperatorImageImport(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, clusterCfg cluster.Config, opts config.TestOptions) error {
	image := strings.TrimSpace(opts.GPUOperatorImage)
	if !clusterCfg.LocalK3D() || image == "" {
		return nil
	}
	if err := ensureDockerImageAvailable(ctx, stdout, runner, opts, image); err != nil {
		return fmt.Errorf("make GPU Operator controller image available locally: %w", err)
	}
	if err := cluster.ImportImageIfPresentWithStep(ctx, runner, stdout, clusterCfg, image, "k3d_import_gpu_operator_image"); err != nil {
		return fmt.Errorf("import GPU Operator controller image into local k3d: %w", err)
	}
	return nil
}

// runK8sGroup prepares one scenario group, executes the k8s suite binary, and captures diagnostics on failure.
func (r *k8sSuiteRun) runK8sGroup(opts *config.TestOptions, group scenario.PlanGroup) error {
	writeSection(r.stdout, "Running k8s group: "+group.Name)
	fmt.Fprintf(r.stdout, "[e2e] scenarios: %d\n", len(group.Labels))
	writeStep(r.stdout, "running", strings.Join(group.Labels, ", "))
	writeVerbose(r.stdout, opts.Verbose, "%s labels: %s", group.Name, strings.Join(group.Labels, " || "))
	markers := &k8sGroupMarkers{
		reporter:     marker.NewReporter(r.stdout),
		stdout:       r.stdout,
		name:         scenarioGroupResultName(group.Name),
		groupMarkers: r.resultMarkers && !r.suiteResultMarkers,
		failMarkers:  r.resultMarkers,
	}
	if err := markers.start(); err != nil {
		return err
	}
	cleanup := noK8sGroupCleanup
	failGroup := func(err error) error {
		return failK8sGroup(r.ctx, r.stdout, r.runner, r.clusterCfg, cleanup, markers.fail, err)
	}

	var err error
	cleanup, err = r.prepareK8sGroup(*opts, group)
	restoreRuntime := k8sGroupRestoresRuntime(group.Name)
	// Kept resources stay as setup left them, even after a setup failure, so they can be inspected.
	if !opts.Cleanup && !restoreRuntime {
		cleanup = noK8sGroupCleanup
	}
	if err != nil {
		return failGroup(err)
	}
	if err := prepareRegistryAuthForNamespaces(r.ctx, r.stdout, r.runner, r.clusterCfg, opts, []string{r.clusterCfg.Namespace}); err != nil {
		return failGroup(err)
	}

	reportPath, removeReport, err := newGroupReportPath(string(scenario.SuiteK8s), group.Name)
	if err != nil {
		return failGroup(err)
	}
	defer removeReport()

	command, err := k8sTestCommand(r.binary, r.probe, r.root, r.clusterCfg, *opts, group, r.suiteResultMarkers)
	if err != nil {
		return failGroup(err)
	}
	command.Args = append(command.Args, ginkgoJSONReportFlag+reportPath)
	command.Env = append(command.Env, r.groupFieldSupport(group)...)
	command.Stdout = r.stdout
	command.Stderr = r.stdout
	result := r.runner.Run(r.ctx, command)
	fmt.Fprintln(r.stdout)
	if err := finishGroup(string(scenario.SuiteK8s), group.Name, reportPath, result, r.skips); err != nil {
		return failGroup(err)
	}
	if opts.Cleanup || restoreRuntime {
		if err := finishK8sGroup(r.ctx, r.stdout, group.Name, cleanup); err != nil {
			markers.fail()
			return err
		}
	}
	return markers.pass()
}

// k8sGroupRestoresRuntime reports groups whose cleanup restores temporary
// device-plugin or runtime changes, which must run even when workloads are retained.
func k8sGroupRestoresRuntime(name string) bool {
	return name == embeddedNVSwitchGroup || name == embeddedMIGGroup || name == embeddedSharedGPUGroup ||
		name == embeddedRenamedGPUGroup
}

// finishK8sGroup runs bounded cleanup after a Kubernetes group finishes.
func finishK8sGroup(ctx context.Context, stdout io.Writer, name string, cleanup func(context.Context) error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	writeStep(stdout, "cleanup", "finished k8s group "+name)
	if err := cleanup(cleanupCtx); err != nil {
		return fmt.Errorf("cleanup k8s group %s: %w", name, err)
	}
	return nil
}

// k8sGroupMarkers emits one Kubernetes group's result markers. Group markers
// are used when the suite binary does not emit per-spec markers; failure
// markers are emitted whenever result markers are enabled.
type k8sGroupMarkers struct {
	reporter     marker.Reporter
	stdout       io.Writer
	name         string
	groupMarkers bool
	failMarkers  bool
	running      bool
}

// emitRunning emits the running marker once when the group needs result markers.
func (m *k8sGroupMarkers) emitRunning() error {
	if m.running {
		return nil
	}
	m.running = true
	return m.reporter.Emit(marker.StatusRunning, m.name)
}

// start starts group reporting and writes a separator when group markers are enabled.
func (m *k8sGroupMarkers) start() error {
	if !m.groupMarkers {
		return nil
	}
	if err := m.emitRunning(); err != nil {
		return err
	}
	fmt.Fprintln(m.stdout)
	return nil
}

// fail emits a running marker if needed, then reports a failed group.
func (m *k8sGroupMarkers) fail() {
	if !m.failMarkers {
		return
	}
	_ = m.emitRunning()
	_ = m.reporter.Emit(marker.StatusFailed, m.name)
}

// pass reports a passed group when group markers are enabled.
func (m *k8sGroupMarkers) pass() error {
	if !m.groupMarkers {
		return nil
	}
	if err := m.reporter.Emit(marker.StatusPassed, m.name); err != nil {
		return err
	}
	fmt.Fprintln(m.stdout)
	return nil
}

// failK8sGroup preserves diagnostics before teardown, then attempts bounded
// cleanup and reports the failed group.
func failK8sGroup(
	ctx context.Context,
	stdout io.Writer,
	runner e2eexec.Runner,
	clusterCfg cluster.Config,
	cleanup func(context.Context) error,
	reportFailure func(),
	err error,
) error {
	collectClusterDiagnostics(ctx, stdout, runner, clusterCfg)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	cleanupErr := cleanup(cleanupCtx)
	reportFailure()
	return errors.Join(err, cleanupErr)
}

// k8sTestCommand builds the test-binary command for one selected Kubernetes group.
func k8sTestCommand(
	binary string,
	probe string,
	root string,
	clusterCfg cluster.Config,
	opts config.TestOptions,
	group scenario.PlanGroup,
	suiteResultMarkers bool,
) (e2eexec.Command, error) {
	runName := clusterCfg.RunName
	if runName == "" {
		runName = cluster.DefaultConfig().RunName
	}
	k8sDefinition, ok := suite.TestBinaryDefinitionFor(scenario.SuiteK8s)
	if !ok {
		return e2eexec.Command{}, fmt.Errorf("kubernetes suite definition is missing")
	}
	args := []string{
		"-test.v",
		"-test.run", "^" + k8sDefinition.TestName + "$",
		"--ginkgo.v",
		"--ginkgo.no-color",
		"--ginkgo.fail-on-empty",
		"--ginkgo.label-filter=" + strings.Join(group.Labels, " || "),
		"-kubeconfig=" + clusterCfg.EffectiveKubeconfig(),
		"-run-name=" + runName,
		"-namespace=" + clusterCfg.Namespace,
		"-chart=" + filepath.Join(root, "deployment"),
		"-exporter-image=" + k8sExporterImage(clusterCfg, opts),
		"-gpu-operator-exporter-image=" + k8sGPUOperatorExporterImage(clusterCfg, opts),
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
		fmt.Sprintf("-no-cleanup=%t", !opts.Cleanup),
		fmt.Sprintf("-owned-local-cluster=%t", clusterCfg.LocalK3D()),
	}
	if visibleDevices := nvidiaVisibleDevicesForGroup(clusterCfg, group); visibleDevices != "" {
		args = append(args, "-nvidia-visible-devices="+visibleDevices)
	}
	if probe != "" {
		args = append(args, "-dcgm-probe-binary="+probe)
	}
	return e2eexec.Command{Name: binary, Args: args, Dir: root}, nil
}

// prepareK8sGroup applies the cluster mutations required by one selected group.
func (r *k8sSuiteRun) prepareK8sGroup(opts config.TestOptions, group scenario.PlanGroup) (func(context.Context) error, error) {
	featureCfg := clusterFeatureConfig(r.root, r.clusterCfg, opts, r.registryDockerConfigJSONPath)
	switch group.Name {
	case embeddedNVSwitchGroup, embeddedMIGGroup, "embedded-dra", embeddedSharedGPUGroup, embeddedRenamedGPUGroup:
		return r.prepareEmbeddedK8sGroup(opts, group, featureCfg)
	case standaloneBaseGroup, standaloneIPv6Group:
		return r.prepareStandaloneK8sGroup(opts, group)
	case "gpu-operator-baseline", "gpu-operator-shared-gpu", "gpu-operator-mig", "gpu-operator-dra",
		gpuOperatorMainEmbeddedGroup, gpuOperatorMainRemoteGroup, gpuOperatorMainDRAGroup:
		return r.prepareGPUOperatorK8sGroup(opts, group, featureCfg)
	default:
		return noK8sGroupCleanup, nil
	}
}

// prepareEmbeddedK8sGroup prepares embedded Kubernetes group features on the owned local cluster.
func (r *k8sSuiteRun) prepareEmbeddedK8sGroup(opts config.TestOptions, group scenario.PlanGroup, featureCfg cluster.FeatureConfig) (func(context.Context) error, error) {
	switch group.Name {
	case embeddedNVSwitchGroup:
		if !r.clusterCfg.LocalK3D() {
			return noK8sGroupCleanup, nil
		}
		local, err := localClusterConfig(r.root, r.clusterCfg, opts)
		if err != nil {
			return noK8sGroupCleanup, err
		}
		return cluster.PrepareNVSwitchRuntime(r.ctx, r.runner, r.stdout, r.clusterCfg, local, hostUsesNVSDM(r.planningCaps))
	case embeddedMIGGroup:
		if !r.clusterCfg.LocalK3D() {
			return noK8sGroupCleanup, nil
		}
		local, err := localClusterConfig(r.root, r.clusterCfg, opts)
		if err != nil {
			return noK8sGroupCleanup, err
		}
		// A failed install may leave the plugin half changed, so the reset
		// is returned with the error and runs before the next group.
		reset := r.resetDevicePlugin(local)
		if err := cluster.ConfigureMIGDevicePlugin(r.ctx, r.runner, r.stdout, r.clusterCfg, local); err != nil {
			return reset, err
		}
		return reset, nil
	case "embedded-dra":
		return cluster.PrepareDRA(r.ctx, r.runner, r.stdout, r.clusterCfg, featureCfg)
	case embeddedSharedGPUGroup:
		return r.prepareSharedGPUGroup(opts, group.Name, false)
	case embeddedRenamedGPUGroup:
		return r.prepareSharedGPUGroup(opts, group.Name, true)
	default:
		return noK8sGroupCleanup, nil
	}
}

// prepareSharedGPUGroup configures time-sliced GPU resources for a group,
// renamed to nvidia.com/gpu.shared when renameByDefault is set, and returns
// the callback that restores the default device plugin. The callback is
// returned with a configuration error too, so a failed install is undone.
func (r *k8sSuiteRun) prepareSharedGPUGroup(
	opts config.TestOptions, groupName string, renameByDefault bool,
) (func(context.Context) error, error) {
	if !opts.SharedGPUConfigure.SetUpOn(r.clusterCfg.LocalK3D()) {
		return noK8sGroupCleanup, nil
	}

	if !r.clusterCfg.LocalK3D() {
		resource := "time-sliced nvidia.com/gpu"
		if renameByDefault {
			resource = "time-sliced nvidia.com/gpu.shared"
		}
		return noK8sGroupCleanup, fmt.Errorf(
			"k8s group %s needs %s, which e2e configures only on its own local k3d cluster; configure the external cluster before running it",
			groupName, resource)
	}

	local, err := localClusterConfig(r.root, r.clusterCfg, opts)
	if err != nil {
		return noK8sGroupCleanup, err
	}

	reset := r.resetDevicePlugin(local)
	if err := cluster.ConfigureSharedGPUDevicePlugin(
		r.ctx, r.runner, r.stdout, r.clusterCfg, local, opts.SharedGPUReplicas, renameByDefault,
	); err != nil {
		return reset, err
	}
	return reset, nil
}

// resetDevicePlugin returns the group cleanup that restores the default
// NVIDIA device plugin on the owned local cluster.
func (r *k8sSuiteRun) resetDevicePlugin(local cluster.LocalConfig) func(context.Context) error {
	return func(cleanupCtx context.Context) error {
		return cluster.ResetDevicePlugin(cleanupCtx, r.runner, r.stdout, r.clusterCfg, local)
	}
}

// prepareStandaloneK8sGroup deploys standalone DCGM and returns the group cleanup callback.
func (r *k8sSuiteRun) prepareStandaloneK8sGroup(opts config.TestOptions, group scenario.PlanGroup) (func(context.Context) error, error) {
	if group.Name == standaloneIPv6Group && opts.RemoteDCGM != "" {
		return noK8sGroupCleanup, fmt.Errorf("remoteDcgmIPv6Hostname requires the E2E-owned DCGM Service hostname; do not set --remote-dcgm")
	}
	if err := prepareRegistryAuthForNamespaces(r.ctx, r.stdout, r.runner, r.clusterCfg, &opts, []string{r.clusterCfg.DCGMNamespace}); err != nil {
		return noK8sGroupCleanup, err
	}
	dcgm := cluster.DefaultDCGMConfig()
	dcgm.Image = dcgmImage(opts)
	dcgm.RuntimeClass = runtimeClassFor(opts, r.clusterCfg)
	dcgm.NodeSelectorKey = nodeSelectorKeyFor(opts, r.clusterCfg)
	dcgm.NodeSelectorValue = nodeSelectorValueFor(opts, r.clusterCfg)
	dcgm.ImagePullSecret = opts.K8sImagePullSecret
	dcgm.Name = dcgmNameFor(opts)
	dcgm.Port = dcgmPortFor(opts)
	dcgm.WaitTimeout = opts.WaitTimeout
	dcgm.IPv6Only = group.Name == standaloneIPv6Group
	if err := cluster.DeployDCGM(r.ctx, r.runner, r.stdout, r.clusterCfg, dcgm); err != nil {
		return noK8sGroupCleanup, err
	}
	return func(cleanupCtx context.Context) error {
		return cluster.DeleteDCGM(cleanupCtx, r.runner, r.stdout, r.clusterCfg)
	}, nil
}

// prepareGPUOperatorK8sGroup selects the GPU Operator chart for a group and prepares its installation.
func (r *k8sSuiteRun) prepareGPUOperatorK8sGroup(opts config.TestOptions, group scenario.PlanGroup, featureCfg cluster.FeatureConfig) (func(context.Context) error, error) {
	chartName := ""
	switch group.Name {
	case "gpu-operator-baseline":
		chartName = "gpuOperatorChart"
		if groupHasLabel(group, "gpuOperatorIPv6") {
			chartName = "gpuOperatorIPv6"
		}
	case "gpu-operator-shared-gpu":
		chartName = "gpuOperatorSharedGpu"
	case "gpu-operator-mig":
		chartName = "gpuOperatorMig"
	case "gpu-operator-dra":
		chartName = "gpuOperatorDRA"
	case gpuOperatorMainEmbeddedGroup:
		chartName = "gpuOperatorMainEmbedded"
	case gpuOperatorMainRemoteGroup:
		chartName = "gpuOperatorMainRemote"
	case gpuOperatorMainDRAGroup:
		chartName = "gpuOperatorMainDRA"
	default:
		return noK8sGroupCleanup, nil
	}
	if strings.HasPrefix(group.Name, "gpu-operator-main-") {
		if err := ensureLocalGPUOperatorImageImport(r.ctx, r.stdout, r.runner, r.clusterCfg, opts); err != nil {
			return noK8sGroupCleanup, err
		}
	}
	return cluster.PrepareGPUOperator(r.ctx, r.runner, r.stdout, r.clusterCfg, featureCfg, chartName)
}

func noK8sGroupCleanup(context.Context) error {
	return nil
}

// groupUsesNVSwitch reports whether a runnable group runs NVSwitch scenarios,
// which use CDI mode inside an owned local k3d node.
func groupUsesNVSwitch(group scenario.PlanGroup) bool {
	for _, label := range []string{"nvswitch", "nvswitchLink", "nvswitchThroughput"} {
		if groupHasLabel(group, label) {
			return true
		}
	}
	return false
}

// nvidiaVisibleDevicesForGroup returns the qualified CDI device name used by
// exporter pods while an owned local cluster is in CDI mode.
func nvidiaVisibleDevicesForGroup(clusterCfg cluster.Config, group scenario.PlanGroup) string {
	if clusterCfg.LocalK3D() && groupUsesNVSwitch(group) {
		return "nvidia.com/gpu=all"
	}
	return ""
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

// planHasAnyLabel reports whether the plan selected any named scenario.
func planHasAnyLabel(plan scenario.Plan, labels ...string) bool {
	for _, group := range plan.Groups {
		for _, label := range labels {
			if groupHasLabel(group, label) {
				return true
			}
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

// applyScenarioInputs forwards selected-scenario readiness data to the k8s suite.
func applyScenarioInputs(opts *config.TestOptions, inputs capability.ScenarioInputs) {
	if inputs.MIGInstanceEntityID != "" {
		opts.MIGInstanceEntityID = inputs.MIGInstanceEntityID
	}
	if inputs.MIGInstanceNVMLID != "" {
		opts.MIGInstanceNVMLID = inputs.MIGInstanceNVMLID
	}
	if inputs.UnsupportedFieldCandidate != "" {
		opts.UnsupportedFieldCandidate = inputs.UnsupportedFieldCandidate
	}
	if inputs.UnsupportedFieldEvidence != "" {
		opts.UnsupportedFieldEvidence = inputs.UnsupportedFieldEvidence
	}
}

// clusterConfig derives the cluster package configuration from test command options.
func clusterConfig(root string, opts config.TestOptions) cluster.Config {
	cfg, err := cluster.ConfigForRunName(opts.RunName)
	if err != nil {
		cfg = cluster.DefaultConfig()
	}
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
	if opts.RunName != "" {
		cfg.RunName = opts.RunName
	}
	cfg.Root = root
	cfg.Kubeconfig = opts.Kubeconfig
	if cfg.LocalK3D() {
		cfg.LocalKubeconfig = filepath.Join(root, ".local-e2e", "kubeconfig-"+cfg.ClusterName+".yaml")
	}
	return cfg
}

// localClusterConfig derives k3d-specific configuration for owned local clusters.
func localClusterConfig(root string, cfg cluster.Config, opts config.TestOptions) (cluster.LocalConfig, error) {
	local, err := cluster.DefaultLocalConfig(root, opts)
	if err != nil {
		return cluster.LocalConfig{}, err
	}
	local.ClusterName = cfg.ClusterName
	local.NetworkName = "k3d-" + local.ClusterName
	local.Kubeconfig = cfg.LocalKubeconfig
	local.IPFamily = opts.K3dIPFamily
	local.CgroupV1 = opts.K3dCgroupV1
	if opts.WaitTimeout != "" {
		local.WaitTimeout = opts.WaitTimeout
	}
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
func clusterFeatureConfig(root string, clusterCfg cluster.Config, opts config.TestOptions, registryDockerConfigJSONPath string) cluster.FeatureConfig {
	return cluster.FeatureConfig{
		Root:                         root,
		DRAConfigure:                 opts.DRAConfigure,
		GPUOperator:                  opts.GPUOperator,
		GPUOperatorChart:             opts.GPUOperatorChart,
		GPUOperatorImage:             opts.GPUOperatorImage,
		GPUOperatorDCGMImage:         opts.GPUOperatorDCGMImage,
		SharedGPUConfigure:           opts.SharedGPUConfigure,
		SharedGPUReplicas:            opts.SharedGPUReplicas,
		WaitTimeout:                  opts.WaitTimeout,
		ExporterImage:                k8sGPUOperatorExporterImage(clusterCfg, opts),
		BusyboxImage:                 opts.BusyboxImage,
		Dependencies:                 opts.Dependencies,
		ImagePullSecret:              opts.K8sImagePullSecret,
		RegistryDockerConfigJSONPath: registryDockerConfigJSONPath,
		RetainResources:              !opts.Cleanup,
	}
}

// scenarioGroupResultName converts a scenario group name to its suite-level result marker name.
func scenarioGroupResultName(group string) string {
	return "dcgm_exporter_e2e_group_" + strings.ReplaceAll(group, "-", "_")
}

// runtimeClassFor prefers explicit test options over cluster defaults.
func runtimeClassFor(opts config.TestOptions, cfg cluster.Config) string {
	if opts.K8sRuntimeClass != "" {
		return opts.K8sRuntimeClass
	}
	if cfg.LocalK3D() {
		return cluster.DefaultRuntimeClass()
	}
	return ""
}

// nodeSelectorKeyFor prefers explicit test options over cluster defaults.
func nodeSelectorKeyFor(opts config.TestOptions, cfg cluster.Config) string {
	if opts.K8sNodeSelectorKey != "" {
		return opts.K8sNodeSelectorKey
	}
	if cfg.LocalK3D() {
		return cluster.DefaultNodeSelectorKey()
	}
	return ""
}

// nodeSelectorValueFor prefers explicit test options over cluster defaults.
func nodeSelectorValueFor(opts config.TestOptions, cfg cluster.Config) string {
	if opts.K8sNodeSelectorValue != "" {
		return opts.K8sNodeSelectorValue
	}
	if cfg.LocalK3D() {
		return cluster.DefaultNodeSelectorValue()
	}
	return ""
}

// dcgmAddressFor chooses the remote DCGM service address visible to validation pods.
func dcgmAddressFor(opts config.TestOptions, cfg cluster.Config) string {
	if opts.RemoteDCGM != "" {
		return opts.RemoteDCGM
	}
	return dcgmNameFor(opts) + "." + cfg.DCGMNamespace + ".svc.cluster.local:" + dcgmPortFor(opts)
}

// dcgmNameFor returns the expected standalone DCGM service name.
func dcgmNameFor(opts config.TestOptions) string {
	if opts.DCGMName != "" {
		return opts.DCGMName
	}
	return "dcgm"
}

// dcgmPortFor returns the expected standalone DCGM service port.
func dcgmPortFor(opts config.TestOptions) string {
	if opts.DCGMPort != "" {
		return opts.DCGMPort
	}
	return "5555"
}

// k8sExporterImage returns the reference deployed inside the cluster.
func k8sExporterImage(clusterCfg cluster.Config, opts config.TestOptions) string {
	if clusterCfg.LocalK3D() {
		return localK3dExporterImage(opts)
	}
	return exporterImage(opts)
}

// k8sGPUOperatorExporterImage returns the exporter reference passed to GPU Operator scenarios.
//
// The checked-in dcgm-exporter chart supports a separate image.digest value, so ordinary
// external k8s deployments use the immutable pull ref. The GPU Operator chart has no
// dcgmExporter.digest value; digest pinning must be expressed as version=tag@digest.
// Preserve the full tag-bearing reference for external GPU Operator installs while keeping
// local k3d on the already-imported tag alias.
func k8sGPUOperatorExporterImage(clusterCfg cluster.Config, opts config.TestOptions) string {
	if clusterCfg.LocalK3D() {
		return localK3dExporterImage(opts)
	}
	return completeImage(opts.ExporterImage)
}
