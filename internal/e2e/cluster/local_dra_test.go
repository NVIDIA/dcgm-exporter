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

package cluster

import (
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	standardDRAResourceSlice = `{
	  "items": [{
	    "spec": {
	      "driver": "gpu.nvidia.com",
	      "devices": [{"name": "gpu-0", "attributes": {"type": {"string": "gpu"}}}]
	    }
	  }]
	}`
	standardDRAResourceSliceV1Beta1 = `{
	  "items": [{
	    "spec": {
	      "driver": "gpu.nvidia.com",
	      "devices": [{
	        "name": "gpu-0",
	        "basic": {"attributes": {"type": {"string": "gpu"}}}
	      }]
	    }
	  }]
	}`
)

func TestNVIDIAResourceSliceClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		data        string
		wantFullGPU bool
	}{
		{
			name:        "standard NVIDIA",
			data:        standardDRAResourceSlice,
			wantFullGPU: true,
		},
		{
			name:        "standard NVIDIA v1beta1",
			data:        standardDRAResourceSliceV1Beta1,
			wantFullGPU: true,
		},
		{
			name: "NVIDIA MIG device",
			data: `{"items":[{"spec":{"driver":"gpu.nvidia.com","devices":[{"attributes":{"type":{"string":"mig"}}}]}}]}`,
		},
		{
			name: "unrelated driver",
			data: `{"items":[{"spec":{"driver":"example.com","devices":[{"name":"gpu-0","attributes":{"type":{"string":"gpu"}}}]}}]}`,
		},
		{
			name: "empty NVIDIA slice",
			data: `{"items":[{"spec":{"driver":"gpu.nvidia.com","devices":[]}}]}`,
		},
		{name: "malformed", data: "not-json"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := []byte(test.data)
			if got := hasNVIDIAFullGPUResourceSlice(data); got != test.wantFullGPU {
				t.Errorf("hasNVIDIAFullGPUResourceSlice() = %t, want %t", got, test.wantFullGPU)
			}
		})
	}
}

// noResourceSlices is kubectl's ResourceSlice list output for a cluster that publishes none.
const noResourceSlices = `{"items":[]}`

// The DRA setup and cleanup commands, as recordingRunner keys them, for a one-second wait timeout.
const (
	draResourceSlicesCommand  = "kubectl get resourceslices -o json"
	draLabelCommand           = "kubectl label nodes --all nvidia.com/dra-kubelet-plugin=true --overwrite"
	draUnlabelCommand         = "kubectl label nodes --all nvidia.com/dra-kubelet-plugin- --overwrite"
	draUninstallCommand       = "helm uninstall nvidia-dra-driver-gpu --namespace nvidia-dra-driver-gpu --ignore-not-found --wait --timeout 1s"
	draNamespaceLookupCommand = "kubectl get namespace nvidia-dra-driver-gpu --ignore-not-found=true -o json"
	draNamespaceDeleteCommand = "kubectl delete namespace nvidia-dra-driver-gpu --ignore-not-found=true"
	draInstallCommand         = "helm upgrade --install nvidia-dra-driver-gpu nvidia/nvidia-dra-driver-gpu" +
		" --namespace nvidia-dra-driver-gpu --version 25.8.0" +
		" --set resources.gpus.enabled=true --set resources.computeDomains.enabled=false" +
		" --set gpuResourcesEnabledOverride=true --set nvidiaDriverRoot=/ --wait --timeout 1s"
)

// draSetupForTest returns an external-cluster config and the DRA settings that make PrepareDRA install the driver.
func draSetupForTest(t *testing.T) (Config, FeatureConfig) {
	t.Helper()
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	featureCfg := FeatureConfig{DRAConfigure: config.FeatureOn, WaitTimeout: "1s"}
	featureCfg.Dependencies.DRADriverVersion = "25.8.0"
	return cfg, featureCfg
}

// commandKeys renders commands the way recordingRunner keys its configured results.
func commandKeys(commands []e2eexec.Command) []string {
	keys := make([]string, 0, len(commands))
	for _, command := range commands {
		keys = append(keys, command.Name+" "+strings.Join(command.Args, " "))
	}
	return keys
}

