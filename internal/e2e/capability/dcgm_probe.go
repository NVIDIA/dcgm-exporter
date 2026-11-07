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

package capability

import (
	"context"
	_ "embed"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

//go:embed dcgmi_probe.sh
var dcgmProbeScript string

// dcgmContainerProbe runs the embedded dcgmi probe script inside the configured DCGM image.
func dcgmContainerProbe(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions, args ...string) string {
	if opts.DryRun {
		return "dry-run: DCGM container probe was not executed"
	}
	if opts.DCGMImage == "" {
		return "DCGM image resolution failed"
	}
	dockerArgs := []string{"run", "--rm", "--gpus", "all", "--privileged", "--entrypoint", "/bin/bash", opts.DCGMImage, "-lc", dcgmProbeScript, "dcgmi-probe"}
	dockerArgs = append(dockerArgs, args...)
	result := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: dockerArgs, Env: opts.DockerEnv})
	return commandResultText(result)
}

// dcgmProbeSession keeps one DCGM probe backend for a related set of checks.
type dcgmProbeSession struct {
	runner      e2eexec.Runner
	opts        ProbeOptions
	hostAddress string
}

// newDCGMProbeSession creates a set of DCGM probes that share backend selection.
func newDCGMProbeSession(runner e2eexec.Runner, opts ProbeOptions) *dcgmProbeSession {
	return &dcgmProbeSession{runner: runner, opts: opts, hostAddress: opts.HostDCGMAddress}
}

// run uses the caller-selected DCGM backend. A configured endpoint takes
// precedence; UseHostDCGM selects local dcgmi without requiring an image.
func (p *dcgmProbeSession) run(ctx context.Context, args ...string) string {
	if p.opts.DryRun {
		return "dry-run: DCGM probe was not executed"
	}
	if p.hostAddress != "" {
		hostArgs := []string{args[0], "--host", p.hostAddress}
		hostArgs = append(hostArgs, args[1:]...)
		return runText(ctx, p.runner, "dcgmi", hostArgs...)
	}
	if p.opts.UseHostDCGM {
		return runText(ctx, p.runner, "dcgmi", args...)
	}
	return dcgmContainerProbe(ctx, p.runner, p.opts, args...)
}

// dcgmProbeFailureDetail returns one bounded output line after the synthetic failure header.
func dcgmProbeFailureDetail(output string) string {
	for _, line := range strings.Split(output, "\n") {
		detail := strings.TrimSpace(line)
		if detail == "" || strings.HasPrefix(detail, "command failed:") {
			continue
		}
		const maxDetailBytes = 240
		if len(detail) > maxDetailBytes {
			return detail[:maxDetailBytes] + "..."
		}
		return detail
	}
	return firstLine(output)
}

// dcgmProbeUnavailable classifies transport/image/runtime probe failures separately from DCGM sentinel values.
func dcgmProbeUnavailable(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "operation not supported") {
		return true
	}
	if dcgmTextualUnsupportedEvidence(output) {
		return false
	}
	if strings.Contains(lower, "command failed:") {
		return true
	}
	if dcgmSentinelEvidence(output) != "" {
		return false
	}
	for _, marker := range []string{"not found", "connection refused", "could not", "error connecting", "unavailable", "dry-run", "dcgm image", "timed out", "timeout"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, marker := range []string{"error:", "dcgm error"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// dcgmTextualUnsupportedEvidence recognizes explicit DCGM field and profiling responses.
func dcgmTextualUnsupportedEvidence(output string) bool {
	lower := strings.ToLower(output)
	for _, response := range []string{
		"dcgm_st_not_supported",
		"field is not supported",
		"field not supported",
		"profiling is not supported",
		"profiling not supported",
	} {
		if strings.Contains(lower, response) {
			return true
		}
	}
	return strings.Contains(lower, "field") && strings.Contains(lower, "not found")
}

// dcgmProbeUnavailableReason turns probe output into an actionable capability reason.
func dcgmProbeUnavailableReason(label, output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		return "DCGM " + label + " probe timed out; check DCGM access, nv-hostengine health, and GPU load"
	case strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "authorization required") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "requires login") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "access forbidden") ||
		strings.Contains(lower, "permission denied"):
		return "DCGM " + label + " probe could not access the DCGM image or GPU runtime; authenticate Docker or pass --docker-registry-login/--docker-registry-login-file"
	case strings.Contains(lower, `exec: "dcgmi"`) || strings.Contains(lower, "dcgmi: command not found"):
		return "DCGM " + label + " probe could not run installed dcgmi; use --bootstrap=check or --bootstrap=install to verify host packages"
	case strings.Contains(lower, "dcgm image resolution failed") || strings.Contains(lower, "no such manifest") || strings.Contains(lower, "manifest unknown") || strings.Contains(lower, "not found"):
		return "DCGM " + label + " probe could not resolve the configured DCGM image; check --dcgm-version, --dcgm-image, and registry access"
	default:
		return "DCGM " + label + " probe was unavailable; check installed dcgmi, the configured DCGM image, GPU access, and nv-hostengine logs"
	}
}
