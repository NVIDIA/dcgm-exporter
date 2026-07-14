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
	if len(result.Stdout) != 0 {
		return stripANSI(string(result.Stdout))
	}
	return stripANSI(string(result.Stderr))
}

// dcgmRemoteVersion runs nv-hostengine --version in the DCGM image used for remote validation.
func dcgmRemoteVersion(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions) string {
	if opts.DryRun {
		return "dry-run: remote DCGM version probe was not executed"
	}
	if opts.DCGMImage == "" {
		return "DCGM image resolution failed"
	}
	result := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{"run", "--rm", "--entrypoint", "/usr/bin/nv-hostengine", opts.DCGMImage, "--version"},
		Env:  opts.DockerEnv,
	})
	if len(result.Stdout) != 0 {
		return stripANSI(string(result.Stdout))
	}
	return stripANSI(string(result.Stderr))
}

// dcgmProbeUnavailable classifies transport/image/runtime probe failures separately from DCGM sentinel values.
func dcgmProbeUnavailable(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{"not found", "connection refused", "could not", "failed", "error connecting", "unavailable", "dry-run", "dcgm image", "timed out", "timeout", "operation not supported"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.Contains(lower, "not supported") || strings.Contains(lower, "not_supported") || strings.Contains(lower, "not-supported") {
		return false
	}
	for _, marker := range []string{"error:", "dcgm error"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// dcgmProbeUnavailableReason turns probe output into an actionable capability reason.
func dcgmProbeUnavailableReason(label, output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		return "DCGM " + label + " probe timed out inside the DCGM container; check GPU container runtime access, nv-hostengine health, and GPU load"
	case strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "authorization required") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "requires login") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "access forbidden") ||
		strings.Contains(lower, "permission denied"):
		return "DCGM " + label + " probe could not access the DCGM image or GPU runtime; authenticate Docker or pass --docker-registry-login/--docker-registry-login-file"
	case strings.Contains(lower, "dcgm image resolution failed") || strings.Contains(lower, "no such manifest") || strings.Contains(lower, "manifest unknown") || strings.Contains(lower, "not found"):
		return "DCGM " + label + " probe could not resolve the configured DCGM image; check --dcgm-version, --dcgm-image, and registry access"
	default:
		return "DCGM " + label + " probe was unavailable; check DCGM image, Docker GPU runtime, and nv-hostengine probe logs"
	}
}
