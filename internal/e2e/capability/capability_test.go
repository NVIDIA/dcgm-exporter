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
	"errors"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const probeGPUQueryCommand = "nvidia-smi --query-gpu=" +
	"index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending " +
	"--format=csv,noheader"

func TestProbeHostClassifiesB200Tiers(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{
		query: "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n" +
			"1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n",
		topo:        "GPU0 GPU1\nGPU0 X NV18\nGPU1 NV18 X\n",
		nvidiaQ:     "Peer Type : Switch Connected\n",
		lscpu:       "Architecture: x86_64\n",
		migProfiles: "MIG 1g.23gb 19\n",
	})

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	assertStatuses(
		t, caps, StatusSupported,
		"host:gpu",
		"host:mig",
		"host:nvlink_fabric",
		"host:nvswitch",
		"host:multi_gpu",
		"host:nvlink5",
		"host:nvswitch_local_telemetry",
		"host:nvswitch_nvsdm",
	)
	assertStatuses(t, caps, StatusUnsupported, "host:nvswitch_nscq")
}

func TestProbeHostSkipsNVLinkOnL4WithNAFabric(t *testing.T) {
	nvidiaQ := "Fabric\n    State : N/A\n    Status : N/A\n"
	runner := newHostProbeRunner(hostFixture{
		query:       "0, NVIDIA L4, GPU-0, 550.54.15, 8.9, Disabled, Disabled\n",
		topo:        "GPU0\nGPU0 X\n",
		nvidiaQ:     nvidiaQ,
		lscpu:       "Architecture: x86_64\n",
		migProfiles: "No MIG-supported devices found.\n",
	})
	runner.results["nvidia-smi nvlink -s"] = e2eexec.Result{
		ExitCode: 1,
		Stderr:   []byte("NVLink is not supported on this device\n"),
	}

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	assertStatuses(t, caps, StatusUnsupported, "host:nvlink_fabric", "host:nvswitch", "host:nvlink5")
	reported, healthy, evidence := FabricHealth(nvidiaQ)
	if reported || healthy || evidence != "" {
		t.Fatalf("FabricHealth() = (%t, %t, %q), want no meaningful fabric sections", reported, healthy, evidence)
	}
}

func TestProbeHostDetectsActiveGB300NVLink(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{
		query:       "0, NVIDIA GB300, GPU-0, 580.105.08, 10.3, Disabled, Disabled\n",
		topo:        "GPU0\nGPU0 X\n",
		nvlink:      "GPU 0: NVIDIA GB300\n    Link 0: 53.125 GB/s\n",
		nvidiaQ:     "Fabric\n    State : N/A\n    Status : N/A\n",
		lscpu:       "Architecture: aarch64\n",
		migProfiles: "No MIG-supported devices found.\n",
	})

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	got := caps.Lookup("host:nvlink_fabric")
	if got.Status != StatusSupported || !strings.Contains(got.Evidence, "Link 0: 53.125 GB/s") {
		t.Fatalf("host:nvlink_fabric = %#v, want active-link support", got)
	}
}

func TestProbeHostKeepsBrokenGB200FabricSelected(t *testing.T) {
	fixture := hostFixture{
		query:       "0, NVIDIA GB200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n",
		topo:        "GPU0 GPU1\nGPU0 X NV18\nGPU1 NV18 X\n",
		nvidiaQ:     "Fabric\n    State : In Progress\n    Status : N/A\n",
		lscpu:       "Architecture: aarch64\n",
		migProfiles: "No MIG-supported devices found.\n",
	}
	runner := newHostProbeRunner(fixture)

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	assertStatuses(
		t, caps, StatusSupported,
		"host:nvlink_fabric",
		"host:nvswitch",
		"host:nvlink5",
	)
	assertStatuses(
		t, caps, StatusUnsupported,
		"host:nvswitch_local_telemetry",
		"host:nvswitch_nscq",
		"host:nvswitch_nvsdm",
	)
	reported, healthy, evidence := FabricHealth(fixture.nvidiaQ)
	if !reported || healthy {
		t.Fatalf(
			"broken GB200 FabricHealth() = (%t, %t, %q), want reported and unhealthy",
			reported,
			healthy,
			evidence,
		)
	}
}

