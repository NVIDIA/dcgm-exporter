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
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestGPUCount(t *testing.T) {
	got := GPUCount("0, NVIDIA A100, GPU-1\n1, NVIDIA A100, GPU-2\n")
	if got != 2 {
		t.Fatalf("GPUCount() = %d, want 2", got)
	}
}

func TestGPUCountRejectsErrorOutput(t *testing.T) {
	got := GPUCount("NVIDIA-SMI error report\nRetry later\n0, NVIDIA A100, GPU-1\n")
	if got != 1 {
		t.Fatalf("GPUCount() = %d, want 1", got)
	}
}

func TestDCGMProbeUnavailableDoesNotMatchGenericErrorText(t *testing.T) {
	if dcgmProbeUnavailable("collector error budget is healthy") {
		t.Fatal("generic error text should not mark DCGM probe unavailable")
	}
	if dcgmProbeUnavailable("Error: Unable to Get supported metric groups: Profiling is not supported for this group of GPUs or GPU.") {
		t.Fatal("DCGM not-supported output should be unsupported capability, not unavailable probe")
	}
	if !dcgmProbeUnavailable("Error: connection refused") {
		t.Fatal("explicit error marker should mark DCGM probe unavailable")
	}
	if !dcgmProbeUnavailable("docker runtime failed: operation not supported") {
		t.Fatal("runtime operation-not-supported output should mark DCGM probe unavailable")
	}
}

func TestDCGMProbeUnavailableReasonRequiresAuthEvidence(t *testing.T) {
	if got := dcgmProbeUnavailableReason("profiling", "a newer runtime is required"); strings.Contains(got, "authenticate Docker") {
		t.Fatalf("generic required text was classified as authentication failure: %q", got)
	}
	if got := dcgmProbeUnavailableReason("profiling", "authentication required"); !strings.Contains(got, "authenticate Docker") {
		t.Fatalf("authentication failure reason = %q", got)
	}
}

func TestHasActiveNVLink(t *testing.T) {
	if !HasActiveNVLink("Link 0: 25.781 GB/s\n", "") {
		t.Fatal("expected active NVLink")
	}
	if HasActiveNVLink("", "GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n") {
		t.Fatal("expected inactive NVLink")
	}
}

func TestDCGMSentinelEvidence(t *testing.T) {
	got := dcgmSentinelEvidence("GPU 0 9223372036854775794")
	if got != "DCGM_FT_INT64_NOT_SUPPORTED" {
		t.Fatalf("sentinel = %q, want DCGM_FT_INT64_NOT_SUPPORTED", got)
	}
}

func TestDCGMFieldIDFromListMatchesDCGMLongNames(t *testing.T) {
	fields := "retired_pages_sbe RPSBE 390\nretired_pages_pending RPPEN 392\necc_sbe_volatile_total ESVTL 310\n"
	tests := map[string]string{
		"DCGM_FI_DEV_RETIRED_SBE":       "390",
		"DCGM_FI_DEV_RETIRED_PENDING":   "392",
		"DCGM_FI_DEV_ECC_SBE_VOL_TOTAL": "310",
	}
	for field, want := range tests {
		if got := dcgmFieldIDFromList(fields, field); got != want {
			t.Fatalf("dcgmFieldIDFromList(%s) = %q, want %q", field, got, want)
		}
	}
}

func TestC2CEnabledChecksSameLine(t *testing.T) {
	if !c2cEnabled("GPU C2C Mode : Enabled\n") {
		t.Fatal("expected enabled C2C")
	}
	if c2cEnabled("GPU C2C Mode : N/A\nPersistence Mode : Enabled\n") {
		t.Fatal("expected C2C to ignore enabled text from other fields")
	}
}

func TestHasMIGProfilesRejectsErrorOutput(t *testing.T) {
	output := "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver."
	if hasMIGProfiles(output) {
		t.Fatal("expected nvidia-smi failure output not to count as MIG profile evidence")
	}
}

