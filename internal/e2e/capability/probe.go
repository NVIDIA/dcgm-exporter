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
	"fmt"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ProbeHost probes only hardware and protocol facts used for scenario selection.
func ProbeHost(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions) Snapshot {
	query := runText(ctx, runner, "nvidia-smi", "--query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending", "--format=csv,noheader")
	topo := runText(ctx, runner, "nvidia-smi", "topo", "-m")
	nvlinkStatus := runText(ctx, runner, "nvidia-smi", "nvlink", "-s")
	nvidiaQ := runText(ctx, runner, "nvidia-smi", "-q")
	lscpu := runText(ctx, runner, "lscpu")
	migProfiles := runMIGProfileProbe(ctx, runner, query)
	dcgm := newDCGMProbeSession(runner, opts)
	dcgmDiscovery := dcgm.run(ctx, "discovery", "-l")
	dcgmProfile := dcgm.run(ctx, "profile", "--list")
	dcgmFields := dcgm.run(ctx, "dmon", "-l")

	gpuCount := GPUCount(query)
	fabric := probeFabricSignals(query, nvidiaQ, topo, nvlinkStatus, lscpu)
	mig := migCapability(migProfiles)
	migOn, migOff := migModeCounts(query)
	// DCGM lists GPU instances only while MIG is on; migStateCapabilities reads migOn before the hierarchy.
	migHierarchy := ""
	if migOn > 0 {
		migHierarchy = dcgm.run(ctx, "discovery", "--compute-hierarchy")
	}
	migActive, mixedMIG := migStateCapabilities(query, migOn, migOff, migHierarchy)
	nvswitch := nvswitchCapability(fabric, nvidiaQ, topo)
	nvswitchLocal, nvswitchNSCQ, nvswitchNVSDM := nvswitchApplicability(nvswitch, fabric)
	entries := runChecks(
		func() Capability { return gpuCapability(query, gpuCount) },
		func() Capability { return profilingCapability(dcgmProfile) },
		func() Capability { return mig },
		func() Capability { return nvlinkFabricCapability(fabric, nvidiaQ, topo, nvlinkStatus) },
		func() Capability { return nvswitch },
		func() Capability { return graceCapability(lscpu, dcgmDiscovery) },
		func() Capability { return c2cCapability(nvidiaQ, dcgmDiscovery, dcgmProfile, dcgmFields) },
		func() Capability { return multiGPUApplicability(gpuCount) },
		func() Capability { return migActive },
		func() Capability { return mixedMIG },
		func() Capability { return nvlink5Applicability(query) },
		func() Capability { return nvswitchLocal },
		func() Capability { return nvswitchNSCQ },
		func() Capability { return nvswitchNVSDM },
	)

	entries = append(entries, unprobedClusterCapabilities()...)
	return NewSnapshot(entries)
}

// ResolveScenarioInputs gathers readiness data only after the corresponding scenarios are selected.
func ResolveScenarioInputs(
	ctx context.Context,
	runner e2eexec.Runner,
	opts ProbeOptions,
	needMIGSelection bool,
	needUnsupportedField bool,
) (ScenarioInputs, error) {
	var inputs ScenarioInputs
	dcgm := newDCGMProbeSession(runner, opts)
	if needMIGSelection {
		hierarchy := dcgm.run(ctx, "discovery", "--compute-hierarchy")
		if dcgmProbeUnavailable(hierarchy) {
			return ScenarioInputs{}, fmt.Errorf(
				"resolve MIG instance scenario: %s: %s",
				dcgmProbeUnavailableReason("compute-hierarchy", hierarchy),
				dcgmProbeFailureDetail(hierarchy),
			)
		}
		var found bool
		inputs.MIGInstanceEntityID, inputs.MIGInstanceNVMLID, found = migInstanceSelectionPair(hierarchy)
		if !found {
			return ScenarioInputs{}, fmt.Errorf(
				"selected MIG instance scenario requires DCGM to enumerate a GPU instance, " +
					"but dcgmi discovery --compute-hierarchy returned no instance rows",
			)
		}
	}
	if needUnsupportedField {
		fields := dcgm.run(ctx, "dmon", "-l")
		selection, err := selectUnsupportedFieldCandidate(
			ctx,
			dcgm,
			fields,
		)
		if err != nil {
			return ScenarioInputs{}, err
		}
		inputs.UnsupportedFieldCandidate = selection.Candidate
		inputs.UnsupportedFieldEvidence = selection.Evidence
		inputs.UnsupportedFieldNotApplicableReason = selection.NotApplicableReason
	}
	return inputs, nil
}

// runText captures a command's text output with ANSI escape sequences stripped.
func runText(ctx context.Context, runner e2eexec.Runner, name string, args ...string) string {
	result := runner.Run(ctx, e2eexec.Command{Name: name, Args: args})
	return commandResultText(result)
}

// commandResultText preserves command failure as the primary probe state while retaining diagnostic output.
func commandResultText(result e2eexec.Result) string {
	stdout := stripANSI(string(result.Stdout))
	stderr := stripANSI(string(result.Stderr))
	if result.Err == nil && result.ExitCode == 0 {
		if stdout != "" {
			return stdout
		}
		return stderr
	}

	failure := "command failed:"
	if result.Err != nil {
		failure += " " + result.Err.Error()
	} else {
		failure += fmt.Sprintf(" exit code %d", result.ExitCode)
	}
	return strings.Join(nonEmptyStrings(failure, stdout, stderr), "\n")
}

func nonEmptyStrings(values ...string) []string {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return nonEmpty
}