func TestProbeHostClassifiesH100WithoutSwitchTelemetry(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{
		query: "0, NVIDIA H100, GPU-0, 550.54.15, 9.0, Disabled, Disabled\n" +
			"1, NVIDIA H100, GPU-1, 550.54.15, 9.0, Disabled, Disabled\n",
		topo:        "GPU0 GPU1\nGPU0 X NV4\nGPU1 NV4 X\n",
		nvidiaQ:     "Peer Type : Direct Connected\n",
		lscpu:       "Architecture: x86_64\n",
		migProfiles: "MIG 1g.10gb 19\n",
	})

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	assertStatuses(
		t, caps, StatusSupported,
		"host:gpu",
		"host:mig",
		"host:nvlink_fabric",
		"host:multi_gpu",
	)
	assertStatuses(
		t, caps, StatusUnsupported,
		"host:nvswitch",
		"host:nvlink5",
		"host:nvswitch_local_telemetry",
	)
}

func TestMIGCapabilityUsesProfilesInsteadOfCurrentConfiguration(t *testing.T) {
	query := "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n"
	runner := newHostProbeRunner(hostFixture{
		query:       query,
		lscpu:       "Architecture: x86_64\n",
		migProfiles: "MIG 1g.23gb 19\n",
	})

	caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
	if got := caps.Lookup("host:mig"); got.Status != StatusSupported {
		t.Fatalf("host:mig = %#v, want supported from profile inventory", got)
	}
	if got := caps.Lookup("host:mixed_mig"); got.Status != StatusUnsupported {
		t.Fatalf("host:mixed_mig = %#v, want one-GPU scenario inapplicable", got)
	}
}

func TestMIGCapabilityTreatsProbeFailureAsUnknown(t *testing.T) {
	got := migCapability("NVIDIA-SMI failed because it couldn't communicate with the driver")
	if got.Status != StatusUnknown {
		t.Fatalf("migCapability() = %#v, want unknown", got)
	}
}

func TestNVLinkCapabilityDoesNotRequireHealthyFabric(t *testing.T) {
	fabric := probeFabricSignals(
		"0, NVIDIA GB200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n",
		"Fabric\n    State : In Progress\n    Status : N/A\n",
		"GPU0 GPU1\nGPU0 X NV18\nGPU1 NV18 X\n",
		"",
		"Architecture: aarch64\n",
	)
	got := nvlinkFabricCapability(fabric, "Fabric\n State : In Progress\n", "GPU0 X NV18\n", "")
	if got.Status != StatusSupported {
		t.Fatalf("nvlinkFabricCapability() = %#v, want supported hardware", got)
	}
}

func TestNVLinkCapabilityRejectsPartialProbeFailure(t *testing.T) {
	nvidiaQ := "command failed: exit code 1\nFabric\n    State : Completed\n    Status : Success\n"
	topo := "GPU0 GPU1\nGPU0 X NV18\nGPU1 NV18 X\n"
	fabric := probeFabricSignals(
		"0, NVIDIA GB200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n",
		nvidiaQ,
		topo,
		"",
		"Architecture: aarch64\n",
	)

	got := nvlinkFabricCapability(fabric, nvidiaQ, topo, "")
	if got.Status != StatusUnknown {
		t.Fatalf("nvlinkFabricCapability() = %#v, want unknown after partial probe failure", got)
	}
}

func TestNVLinkCapabilityUsesTopologyWhenStatusProbeFails(t *testing.T) {
	nvidiaQ := "Fabric\n    State : N/A\n    Status : N/A\n"
	topo := "GPU0 GPU1\nGPU0 X NV4\nGPU1 NV4 X\n"
	nvlinkStatus := "command failed: exit code 1\nNVLink status command unavailable\n"
	fabric := probeFabricSignals(
		"0, NVIDIA H100, GPU-0, 550.54.15, 9.0, Disabled, Disabled\n",
		nvidiaQ,
		topo,
		nvlinkStatus,
		"Architecture: x86_64\n",
	)

	got := nvlinkFabricCapability(fabric, nvidiaQ, topo, nvlinkStatus)
	if got.Status != StatusSupported {
		t.Fatalf("nvlinkFabricCapability() = %#v, want topology evidence to survive optional status failure", got)
	}
}