func TestMIGInstanceLineRecognizesNvidiaSMIFormats(t *testing.T) {
	tests := map[string]struct {
		output string
		want   string
	}{
		"b200 uuid": {
			output: "GPU 0: NVIDIA B200 (UUID: GPU-0)\n  MIG 1g.23gb     Device  0: (UUID: MIG-7f37c7aa-357a-5286-8db6-584f7500ad74)\n",
			want:   "MIG 1g.23gb     Device  0: (UUID: MIG-7f37c7aa-357a-5286-8db6-584f7500ad74)",
		},
		"legacy uuid": {
			output: "GPU 0: NVIDIA H100 (UUID: GPU-0)\n  MIG 1g.10gb Device 0: (UUID: MIG-GPU-0/1/0)\n",
			want:   "MIG 1g.10gb Device 0: (UUID: MIG-GPU-0/1/0)",
		},
		"mode only": {
			output: "GPU 0: NVIDIA B200 (UUID: GPU-0)\n  MIG Mode: Enabled\n",
			want:   "",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := migInstanceLine(test.output); got != test.want {
				t.Fatalf("migInstanceLine() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProbeHostMIGAutoRequiresExistingInstances(t *testing.T) {
	runner := migProbeRunner(
		"GPU 0: NVIDIA B200 (UUID: GPU-0)\nGPU 1: NVIDIA B200 (UUID: GPU-1)\n",
		"0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n",
		"MIG 1g.23gb 19\n",
	)

	result := ProbeHost(context.Background(), runner, ProbeOptions{MIGConfigure: "auto"})

	if got := result.Snapshot.Lookup("host:mig"); got.Status != StatusUnsupported {
		t.Fatalf("host:mig status = %s, want unsupported (%s)", got.Status, got.Reason)
	}
	if got := result.Snapshot.Lookup("host:mixed_mig"); got.Status != StatusUnsupported {
		t.Fatalf("host:mixed_mig status = %s, want unsupported (%s)", got.Status, got.Reason)
	}
}

func TestProbeHostMIGExplicitConfigureAllowsProfileOnlyHosts(t *testing.T) {
	runner := migProbeRunner(
		"GPU 0: NVIDIA B200 (UUID: GPU-0)\nGPU 1: NVIDIA B200 (UUID: GPU-1)\n",
		"0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n",
		"MIG 1g.23gb 19\n",
	)

	result := ProbeHost(context.Background(), runner, ProbeOptions{MIGConfigure: "true"})

	if got := result.Snapshot.Lookup("host:mig"); got.Status != StatusSupported {
		t.Fatalf("host:mig status = %s, want supported (%s)", got.Status, got.Reason)
	}
	if got := result.Snapshot.Lookup("host:mixed_mig"); got.Status != StatusSupported {
		t.Fatalf("host:mixed_mig status = %s, want supported (%s)", got.Status, got.Reason)
	}
}

func TestProbeHostMIGAutoAllowsExistingInstances(t *testing.T) {
	runner := migProbeRunner(
		"GPU 0: NVIDIA B200 (UUID: GPU-0)\n  MIG 1g.23gb     Device  0: (UUID: MIG-7f37c7aa-357a-5286-8db6-584f7500ad74)\nGPU 1: NVIDIA B200 (UUID: GPU-1)\nGPU 2: NVIDIA B200 (UUID: GPU-2)\nGPU 3: NVIDIA B200 (UUID: GPU-3)\nGPU 4: NVIDIA B200 (UUID: GPU-4)\nGPU 5: NVIDIA B200 (UUID: GPU-5)\nGPU 6: NVIDIA B200 (UUID: GPU-6)\nGPU 7: NVIDIA B200 (UUID: GPU-7)\n",
		"0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Enabled, Enabled\n1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n2, NVIDIA B200, GPU-2, 595.58.03, 10.0, Disabled, Disabled\n3, NVIDIA B200, GPU-3, 595.58.03, 10.0, Disabled, Disabled\n4, NVIDIA B200, GPU-4, 595.58.03, 10.0, Disabled, Disabled\n5, NVIDIA B200, GPU-5, 595.58.03, 10.0, Disabled, Disabled\n6, NVIDIA B200, GPU-6, 595.58.03, 10.0, Disabled, Disabled\n7, NVIDIA B200, GPU-7, 595.58.03, 10.0, Disabled, Disabled\n",
		"MIG 1g.23gb 19\n",
	)

	result := ProbeHost(context.Background(), runner, ProbeOptions{MIGConfigure: "auto", DCGMImage: "dcgm:test"})

	if got := result.Snapshot.Lookup("host:mig"); got.Status != StatusSupported {
		t.Fatalf("host:mig status = %s, want supported (%s)", got.Status, got.Reason)
	}
	if got := result.Snapshot.Lookup("host:mixed_mig"); got.Status != StatusSupported {
		t.Fatalf("host:mixed_mig status = %s, want supported (%s)", got.Status, got.Reason)
	}
	if got := result.Snapshot.Lookup("host:mig_instance_entity"); got.Status != StatusUnsupported || !strings.Contains(got.Reason, "DCGM did not report") {
		t.Fatalf("host:mig_instance_entity = %#v, want DCGM hierarchy unsupported", got)
	}
}

func TestProbeHostMixedMIGRequiresCurrentFullGPU(t *testing.T) {
	runner := migProbeRunner(
		"GPU 0: NVIDIA B200 (UUID: GPU-0)\n  MIG 1g.23gb Device 0: (UUID: MIG-GPU-0/1/0)\nGPU 1: NVIDIA B200 (UUID: GPU-1)\n  MIG 1g.23gb Device 0: (UUID: MIG-GPU-1/1/0)\n",
		"0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Enabled, Enabled\n1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Enabled, Enabled\n",
		"MIG 1g.23gb 19\n",
	)

	result := ProbeHost(context.Background(), runner, ProbeOptions{MIGConfigure: "auto"})

	if got := result.Snapshot.Lookup("host:mixed_mig"); got.Status != StatusUnsupported || !strings.Contains(got.Reason, "no full GPU") {
		t.Fatalf("host:mixed_mig = %#v, want unsupported without a current full GPU", got)
	}
}

func TestProbeHostMIGExplicitConfigureUsesPerGPUProfiles(t *testing.T) {
	runner := migProbeRunner(
		"GPU 0: NVIDIA B200 (UUID: GPU-0)\nGPU 1: NVIDIA B200 (UUID: GPU-1)\n",
		"0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n1, NVIDIA B200, GPU-1, 595.58.03, 10.0, Disabled, Disabled\n",
		"No MIG-supported devices found.\n",
	)
	runner.outputs["nvidia-smi mig -i 0 -lgip"] = "MIG 1g.23gb 19\n"

	result := ProbeHost(context.Background(), runner, ProbeOptions{MIGConfigure: "true"})

	if got := result.Snapshot.Lookup("host:mig"); got.Status != StatusSupported {
		t.Fatalf("host:mig status = %s, want supported (%s)", got.Status, got.Reason)
	}
	if got := result.Snapshot.Lookup("host:mig"); got.Evidence != "MIG 1g.23gb 19" {
		t.Fatalf("host:mig evidence = %q, want per-GPU profile", got.Evidence)
	}
}

func TestSnapshotLookupUnknown(t *testing.T) {
	got := NewSnapshot(nil).Lookup("host:nvlink")
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %s, want unknown", got.Status)
	}
}

func TestProbeHostWithDCGMContainerFixtures(t *testing.T) {
	runner := &probeRunner{
		outputs: map[string]string{
			"nvidia-smi -L": "GPU 0: NVIDIA H100 (UUID: GPU-0)\n  MIG 1g.10gb Device 0: (UUID: MIG-GPU-0/1/0)\nGPU 1: NVIDIA H100 (UUID: GPU-1)\n",
			"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": "0, NVIDIA H100, GPU-0, 595.71.05, 9.0, Enabled, Enabled\n1, NVIDIA H100, GPU-1, 595.71.05, 9.0, Disabled, Disabled\n",
			"nvidia-smi topo -m":   "GPU0 GPU1 CPU Affinity\nGPU0 X NV4\nGPU1 NV4 X\n",
			"nvidia-smi nvlink -s": "Link 0: 25.781 GB/s\n",
			"nvidia-smi -q":        "GPU C2C Mode : Enabled\n",
			"lscpu":                "Model name: NVIDIA Grace CPU\n",
			"nvidia-smi mig -lgip": "MIG 1g.10gb 19\n",
		},
		dcgm: map[string]string{
			"version":                       "Version : 4.5.3\n",
			"discovery -l":                  "1 GPU found.\n1 NVSwitch found.\nGrace CPU hierarchy present.\n",
			"discovery --compute-hierarchy": "I 0/7 GPU Instance (EntityID: 101)\n",
			"profile --list":                "DCGM_FI_PROF_GR_ENGINE_ACTIVE\nc2c_tx_bytes\n",
			"dmon -l":                       "201 DCGM_FI_DEV_ECC_SBE_VOL_TOTAL\n900 DCGM_FI_DEV_C2C_TX_BYTES\n",
			"dmon -e 201 -c 1":              "Field is not supported on this GPU\n",
			"dmon -e 409 -c 1":              "GPU 0 0\n",
		},
	}

	result := ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage:           "dcgm:test",
		ExpectedDCGMVersion: "4.5.3",
		FailureInjection:    true,
	})

	for _, name := range []string{
		"host:gpu",
		"host:profiling",
		"host:mig",
		"host:mixed_mig",
		"host:mig_instance_entity",
		"host:unsupported_field",
		"host:nvlink",
		"host:p2p",
		"host:nvswitch",
		"host:grace_cpu",
		"host:c2c",
		"dcgm:remote_dcgm",
		"dcgm:failure_injection",
		"dcgm:failure_injection_nvlink_crc",
	} {
		if got := result.Snapshot.Lookup(name); got.Status != StatusSupported {
			t.Fatalf("%s status = %s, want supported (%s)", name, got.Status, got.Reason)
		}
	}
	if result.MIGInstanceEntityID != "101" || result.MIGInstanceNVMLID != "7" {
		t.Fatalf("MIG selection = entity %q nvml %q, want 101/7", result.MIGInstanceEntityID, result.MIGInstanceNVMLID)
	}
	if result.UnsupportedFieldCandidate != "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL" {
		t.Fatalf("unsupported field candidate = %q", result.UnsupportedFieldCandidate)
	}
	if !runner.sawDockerProbe("discovery -l") || !runner.sawDockerProbe("dmon -e 201 -c 1") || !runner.sawDockerProbe("dmon -e 409 -c 1") {
		t.Fatalf("DCGM docker probes missing: %#v", runner.dockerProbes)
	}
}

func TestProbeHostPassesDockerEnvToDCGMProbes(t *testing.T) {
	runner := nvlinkFailureInjectionRunner("GPU 0 0\n")

	_ = ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage: "dcgm:test",
		DockerEnv: []string{"DOCKER_CONFIG=/tmp/e2e-docker"},
	})

	if len(runner.dockerEnvs) == 0 {
		t.Fatal("expected docker probe commands")
	}
	for _, env := range runner.dockerEnvs {
		if !hasArg(env, "DOCKER_CONFIG=/tmp/e2e-docker") {
			t.Fatalf("docker env = %#v, want DOCKER_CONFIG", env)
		}
	}
}

