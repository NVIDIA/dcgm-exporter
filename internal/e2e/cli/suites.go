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

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/installdeps"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/marker"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

var hostDCGMURIScenario = scenario.MustFind(scenario.Catalog, scenario.SuiteHost, "dcgmUri")

// runSelectedSuites runs selected suites with a background context.
func runSelectedSuites(stdout io.Writer, root string, runner e2eexec.Runner, cfg config.Config) error {
	return runSelectedSuitesContext(context.Background(), stdout, root, runner, cfg)
}

// runSelectedSuitesContext dispatches selected suites and preserves setup failures across joined errors.
func runSelectedSuitesContext(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, cfg config.Config) error {
	manager := suite.NewManager(root, runner)
	manager.ToolsDir = installdeps.TestsToolsDir(root, cfg.Tests)
	if cfg.Tests.BuildOnly {
		return setupFailure(buildSelectedSuites(ctx, stdout, manager, cfg))
	}
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
	if suiteSelected(cfg, scenario.SuiteK8s) {
		addErr(runK8sSuite(ctx, stdout, root, manager, runner, cfg))
	}
	if suiteSelected(cfg, scenario.SuiteStatic) {
		addErr(scenarioFailure(runStaticSuite(ctx, stdout, manager, runner, cfg)))
	}
	if suiteSelected(cfg, scenario.SuiteHost) {
		addErr(scenarioFailure(runHostSuite(ctx, stdout, manager, runner, cfg)))
	}
	if suiteSelected(cfg, scenario.SuiteContainer) {
		addErr(scenarioFailure(runContainerSuite(ctx, stdout, manager, runner, cfg)))
	}
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
func buildSelectedSuites(ctx context.Context, stdout io.Writer, manager suite.Manager, cfg config.Config) error {
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
func runStaticSuite(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Static suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.Tests, "dcgm_exporter_e2e_static", func() error {
		return runStaticSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

// runStaticSuiteBody builds the static test regex from catalog selections and executes it.
func runStaticSuiteBody(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteStatic, cfg)
	regex := suite.StaticRegex(selected)
	if regex == "" {
		return nil
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteStatic, false)
	if err != nil {
		return err
	}
	writeSection(stdout, "Static scenarios")
	fmt.Fprintln(stdout)
	result := runner.Run(ctx, e2eexec.Command{
		Name:   binary,
		Args:   []string{"-test.v", "-test.run", "^(" + regex + ")$"},
		Dir:    manager.Root,
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
func runHostSuite(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Host suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.Tests, "dcgm_exporter_e2e_integration_host", func() error {
		return runHostSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

// runHostSuiteBody runs direct host integration scenarios and isolates the VSOCK/DCGM URI case.
func runHostSuiteBody(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteHost, cfg)
	labels := suite.LabelFilter(selected)
	injection := cfg.Tests.DCGMNVMLInjectionYAML != ""
	if labels == "" {
		return nil
	}
	if !installdeps.HostDCGMLibraryAvailable(ctx, runner) {
		if hostExplicitlySelected(cfg.Tests) {
			return fmt.Errorf("host libdcgm.so.4 was not found; explicit host validation cannot run")
		}
		fmt.Fprintln(stdout, "[e2e] host libdcgm.so.4 was not found; skipping direct host integration suite")
		return errSuiteSkipped
	}
	if injection && !installdeps.HostDCGMInjectionLibraryAvailable(ctx, runner) {
		return fmt.Errorf("DCGM NVML injection was requested but libnvml_injection.so was not found")
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteHost, false)
	if err != nil {
		return err
	}
	productBinary, err := manager.EnsureProductBinary(ctx)
	if err != nil {
		return err
	}
	var injectionArgs []string
	if injection {
		probeBinary, err := manager.EnsureDCGMProbeBinary(ctx)
		if err != nil {
			return err
		}
		fieldsSource, err := dcgmFieldsSource(ctx, manager.Root, runner)
		if err != nil {
			return err
		}
		injectionArgs = []string{
			"-dcgm-probe-binary=" + probeBinary,
			"-dcgm-fields-file=" + fieldsSource,
		}
	}
	hostEnv := []string{fmt.Sprintf("E2E_SUITE_RESULT_MARKERS=%t", specMarkersEnabled(cfg.Tests))}
	if injection {
		hostEnv = append(
			hostEnv,
			"NVML_INJECTION_MODE=True",
			"NVML_YAML_FILE="+cfg.Tests.DCGMNVMLInjectionYAML,
		)
		if expectedDevices := strings.TrimSpace(os.Getenv("E2E_DCGM_EXPECT_DEVICES")); expectedDevices != "" {
			hostEnv = append(hostEnv, "E2E_DCGM_EXPECT_DEVICES="+expectedDevices)
		}
	}
	runLabels := labels
	switch {
	case injection:
		runLabels = "(" + labels + ") && !" + hostDCGMURIScenario.ResultName + " && !profiling"
	case labels == hostDCGMURIScenario.ResultName:
		runLabels = ""
	case hostLabelFilterIncludesDCGMURI(labels):
		runLabels = "(" + labels + ") && !" + hostDCGMURIScenario.ResultName
	}
	if runLabels != "" {
		writeSection(stdout, "Host scenarios")
		fmt.Fprintln(stdout)
		testArgs := []string{
			"-test.v",
			"-test.run", "^TestHostIntegration$",
			"-exporter-binary=" + productBinary,
			"--ginkgo.v",
			"--ginkgo.no-color",
			"--ginkgo.label-filter=" + runLabels,
		}
		testArgs = append(testArgs, injectionArgs...)
		result := runner.Run(ctx, e2eexec.Command{
			Name:   binary,
			Args:   testArgs,
			Env:    hostEnv,
			Dir:    filepath.Join(manager.Root, "tests", "host"),
			Stdout: stdout,
			Stderr: stdout,
		})
		if result.ExitCode != 0 {
			return fmt.Errorf("host suite failed: %s", result.Stderr)
		}
		fmt.Fprintln(stdout)
	}
	if !injection && hostLabelFilterIncludesDCGMURI(labels) {
		writeSection(stdout, "Host DCGM URI scenarios")
		fmt.Fprintln(stdout)
		result := runner.Run(ctx, e2eexec.Command{
			Name: binary,
			Args: []string{
				"-test.v",
				"-test.run", "^TestHostIntegration$",
				"-exporter-binary=" + productBinary,
				"--ginkgo.v",
				"--ginkgo.no-color",
				"--ginkgo.label-filter=" + hostDCGMURIScenario.ResultName,
			},
			Env:    append([]string{"E2E_REQUIRE_VSOCK=1", "E2E_REQUIRE_DCGM=1"}, hostEnv...),
			Dir:    filepath.Join(manager.Root, "tests", "host"),
			Stdout: stdout,
			Stderr: stdout,
		})
		if result.ExitCode != 0 {
			return fmt.Errorf("host suite failed: %s", result.Stderr)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "[e2e] PASS dcgm_exporter_e2e_integration_host\n")
	return nil
}

// hostLabelFilterIncludesDCGMURI detects whether the composed Ginkgo label filter includes the DCGM URI scenario.
func hostLabelFilterIncludesDCGMURI(labels string) bool {
	for _, part := range strings.Split(labels, "||") {
		if strings.TrimSpace(strings.Trim(part, "()")) == hostDCGMURIScenario.ResultName {
			return true
		}
	}
	return labels == hostDCGMURIScenario.ResultName || strings.Contains(labels, hostDCGMURIScenario.ResultName)
}

// hostExplicitlySelected distinguishes user-requested host coverage from the default best-effort host suite.
func hostExplicitlySelected(opts config.Tests) bool {
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
func runContainerSuite(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	writeSection(stdout, "Container suite")
	fmt.Fprintln(stdout)
	return runMarkedSuite(stdout, cfg.Tests, "dcgm_exporter_e2e_integration_container", func() error {
		return runContainerSuiteBody(ctx, stdout, manager, runner, cfg)
	})
}

// runContainerSuiteBody prepares image references and registry auth before running Docker-image scenarios.
func runContainerSuiteBody(ctx context.Context, stdout io.Writer, manager suite.Manager, runner e2eexec.Runner, cfg config.Config) error {
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteContainer, cfg)
	labels := suite.LabelFilter(selected)
	if labels == "" {
		return nil
	}
	needsDCGMImage := containerRemoteDCGMImageNeeded(cfg.Tests)
	dcgmImageRef := ""
	if needsDCGMImage {
		dcgmImageRef = dcgmImage(cfg.Tests)
		if err := validateRequiredImage("--dcgm-image", dcgmImageRef); err != nil {
			return err
		}
	}
	cleanupDockerConfig, _, err := prepareDockerRegistryLogin(ctx, stdout, runner, &cfg.Tests)
	if err != nil {
		return err
	}
	defer cleanupDockerConfig()
	if needsDCGMImage {
		if err := ensureDCGMImageAvailable(ctx, runner, cfg.Tests); err != nil {
			return err
		}
	}
	binary, err := manager.Ensure(ctx, scenario.SuiteContainer, false)
	if err != nil {
		return err
	}
	env := []string{
		"E2E_REQUIRE_CONTAINER_IMAGES=1",
		fmt.Sprintf("E2E_SUITE_RESULT_MARKERS=%t", specMarkersEnabled(cfg.Tests)),
	}
	if image := exporterImage(cfg.Tests); image != "" {
		env = append(env, "EXPORTER_DISTROLESS_IMAGE="+image)
	}
	env = append(env, "EXPORTER_UBUNTU_IMAGE="+exporterUbuntuImage(cfg.Tests))
	if dcgmImageRef != "" {
		env = append(env, "DCGM_IMAGE="+dcgmImageRef)
	}
	env = append(env, dockerConfigEnv(cfg.Tests)...)
	writeSection(stdout, "Container scenarios")
	fmt.Fprintln(stdout)
	result := runner.Run(ctx, e2eexec.Command{
		Name: binary,
		Args: []string{
			"-test.v",
			"-test.run", "^TestDockerImages$",
			"--ginkgo.v",
			"--ginkgo.no-color",
			"--ginkgo.label-filter=" + labels,
		},
		Env:    env,
		Dir:    manager.Root,
		Stdout: stdout,
		Stderr: stdout,
	})
	if result.ExitCode != 0 {
		return fmt.Errorf("container suite failed: %s", result.Stderr)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "[e2e] PASS dcgm_exporter_e2e_integration_container\n")
	return nil
}

// runMarkedSuite emits lifecycle markers around a whole suite when marker output is enabled.
func runMarkedSuite(stdout io.Writer, opts config.Tests, name string, run func() error) error {
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
func suiteMarkersEnabled(opts config.Tests) bool {
	return opts.ResultMarkers && !opts.NoResultMarkers
}

// specMarkersEnabled applies the positive and negative flags to Ginkgo markers.
func specMarkersEnabled(opts config.Tests) bool {
	return opts.E2EResultMarkers && !opts.NoResultMarkers
}