func TestFabricHealth(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		reported bool
		healthy  bool
	}{
		{
			name: "healthy",
			output: `Fabric
        State  : Completed
        Status : Success`,
			reported: true,
			healthy:  true,
		},
		{
			name: "unhealthy",
			output: `Fabric
        State  : In Progress
        Status : N/A`,
			reported: true,
			healthy:  false,
		},
		{name: "not reported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reported, healthy, _ := FabricHealth(test.output)
			if reported != test.reported || healthy != test.healthy {
				t.Fatalf(
					"FabricHealth() = (%t, %t), want (%t, %t)",
					reported,
					healthy,
					test.reported,
					test.healthy,
				)
			}
		})
	}
}

func TestHasActiveNVLink(t *testing.T) {
	for _, output := range []string{"Link 0: 53.125 GB/s", "Link 0: 0.5 GB/s"} {
		if !HasActiveNVLink(output) {
			t.Fatalf("HasActiveNVLink() rejected active link output %q", output)
		}
	}
	for _, output := range []string{"Link 0: inactive", "Link 0: 0 GB/s", "Link 0: 0.000 GB/s"} {
		if HasActiveNVLink(output) {
			t.Fatalf("HasActiveNVLink() accepted inactive link output %q", output)
		}
	}
}

func TestProbeOutputFailedIgnoresNormalErrorCounters(t *testing.T) {
	if probeOutputFailed("ECC Errors\n    Volatile Uncorrectable Errors : 0") {
		t.Fatal("probeOutputFailed() treated normal nvidia-smi error counters as command failure")
	}
	if !probeOutputFailed("NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver") {
		t.Fatal("probeOutputFailed() missed an explicit nvidia-smi failure")
	}
}

func TestRunTextPreservesFailureWithPartialOutput(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.results["nvidia-smi -q"] = e2eexec.Result{
		Stdout: []byte("partial inventory\n"),
		Err:    errors.New("probe interrupted"),
	}

	output := runText(context.Background(), runner, "nvidia-smi", "-q")
	if !probeOutputFailed(output) || !strings.Contains(output, "partial inventory") {
		t.Fatalf("runText() = %q, want command failure with partial output", output)
	}
}

func TestGPUCapabilityDistinguishesFailureFromAbsentHardware(t *testing.T) {
	failure := gpuCapability("NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver", 0)
	if failure.Status != StatusUnknown {
		t.Fatalf("failed GPU probe = %#v, want unknown", failure)
	}
	absent := gpuCapability("", 0)
	if absent.Status != StatusUnsupported {
		t.Fatalf("empty GPU inventory = %#v, want unsupported", absent)
	}
}

func TestHealthyFabricEvidenceRequiresEverySection(t *testing.T) {
	healthy, evidence := healthyFabricEvidence(
		"Fabric\n    State : Completed\n    Status : Success\n" +
			"GPU 1\nFabric\n    State : In Progress\n    Status : N/A\n",
	)
	if healthy || !strings.Contains(evidence, "1/2") {
		t.Fatalf("healthyFabricEvidence() = %v, %q, want unhealthy 1/2", healthy, evidence)
	}
}

func TestResolveScenarioInputsOnlyRunsSelectedReadinessChecks(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgm["discovery --compute-hierarchy"] = "I 0/7 GPU Instance (EntityID: 101)\n"
	runner.dcgm["dmon -l"] = "201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n"
	runner.dcgm["dmon -e 201 -c 1"] = "Field is not supported on this GPU\n"

	inputs, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		true,
		true,
	)
	if err != nil {
		t.Fatalf("ResolveScenarioInputs() error = %v", err)
	}
	if inputs.MIGInstanceEntityID != "101" || inputs.MIGInstanceNVMLID != "7" {
		t.Fatalf("MIG inputs = %#v, want entity 101 and NVML ID 7", inputs)
	}
	if inputs.UnsupportedFieldCandidate != "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL" {
		t.Fatalf("unsupported field = %q", inputs.UnsupportedFieldCandidate)
	}
}