func TestProbeHostNVLinkFailureInjectionSkipsDCGMNotSupported(t *testing.T) {
	runner := &probeRunner{
		outputs: map[string]string{
			"nvidia-smi -L": "GPU 0: NVIDIA B200 (UUID: GPU-0)\n",
			"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n",
			"nvidia-smi topo -m":   "GPU0 X\n",
			"nvidia-smi nvlink -s": "Link 0: 53.125 GB/s\n",
			"nvidia-smi -q":        "",
			"lscpu":                "Architecture: x86_64\n",
			"nvidia-smi mig -lgip": "No MIG-supported devices found.\n",
		},
		dcgm: map[string]string{
			"version":                       "Version : 4.5.3\n",
			"discovery -l":                  "1 GPU found.\n",
			"discovery --compute-hierarchy": "",
			"profile --list":                "",
			"dmon -l":                       "409 DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL\n",
			"dmon -e 409 -c 1":              "GPU 0 9223372036854775794\n",
		},
	}

	result := ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage:           "dcgm:test",
		ExpectedDCGMVersion: "4.5.3",
		FailureInjection:    true,
	})

	got := result.Snapshot.Lookup("dcgm:failure_injection_nvlink_crc")
	if got.Status != StatusUnsupported {
		t.Fatalf("status = %s, want unsupported", got.Status)
	}
	if !got.DCGMNotSupported {
		t.Fatal("expected DCGMNotSupported evidence")
	}
	if !strings.Contains(got.Reason, "DCGM_FT_INT64_NOT_SUPPORTED") {
		t.Fatalf("reason = %q, want decoded sentinel", got.Reason)
	}
}

