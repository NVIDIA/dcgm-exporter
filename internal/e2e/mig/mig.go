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
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

const defaultMIGGPUIndex = "0"

var migProfileIDRE = regexp.MustCompile(`\b[0-9]+\b`)

// PrepareHostBeforeCluster optionally creates MIG instances before cluster setup and returns a restore hook.
func PrepareHostBeforeCluster(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests) (func(context.Context), error) {
	if !migPreconfigureRequested(opts) {
		return func(context.Context) {}, nil
	}
	profile := migConfigurationProfile(opts)
	profileID, err := selectMIGProfileID(ctx, runner, profile)
	if err != nil {
		return func(context.Context) {}, err
	}
	if profile == "auto" && profileID == "" {
		fmt.Fprintln(stdout, "[e2e] MIG configuration requested, but no MIG profiles were detected; continuing without host MIG mutation")
		return func(context.Context) {}, nil
	}
	if profileID == "" {
		return func(context.Context) {}, fmt.Errorf("could not determine a MIG profile id for --mig-configure %s", profile)
	}
	previousMode, err := migCurrentMode(ctx, runner)
	if err != nil {
		return func(context.Context) {}, err
	}
	if err := configureMIGInstances(ctx, stdout, runner, profileID); err != nil {
		return func(context.Context) {}, err
	}
	cleanup := func(cleanupCtx context.Context) {
		restoreMIGInstances(cleanupCtx, stdout, runner, previousMode)
	}
	return cleanup, nil
}

// migPreconfigureRequested reports whether selected k8s scenarios require host MIG mutation.
func migPreconfigureRequested(opts config.Tests) bool {
	if !MutationRequested(opts.MIGConfigure) {
		return false
	}
	selected := scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, config.Config{Tests: opts})
	if len(opts.Scenarios) == 0 {
		return true
	}
	for _, entry := range selected {
		switch entry.Selector() {
		case "k8s/mig", "k8s/migFullGpuSelection", "k8s/migInstanceSelection", "k8s/migSpecificInstanceSelection", "k8s/migCombinedDeviceSelection", "k8s/gpuOperatorMig":
			return true
		}
	}
	return false
}

// MutationRequested treats explicit true-like MIG modes as permission to mutate the host.
func MutationRequested(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// migConfigurationProfile normalizes --mig-configure into "auto" or an explicit profile ID.
func migConfigurationProfile(opts config.Tests) string {
	value := strings.TrimSpace(opts.MIGConfigure)
	if strings.EqualFold(value, "true") {
		return "auto"
	}
	if value == "" {
		return "auto"
	}
	return value
}

// configureMIGInstances creates one MIG GPU instance after refusing unsafe active or preexisting layouts.
func configureMIGInstances(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, profileID string) error {
	processes, err := runCommandText(ctx, runner, "nvidia-smi", "--query-compute-apps=pid,gpu_uuid,process_name", "--format=csv,noheader")
	if err != nil {
		return fmt.Errorf("query active GPU compute processes before MIG configuration: %w", err)
	}
	if processes := strings.TrimSpace(processes); processes != "" {
		return fmt.Errorf("refusing to configure MIG while GPU compute processes are active:\n%s", processes)
	}
	existing, err := runCommandText(ctx, runner, "nvidia-smi", "-L")
	if err != nil {
		return fmt.Errorf("query existing GPU layout before MIG configuration: %w", err)
	}
	if existing := strings.TrimSpace(existing); strings.Contains(existing, "MIG-GPU") || strings.Contains(existing, "MIG ") {
		return fmt.Errorf("refusing to reconfigure existing MIG instances; use --mig-configure auto to validate the current layout")
	}
	fmt.Fprintln(stdout, "[e2e] Configuring MIG on GPU "+defaultMIGGPUIndex+" with profile "+profileID)
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_smi_enable_mig", sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "1")); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_smi_create_mig_instance", sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-cgi", profileID, "-C")); err != nil {
		return err
	}
	regenerateNvidiaCDISpec(ctx, stdout, runner)
	return nil
}

// restoreMIGInstances removes created MIG instances and restores MIG mode when it was originally disabled.
func restoreMIGInstances(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, previouslyEnabled string) {
	fmt.Fprintln(stdout, "[e2e] Restoring MIG state after validation")
	_ = runInstallCommand(ctx, stdout, runner, "nvidia_smi_delete_mig_compute_instances", sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dci"))
	_ = runInstallCommand(ctx, stdout, runner, "nvidia_smi_delete_mig_gpu_instances", sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dgi"))
	if !strings.EqualFold(strings.TrimSpace(previouslyEnabled), "enabled") {
		_ = runInstallCommand(ctx, stdout, runner, "nvidia_smi_disable_mig", sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "0"))
	}
	regenerateNvidiaCDISpec(ctx, stdout, runner)
}

// selectMIGProfileID returns an explicit profile or chooses the first profile reported by nvidia-smi.
func selectMIGProfileID(ctx context.Context, runner e2eexec.Runner, requested string) (string, error) {
	if requested != "auto" {
		return requested, nil
	}
	result := runner.Run(ctx, e2eexec.Command{Name: "nvidia-smi", Args: []string{"mig", "-i", defaultMIGGPUIndex, "-lgip"}})
	output := string(result.Stdout) + string(result.Stderr)
	if result.ExitCode != 0 || result.Err != nil {
		if migProfilesUnsupported(result, output) {
			return "", nil
		}
		return "", fmt.Errorf("query MIG profiles before MIG configuration: %w", runResultError("nvidia-smi", result))
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
	output, err := runCommandText(ctx, runner, "nvidia-smi", "--query-gpu=index,mig.mode.current", "--format=csv,noheader")
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

// regenerateNvidiaCDISpec refreshes CDI device specs after MIG layout changes when nvidia-ctk is available.
func regenerateNvidiaCDISpec(ctx context.Context, stdout io.Writer, runner e2eexec.Runner) {
	if !commandAvailable(ctx, runner, "nvidia-ctk") {
		fmt.Fprintln(stdout, "[e2e] nvidia-ctk not found; skipping NVIDIA CDI spec regeneration")
		return
	}
	_ = runInstallCommand(ctx, stdout, runner, "mkdir_cdi", sudoCommand("mkdir", "-p", "/etc/cdi"))
	_ = runInstallCommand(ctx, stdout, runner, "nvidia_ctk_cdi_generate", sudoCommand("nvidia-ctk", "cdi", "generate", "--output=/etc/cdi/nvidia.yaml"))
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
