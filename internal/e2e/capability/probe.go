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

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ProbeHost probes host/DCGM signals used by scenario selection.
func ProbeHost(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions) ProbeResult {
	nvidiaL := runText(ctx, runner, "nvidia-smi", "-L")
	query := runText(ctx, runner, "nvidia-smi", "--query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending", "--format=csv,noheader")
	topo := runText(ctx, runner, "nvidia-smi", "topo", "-m")
	nvlink := runText(ctx, runner, "nvidia-smi", "nvlink", "-s")
	nvidiaQ := runText(ctx, runner, "nvidia-smi", "-q")
	lscpu := runText(ctx, runner, "lscpu")
	migProfiles := runMIGProfileProbe(ctx, runner, query)
	dcgmDiscovery := dcgmContainerProbe(ctx, runner, opts, "discovery", "-l")
	dcgmHierarchy := dcgmContainerProbe(ctx, runner, opts, "discovery", "--compute-hierarchy")
	dcgmProfile := dcgmContainerProbe(ctx, runner, opts, "profile", "--list")
	dcgmFields := dcgmContainerProbe(ctx, runner, opts, "dmon", "-l")
	dcgmVersion := dcgmRemoteVersion(ctx, runner, opts)

	gpuCount := GPUCount(query)
	fullGPUs := fullGPUCount(query)
	migEvidence := migInstanceLine(nvidiaL)
	migProfilesAvailable := hasMIGProfiles(migProfiles)
	migPresent := migEvidence != ""
	activeNVLink := HasActiveNVLink(nvlink, topo)
	migEntityID, migNVMLID := migInstanceSelectionPair(dcgmHierarchy)
	unsupportedCandidate, unsupportedEvidence := selectUnsupportedFieldCandidate(ctx, runner, opts, dcgmFields)

	migCanConfigure := boolModeRequired(opts.MIGConfigure)
	entries := runChecks(
		func() Capability { return gpuCapability(query, gpuCount) },
		func() Capability { return profilingCapability(dcgmProfile) },
		func() Capability {
			return migCapability(migPresent, migProfilesAvailable, migCanConfigure, migProfiles, migEvidence)
		},
		func() Capability {
			return mixedMIGCapability(migPresent, migProfilesAvailable, migCanConfigure, fullGPUs, migProfiles, migEvidence)
		},
		func() Capability {
			return migInstanceCapability(migPresent || (migProfilesAvailable && migCanConfigure), dcgmHierarchy, migEntityID, migNVMLID)
		},
		func() Capability {
			return unsupportedFieldCapability(dcgmFields, unsupportedCandidate, unsupportedEvidence)
		},
		func() Capability { return nvlinkCapability(activeNVLink, topo) },
		func() Capability { return p2pCapability(gpuCount, activeNVLink) },
		func() Capability { return nvswitchCapability(dcgmDiscovery) },
		func() Capability { return graceCapability(lscpu, dcgmDiscovery) },
		func() Capability { return c2cCapability(nvidiaQ, dcgmDiscovery, dcgmProfile, dcgmFields) },
	)

	entries = append(entries, unprobedClusterCapabilities()...)

	remoteDcgm := remoteDcgmCapability(opts, dcgmVersion, dcgmDiscovery)
	failureInjection := failureInjectionCapability(opts, remoteDcgm)
	entries = append(entries, remoteDcgm, failureInjection, nvlinkFailureInjectionCapability(ctx, runner, opts, failureInjection, dcgmFields))

	return ProbeResult{
		Snapshot:                  NewSnapshot(entries),
		MIGInstanceEntityID:       migEntityID,
		MIGInstanceNVMLID:         migNVMLID,
		UnsupportedFieldCandidate: unsupportedCandidate,
		UnsupportedFieldEvidence:  unsupportedEvidence,
	}
}

// runText captures a command's text output with ANSI escape sequences stripped.
func runText(ctx context.Context, runner e2eexec.Runner, name string, args ...string) string {
	result := runner.Run(ctx, e2eexec.Command{Name: name, Args: args})
	if len(result.Stdout) != 0 {
		return stripANSI(string(result.Stdout))
	}
	return stripANSI(string(result.Stderr))
}