func TestProbeHostNVLinkFailureInjectionSkipsUnreadableSamples(t *testing.T) {
	runner := nvlinkFailureInjectionRunner("GPU 0 N/A\n")

	result := ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage:           "dcgm:test",
		ExpectedDCGMVersion: "4.5.3",
		FailureInjection:    true,
	})

	got := result.Snapshot.Lookup("dcgm:failure_injection_nvlink_crc")
	if got.Status != StatusUnsupported || !got.DCGMNotSupported {
		t.Fatalf("capability = %#v, want DCGM unsupported", got)
	}
}

func TestProbeHostNVLinkFailureInjectionUsesListedFieldID(t *testing.T) {
	runner := nvlinkFailureInjectionRunner("")
	runner.dcgm["dmon -l"] = "510 DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL\n"
	runner.dcgm["dmon -e 510 -c 1"] = "GPU 0 0\n"
	delete(runner.dcgm, "dmon -e 409 -c 1")

	result := ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage:           "dcgm:test",
		ExpectedDCGMVersion: "4.5.3",
		FailureInjection:    true,
	})

	got := result.Snapshot.Lookup("dcgm:failure_injection_nvlink_crc")
	if got.Status != StatusSupported {
		t.Fatalf("status = %s, want supported (%s)", got.Status, got.Reason)
	}
	if got.Source != "DCGM container dmon -e 510" {
		t.Fatalf("source = %q, want listed field id", got.Source)
	}
	if !runner.sawDockerProbe("dmon -e 510 -c 1") {
		t.Fatalf("listed DCGM field probe missing: %#v", runner.dockerProbes)
	}
	if runner.sawDockerProbe("dmon -e 409 -c 1") {
		t.Fatalf("fallback field probe ran despite listed field id: %#v", runner.dockerProbes)
	}
}