// TestPrepareDRAReturnsCleanupForPartialSetup checks that a failed setup step comes with the cleanup of
// everything set up so far, so the group runner can undo it after collecting diagnostics.
func TestPrepareDRAReturnsCleanupForPartialSetup(t *testing.T) {
	for _, test := range []struct {
		name        string
		failing     string
		wantErr     string
		wantCleanup []string
	}{
		{
			name:        "label fails",
			failing:     draLabelCommand,
			wantErr:     "label nodes for the NVIDIA DRA driver",
			wantCleanup: []string{draUnlabelCommand},
		},
		{
			name:    "install fails",
			failing: draInstallCommand,
			wantErr: "injected setup failure",
			wantCleanup: []string{
				draNamespaceLookupCommand, draUninstallCommand,
				draNamespaceLookupCommand, draNamespaceDeleteCommand,
				draUnlabelCommand,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, featureCfg := draSetupForTest(t)
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				draResourceSlicesCommand: {Stdout: []byte(noResourceSlices)},
				test.failing:             {ExitCode: 1, Stderr: []byte("injected setup failure")},
			}}

			cleanup, err := PrepareDRA(context.Background(), runner, io.Discard, cfg, featureCfg)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("PrepareDRA() error = %v, want %q", err, test.wantErr)
			}

			checkCleanupCommands(t, runner, cleanup, test.wantCleanup)
		})
	}
}

// checkCleanupCommands runs cleanup and fails the test unless it succeeds and
// runs exactly want, in order, after the commands runner has already recorded.
func checkCleanupCommands(t *testing.T, runner *recordingRunner, cleanup func(context.Context) error, want []string) {
	t.Helper()
	setupCommands := len(runner.commands)

	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}

	if got := commandKeys(runner.commands[setupCommands:]); !slices.Equal(got, want) {
		t.Fatalf("cleanup commands = %q, want %q", got, want)
	}
}

// TestPrepareDRACleanupReportsEveryFailure checks that cleanup attempts every step and reports each failure.
func TestPrepareDRACleanupReportsEveryFailure(t *testing.T) {
	cfg, featureCfg := draSetupForTest(t)
	runner := &recordingRunner{resultSequences: map[string][]e2eexec.Result{
		draResourceSlicesCommand: {
			{Stdout: []byte(noResourceSlices)},
			{Stdout: []byte(standardDRAResourceSlice)},
		},
	}}
	cleanup, err := PrepareDRA(context.Background(), runner, io.Discard, cfg, featureCfg)
	if err != nil {
		t.Fatalf("PrepareDRA() error = %v", err)
	}

	runner.results = map[string]e2eexec.Result{
		draUninstallCommand: {ExitCode: 1, Stderr: []byte("injected uninstall failure")},
		draUnlabelCommand:   {ExitCode: 1, Stderr: []byte("injected unlabel failure")},
	}
	err = cleanup(context.Background())
	for _, want := range []string{
		"uninstall release nvidia-dra-driver-gpu", "injected uninstall failure",
		"remove NVIDIA DRA node labels", "injected unlabel failure",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("cleanup() error = %v, want %q", err, want)
		}
	}
	if !runner.deletedNamespace(draDriverNamespace) {
		t.Fatalf("cleanup skipped the DRA namespace after a failed uninstall: %#v", commandKeys(runner.commands))
	}
}

// TestPrepareDRARefusesStateItDoesNotOwn checks that DRA setup stops before changing a node label or a
// namespace this run cannot prove it owns, and that its cleanup then leaves both alone.
func TestPrepareDRARefusesStateItDoesNotOwn(t *testing.T) {
	const labelLookupCommand = "kubectl get nodes -l nvidia.com/dra-kubelet-plugin -o name"
	for _, test := range []struct {
		name        string
		results     map[string]e2eexec.Result
		wantErr     string
		wantLabel   bool
		wantCleanup []string
	}{
		{
			name:    "label already set",
			results: map[string]e2eexec.Result{labelLookupCommand: {Stdout: []byte("node/gpu-0\n")}},
			wantErr: `label "nvidia.com/dra-kubelet-plugin" is already set on node/gpu-0`,
		},
		{
			name:    "label lookup fails",
			results: map[string]e2eexec.Result{labelLookupCommand: {ExitCode: 1, Stderr: []byte("nodes is forbidden")}},
			wantErr: "nodes is forbidden",
		},
		{
			name: "namespace owned by another run",
			results: map[string]e2eexec.Result{
				"kubectl create -f -":     {ExitCode: 1, Stderr: []byte(`namespaces "nvidia-dra-driver-gpu" already exists`)},
				draNamespaceLookupCommand: namespaceResult(ownedNamespaceLabels("another-run")),
			},
			wantErr:     `namespace "nvidia-dra-driver-gpu" already exists and is not owned by run`,
			wantLabel:   true,
			wantCleanup: []string{draUnlabelCommand},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, featureCfg := draSetupForTest(t)
			runner := &recordingRunner{results: maps.Clone(test.results)}
			runner.results[draResourceSlicesCommand] = e2eexec.Result{Stdout: []byte(noResourceSlices)}

			cleanup, err := PrepareDRA(context.Background(), runner, io.Discard, cfg, featureCfg)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("PrepareDRA() error = %v, want %q", err, test.wantErr)
			}
			if labeled := runner.commandIndex("kubectl", "label", "nodes", "--all", draNodePluginLabelKey+"=true") >= 0; labeled != test.wantLabel {
				t.Fatalf("nodes labeled = %t, want %t; commands = %q", labeled, test.wantLabel, commandKeys(runner.commands))
			}
			if runner.commandIndex("helm", "upgrade", "--install", draDriverReleaseName) >= 0 {
				t.Fatalf("DRA driver installed into state this run does not own: %q", commandKeys(runner.commands))
			}

			checkCleanupCommands(t, runner, cleanup, test.wantCleanup)
		})
	}
}

