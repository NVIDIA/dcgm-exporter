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

// Package mig prepares and restores host MIG state for e2e cluster runs.
package mig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

const (
	// nvidiaSMI is the command that reads and changes MIG state.
	nvidiaSMI          = "nvidia-smi"
	defaultMIGGPUIndex = "0"
	migRollbackTimeout = 2 * time.Minute
	migLockPath        = "/run/dcgm-exporter-e2e/mig-gpu-0.lock"
)

var (
	migProfileIDRE      = regexp.MustCompile(`\b[0-9]+\b`)
	errMIGRequiresLinux = errors.New("MIG host configuration requires Linux file locking")
)

// PrepareHostBeforeCluster optionally creates MIG instances before cluster setup and returns a restore hook.
func PrepareHostBeforeCluster(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions) (func(context.Context) error, error) {
	return prepareHostBeforeCluster(ctx, stdout, runner, opts, migLockPath, 0)
}

// prepareHostBeforeCluster holds GPU ownership until setup rollback or returned cleanup finishes.
func prepareHostBeforeCluster(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, lockPath string, lockUID int) (cleanup func(context.Context) error, err error) {
	if !migPreconfigureRequested(opts) {
		return func(context.Context) error { return nil }, nil
	}
	if runtime.GOOS != "linux" {
		return func(context.Context) error { return nil }, errMIGRequiresLinux
	}
	profile := migConfigurationProfile(opts)
	profileID, err := selectMIGProfileID(ctx, runner, profile)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}
	if profileID == "" {
		fmt.Fprintln(stdout, "[e2e] MIG configuration requested, but no MIG profiles were detected; continuing without host MIG mutation")
		return func(context.Context) error { return nil }, nil
	}
	release, err := acquireMIGLock(ctx, stdout, runner, lockPath, lockUID)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, release())
		}
	}()
	previousMode, err := migCurrentMode(ctx, runner)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}
	cdiSpec, err := saveCDISpec(ctx, runner)
	if err != nil {
		return func(context.Context) error { return nil }, err
	}
	if err := configureMIGInstances(ctx, stdout, runner, profileID, previousMode, cdiSpec); err != nil {
		return func(context.Context) error { return nil }, err
	}
	var once sync.Once
	var cleanupErr error
	cleanup = func(cleanupCtx context.Context) error {
		once.Do(func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(cleanupCtx), migRollbackTimeout)
			defer cancel()
			cleanupErr = errors.Join(restoreMIGInstances(restoreCtx, stdout, runner, previousMode, cdiSpec), release())
		})
		return cleanupErr
	}
	return cleanup, nil
}

// migPreconfigureRequested reports whether selected k8s scenarios require host MIG mutation.
func migPreconfigureRequested(opts config.TestOptions) bool {
	if !MutationRequested(opts.MIGConfigure) {
		return false
	}
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, config.Config{TestOptions: opts})
	for _, entry := range selected {
		if entry.UsesKubernetesAllocation(scenario.KubernetesAllocationMIG, scenario.KubernetesAllocationMixedMIG) {
			return true
		}
	}
	return false
}

// MutationRequested treats explicit true-like MIG modes as permission to mutate the host.
// --mig-configure is not a config.FeatureMode: its value can also be a profile
// ID, and its auto must never change MIG on the host.
func MutationRequested(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// firstMIGProfile asks selectMIGProfileID for the first profile GPU 0 lists.
const firstMIGProfile = "first"

// migConfigurationProfile turns a MIG change request (--mig-configure true or a
// profile ID) into firstMIGProfile or that profile ID.
func migConfigurationProfile(opts config.TestOptions) string {
	value := strings.TrimSpace(opts.MIGConfigure)
	if value == "" || strings.EqualFold(value, "true") {
		return firstMIGProfile
	}
	return value
}

// configureMIGInstances creates one MIG GPU instance after refusing unsafe active or preexisting layouts.
// It regenerates the host CDI spec so pods see the MIG device; a failure rolls back MIG and puts
// cdiSpec back.
func configureMIGInstances(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, profileID, previousMode string, cdiSpec cdiSpecState) (err error) {
	if err := refuseUnsafeMIGChange(ctx, runner); err != nil {
		return err
	}
	// A failed command can still change MIG state. Roll back only after the safety checks pass.
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), migRollbackTimeout)
		defer cancel()
		if cleanupErr := restoreMIGInstances(cleanupCtx, stdout, runner, previousMode, cdiSpec); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("restore MIG state after failed configuration: %w", cleanupErr))
		}
	}()
	fmt.Fprintln(stdout, "[e2e] Configuring MIG on GPU "+defaultMIGGPUIndex+" with profile "+profileID)
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_smi_enable_mig", sudoCommand(nvidiaSMI, "-i", defaultMIGGPUIndex, "-mig", "1")); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_smi_create_mig_instance", sudoCommand(nvidiaSMI, "mig", "-i", defaultMIGGPUIndex, "-cgi", profileID, "-C")); err != nil {
		return err
	}
	return regenerateNvidiaCDISpec(ctx, stdout, runner)
}