func nvlinkFailureInjectionRunner(readback string) *probeRunner {
	return &probeRunner{
		outputs: map[string]string{
			"nvidia-smi -L": "GPU 0: NVIDIA B200 (UUID: GPU-0)\n",
			"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": "0, NVIDIA B200, GPU-0, 595.58.03, 10.0, Disabled, Disabled\n",
			"nvidia-smi topo -m":   "GPU0 X\n",
			"nvidia-smi nvlink -s": "Link 0: 53.125 GB/s\n",
			"nvidia-smi -q":        "",
			"lscpu":                "Architecture: x86_64\n",
			"nvidia-smi mig -lgip": "No MIG-supported devices found.\n",
		},
		dcgm: map[string]string{
			"version":                       "Version : 4.5.3\n",
			"discovery -l":                  "1 GPU found.\n",
			"discovery --compute-hierarchy": "",
			"profile --list":                "",
			"dmon -l":                       "409 DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL\n",
			"dmon -e 409 -c 1":              readback,
		},
	}
}

func TestProbeHostRejectsMismatchedRemoteDCGMVersion(t *testing.T) {
	runner := &probeRunner{
		outputs: map[string]string{
			"nvidia-smi -L": "GPU 0: NVIDIA Test GPU (UUID: GPU-0)\n",
			"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": "0, NVIDIA Test GPU, GPU-0, 595.71.05, 9.0, [N/A], [N/A]\n",
			"nvidia-smi topo -m":   "GPU0 CPU Affinity\n",
			"nvidia-smi nvlink -s": "",
			"nvidia-smi -q":        "",
			"lscpu":                "Architecture: x86_64\n",
			"nvidia-smi mig -lgip": "No MIG-supported devices found.\n",
		},
		dcgm: map[string]string{
			"version":                       "Version : 4.5.2\n",
			"discovery -l":                  "1 GPU found.\n",
			"discovery --compute-hierarchy": "",
			"profile --list":                "",
			"dmon -l":                       "",
		},
	}

	result := ProbeHost(context.Background(), runner, ProbeOptions{
		DCGMImage:           "dcgm:test",
		ExpectedDCGMVersion: "4.5.3",
		FailureInjection:    true,
	})

	remote := result.Snapshot.Lookup("dcgm:remote_dcgm")
	if remote.Status != StatusUnsupported {
		t.Fatalf("remote DCGM status = %s, want unsupported", remote.Status)
	}
	if !strings.Contains(remote.Reason, "does not match required 4.5.3") {
		t.Fatalf("remote DCGM reason = %q", remote.Reason)
	}
	injection := result.Snapshot.Lookup("dcgm:failure_injection")
	if injection.Status != StatusUnsupported || !strings.Contains(injection.Reason, "verified remote DCGM") {
		t.Fatalf("failure injection capability = %#v", injection)
	}
}