func TestResolveScenarioInputsAllowsMissingUnsupportedFieldCandidate(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgm["dmon -l"] = "201 DCGM_FI_DEV_GPU_TEMP\n"

	inputs, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		false,
		true,
	)
	if err != nil {
		t.Fatalf("ResolveScenarioInputs() error = %v", err)
	}
	if inputs.UnsupportedFieldCandidate != "" {
		t.Fatalf("UnsupportedFieldCandidate = %q, want empty", inputs.UnsupportedFieldCandidate)
	}
	if !strings.Contains(inputs.UnsupportedFieldNotApplicableReason, "none of the unsupported-field candidates") {
		t.Fatalf("not-applicable reason = %q, want absent candidates", inputs.UnsupportedFieldNotApplicableReason)
	}
}

func TestResolveScenarioInputsAllowsValidUnsupportedFieldCandidates(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgm["dmon -l"] = "201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n"
	runner.dcgm["dmon -e 201 -c 1"] = "42\n"

	inputs, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		false,
		true,
	)
	if err != nil {
		t.Fatalf("ResolveScenarioInputs() error = %v", err)
	}
	if inputs.UnsupportedFieldCandidate != "" {
		t.Fatalf("UnsupportedFieldCandidate = %q, want empty", inputs.UnsupportedFieldCandidate)
	}
	if !strings.Contains(inputs.UnsupportedFieldNotApplicableReason, "all probed DCGM field candidates") {
		t.Fatalf("not-applicable reason = %q, want valid samples", inputs.UnsupportedFieldNotApplicableReason)
	}
}

func TestResolveScenarioInputsFailsFieldInventoryProbe(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgmResults["dmon -l"] = e2eexec.Result{
		Stdout:   []byte("201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n"),
		Stderr:   []byte("dcgmi probe timed out\n"),
		ExitCode: 1,
	}

	_, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		false,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "field-list probe timed out") {
		t.Fatalf("ResolveScenarioInputs() error = %v, want field-list probe failure", err)
	}
}

func TestResolveScenarioInputsFailsCandidateProbeWithPartialSentinel(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgm["dmon -l"] = "201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n"
	runner.dcgmResults["dmon -e 201 -c 1"] = e2eexec.Result{
		Stdout:   []byte("GPU 0 DCGM_FT_INT64_NOT_SUPPORTED\n"),
		Stderr:   []byte("dcgmi probe timed out\n"),
		ExitCode: 1,
	}

	_, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		false,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "field-sample probe timed out") {
		t.Fatalf("ResolveScenarioInputs() error = %v, want candidate probe failure", err)
	}
}

func TestResolveScenarioInputsFailsMissingReadiness(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	_, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		true,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "returned no instance rows") {
		t.Fatalf("ResolveScenarioInputs() error = %v, want missing MIG readiness", err)
	}
}

// migProbeCase is one fake host for TestProbeHostReportsCurrentMIGState.
type migProbeCase struct {
	name               string
	query              string
	profiles           string
	hierarchy          string
	hierarchyResult    *e2eexec.Result
	wantProfiles       Status
	wantActive         Status
	wantMixed          Status
	wantMixedReason    string
	wantHierarchyProbe bool
}