// TestPrepareDRAStopsWhenResourceSliceLookupFails checks that a failed or unreadable ResourceSlice
// lookup stops DRA setup with that error before any node is labeled or the driver is installed.
func TestPrepareDRAStopsWhenResourceSliceLookupFails(t *testing.T) {
	for _, test := range []struct {
		name    string
		lookup  e2eexec.Result
		wantErr string
	}{
		{
			name:    "lookup fails",
			lookup:  e2eexec.Result{ExitCode: 1, Stderr: []byte("resourceslices is forbidden")},
			wantErr: "resourceslices is forbidden",
		},
		{
			name:    "output is not a ResourceSlice list",
			lookup:  e2eexec.Result{Stdout: []byte("not json")},
			wantErr: "is not a ResourceSlice list",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, featureCfg := draSetupForTest(t)
			runner := &recordingRunner{results: map[string]e2eexec.Result{draResourceSlicesCommand: test.lookup}}

			_, err := PrepareDRA(context.Background(), runner, io.Discard, cfg, featureCfg)

			if err == nil || !strings.Contains(err.Error(), "check NVIDIA DRA ResourceSlices before setup") ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("PrepareDRA() error = %v, want the lookup failure %q", err, test.wantErr)
			}
			if got := commandKeys(runner.commands); !slices.Equal(got, []string{draResourceSlicesCommand}) {
				t.Fatalf("commands = %q, want only the ResourceSlice lookup", got)
			}
		})
	}
}

// TestWaitForDRAResourcesTimeoutNamesOnlyTheLastLookupFailure checks that a wait that times out while the
// lookup keeps failing names that failure, and that a later lookup returning a valid empty list clears it.
func TestWaitForDRAResourcesTimeoutNamesOnlyTheLastLookupFailure(t *testing.T) {
	lookupFailure := e2eexec.Result{ExitCode: 1, Stderr: []byte("connection refused")}
	for _, test := range []struct {
		name          string
		laterLookups  e2eexec.Result
		wantLastError bool
	}{
		{name: "lookup keeps failing", laterLookups: lookupFailure, wantLastError: true},
		{name: "failure then empty list", laterLookups: e2eexec.Result{Stdout: []byte(noResourceSlices)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, _ := draSetupForTest(t)
			runner := &recordingRunner{
				resultSequences: map[string][]e2eexec.Result{draResourceSlicesCommand: {lookupFailure}},
				results:         map[string]e2eexec.Result{draResourceSlicesCommand: test.laterLookups},
			}

			err := WaitForDRAResources(context.Background(), runner, cfg, "50ms")

			if err == nil || !strings.Contains(err.Error(), "timed out waiting for NVIDIA DRA resources") {
				t.Fatalf("WaitForDRAResources() error = %v, want a timeout", err)
			}
			if got := strings.Contains(err.Error(), "last lookup failed: "); got != test.wantLastError {
				t.Fatalf("WaitForDRAResources() error = %v, names a lookup failure = %t, want %t", err, got, test.wantLastError)
			}
			if lookups := len(runner.commands); lookups < 2 {
				t.Fatalf("WaitForDRAResources() looked up ResourceSlices %d times, want at least 2", lookups)
			}
		})
	}
}

// TestPrepareDRARequiresDriverVersionBeforeLabeling checks that a missing NVIDIA_DRA_DRIVER_VERSION stops
// DRA setup after the ResourceSlice lookup and before any node is labeled.
func TestPrepareDRARequiresDriverVersionBeforeLabeling(t *testing.T) {
	cfg, featureCfg := draSetupForTest(t)
	featureCfg.Dependencies.DRADriverVersion = " "
	runner := &recordingRunner{results: map[string]e2eexec.Result{draResourceSlicesCommand: {Stdout: []byte(noResourceSlices)}}}

	_, err := PrepareDRA(context.Background(), runner, io.Discard, cfg, featureCfg)

	if err == nil || !strings.Contains(err.Error(), "NVIDIA_DRA_DRIVER_VERSION is required") {
		t.Fatalf("PrepareDRA() error = %v, want the missing version", err)
	}
	if got := commandKeys(runner.commands); !slices.Equal(got, []string{draResourceSlicesCommand}) {
		t.Fatalf("commands = %q, want only the ResourceSlice lookup", got)
	}
}