// refuseUnsafeMIGChange fails while GPU compute processes run or MIG instances
// already exist, because changing MIG would disturb them and restoring would
// delete instances the run did not create.
func refuseUnsafeMIGChange(ctx context.Context, runner e2eexec.Runner) error {
	processes, err := runCommandText(ctx, runner, nvidiaSMI, "--query-compute-apps=pid,gpu_uuid,process_name", "--format=csv,noheader")
	if err != nil {
		return fmt.Errorf("query active GPU compute processes before MIG configuration: %w", err)
	}
	if processes := strings.TrimSpace(processes); processes != "" {
		return fmt.Errorf("refusing to configure MIG while GPU compute processes are active:\n%s", processes)
	}

	existing, err := runCommandText(ctx, runner, nvidiaSMI, "-L")
	if err != nil {
		return fmt.Errorf("query existing GPU layout before MIG configuration: %w", err)
	}
	if existing := strings.TrimSpace(existing); strings.Contains(existing, "MIG-GPU") || strings.Contains(existing, "MIG ") {
		return fmt.Errorf("refusing to reconfigure existing MIG instances; use --mig-configure auto to validate the current layout")
	}
	return nil
}

// gpuMIGMode is one GPU's index and its MIG mode before the run changed it.
type gpuMIGMode struct {
	index string
	mode  string
}

// restoreMIGInstances removes created MIG instances, restores MIG mode when it was originally
// disabled, and puts the host CDI spec back as cdiSpec recorded it.
func restoreMIGInstances(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, previouslyEnabled string, cdiSpec cdiSpecState) error {
	return restoreGPUs(ctx, stdout, runner, []gpuMIGMode{{index: defaultMIGGPUIndex, mode: previouslyEnabled}}, cdiSpec)
}

// restoreGPUs removes the MIG instances of every GPU in previous, turns MIG off
// again on the GPUs where it was not enabled, and puts the host CDI spec back
// as cdiSpec recorded it. It attempts every step and returns all failures.
func restoreGPUs(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, previous []gpuMIGMode, cdiSpec cdiSpecState) error {
	fmt.Fprintln(stdout, "[e2e] Restoring MIG state after validation")
	var errs []error
	for _, gpu := range previous {
		errs = append(errs,
			runInstallCommand(ctx, stdout, runner, "nvidia_smi_delete_mig_compute_instances", sudoCommand(nvidiaSMI, "mig", "-i", gpu.index, "-dci")),
			runInstallCommand(ctx, stdout, runner, "nvidia_smi_delete_mig_gpu_instances", sudoCommand(nvidiaSMI, "mig", "-i", gpu.index, "-dgi")),
		)
		if !strings.EqualFold(strings.TrimSpace(gpu.mode), "enabled") {
			errs = append(errs, runInstallCommand(ctx, stdout, runner, "nvidia_smi_disable_mig", sudoCommand(nvidiaSMI, "-i", gpu.index, "-mig", "0")))
		}
	}

	errs = append(errs, restoreCDISpec(ctx, stdout, runner, cdiSpec))
	return errors.Join(errs...)
}

// selectMIGProfileID first verifies MIG support, then returns an explicit
// profile or chooses the first profile reported by nvidia-smi.
func selectMIGProfileID(ctx context.Context, runner e2eexec.Runner, requested string) (string, error) {
	result := runner.Run(ctx, e2eexec.Command{Name: nvidiaSMI, Args: []string{"mig", "-i", defaultMIGGPUIndex, "-lgip"}})
	output := string(result.Stdout) + string(result.Stderr)
	if result.ExitCode != 0 || result.Err != nil {
		if migProfilesUnsupported(result, output) {
			return "", nil
		}
		return "", fmt.Errorf("query MIG profiles before MIG configuration: %w", runResultError(nvidiaSMI, result))
	}
	if requested != firstMIGProfile {
		return requested, nil
	}
	return selectMIGProfileIDFromOutput(output), nil
}

// migProfilesUnsupported recognizes nvidia-smi responses from hosts without MIG support.
func migProfilesUnsupported(result e2eexec.Result, output string) bool {
	lower := strings.ToLower(output)
	return result.ExitCode == 6 ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "no mig-supported") ||
		strings.Contains(lower, "no devices were found")
}

