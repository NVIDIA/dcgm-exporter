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

// Package capability records host, cluster, and remote DCGM readiness.
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
	Name             string
	Status           Status
	Source           string
	Reason           string
	Evidence         string
	DCGMNotSupported bool
}

// Snapshot is an immutable capability lookup table.
type Snapshot struct {
	entries map[string]Capability
}

// ProbeOptions controls host capability probing.
type ProbeOptions struct {
	DryRun              bool
	FailureInjection    bool
	MIGConfigure        string
	DCGMImage           string
	DockerEnv           []string
	ExpectedDCGMVersion string
}

// ProbeResult carries capability decisions plus derived suite arguments.
type ProbeResult struct {
	Snapshot                  Snapshot
	MIGInstanceEntityID       string
	MIGInstanceNVMLID         string
	UnsupportedFieldCandidate string
	UnsupportedFieldEvidence  string
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

// HostNames returns host capabilities in display order.
func HostNames() []string {
	return []string{"gpu", "profiling", "mig", "mixed_mig", "mig_instance_entity", "unsupported_field", "nvlink", "p2p", "nvswitch", "grace_cpu", "c2c"}
}

// ClusterNames returns cluster capabilities in display order.
func ClusterNames() []string {
	return []string{"gpu_resources", "standalone_dcgm_resources", "mig_resources", "dra", "shared_gpu", "pod_resources", "ipv6", "gpu_operator"}
}

// DCGMNames returns remote DCGM capabilities in display order.
func DCGMNames() []string {
	return []string{"remote_dcgm", "failure_injection", "failure_injection_nvlink_crc"}
}