// TestProbeHostReportsCurrentMIGState checks the MIG gates against fake MIG-on, mixed, MIG-off, no-profile and failed-probe hosts.
func TestProbeHostReportsCurrentMIGState(t *testing.T) {
	const (
		instance = "I 0/7 GPU Instance (EntityID: 101)\n"
		gpu0On   = "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Enabled, Enabled\n"
		gpu1On   = "1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Enabled, Enabled\n"
		gpu0Off  = "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n"
		gpu1Off  = "1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n"
		gpuNA    = "0, NVIDIA L4, GPU-0, 595.58.03, 8.9, [N/A], [N/A]\n"
		profiles = "MIG 1g.23gb 19\n"
		noMIG    = "No MIG-supported devices found.\n"
	)
	tests := []migProbeCase{
		{
			name: "MIG on with an instance beside a full GPU", query: gpu0On + gpu1Off, profiles: profiles, hierarchy: instance,
			wantProfiles: StatusSupported, wantActive: StatusSupported, wantMixed: StatusSupported, wantHierarchyProbe: true,
		},
		{
			name: "every GPU in MIG mode", query: gpu0On + gpu1On, profiles: profiles, hierarchy: instance,
			wantProfiles: StatusSupported, wantActive: StatusSupported, wantMixed: StatusUnsupported,
			wantMixedReason: "mixed MIG needs a GPU with MIG off", wantHierarchyProbe: true,
		},
		{
			name: "MIG on without an instance", query: gpu0On + gpu1Off, profiles: profiles,
			wantProfiles: StatusSupported, wantActive: StatusUnsupported, wantMixed: StatusUnsupported,
			wantMixedReason: "DCGM lists no GPU instance", wantHierarchyProbe: true,
		},
		{
			name: "MIG off on every GPU with profiles listed", query: gpu0Off + gpu1Off, profiles: profiles,
			wantProfiles: StatusSupported, wantActive: StatusUnsupported, wantMixed: StatusUnsupported,
			wantMixedReason: "MIG is off on every GPU",
		},
		{
			name: "GPU without MIG profiles", query: gpuNA, profiles: noMIG,
			wantProfiles: StatusUnsupported, wantActive: StatusUnsupported, wantMixed: StatusUnsupported,
			wantMixedReason: "MIG is off on every GPU",
		},
		{
			name: "nvidia-smi query fails", query: "NVIDIA-SMI has failed because it couldn't communicate with the driver\n", profiles: noMIG,
			wantProfiles: StatusUnsupported, wantActive: StatusUnknown, wantMixed: StatusUnknown,
			wantMixedReason: "could not be determined",
		},
		{
			name: "compute-hierarchy probe unavailable with MIG on", query: gpu0On + gpu1Off, profiles: profiles,
			hierarchyResult: &e2eexec.Result{ExitCode: 1, Stderr: []byte("dcgmi probe timed out")},
			wantProfiles:    StatusSupported, wantActive: StatusUnknown, wantMixed: StatusUnknown,
			wantMixedReason: "compute-hierarchy", wantHierarchyProbe: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newHostProbeRunner(hostFixture{query: test.query, migProfiles: test.profiles})
			runner.dcgm["discovery --compute-hierarchy"] = test.hierarchy
			if test.hierarchyResult != nil {
				runner.dcgmResults["discovery --compute-hierarchy"] = *test.hierarchyResult
			}
			caps := ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test"})
			profiles, active, mixed := caps.Lookup("host:mig"), caps.Lookup("host:mig_active"), caps.Lookup("host:mixed_mig")
			if profiles.Status != test.wantProfiles || active.Status != test.wantActive || mixed.Status != test.wantMixed {
				t.Fatalf("mig = %#v, mig_active = %#v, mixed_mig = %#v, want %s, %s and %s",
					profiles, active, mixed, test.wantProfiles, test.wantActive, test.wantMixed)
			}
			if !strings.Contains(mixed.Reason, test.wantMixedReason) {
				t.Fatalf("mixed_mig reason = %q, want %q", mixed.Reason, test.wantMixedReason)
			}
			if got := contains(runner.dockerProbes, "discovery --compute-hierarchy"); got != test.wantHierarchyProbe {
				t.Fatalf("compute-hierarchy probed = %t, want %t", got, test.wantHierarchyProbe)
			}
		})
	}
}

// TestResolveScenarioInputsPreservesHierarchyProbeFailure keeps the useful DCGM error text.
func TestResolveScenarioInputsPreservesHierarchyProbeFailure(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgmResults["discovery --compute-hierarchy"] = e2eexec.Result{
		ExitCode: 1,
		Stderr:   []byte("dcgmi probe timed out"),
	}

	_, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		true,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "compute-hierarchy probe timed out") {
		t.Fatalf("ResolveScenarioInputs() error = %v, want hierarchy probe failure", err)
	}
	if strings.Contains(err.Error(), "returned no instance rows") {
		t.Fatalf("ResolveScenarioInputs() error masked the probe failure: %v", err)
	}
}

// TestResolveScenarioInputsIncludesGenericHierarchyProbeDetail reports nonstandard probe failures.
func TestResolveScenarioInputsIncludesGenericHierarchyProbeDetail(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgmResults["discovery --compute-hierarchy"] = e2eexec.Result{
		ExitCode: 1,
		Stderr:   []byte("DCGM protocol mismatch"),
	}

	_, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		true,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "DCGM protocol mismatch") {
		t.Fatalf("ResolveScenarioInputs() error = %v, want generic hierarchy detail", err)
	}
}

