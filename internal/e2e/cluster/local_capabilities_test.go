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
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestParseNVIDIAAllocatableResources proves mixed-MIG means co-located ordinary-GPU and MIG resources on one node.
// Separate nodes must not qualify that workload because Kubernetes schedules one pod against one node.
func TestParseNVIDIAAllocatableResources(t *testing.T) {
	tests := []struct {
		name string
		data string
		want nvidiaAllocatableResources
		ok   bool
	}{
		{
			name: "ordinary GPU",
			data: `{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"2"}}}]}`,
			want: nvidiaAllocatableResources{GPU: 2},
			ok:   true,
		},
		{
			name: "MIG only",
			data: `{"items":[{"status":{"allocatable":{"nvidia.com/mig-1g.10gb":"7"}}}]}`,
			want: nvidiaAllocatableResources{MIG: 7},
			ok:   true,
		},
		{
			name: "mixed MIG on one node",
			data: `{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1","nvidia.com/mig-1g.10gb":"7"}}}]}`,
			want: nvidiaAllocatableResources{GPU: 1, MIG: 7, MixedMIG: true},
			ok:   true,
		},
		{
			name: "ordinary and MIG on separate nodes",
			data: `{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}},{"status":{"allocatable":{"nvidia.com/mig-1g.10gb":"7"}}}]}`,
			want: nvidiaAllocatableResources{GPU: 1, MIG: 7},
			ok:   true,
		},
		{name: "invalid response", data: `not JSON`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseNVIDIAAllocatableResources([]byte(test.data))
			if ok != test.ok || got != test.want {
				t.Fatalf("parseNVIDIAAllocatableResources() = (%#v, %t), want (%#v, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}

// TestNVIDIAAllocationCapabilitiesKeepsUnknownTargetEvidence verifies a failed node query does not become controller fallback evidence.
// Every allocation kind stays unknown until the selected Kubernetes target can answer the query.
func TestNVIDIAAllocationCapabilitiesKeepsUnknownTargetEvidence(t *testing.T) {
	capabilities := nvidiaAllocationCapabilities(e2eexec.Result{ExitCode: 1, Stderr: []byte("forbidden")})
	for _, entry := range capabilities {
		if entry.Status != capability.StatusUnknown {
			t.Fatalf("%s status = %s, want unknown", entry.Name, entry.Status)
		}
		if entry.Evidence != "forbidden" {
			t.Fatalf("%s evidence = %q, want target command error", entry.Name, entry.Evidence)
		}
	}
}

// TestNVIDIAAllocationCapabilitiesSummarizesNodeData reports compact allocatable counts without copying unrelated node metadata.
func TestNVIDIAAllocationCapabilitiesSummarizesNodeData(t *testing.T) {
	data := `{"items":[{"metadata":{"annotations":{"unrelated":"` + strings.Repeat("verbose node metadata", 1000) + `"}},"status":{"allocatable":{"nvidia.com/gpu":"2","nvidia.com/mig-1g.10gb":"7"}}}]}`
	capabilities := nvidiaAllocationCapabilities(e2eexec.Result{Stdout: []byte(data)})
	for _, entry := range capabilities {
		if entry.Status != capability.StatusSupported {
			t.Fatalf("%s status = %s, want supported", entry.Name, entry.Status)
		}
		if entry.Evidence != "allocatable GPU=2 MIG=7 mixed-MIG=true" {
			t.Fatalf("%s evidence = %q, want allocation summary", entry.Name, entry.Evidence)
		}
	}
}

// TestNVIDIAAllocationCapabilitiesKeepsMalformedTargetEvidence leaves allocation unknown and retains the target response for diagnosis.
func TestNVIDIAAllocationCapabilitiesKeepsMalformedTargetEvidence(t *testing.T) {
	capabilities := nvidiaAllocationCapabilities(e2eexec.Result{Stdout: []byte("invalid node JSON")})
	for _, entry := range capabilities {
		if entry.Status != capability.StatusUnknown {
			t.Fatalf("%s status = %s, want unknown", entry.Name, entry.Status)
		}
		if entry.Evidence != "invalid node JSON" {
			t.Fatalf("%s evidence = %q, want malformed response", entry.Name, entry.Evidence)
		}
	}
}

// TestDRAResourceCapabilityDistinguishesMalformedAndUnsupportedResourceSlices separates an unreadable response from valid evidence of missing resources.
func TestDRAResourceCapabilityDistinguishesMalformedAndUnsupportedResourceSlices(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		status   capability.Status
		reason   string
		evidence string
	}{
		{
			name:     "malformed response is unknown",
			output:   `{"items":[`,
			status:   capability.StatusUnknown,
			reason:   "ResourceSlice query returned invalid JSON",
			evidence: `{"items":[`,
		},
		{
			name:     "valid response without NVIDIA full GPU is unsupported",
			output:   `{"items":[]}`,
			status:   capability.StatusUnsupported,
			reason:   "does not report NVIDIA full-GPU DRA resources",
			evidence: `{"items":[]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				"kubectl get resourceslices -o json": {Stdout: []byte(test.output)},
			}}
			got := draResourceCapability(context.Background(), runner, Config{}, capability.Capability{Status: capability.StatusSupported})
			if got.Status != test.status {
				t.Fatalf("draResourceCapability() status = %s, want %s", got.Status, test.status)
			}
			if !strings.Contains(got.Reason, test.reason) {
				t.Fatalf("draResourceCapability() reason = %q, want it to contain %q", got.Reason, test.reason)
			}
			if got.Evidence != test.evidence {
				t.Fatalf("draResourceCapability() evidence = %q, want %q", got.Evidence, test.evidence)
			}
		})
	}
}

func TestProbeDualStackServices(t *testing.T) {
	tests := []struct {
		name         string
		result       e2eexec.Result
		wantStatus   capability.Status
		wantReason   string
		wantEvidence string
	}{
		{
			name:         "supported",
			result:       e2eexec.Result{Stdout: []byte(dualStackProbeResponse)},
			wantStatus:   capability.StatusSupported,
			wantReason:   "allocates dual-stack Services",
			wantEvidence: "IPv4 IPv6",
		},
		{
			name:         "single-stack rejection",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte(singleStackProbeError)},
			wantStatus:   capability.StatusUnsupported,
			wantReason:   "rejects dual-stack Services",
			wantEvidence: "not configured for dual-stack",
		},
		{
			name:         "older single-stack rejection",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte(`ipFamilyPolicy: Unsupported value: "RequireDualStack" on a single stack cluster`)},
			wantStatus:   capability.StatusUnsupported,
			wantReason:   "rejects dual-stack Services",
			wantEvidence: "single stack",
		},
		{
			name:         "forbidden",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte("services is forbidden")},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "probe failed",
			wantEvidence: "forbidden",
		},
		{
			name:         "transport failure",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte("connection refused")},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "probe failed",
			wantEvidence: "connection refused",
		},
		{
			name:         "invalid JSON",
			result:       e2eexec.Result{Stdout: []byte("not-json")},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "invalid JSON",
			wantEvidence: "invalid character",
		},
		{
			name:         "missing IPv6 family",
			result:       e2eexec.Result{Stdout: []byte(`{"spec":{"ipFamilies":["IPv4"]}}`)},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "without returning both IP families",
			wantEvidence: "IPv4",
		},
		{
			name:         "missing IPv4 family",
			result:       e2eexec.Result{Stdout: []byte(`{"spec":{"ipFamilies":["IPv6"]}}`)},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "without returning both IP families",
			wantEvidence: "IPv6",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				dualStackProbeCommand: test.result,
			}}
			cfg := DefaultConfig()
			cfg.Kubeconfig = "/tmp/probe-kubeconfig"

			got := probeDualStackServices(context.Background(), runner, cfg)

			if got.Status != test.wantStatus ||
				!strings.Contains(got.Reason, test.wantReason) ||
				!strings.Contains(got.Evidence, test.wantEvidence) {
				t.Fatalf("probeDualStackServices() = %#v, want status %q, reason containing %q, evidence containing %q", got, test.wantStatus, test.wantReason, test.wantEvidence)
			}
			if got.Source != dualStackServiceProbeSource {
				t.Fatalf("probe source = %q, want %q", got.Source, dualStackServiceProbeSource)
			}
			checkDualStackProbeCommand(t, runner)
		})
	}
}

// checkDualStackProbeCommand fails the test unless the recorded probe ran a server-side dry-run with the
// configured kubeconfig and sent a manifest for a generated dual-stack Service.
func checkDualStackProbeCommand(t *testing.T, runner *recordingRunner) {
	t.Helper()

	if !runner.has("kubectl", "create", "--dry-run=server", "-f", "-", "-o", "json") {
		t.Fatalf("probe did not use server-side dry-run: %#v", runner.commands)
	}
	if !runner.hasEnv("KUBECONFIG=/tmp/probe-kubeconfig") {
		t.Fatalf("probe did not use configured kubeconfig: %#v", runner.commands)
	}
	if !runner.stdinContains("ipFamilyPolicy: RequireDualStack") ||
		!runner.stdinContains("generateName: dcgm-exporter-ipv6-probe-") {
		t.Fatalf("probe manifest does not request a generated dual-stack Service: %#v", runner.commands)
	}
}

func TestProbeIPv6SingleStackServices(t *testing.T) {
	tests := []struct {
		name         string
		result       e2eexec.Result
		wantStatus   capability.Status
		wantReason   string
		wantEvidence string
	}{
		{
			name:         "supported",
			result:       e2eexec.Result{Stdout: []byte(ipv6SingleStackResponse)},
			wantStatus:   capability.StatusSupported,
			wantReason:   "allocates IPv6 single-stack Services",
			wantEvidence: "IPv6",
		},
		{
			name:         "IPv6 rejection",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte(ipv6SingleStackError)},
			wantStatus:   capability.StatusUnsupported,
			wantReason:   "rejects IPv6 single-stack Services",
			wantEvidence: "not configured for IPv6",
		},
		{
			name:         "forbidden",
			result:       e2eexec.Result{ExitCode: 1, Stderr: []byte("services is forbidden")},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "probe failed",
			wantEvidence: "forbidden",
		},
		{
			name:         "wrong family",
			result:       e2eexec.Result{Stdout: []byte(`{"spec":{"ipFamilies":["IPv4"]}}`)},
			wantStatus:   capability.StatusUnknown,
			wantReason:   "without returning only IPv6",
			wantEvidence: "IPv4",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				dualStackProbeCommand: test.result,
			}}
			cfg := DefaultConfig()
			cfg.Kubeconfig = "/tmp/probe-kubeconfig"

			got := probeIPv6SingleStackServices(context.Background(), runner, cfg)

			if got.Status != test.wantStatus ||
				!strings.Contains(got.Reason, test.wantReason) ||
				!strings.Contains(got.Evidence, test.wantEvidence) {
				t.Fatalf("probeIPv6SingleStackServices() = %#v, want status %q, reason containing %q, evidence containing %q", got, test.wantStatus, test.wantReason, test.wantEvidence)
			}
			if got.Source != ipv6SingleStackServiceProbeSource {
				t.Fatalf("probe source = %q, want %q", got.Source, ipv6SingleStackServiceProbeSource)
			}
			if !runner.stdinContains("ipFamilyPolicy: SingleStack") ||
				!runner.stdinContains("- IPv6") ||
				!runner.stdinContains("generateName: dcgm-exporter-ipv6-single-stack-probe-") {
				t.Fatalf("probe manifest does not request an IPv6 single-stack Service: %#v", runner.commands)
			}
		})
	}
}
