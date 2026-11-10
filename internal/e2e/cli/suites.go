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
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/bootstrap"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/marker"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/mig"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

// runSelectedSuites runs selected suites with a background context.
func runSelectedSuites(stdout io.Writer, root string, runner e2eexec.Runner, cfg config.Config) error {
	return runSelectedSuitesContext(context.Background(), stdout, root, runner, cfg)
}

// runSelectedSuitesContext dispatches selected suites and preserves setup failures across joined errors.
func runSelectedSuitesContext(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, cfg config.Config) error {
	manager := suite.NewTestBinaryManager(root, runner)
	manager.ToolsDir = bootstrap.TestsToolsDir(root, cfg.TestOptions)
	manager.PrebuiltBinaries = cfg.TestOptions.PrebuiltBinaries
	manager.ExporterBinary = cfg.TestOptions.ExporterBinary
	if cfg.TestOptions.BuildOnly {
		return setupFailure(buildSelectedSuites(ctx, stdout, manager, cfg))
	}
	skips := new(skippedSpecs)
	ctx = withSkippedSpecs(ctx, skips)
	var errs []error
	setupFailed := false
	addErr := func(err error) {
		if err == nil {
			return
		}
		errs = append(errs, err)
		var ee exitError
		setupFailed = setupFailed || errors.As(err, &ee) && ee.code == exitSetupFailure
	}
	for _, suiteName := range scenario.AllSuites() {
		if err := ctx.Err(); err != nil {
			addErr(fmt.Errorf("stop suite scheduling: %w", err))
			break
		}
		if !suiteSelected(cfg, suiteName) {
			continue
		}
		switch suiteName {
		case scenario.SuiteStatic:
			addErr(scenarioFailure(runStaticSuite(ctx, stdout, manager, runner, cfg)))
		case scenario.SuiteHost:
			addErr(scenarioFailure(runHostSuite(ctx, stdout, manager, runner, cfg)))
		case scenario.SuiteContainer:
			addErr(scenarioFailure(runContainerSuite(ctx, stdout, manager, runner, cfg)))
		case scenario.SuiteK8s:
			addErr(runK8sSuite(ctx, stdout, root, manager, runner, cfg))
		}
	}
	skips.write(stdout)
	if len(errs) != 0 {
		joined := errors.Join(errs...)
		if setupFailed {
			return setupFailure(joined)
		}
		return scenarioFailure(joined)
	}
	fmt.Fprintln(stdout, "[e2e] Run passed: selected scenarios completed")
	return nil
}

// suiteSelected reports whether catalog selection leaves any scenario for a suite.
func suiteSelected(cfg config.Config, suiteName scenario.Suite) bool {
	return len(scenario.SelectedForSuite(scenario.Catalog, suiteName, cfg)) != 0
}

// buildSelectedSuites compiles or locates every selected suite binary without running it.
func buildSelectedSuites(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, cfg config.Config) error {
	for _, suiteName := range scenario.AllSuites() {
		if len(scenario.SelectedForSuite(scenario.Catalog, suiteName, cfg)) == 0 {
			continue
		}
		binary, err := manager.Ensure(ctx, suiteName, false)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "[e2e] %s binary ready: %s\n", suiteName, binary)
	}
	return nil
}

