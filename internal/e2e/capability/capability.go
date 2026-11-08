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

// Package capability records hardware and cluster features used to select e2e
// scenarios, and which CSV rows DCGM returns values for on the run's GPUs.
//
// Field support is read with dcgmi dmon rather than the cgo e2e-dcgm-probe
// command, because dcgmi also runs inside the DCGM image, where the host needs
// neither libdcgm nor a cgo build. That covers the container suite and the
// local k3d cluster as well as the host.
package capability

// Status is the availability state for one capability.
type Status string

const (
	StatusUnknown     Status = "unknown"
	StatusSupported   Status = "supported"
	StatusUnsupported Status = "unsupported"
)

// Capability is one probed selection gate.
type Capability struct {
	Name     string
	Status   Status
	Source   string
	Reason   string
	Evidence string
}

// Snapshot is an immutable capability lookup table.
type Snapshot struct {
	entries map[string]Capability
}

// ProbeOptions controls host capability probing.
type ProbeOptions struct {
	DryRun    bool
	DCGMImage string
	DockerEnv []string
	// UseHostDCGM runs dcgmi with its local default hostengine connection.
	UseHostDCGM bool
	// HostDCGMAddress runs dcgmi against this caller-verified hostengine endpoint.
	HostDCGMAddress string
}

// ScenarioInputs contains runtime values resolved only for selected scenarios.
type ScenarioInputs struct {
	MIGInstanceEntityID                 string
	MIGInstanceNVMLID                   string
	UnsupportedFieldCandidate           string
	UnsupportedFieldEvidence            string
	UnsupportedFieldNotApplicableReason string
}

// check is one lazily evaluated capability probe.
type check func() Capability

// runChecks evaluates capability probes in display order.
func runChecks(checks ...check) []Capability {
	entries := make([]Capability, 0, len(checks))
	for _, check := range checks {
		entries = append(entries, check())
	}
	return entries
}

// NewSnapshot builds a capability snapshot from entries.
func NewSnapshot(entries []Capability) Snapshot {
	byName := make(map[string]Capability, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	return Snapshot{entries: byName}
}

// Entries returns the snapshot entries in unspecified order.
func (s Snapshot) Entries() []Capability {
	entries := make([]Capability, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	return entries
}

// With returns a new snapshot with entries replacing matching names.
func (s Snapshot) With(entries ...Capability) Snapshot {
	all := s.Entries()
	all = append(all, entries...)
	return NewSnapshot(all)
}

// Lookup returns one capability or an unknown placeholder.
func (s Snapshot) Lookup(name string) Capability {
	if entry, ok := s.entries[name]; ok {
		return entry
	}
	return Capability{
		Name:   name,
		Status: StatusUnknown,
		Reason: "capability has not been probed",
		Source: "capability probe",
	}
}

// HostFeatureNames returns coarse host features in display order.
func HostFeatureNames() []string {
	return []string{"gpu", "profiling", "mig", "mig_active", "nvlink_fabric", "nvswitch", "grace_cpu", "c2c"}
}

// ClusterFeatureNames returns coarse cluster features in display order.
func ClusterFeatureNames() []string {
	return []string{
		"gpu_resources", "mig_resources", "mixed_mig_resources", "dra",
		"dra_resources", "ipv6", "ipv6_single_stack",
	}
}

// ApplicabilityNames returns scenario-specific applicability gates in display order.
func ApplicabilityNames() []string {
	return []string{
		"multi_gpu",
		"mixed_mig",
		"nvlink5",
		"nvswitch_local_telemetry",
		"nvswitch_nscq",
		"nvswitch_nvsdm",
	}
}