// selectMIGProfileIDFromOutput extracts a profile ID from nvidia-smi mig -lgip output.
func selectMIGProfileIDFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "mig") && !strings.Contains(lower, "profile") {
			continue
		}
		fields := strings.Fields(line)
		for i := 0; i <= len(fields)-3; i++ {
			if strings.EqualFold(fields[i], "MIG") && strings.Contains(fields[i+1], "g.") {
				if id := strings.Trim(fields[i+2], " ,"); migProfileIDRE.MatchString(id) {
					return migProfileIDRE.FindString(id)
				}
			}
		}
		for _, field := range fields {
			if id := migProfileIDRE.FindString(field); id != "" {
				return id
			}
		}
	}
	return ""
}

// migCurrentMode reads the current MIG mode for the GPU the harness may mutate.
func migCurrentMode(ctx context.Context, runner e2eexec.Runner) (string, error) {
	output, err := runCommandText(ctx, runner, nvidiaSMI, "--query-gpu=index,mig.mode.current", "--format=csv,noheader")
	if err != nil {
		return "", fmt.Errorf("query current MIG mode before MIG configuration: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) >= 2 && strings.TrimSpace(parts[0]) == defaultMIGGPUIndex {
			return strings.TrimSpace(parts[1]), nil
		}
	}
	return "", fmt.Errorf("query current MIG mode before MIG configuration: GPU %s not found in nvidia-smi output", defaultMIGGPUIndex)
}

// regenerateNvidiaCDISpec refreshes CDI device specs after MIG layout changes when nvidia-ctk is
// available, and returns an error when that fails, so the caller rolls MIG back instead of leaving
// pods a missing or stale spec. restoreCDISpec puts the host's own spec back when MIG is restored.
func regenerateNvidiaCDISpec(ctx context.Context, stdout io.Writer, runner e2eexec.Runner) error {
	if !commandAvailable(ctx, runner, "nvidia-ctk") {
		fmt.Fprintln(stdout, "[e2e] nvidia-ctk not found; skipping NVIDIA CDI spec regeneration")
		return nil
	}

	if err := runInstallCommand(ctx, stdout, runner, "mkdir_cdi", sudoCommand("mkdir", "-p", cdiSpecDir)); err != nil {
		return fmt.Errorf("regenerate the NVIDIA CDI spec after MIG configuration: %w", err)
	}
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_ctk_cdi_generate", sudoCommand("nvidia-ctk", "cdi", "generate", "--output="+cdiSpecPath)); err != nil {
		return fmt.Errorf("regenerate the NVIDIA CDI spec after MIG configuration: %w", err)
	}
	return nil
}

// commandAvailable checks whether a command can be resolved by the current shell.
func commandAvailable(ctx context.Context, runner e2eexec.Runner, name string) bool {
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v " + name}}).ExitCode == 0
}

// runInstallCommand logs a host mutation step and fails when it exits non-zero.
func runInstallCommand(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, name string, command e2eexec.Command) error {
	fmt.Fprintf(stdout, "[e2e] step: %s\n", name)
	command.Stdout = stdout
	command.Stderr = stdout
	command.LogName = name
	command.QuietOnSuccess = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return fmt.Errorf("%s failed: %s%s", installCommandString(command), result.Stdout, result.Stderr)
	}
	return nil
}

// installCommandString renders a command for human-readable e2e logs.
func installCommandString(command e2eexec.Command) string {
	out := command.Name
	for _, arg := range command.Args {
		out += " " + arg
	}
	return out
}

// sudoCommand wraps a command with sudo when the current process is not root and sudo exists.
func sudoCommand(name string, args ...string) e2eexec.Command {
	if os.Geteuid() == 0 {
		return e2eexec.Command{Name: name, Args: args}
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return e2eexec.Command{Name: name, Args: args}
	}
	return e2eexec.Command{Name: "sudo", Args: append([]string{name}, args...)}
}

// runCommandText returns a successful command's text output.
func runCommandText(ctx context.Context, runner e2eexec.Runner, name string, args ...string) (string, error) {
	result := runner.Run(ctx, e2eexec.Command{Name: name, Args: args})
	if result.ExitCode != 0 || result.Err != nil {
		return "", runResultError(name, result)
	}
	if len(result.Stdout) != 0 {
		return string(result.Stdout), nil
	}
	return string(result.Stderr), nil
}

// runResultError turns a command result into an error with the most useful output text.
func runResultError(name string, result e2eexec.Result) error {
	if result.Err != nil {
		return result.Err
	}
	text := strings.TrimSpace(string(result.Stderr))
	if text == "" {
		text = strings.TrimSpace(string(result.Stdout))
	}
	if text == "" {
		return fmt.Errorf("%s exited with status %d", name, result.ExitCode)
	}
	return fmt.Errorf("%s exited with status %d: %s", name, result.ExitCode, text)
}