// runStaticSuite wraps the static suite body with the static result marker.
func runStaticSuite(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Static suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.TestOptions, "dcgm_exporter_e2e_static", func() error {
		return runStaticSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

// runStaticSuiteBody builds the static test regex from catalog selections and executes it.
func runStaticSuiteBody(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteStatic, cfg)
	regex := suite.StaticRegex(selected)
	if regex == "" {
		return nil
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteStatic, false)
	if err != nil {
		return err
	}
	staticDir := manager.Root
	if manager.SourceTreeAvailable() {
		staticDir = filepath.Join(manager.Root, "tests", "static")
	}
	if err := suite.VerifyStaticCatalog(ctx, runner, binary, staticDir, scenario.Catalog); err != nil {
		// A missing working directory makes the start of the test binary fail with
		// an error that blames the binary, so name the directory.
		if info, statErr := os.Stat(staticDir); statErr != nil || !info.IsDir() {
			return fmt.Errorf("the static suite needs its working directory %s, which is missing; run it from a source tree or a bundle that includes it: %w", staticDir, err)
		}
		return err
	}
	writeSection(stdout, "Static scenarios")
	fmt.Fprintln(stdout)
	result := runner.Run(ctx, e2eexec.Command{
		Name:   binary,
		Args:   []string{goTestVerboseFlag, goTestRunFlag, "^(" + regex + ")$"},
		Dir:    staticDir,
		Stdout: stdout,
		Stderr: stdout,
	})
	if result.ExitCode != 0 {
		return fmt.Errorf("static suite failed: %s", result.Stderr)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "[e2e] PASS dcgm_exporter_e2e_static\n")
	return nil
}

// runHostSuite wraps the host integration suite body with the host result marker.
func runHostSuite(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Host suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.TestOptions, "dcgm_exporter_e2e_integration_host", func() error {
		return runHostSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

const (
	goTestVerboseFlag = "-test.v"
	goTestRunFlag     = "-test.run"
)

// runHostSuiteBody runs direct host integration scenarios with transport-specific prerequisites.
func runHostSuiteBody(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) (err error) {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteHost, cfg)
	groups := scenario.GroupSelected(selected)
	if len(groups) == 0 {
		return nil
	}
	if err := checkHostSuitePrerequisites(ctx, stdout, runner, cfg.TestOptions, groups); err != nil {
		return err
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteHost, false)
	if err != nil {
		return err
	}
	hostDefinition, ok := suite.TestBinaryDefinitionFor(scenario.SuiteHost)
	if !ok {
		return fmt.Errorf("host suite definition is missing")
	}
	productBinary, err := manager.EnsureProductBinary(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[e2e] product binary: %s\n", productBinary)

	var fieldSupport []string
	if needsFieldSupport(scenario.SuiteHost, selected) {
		fieldSupport, err = hostFieldSupportEnv(ctx, stdout, manager, runner, cfg.TestOptions)
		if err != nil {
			return err
		}
	}
	// The MIG layout the scenarios declare stays in place for the whole suite.
	restoreMIG, migEnv, err := prepareHostMIGLayout(ctx, stdout, runner, selected)
	if err != nil {
		return err
	}
	defer func() {
		if restoreErr := restoreMIG(context.WithoutCancel(ctx)); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore host MIG layout: %w", restoreErr))
		}
	}()

	run := hostGroupRun{
		stdout:        stdout,
		manager:       manager,
		runner:        runner,
		opts:          cfg.TestOptions,
		binary:        binary,
		testName:      hostDefinition.TestName,
		productBinary: productBinary,
		fieldSupport:  fieldSupport,
		migEnv:        migEnv,
		skips:         skippedSpecsFrom(ctx),
	}
	return run.runGroups(ctx, groups)
}

// runGroups runs the host groups in order until the context ends, and turns
// their outcomes into the suite result.
func (r hostGroupRun) runGroups(ctx context.Context, groups []scenario.PlanGroup) error {
	var errs []error
	ranGroups := 0
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stop host group scheduling: %w", err))
			break
		}
		ran, err := r.run(ctx, group)
		if ran {
			ranGroups++
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return hostSuiteResult(r.stdout, r.opts, errs, ranGroups)
}

// requireHostDCGM skips an implicit host run, or fails an explicit one, when libdcgm is absent.
func requireHostDCGM(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions) error {
	if bootstrap.HostDCGMLibraryAvailable(ctx, runner) {
		return nil
	}
	if hostExplicitlySelected(opts) {
		return fmt.Errorf("host libdcgm.so.4 was not found; explicit host validation cannot run")
	}
	fmt.Fprintln(stdout, "[e2e] host libdcgm.so.4 was not found; skipping direct host integration suite")
	return errSuiteSkipped
}

// hostSuiteResult turns group outcomes into the suite result. A run in which
// every group was skipped as unsupported is a skip, or a failure when explicit.
func hostSuiteResult(stdout io.Writer, opts config.TestOptions, errs []error, ranGroups int) error {
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	if ranGroups == 0 {
		if hostExplicitlySelected(opts) {
			return fmt.Errorf("every selected host group was unsupported; explicit host validation did not run")
		}
		return errSuiteSkipped
	}
	fmt.Fprintf(stdout, "[e2e] PASS dcgm_exporter_e2e_integration_host\n")
	return nil
}

// hostFieldSupportEnv produces the field support evidence for the host suite
// from the host's DCGM, or from the DCGM image when no host hostengine is
// reachable, and stops any hostengine it started before the suite runs. The
// DCGM image stands in for the host only when it has the host's DCGM version.
func hostFieldSupportEnv(
	ctx context.Context,
	stdout io.Writer,
	manager suite.TestBinaryManager,
	runner e2eexec.Runner,
	opts config.TestOptions,
) ([]string, error) {
	// The host exporter runs as the invoking user, which may lack the
	// privilege the probe has, so profiling rows are never required.
	run := fieldSupportRun{stdout: stdout, root: manager.Root, toolsDir: manager.ToolsDir, runner: runner, withoutProfiling: true}

	// Evidence the caller already set needs no probe hostengine.
	if fieldSupportPreset() {
		return run.env(ctx, capability.ProbeOptions{}), nil
	}

	address, cleanup, err := bootstrap.PrepareHostDCGMProbe(ctx, stdout, runner, opts)
	if err != nil {
		fmt.Fprintf(stdout, "[e2e] DCGM field support: %v; probing with the DCGM image instead\n", err)
		run.wantDCGMVersion = func(ctx context.Context) (string, error) { return hostDCGMVersion(ctx, runner) }
		return run.env(ctx, capability.ProbeOptions{DCGMImage: dcgmImage(opts), DockerEnv: dockerConfigEnv(opts)}), nil
	}

	env := run.env(ctx, capability.ProbeOptions{HostDCGMAddress: address})

	// Stop the probe hostengine before the native tests start their embedded engine.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	if err := cleanup(cleanupCtx); err != nil {
		return nil, fmt.Errorf("clean up the DCGM field support probe: %w", err)
	}
	return env, nil
}

// hostGroupRun holds the resolved binaries and options shared by every host group in one suite run.
type hostGroupRun struct {
	stdout        io.Writer
	manager       suite.TestBinaryManager
	runner        e2eexec.Runner
	opts          config.TestOptions
	binary        string
	testName      string
	productBinary string
	// fieldSupport is the E2E_DCGM_FIELD_SUPPORT entry for groups on the host's own GPUs.
	fieldSupport []string
	// migEnv names the MIG layout applied for the suite, if any.
	migEnv []string
	// skips collects the specs each group skipped, for the end-of-run listing.
	skips *skippedSpecs
}

// run executes one host dependency group. It reports whether the group's test
// binary ran; an unsupported group is skipped with per-scenario markers instead.
func (r hostGroupRun) run(ctx context.Context, group scenario.PlanGroup) (bool, error) {
	section := "Host scenarios"
	if group.Name == scenario.GroupHostRemoteDCGM || group.Name == scenario.GroupHostVSOCK {
		section = "Host DCGM URI scenarios"
	}
	writeSection(r.stdout, section)
	writeVerbose(r.stdout, r.opts.Verbose, "dependency group: %s", group.Name)
	fmt.Fprintln(r.stdout)
	groupArgs, groupEnv, err := prepareHostGroupRuntime(ctx, r.stdout, r.manager, r.runner, r.opts, group.Name)
	if errors.Is(err, errSuiteSkipped) {
		reason := strings.TrimPrefix(err.Error(), errSuiteSkipped.Error()+": ")
		return false, r.skipGroup(group, reason)
	}
	if err != nil {
		return false, fmt.Errorf("host group %s setup failed: %w", group.Name, err)
	}
	if hostGroupUsesFieldSupport(group.Name) {
		groupEnv = append(groupEnv, r.fieldSupport...)
	}
	groupEnv = append(groupEnv, r.migEnv...)
	reportPath, removeReport, err := newGroupReportPath(string(scenario.SuiteHost), group.Name)
	if err != nil {
		return false, err
	}
	defer removeReport()

	testArgs := append([]string{"-exporter-binary=" + r.productBinary}, ginkgoGroupArgs(r.testName, group.Labels, reportPath)...)
	result := r.runner.Run(ctx, e2eexec.Command{
		Name:   r.binary,
		Args:   append(testArgs, groupArgs...),
		Env:    groupEnv,
		Dir:    filepath.Join(r.manager.Root, "tests", "host"),
		Stdout: r.stdout,
		Stderr: r.stdout,
	})
	fmt.Fprintln(r.stdout)

	return true, finishGroup(string(scenario.SuiteHost), group.Name, reportPath, result, r.skips)
}

// prepareHostLayout applies the MIG layout of the selected host scenarios;
// tests replace it to avoid changing the host.
var prepareHostLayout = mig.PrepareHostLayout

// prepareHostMIGLayout applies the MIG layout the selected scenarios declare
// and returns its cleanup and the environment for their tests. A host that
// cannot take the layout skips the suite with the reason.
func prepareHostMIGLayout(
	ctx context.Context, stdout io.Writer, runner e2eexec.Runner, selected []scenario.Scenario,
) (func(context.Context) error, []string, error) {
	cleanup, env, err := prepareHostLayout(ctx, stdout, runner, selected)
	if errors.Is(err, mig.ErrLayoutUnsupported) {
		fmt.Fprintf(stdout, "[e2e] %v; skipping the host scenarios that need it\n", err)
		return cleanup, nil, fmt.Errorf("%w: %v", errSuiteSkipped, err)
	}
	return cleanup, env, err
}

// hostGroupUsesFieldSupport reports whether a host group runs the exporter on
// the host's own GPUs with no injected or mocked DCGM, so the field support
// evidence describes its GPUs.
func hostGroupUsesFieldSupport(name string) bool {
	return name == scenario.GroupHostBaseline || name == scenario.GroupHostDefaultPreset
}

// skipGroup reports an unsupported host group and, when spec markers are on, its scenarios.
func (r hostGroupRun) skipGroup(group scenario.PlanGroup, reason string) error {
	fmt.Fprintf(r.stdout, "[e2e] host group %s is unsupported: %s; skipping\n", group.Name, reason)
	if !specMarkersEnabled(r.opts) {
		return nil
	}
	return emitSkippedHostMarkers(marker.NewReporter(r.stdout), group.Labels)
}

// emitSkippedHostMarkers reports each scenario of a host group that the CLI
// skipped before its test binary could emit per-scenario markers.
func emitSkippedHostMarkers(reporter marker.Reporter, labels []string) error {
	names := scenario.MarkerBaseNames(scenario.Catalog, scenario.SuiteHost)
	for _, label := range labels {
		name, ok := names[label]
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

// checkHostSuitePrerequisites validates selected host fixtures and checks that DCGM is available.
func checkHostSuitePrerequisites(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, groups []scenario.PlanGroup) error {
	if err := validateHostGroupInputs(groups); err != nil {
		return err
	}
	return requireHostDCGM(ctx, stdout, runner, opts)
}

const hostDCGMHostengineAddressEnv = "E2E_DCGM_HOSTENGINE_ADDRESS"

// validateHostGroupInputs checks manually selected host fixtures before suite setup.
func validateHostGroupInputs(groups []scenario.PlanGroup) error {
	for _, group := range groups {
		if group.Name != scenario.GroupHostGPULinkFields {
			continue
		}
		if strings.TrimSpace(os.Getenv(hostDCGMHostengineAddressEnv)) == "" {
			return fmt.Errorf("host/gpuLinkFields requires %s to name the private DCGM hostengine shared by the probe and exporter", hostDCGMHostengineAddressEnv)
		}
	}
	return nil
}

// prepareHostGroupRuntime returns only the probe arguments and environment
// owned by one host dependency group.
func prepareHostGroupRuntime(
	ctx context.Context,
	stdout io.Writer,
	manager suite.TestBinaryManager,
	runner e2eexec.Runner,
	opts config.TestOptions,
	groupName string,
) ([]string, []string, error) {
	env := []string{fmt.Sprintf("E2E_SUITE_RESULT_MARKERS=%t", specMarkersEnabled(opts))}
	switch groupName {
	case scenario.GroupHostRemoteDCGM:
		env = append([]string{"E2E_REQUIRE_DCGM=1"}, env...)
	case scenario.GroupHostVSOCK:
		env = append([]string{"E2E_REQUIRE_DCGM=1"}, env...)
		if requireVSOCK(opts) {
			env = append([]string{"E2E_REQUIRE_VSOCK=1"}, env...)
		}
	case scenario.GroupHostProfiling:
		if err := prepareHostProfiling(ctx, stdout, runner, opts); err != nil {
			return nil, nil, err
		}
		env = append(env, "E2E_REQUIRE_PROFILING=1")
	case scenario.GroupHostConfigInjection:
		return configInjectionGroupRuntime(ctx, manager, runner, opts, env)
	case scenario.GroupHostNVMLInjection:
		env, err := withNVMLInjectionEnv(ctx, runner, opts, env)
		if err != nil {
			return nil, nil, err
		}
		probeArgs, err := hostDCGMProbeArgs(ctx, manager, runner)
		if err != nil {
			return nil, nil, err
		}
		return probeArgs, env, nil
	case scenario.GroupHostNVSDMMock:
		probeArgs, err := hostDCGMProbeArgs(ctx, manager, runner)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, "DCGM_NVSDM_MOCK_YAML="+opts.DCGMNVSDMMockYAML)
		return probeArgs, env, nil
	case scenario.GroupHostGPULinkFields:
		probeArgs, err := hostDCGMProbeArgs(ctx, manager, runner)
		if err != nil {
			return nil, nil, err
		}
		return probeArgs, env, nil
	case scenario.GroupHostGPUBindUnbind:
		// The group is planned only when the scenario is named, so this is
		// the explicit request the test requires before it unbinds a GPU.
		env = append(env, "E2E_ALLOW_GPU_UNBIND=1")
	case scenario.GroupHostGPULoad:
		// The load runs dcgmproftester from the DCGM image, pulled with the
		// session's registry credentials.
		env = append(env, "DCGM_IMAGE="+dcgmImage(opts))
		env = append(env, dockerConfigEnv(opts)...)
	}
	return nil, env, nil
}

// configInjectionGroupRuntime returns the probe arguments and environment
// for the config injection group. Its scenarios run on the host's own GPUs
// unless an injection fixture is given, so the GPU job and the CPU job share
// them.
func configInjectionGroupRuntime(
	ctx context.Context,
	manager suite.TestBinaryManager,
	runner e2eexec.Runner,
	opts config.TestOptions,
	env []string,
) ([]string, []string, error) {
	probeArgs, err := hostDCGMProbeArgs(ctx, manager, runner)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(opts.DCGMNVMLInjectionYAML) == "" {
		return probeArgs, env, nil
	}

	env, err = withNVMLInjectionEnv(ctx, runner, opts, env)
	return probeArgs, env, err
}

// withNVMLInjectionEnv checks that the NVML injection library is installed and
// adds the variables that turn injection on to env.
func withNVMLInjectionEnv(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions, env []string) ([]string, error) {
	if !bootstrap.HostDCGMInjectionLibraryAvailable(ctx, runner) {
		return nil, fmt.Errorf("DCGM NVML injection was requested but libnvml_injection.so was not found")
	}
	return append(env, "NVML_INJECTION_MODE=True", "NVML_YAML_FILE="+opts.DCGMNVMLInjectionYAML), nil
}

// prepareHostProfiling checks DCGM profiling support and stops any hostengine started for the probe.
func prepareHostProfiling(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions) error {
	address, cleanup, err := bootstrap.PrepareHostDCGMProbe(ctx, stdout, runner, opts)
	if err != nil {
		return err
	}
	profiling := capability.ProbeHost(ctx, runner, capability.ProbeOptions{HostDCGMAddress: address}).Lookup("host:profiling")
	// Stop the probe hostengine before the native test starts its embedded engine.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterInfoTimeout)
	defer cancel()
	if err := cleanup(cleanupCtx); err != nil {
		return fmt.Errorf("clean up host profiling probe: %w", err)
	}
	return profilingGroupRuntime(profiling)
}

// profilingGroupRuntime maps the independent DCGM capability result to a host profiling run decision.
func profilingGroupRuntime(profiling capability.Capability) error {
	switch profiling.Status {
	case capability.StatusSupported:
		return nil
	case capability.StatusUnsupported:
		return fmt.Errorf("%w: %s", errSuiteSkipped, profiling.Reason)
	default:
		return fmt.Errorf("could not determine DCGM profiling support: %s", profiling.Reason)
	}
}

// hostDCGMProbeArgs resolves the probe binary and field definitions used by
// host groups that compare exporter output with direct DCGM reads.
func hostDCGMProbeArgs(ctx context.Context, manager suite.TestBinaryManager, runner e2eexec.Runner) ([]string, error) {
	probeBinary, err := manager.EnsureDCGMProbeBinary(ctx)
	if err != nil {
		return nil, err
	}
	fieldsSource, err := dcgmFieldsSource(ctx, manager.Root, manager.ToolsDir, runner)
	if err != nil {
		return nil, err
	}
	return []string{
		"-dcgm-probe-binary=" + probeBinary,
		"-dcgm-fields-file=" + fieldsSource,
	}, nil
}

// hostExplicitlySelected distinguishes user-requested host coverage from the default best-effort host suite.
func hostExplicitlySelected(opts config.TestOptions) bool {
	for _, suiteName := range opts.Suites {
		if suiteName == string(scenario.SuiteHost) {
			return true
		}
	}
	for _, selector := range opts.Scenarios {
		if strings.HasPrefix(selector, string(scenario.SuiteHost)+"/") {
			return true
		}
	}
	return false
}

// runContainerSuite wraps the container integration suite body with the container result marker.
func runContainerSuite(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Container suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.TestOptions, "dcgm_exporter_e2e_integration_container", func() error {
		return runContainerSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

// requireVSOCK reports whether VSOCK specs must fail, not skip, when VSOCK is missing.
// Only vsock=skip leaves them free to skip.
func requireVSOCK(opts config.TestOptions) bool {
	return opts.BootstrapModeFor(config.BootstrapVSOCK) != config.BootstrapSkip
}

// containerGroupNeedsVSOCK reports whether the container group must fail, not skip, its
// VSOCK specs: a selected scenario in the group requires VSOCK and the vsock bootstrap
// component is not skipped.
func containerGroupNeedsVSOCK(selected []scenario.Scenario, groupName string, opts config.TestOptions) bool {
	if !requireVSOCK(opts) {
		return false
	}

	for _, entry := range selected {
		if entry.Group != groupName {
			continue
		}
		for _, requirement := range entry.AllRequirements() {
			if requirement.Kind == scenario.RequirementVSOCK {
				return true
			}
		}
	}
	return false
}

// containerSuiteEnv returns the environment every container group gets: the
// exporter images, Docker registry settings and, when a selected scenario needs
// it, the field-support evidence from the DCGM image.
func containerSuiteEnv(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config, selected []scenario.Scenario) []string {
	env := []string{
		"E2E_REQUIRE_CONTAINER_IMAGES=1",
		fmt.Sprintf("E2E_SUITE_RESULT_MARKERS=%t", specMarkersEnabled(cfg.TestOptions)),
	}
	if image := exporterImage(cfg.TestOptions); image != "" {
		env = append(env, "EXPORTER_DISTROLESS_IMAGE="+image)
	}
	env = append(env, dockerConfigEnv(cfg.TestOptions)...)
	if !needsFieldSupport(scenario.SuiteContainer, selected) {
		return env
	}

	// The DCGM image runs on the Docker host that runs the exporter image.
	// Its evidence holds only for an exporter image with the same DCGM version.
	run := fieldSupportRun{stdout: stdout, root: manager.Root, toolsDir: manager.ToolsDir, runner: runner}
	run.wantDCGMVersion = wantExporterDCGMVersion(stdout, runner, cfg.TestOptions, exporterImage(cfg.TestOptions))

	return append(env, run.env(ctx, capability.ProbeOptions{
		DCGMImage: dcgmImage(cfg.TestOptions),
		DockerEnv: dockerConfigEnv(cfg.TestOptions),
	})...)
}

// containerGroupEnv returns a copy of the suite environment with the settings
// one group adds: the DCGM image for remote-DCGM scenarios and the VSOCK
// requirement when the group must fail rather than skip without VSOCK.
func containerGroupEnv(env []string, selected []scenario.Scenario, groupName string, opts config.TestOptions) []string {
	groupEnv := append([]string(nil), env...)
	if groupName == scenario.GroupContainerRemoteDCGM {
		groupEnv = append(groupEnv, "DCGM_IMAGE="+dcgmImage(opts))
	}
	if containerGroupNeedsVSOCK(selected, groupName, opts) {
		groupEnv = append(groupEnv, "E2E_REQUIRE_VSOCK=1")
	}

	return groupEnv
}

// runContainerSuiteBody prepares image references and registry auth before running Docker-image scenarios.
func runContainerSuiteBody(ctx context.Context, stdout io.Writer, manager suite.TestBinaryManager, runner e2eexec.Runner, cfg config.Config) error {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteContainer, cfg)
	groups := scenario.GroupSelected(selected)
	if len(groups) == 0 {
		return nil
	}
	cleanupDockerConfig, _, err := prepareDockerRegistryLogin(ctx, stdout, runner, &cfg.TestOptions)
	if err != nil {
		return err
	}
	defer cleanupDockerConfig()
	binary, err := manager.Ensure(ctx, scenario.SuiteContainer, false)
	if err != nil {
		return err
	}
	containerDefinition, ok := suite.TestBinaryDefinitionFor(scenario.SuiteContainer)
	if !ok {
		return fmt.Errorf("container suite definition is missing")
	}
	env := containerSuiteEnv(ctx, stdout, manager, runner, cfg, selected)

	var errs []error
	run := containerGroupRun{
		stdout:   stdout,
		runner:   runner,
		binary:   binary,
		testName: containerDefinition.TestName,
		dir:      manager.Root,
		skips:    skippedSpecsFrom(ctx),
	}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stop container group scheduling: %w", err))
			break
		}
		writeSection(stdout, "Container scenarios")
		writeVerbose(stdout, cfg.TestOptions.Verbose, "dependency group: %s", group.Name)
		fmt.Fprintln(stdout)
		groupEnv := containerGroupEnv(env, selected, group.Name, cfg.TestOptions)
		if err := run.run(ctx, group, groupEnv); err != nil {
			errs = append(errs, err)
		}
		fmt.Fprintln(stdout)
	}
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintf(stdout, "[e2e] PASS dcgm_exporter_e2e_integration_container\n")
	return nil
}

// containerGroupRun holds what every container group of one suite run shares.
type containerGroupRun struct {
	stdout   io.Writer
	runner   e2eexec.Runner
	binary   string
	testName string
	dir      string
	// skips collects the specs each group skipped, for the end-of-run listing.
	skips *skippedSpecs
}

// run executes the container test binary for one group with a Ginkgo JSON
// report. It returns an error built from the report when the binary exits non-zero.
func (r containerGroupRun) run(ctx context.Context, group scenario.PlanGroup, env []string) error {
	reportPath, removeReport, err := newGroupReportPath(string(scenario.SuiteContainer), group.Name)
	if err != nil {
		return err
	}
	defer removeReport()

	result := r.runner.Run(ctx, e2eexec.Command{
		Name:   r.binary,
		Args:   ginkgoGroupArgs(r.testName, group.Labels, reportPath),
		Env:    env,
		Dir:    r.dir,
		Stdout: r.stdout,
		Stderr: r.stdout,
	})

	return finishGroup(string(scenario.SuiteContainer), group.Name, reportPath, result, r.skips)
}

// runMarkedSuite emits lifecycle markers around a whole suite when marker output is enabled.
func runMarkedSuite(stdout io.Writer, opts config.TestOptions, name string, run func() error) error {
	if !suiteMarkersEnabled(opts) {
		if err := run(); errors.Is(err, errSuiteSkipped) {
			return nil
		} else {
			return err
		}
	}
	reporter := marker.NewReporter(stdout)
	if err := reporter.Emit(marker.StatusRunning, name); err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	err := run()
	fmt.Fprintln(stdout)
	if errors.Is(err, errSuiteSkipped) {
		err = reporter.Emit(marker.StatusSkipped, name)
		fmt.Fprintln(stdout)
		return err
	}
	if err != nil {
		_ = reporter.Emit(marker.StatusFailed, name)
		fmt.Fprintln(stdout)
		return err
	}
	err = reporter.Emit(marker.StatusPassed, name)
	fmt.Fprintln(stdout)
	return err
}

// suiteMarkersEnabled applies the positive and negative marker flags in one place.
func suiteMarkersEnabled(opts config.TestOptions) bool {
	return opts.ResultMarkers && !opts.NoResultMarkers
}

// specMarkersEnabled applies the positive and negative flags to Ginkgo markers.
func specMarkersEnabled(opts config.TestOptions) bool {
	return opts.E2EResultMarkers && !opts.NoResultMarkers
}