func TestResolveScenarioInputsDoesNothingWithoutSelectedScenarios(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	inputs, err := ResolveScenarioInputs(
		context.Background(),
		runner,
		ProbeOptions{DCGMImage: "dcgm:test"},
		false,
		false,
	)
	if err != nil || inputs != (ScenarioInputs{}) {
		t.Fatalf("ResolveScenarioInputs() = %#v, %v, want empty", inputs, err)
	}
	if len(runner.dockerProbes) != 0 {
		t.Fatalf("unexpected readiness probes: %v", runner.dockerProbes)
	}
}

func TestProbeHostPassesDockerEnvironment(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	_ = ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage: "dcgm:test",
		DockerEnv: []string{"DOCKER_CONFIG=/tmp/e2e-docker"},
	})

	if len(runner.dockerEnvs) == 0 {
		t.Fatal("expected DCGM container probes")
	}
	for _, env := range runner.dockerEnvs {
		if !contains(env, "DOCKER_CONFIG=/tmp/e2e-docker") {
			t.Fatalf("docker environment = %v, want DOCKER_CONFIG", env)
		}
	}
}

func TestGPUCountRejectsErrorOutput(t *testing.T) {
	got := GPUCount("NVIDIA-SMI error report\n0, NVIDIA A100, GPU-1\n")
	if got != 1 {
		t.Fatalf("GPUCount() = %d, want 1", got)
	}
}

func TestDCGMProbeUnavailableDistinguishesUnsupportedFields(t *testing.T) {
	if dcgmProbeUnavailable("Field is not supported on this GPU") {
		t.Fatal("DCGM field sentinel should not be a transport failure")
	}
	unsupportedAfterRetries := "command failed: exit code 1\n" +
		"dcgmi probe timed out or failed within 45s\n" +
		"Profiling is not supported for this GPU"
	if dcgmProbeUnavailable(unsupportedAfterRetries) {
		t.Fatal("a completed unsupported response should take precedence over the retry timeout")
	}
	if got := profilingCapability(unsupportedAfterRetries); got.Status != StatusUnsupported {
		t.Fatalf("profilingCapability() status = %q, want unsupported", got.Status)
	}
	if !dcgmProbeUnavailable("socket: operation not supported") {
		t.Fatal("an unsupported transport operation should make the probe unknown")
	}
	if !dcgmProbeUnavailable("Error: connection refused") {
		t.Fatal("connection failure should make the probe unknown")
	}
	failedRuntime := "command failed: exit code 1\nDocker API version not supported"
	if !dcgmProbeUnavailable(failedRuntime) {
		t.Fatal("a failed runtime command must not become an unsupported DCGM capability")
	}
	if got := profilingCapability(failedRuntime); got.Status != StatusUnknown {
		t.Fatalf("profilingCapability() status = %q, want unknown for runtime failure", got.Status)
	}
	missingHostTool := `command failed: exec: "dcgmi": executable file not found in $PATH`
	if reason := dcgmProbeUnavailableReason("profiling", missingHostTool); !strings.Contains(reason, "--bootstrap=check") {
		t.Fatalf("missing dcgmi reason = %q, want bootstrap guidance", reason)
	}
	if dcgmProbeUnavailable("nvlink_link_recovery_failed NLLRF 1212") {
		t.Fatal("DCGM field names containing failed should not be transport failures")
	}
	if dcgmProbeUnavailable("command failed: exit code 1\nfield 999 not found") {
		t.Fatal("an unsupported field response should not be a transport failure")
	}
}

// TestDCGMProbeDoesNotUseUnqualifiedHostFallback keeps backend choice with the CLI preflight.
func TestDCGMProbeDoesNotUseUnqualifiedHostFallback(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.dcgmResults["profile --list"] = e2eexec.Result{
		ExitCode: 1,
		Stderr:   []byte("no matching manifest for linux/arm64/v8 in the manifest list entries"),
	}
	runner.nativeDCGM["profile --list"] = "DCGM_FI_PROF_GR_ENGINE_ACTIVE\n"

	dcgm := newDCGMProbeSession(runner, ProbeOptions{DCGMImage: "dcgm:test"})
	output := dcgm.run(context.Background(), "profile", "--list")
	if profilingEvidence(output) {
		t.Fatalf("DCGM probe = %q, want image platform failure", output)
	}
	if len(runner.nativeProbes) != 0 {
		t.Fatalf("native DCGM probes = %v, want none", runner.nativeProbes)
	}
}