func TestRemoteDCGMRejectsUnparseableRequiredVersion(t *testing.T) {
	got := remoteDcgmCapability(
		ProbeOptions{DCGMImage: "dcgm:test", ExpectedDCGMVersion: "4.5.3"},
		"nv-hostengine build information\n",
		"1 GPU found.\n",
	)

	if got.Status != StatusUnsupported || !strings.Contains(got.Reason, "could not be parsed") {
		t.Fatalf("remote DCGM capability = %#v", got)
	}
}

func TestProbeRunnerRejectsUnexpectedCommand(t *testing.T) {
	runner := &probeRunner{outputs: map[string]string{}}
	result := runner.Run(context.Background(), e2eexec.Command{Name: "nvidia-smi", Args: []string{"unexpected"}})
	if result.ExitCode == 0 || !strings.Contains(string(result.Stderr), "unexpected probe command") {
		t.Fatalf("unexpected command result = %#v", result)
	}
}

type probeRunner struct {
	outputs      map[string]string
	dcgm         map[string]string
	dockerProbes []string
	dockerEnvs   [][]string
}

func migProbeRunner(nvidiaL, query, migProfiles string) *probeRunner {
	outputs := map[string]string{
		"nvidia-smi -L": nvidiaL,
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": query,
		"nvidia-smi topo -m":   "GPU0 GPU1 CPU Affinity\nGPU0 X SYS\nGPU1 SYS X\n",
		"nvidia-smi nvlink -s": "",
		"nvidia-smi -q":        "",
		"lscpu":                "Architecture: x86_64\n",
		"nvidia-smi mig -lgip": migProfiles,
	}
	for _, index := range gpuIndexes(query) {
		outputs["nvidia-smi mig -i "+index+" -lgip"] = migProfiles
	}
	return &probeRunner{outputs: outputs}
}

func (r *probeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name == "docker" {
		return r.runDocker(command)
	}
	key := strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
	output, ok := r.outputs[key]
	if !ok {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("error: unexpected probe command: " + key)}
	}
	return e2eexec.Result{Stdout: []byte(output)}
}

func (r *probeRunner) runDocker(command e2eexec.Command) e2eexec.Result {
	args := command.Args
	r.dockerEnvs = append(r.dockerEnvs, command.Env)
	if hasArg(args, "--entrypoint") && hasArg(args, "/usr/bin/nv-hostengine") {
		return e2eexec.Result{Stdout: []byte(r.dcgm["version"])}
	}
	for i, arg := range args {
		if arg == "dcgmi-probe" && i+1 < len(args) {
			probe := strings.Join(args[i+1:], " ")
			r.dockerProbes = append(r.dockerProbes, probe)
			return e2eexec.Result{Stdout: []byte(r.dcgm[probe])}
		}
	}
	return e2eexec.Result{ExitCode: 1, Stderr: []byte("unexpected docker invocation")}
}

func (r *probeRunner) sawDockerProbe(probe string) bool {
	for _, got := range r.dockerProbes {
		if got == probe {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
