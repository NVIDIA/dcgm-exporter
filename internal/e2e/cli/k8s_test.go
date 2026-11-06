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
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// migHostSnapshot builds a minimal stand-in for the host MIG entries that ProbeHost reports: the profile inventory,
// the current state and whether the host has two or more GPUs. Both state entries share one reason.
func migHostSnapshot(profiles, active, mixed, multiGPU capability.Status, reason string) capability.Snapshot {
	return capability.NewSnapshot([]capability.Capability{
		{Name: "host:mig", Status: profiles},
		{Name: "host:multi_gpu", Status: multiGPU},
		{Name: "host:mig_active", Status: active, Reason: reason},
		{Name: "host:mixed_mig", Status: mixed, Reason: reason},
	})
}

// TestLocalKubernetesPlanningCapabilitiesGateMIGOnCurrentState checks which MIG scenarios the planner keeps, skips or fails for each MIG state.
func TestLocalKubernetesPlanningCapabilitiesGateMIGOnCurrentState(t *testing.T) {
	const (
		supported   = capability.StatusSupported
		unsupported = capability.StatusUnsupported
		unknown     = capability.StatusUnknown
		rerun       = "--mig-configure=auto leaves MIG as it is; rerun with --mig-configure=true or a profile ID to configure MIG on GPU 0 and restore it afterward"
	)
	tests := []struct {
		name         string
		host         capability.Snapshot
		migConfigure string
		wantMIG      scenario.Outcome
		wantMixed    scenario.Outcome
		wantErr      string
		wantReason   string
		wantAbsent   string
	}{
		{
			name:         "MIG on with an instance beside a full GPU",
			host:         migHostSnapshot(supported, supported, supported, supported, "ok"),
			migConfigure: "auto",
			wantMIG:      scenario.OutcomeSelected,
			wantMixed:    scenario.OutcomeSelected,
		},
		{
			name:         "every GPU in MIG mode",
			host:         migHostSnapshot(supported, supported, unsupported, supported, "MIG is on for every GPU; mixed MIG needs a GPU with MIG off"),
			migConfigure: "auto",
			wantMIG:      scenario.OutcomeSelected,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "mixed MIG needs a GPU with MIG off",
			wantAbsent:   "--mig-configure",
		},
		{
			name:         "MIG off on every GPU",
			host:         migHostSnapshot(supported, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "auto",
			wantMIG:      scenario.OutcomeSkipped,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "MIG is off on every GPU; " + rerun,
		},
		{
			name:         "no MIG profiles, change requested",
			host:         migHostSnapshot(unsupported, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "true",
			wantMIG:      scenario.OutcomeSkipped,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "GPU does not advertise MIG profiles",
			wantAbsent:   "--mig-configure",
		},
		{
			name:         "no MIG profiles, default value",
			host:         migHostSnapshot(unsupported, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "auto",
			wantMIG:      scenario.OutcomeSkipped,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "GPU does not advertise MIG profiles",
			wantAbsent:   "rerun",
		},
		{
			name:         "single GPU in MIG mode, default value",
			host:         migHostSnapshot(supported, supported, unsupported, unsupported, "MIG is on for every GPU; mixed MIG needs a GPU with MIG off"),
			migConfigure: "auto",
			wantMIG:      scenario.OutcomeSelected,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "mixed MIG needs at least two GPUs",
			wantAbsent:   "rerun",
		},
		{
			name:         "empty flag value reads as auto",
			host:         migHostSnapshot(supported, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "",
			wantMIG:      scenario.OutcomeSkipped,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "MIG is off on every GPU; " + rerun,
		},
		{
			name:         "every GPU in MIG mode after a requested change",
			host:         migHostSnapshot(supported, supported, unsupported, supported, "MIG is on for every GPU; mixed MIG needs a GPU with MIG off"),
			migConfigure: "true",
			wantMIG:      scenario.OutcomeSelected,
			wantMixed:    scenario.OutcomeSkipped,
			wantReason:   "mixed MIG needs a GPU with MIG off",
			wantAbsent:   "--mig-configure",
		},
		{
			name:         "requested change left no GPU instance",
			host:         migHostSnapshot(supported, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "true",
			wantErr:      "MIG change requested; MIG is off on every GPU",
		},
		{
			name:         "requested change left MIG on with no GPU instance",
			host:         migHostSnapshot(supported, unsupported, unsupported, supported, "MIG is on but DCGM lists no GPU instance"),
			migConfigure: "true",
			wantErr:      "MIG change requested; MIG is on but DCGM lists no GPU instance",
		},
		{
			name:         "requested change with unknown MIG profiles",
			host:         migHostSnapshot(unknown, unsupported, unsupported, supported, "MIG is off on every GPU"),
			migConfigure: "true",
			wantErr:      "MIG change requested; MIG is off on every GPU",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := planMIGScenarios(test.host, test.migConfigure)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Select() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			checkMIGOutcomes(t, got, test.wantMIG, test.wantMixed)
			checkSkipReasons(t, got, test.wantReason, test.wantAbsent)
		})
	}
}

// TestLocalMIGAllocationCapabilityKeepsSingleGPUMixedSkip checks that a single-GPU host keeps the definite
// mixed-MIG skip even when a requested MIG change left MIG off.
func TestLocalMIGAllocationCapabilityKeepsSingleGPUMixedSkip(t *testing.T) {
	host := migHostSnapshot(capability.StatusSupported, capability.StatusUnsupported, capability.StatusUnsupported,
		capability.StatusUnsupported, "MIG is off on every GPU")

	got := localMIGAllocationCapability("mixed_mig_resources", host, hostMixedMIG, "true")

	if got.Status != capability.StatusUnsupported || got.Reason != "mixed MIG needs at least two GPUs" {
		t.Fatalf("mixed MIG capability = %s %q, want unsupported \"mixed MIG needs at least two GPUs\"", got.Status, got.Reason)
	}
}

// planMIGScenarios plans the k8s/mig and k8s/migFullGpuSelection scenarios for host and returns them by
// scenario name.
func planMIGScenarios(host capability.Snapshot, migConfigure string) (map[string]scenario.PlannedScenario, error) {
	caps := localKubernetesPlanningCapabilities(host, capability.NewSnapshot(nil), migConfigure)
	cfg := config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/mig", "k8s/migFullGpuSelection"}}}
	plan, err := scenario.Select(scenario.Catalog, caps, cfg)
	if err != nil {
		return nil, err
	}
	planned := map[string]scenario.PlannedScenario{}
	for _, item := range plan.Scenarios {
		planned[item.Scenario.Name] = item
	}
	return planned, nil
}

// checkMIGOutcomes fails the test when the MIG or mixed-MIG scenario outcome differs from the wanted one.
func checkMIGOutcomes(t *testing.T, planned map[string]scenario.PlannedScenario, wantMIG, wantMixed scenario.Outcome) {
	t.Helper()
	mig, mixed := planned["mig"].Outcome, planned["migFullGpuSelection"].Outcome
	if mig != wantMIG || mixed != wantMixed {
		t.Fatalf("outcomes = %q and %q, want %q and %q", mig, mixed, wantMIG, wantMixed)
	}
}

// checkSkipReasons fails the test when a skipped scenario's reason lacks want or
// contains absent (when absent is set).
func checkSkipReasons(t *testing.T, planned map[string]scenario.PlannedScenario, want, absent string) {
	t.Helper()
	for _, item := range planned {
		if item.Outcome != scenario.OutcomeSkipped {
			continue
		}
		if !strings.Contains(item.Reason, want) {
			t.Fatalf("%s skip reason = %q, want it to contain %q", item.Scenario.Name, item.Reason, want)
		}
		if absent != "" && strings.Contains(item.Reason, absent) {
			t.Fatalf("%s skip reason = %q, must not contain %q", item.Scenario.Name, item.Reason, absent)
		}
	}
}

// TestLocalK8sRunPreparesMIGBeforePlanningProbe checks MIG preparation runs before the plan-time MIG state probe.
func TestLocalK8sRunPreparesMIGBeforePlanningProbe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MIG preparation needs Linux")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	for _, dir := range []string{"tests/k8s", "deployment"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outputs := fakeK8sProbeOutputs()
	outputs["docker run --rm --entrypoint /usr/bin/nv-hostengine registry.example/dcgm@"+testImageDigest+" --version"] = []byte("Version : 4.5.3\n")
	runner := &fakeRunner{
		outputs: outputs,
		results: map[string]e2eexec.Result{"nvidia-smi mig -i 0 -lgip": {ExitCode: 6, Stderr: []byte("Not Supported")}},
	}

	var stdout, stderr bytes.Buffer
	// Every MIG scenario is skipped on this fake host, so the run fails with a setup error; only that failure is expected.
	err := runWithRootRunner(
		[]string{
			"tests", "--suite", "k8s", "--scenario", "k8s/mig",
			"--mig-configure", "19",
			"--exporter-image", "registry.example/dcgm-exporter:dev",
			"--dcgm-image", "registry.example/dcgm@" + testImageDigest,
			"--bootstrap=skip",
		},
		&stdout, &stderr, root, runner,
	)
	if code, ok := ExitCode(err); !ok || code != exitSetupFailure || !strings.Contains(err.Error(), "all selected Kubernetes scenarios were skipped") {
		t.Fatalf("run error = %v, want the all-scenarios-skipped setup failure", err)
	}
	prepared := firstCommandIndex(runner.commands, "nvidia-smi mig -i 0 -lgip")
	probed := firstCommandIndex(runner.commands,
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader")
	if prepared < 0 || probed < 0 || prepared > probed {
		t.Fatalf("MIG preparation command at %d, planning MIG probe at %d; want preparation first\nstdout:\n%s\nstderr:\n%s", prepared, probed, stdout.String(), stderr.String())
	}
}

// firstCommandIndex returns the index of the first command whose key is key, or -1.
func firstCommandIndex(commands []e2eexec.Command, key string) int {
	for index, command := range commands {
		if commandKey(command) == key {
			return index
		}
	}
	return -1
}

// TestHostUsesNVSDMFollowsHostCapability checks that only a supported host NVSDM capability selects the NVSDM libraries.
func TestHostUsesNVSDMFollowsHostCapability(t *testing.T) {
	tests := []struct {
		name string
		caps capability.Snapshot
		want bool
	}{
		{name: "supported", caps: capability.NewSnapshot([]capability.Capability{{Name: "host:nvswitch_nvsdm", Status: capability.StatusSupported}}), want: true},
		{name: "unsupported", caps: capability.NewSnapshot([]capability.Capability{{Name: "host:nvswitch_nvsdm", Status: capability.StatusUnsupported}})},
		{name: "not probed", caps: capability.NewSnapshot(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hostUsesNVSDM(test.caps); got != test.want {
				t.Fatalf("hostUsesNVSDM() = %t, want %t", got, test.want)
			}
		})
	}
}