// TestDCGMProbeUsesQualifiedHostBackend avoids trying the image after the CLI selects host DCGM.
func TestDCGMProbeUsesQualifiedHostBackend(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.nativeDCGM["profile --host 127.0.0.1:5556 --list"] = "DCGM_FI_PROF_GR_ENGINE_ACTIVE\n"

	dcgm := newDCGMProbeSession(runner, ProbeOptions{DCGMImage: "dcgm:test", HostDCGMAddress: "127.0.0.1:5556"})
	output := dcgm.run(context.Background(), "profile", "--list")
	if !profilingEvidence(output) {
		t.Fatalf("DCGM probe = %q, want native profiling evidence", output)
	}
	if len(runner.dockerProbes) != 0 {
		t.Fatalf("Docker DCGM probes = %v, want none", runner.dockerProbes)
	}
}

// TestDCGMProbeUsesLocalHostBackend keeps explicit host-only validation from
// requiring the container image used by general hardware capability probing.
func TestDCGMProbeUsesLocalHostBackend(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.nativeDCGM["profile --list"] = "DCGM_FI_PROF_GR_ENGINE_ACTIVE\n"

	dcgm := newDCGMProbeSession(runner, ProbeOptions{UseHostDCGM: true})
	output := dcgm.run(context.Background(), "profile", "--list")
	if !profilingEvidence(output) {
		t.Fatalf("DCGM probe = %q, want native profiling evidence", output)
	}
	if len(runner.dockerProbes) != 0 {
		t.Fatalf("Docker DCGM probes = %v, want none", runner.dockerProbes)
	}
}

// TestDCGMProbeDryRunDoesNotUseHostBackend keeps dry-run capability checks read-only.
func TestDCGMProbeDryRunDoesNotUseHostBackend(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	dcgm := newDCGMProbeSession(runner, ProbeOptions{DryRun: true, HostDCGMAddress: "127.0.0.1:5556"})

	if output := dcgm.run(context.Background(), "profile", "--list"); !strings.Contains(output, "dry-run") {
		t.Fatalf("DCGM probe = %q, want dry-run sentinel", output)
	}
	if len(runner.nativeProbes) != 0 || len(runner.dockerProbes) != 0 {
		t.Fatalf("dry-run executed DCGM probes: native=%v docker=%v", runner.nativeProbes, runner.dockerProbes)
	}
}

// TestProbeHostUsesQualifiedHostDCGMForEveryCheck keeps related probes on the selected backend.
func TestProbeHostUsesQualifiedHostDCGMForEveryCheck(t *testing.T) {
	runner := newHostProbeRunner(hostFixture{})
	runner.nativeDCGM["discovery --host 127.0.0.1:5556 -l"] = "1 GPU found.\n"
	runner.nativeDCGM["profile --host 127.0.0.1:5556 --list"] = "DCGM_FI_PROF_GR_ENGINE_ACTIVE\n"
	runner.nativeDCGM["dmon --host 127.0.0.1:5556 -l"] = "900 DCGM_FI_DEV_C2C_TX_BYTES\n"

	_ = ProbeHost(context.Background(), runner, ProbeOptions{DCGMImage: "dcgm:test", HostDCGMAddress: "127.0.0.1:5556"})

	if len(runner.dockerProbes) != 0 {
		t.Fatalf("Docker DCGM probes = %v, want none", runner.dockerProbes)
	}
	if len(runner.nativeProbes) != 3 {
		t.Fatalf("native DCGM probes = %v, want all three host checks", runner.nativeProbes)
	}
}

func TestDCGMSentinelEvidence(t *testing.T) {
	for _, output := range []string{
		"GPU 0 DCGM_FT_INT64_NOT_SUPPORTED",
		"GPU 0 DCGM_FT_INT64_NOT_FOUND",
		"GPU 0 DCGM_FT_INT64_NOT_PERMISSIONED",
		"GPU 0 DCGM_FT_INT64_BLANK",
	} {
		if got := dcgmSentinelEvidence(output); got == "" {
			t.Fatalf("dcgmSentinelEvidence(%q) was empty", output)
		}
	}
}

func TestDCGMFieldIDFromListMatchesLongNames(t *testing.T) {
	output := "201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n1209 DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL\n"
	if got := dcgmFieldIDFromList(output, "DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL"); got != "1209" {
		t.Fatalf("dcgmFieldIDFromList() = %q, want 1209", got)
	}
}

func TestC2CEnabledChecksSameLine(t *testing.T) {
	if c2cEnabled("GPU C2C Mode : Disabled\nUnrelated: Enabled\n") {
		t.Fatal("unrelated enabled text should not enable C2C")
	}
	if !c2cEnabled("GPU C2C Mode : Enabled\n") {
		t.Fatal("expected C2C to be enabled")
	}
}

func TestSnapshotLookupUnknown(t *testing.T) {
	got := NewSnapshot(nil).Lookup("host:nvlink_fabric")
	if got.Status != StatusUnknown || !strings.Contains(got.Reason, "not been probed") {
		t.Fatalf("Lookup() = %#v, want unprobed unknown", got)
	}
}

func assertStatuses(t *testing.T, caps Snapshot, want Status, names ...string) {
	t.Helper()
	for _, name := range names {
		if got := caps.Lookup(name); got.Status != want {
			t.Errorf("%s = %#v, want status %s", name, got, want)
		}
	}
}

// hostFixture defines command output for one synthetic hardware platform.
type hostFixture struct {
	query       string
	topo        string
	nvlink      string
	nvidiaQ     string
	lscpu       string
	migProfiles string
}

// probeRunner implements e2eexec.Runner from a host fixture.
type probeRunner struct {
	outputs      map[string]string
	results      map[string]e2eexec.Result
	dcgm         map[string]string
	dcgmResults  map[string]e2eexec.Result
	nativeDCGM   map[string]string
	dockerProbes []string
	dockerEnvs   [][]string
	nativeProbes []string
}

func newHostProbeRunner(fixture hostFixture) *probeRunner {
	outputs := map[string]string{
		probeGPUQueryCommand:   fixture.query,
		"nvidia-smi topo -m":   fixture.topo,
		"nvidia-smi nvlink -s": fixture.nvlink,
		"nvidia-smi -q":        fixture.nvidiaQ,
		"lscpu":                fixture.lscpu,
	}
	for _, index := range gpuIndexes(fixture.query) {
		outputs["nvidia-smi mig -i "+index+" -lgip"] = fixture.migProfiles
	}
	outputs["nvidia-smi mig -lgip"] = fixture.migProfiles
	return &probeRunner{
		outputs:     outputs,
		results:     make(map[string]e2eexec.Result),
		dcgmResults: make(map[string]e2eexec.Result),
		nativeDCGM:  make(map[string]string),
		dcgm: map[string]string{
			"discovery -l":   "1 GPU found.\n",
			"profile --list": "DCGM_FI_PROF_GR_ENGINE_ACTIVE\n",
			"dmon -l":        "900 DCGM_FI_DEV_C2C_TX_BYTES\n",
		},
	}
}

// Run returns fixture output for one host or Docker probe command.
func (r *probeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name == "docker" {
		return r.runDocker(command)
	}
	if command.Name == "dcgmi" {
		probe := strings.Join(command.Args, " ")
		r.nativeProbes = append(r.nativeProbes, probe)
		if output, ok := r.nativeDCGM[probe]; ok {
			return e2eexec.Result{Stdout: []byte(output)}
		}
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("error: unexpected native DCGM probe: " + probe)}
	}
	key := strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
	if result, ok := r.results[key]; ok {
		return result
	}
	output, ok := r.outputs[key]
	if !ok {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("error: unexpected probe command: " + key)}
	}
	return e2eexec.Result{Stdout: []byte(output)}
}

// runDocker returns the fixture output corresponding to one DCGM probe.
func (r *probeRunner) runDocker(command e2eexec.Command) e2eexec.Result {
	r.dockerEnvs = append(r.dockerEnvs, command.Env)
	for index, arg := range command.Args {
		if arg != "dcgmi-probe" || index+1 >= len(command.Args) {
			continue
		}
		probe := strings.Join(command.Args[index+1:], " ")
		r.dockerProbes = append(r.dockerProbes, probe)
		if result, ok := r.dcgmResults[probe]; ok {
			return result
		}
		return e2eexec.Result{Stdout: []byte(r.dcgm[probe])}
	}
	return e2eexec.Result{ExitCode: 1, Stderr: []byte("error: unexpected docker probe")}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
