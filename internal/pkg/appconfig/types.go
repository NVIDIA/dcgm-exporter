/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package appconfig

import (
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

type KubernetesGPUIDType string

type DeviceOptions struct {
	Flex       bool  // If true, then monitor all GPUs if MIG mode is disabled or all GPU instances if MIG is enabled.
	MajorRange []int // The indices of each GPU/NvSwitch to monitor, or -1 to monitor all
	MinorRange []int // The indices of each GPUInstance/NvLink to monitor, or -1 to monitor all
}

// DumpConfig controls file-based debugging dumps
type DumpConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`         // Enable file-based dumps
	Directory   string `yaml:"directory" json:"directory"`     // Directory to store dump files
	Retention   int    `yaml:"retention" json:"retention"`     // Retention period in hours (0 = no cleanup)
	Compression bool   `yaml:"compression" json:"compression"` // Use gzip compression for dump files
}

type Config struct {
	CollectorsFile                   string
	Address                          string
	CollectInterval                  int
	Kubernetes                       bool
	KubernetesEnablePodLabels        bool
	KubernetesEnablePodUID           bool
	KubernetesGPUIdType              KubernetesGPUIDType
	KubernetesPodLabelAllowlistRegex []string // Regex patterns for filtering pod labels
	KubernetesPodLabelCacheSize      int      // Maximum number of label keys to cache (<=0 means default size)
	CollectDCP                       bool
	UseOldNamespace                  bool
	UseRemoteHE                      bool
	RemoteHEInfo                     string
	GPUDeviceOptions                 DeviceOptions
	SwitchDeviceOptions              DeviceOptions
	CPUDeviceOptions                 DeviceOptions
	NoHostname                       bool
	UseFakeGPUs                      bool
	ConfigMapData                    string
	MetricGroups                     []dcgm.MetricGroup
	WebSystemdSocket                 bool
	WebConfigFile                    string
	XIDCountWindowSize               int
	ReplaceBlanksInModelName         bool
	Debug                            bool
	ClockEventsCountWindowSize       int
	EnableDCGMLog                    bool
	DCGMLogLevel                     string
	PodResourcesKubeletSocket        string
	HPCJobMappingDir                 string
	NvidiaResourceNames              []string
	KubernetesVirtualGPUs            bool
	DumpConfig                       DumpConfig // Configuration for file-based dumps
	KubernetesEnableDRA              bool
	DisableStartupValidate           bool
	EnableGPUBindUnbindWatch         bool          // Enable GPU bind/unbind event monitoring
	GPUBindUnbindPollInterval        time.Duration // Poll interval for GPU bind/unbind events

	// Labels holds the feature-001 (multi-user GPU utilization) label schema
	// loaded from config.yaml (`labels:` section). When the feature is enabled
	// (i.e. a config.yaml is successfully loaded), these labels are attached
	// exclusively to DCGM_FI_DEV_GPU_UTIL samples.
	Labels LabelsConfig
	// Server holds the HTTP listener + timeout settings loaded from the
	// config.yaml `server:` section.
	Server ServerConfig
}

// LabelsConfig corresponds to the `labels:` section of config.yaml.
// Loaded from YAML at startup; never mutated at runtime.
type LabelsConfig struct {
	Static []StaticLabel `yaml:"static"`
	Env    []EnvLabel    `yaml:"env"`
}

// StaticLabel corresponds to one entry in `labels.static[]`. The Value is
// resolved at startup via the chain: explicit Value -> same-named env var ->
// "unknown". Resolved value is cached in ResolvedValue by ApplyDefaults().
type StaticLabel struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
	// ResolvedValue is the startup-resolved string that is actually injected
	// into DCGM_FI_DEV_GPU_UTIL samples. It is populated by ApplyDefaults()
	// and is not itself serialized to YAML.
	ResolvedValue string `yaml:"-"`
}

// EnvLabel corresponds to one entry in `labels.env[]`. EnvVar defaults to Name
// when unset; the value is read per-process from /proc/<pid>/environ at every
// collection cycle.
type EnvLabel struct {
	Name   string `yaml:"name"`
	EnvVar string `yaml:"env_var"`
}

// ServerConfig corresponds to the `server:` section of config.yaml.
// Authoritative semantics: see
// specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md §server.
type ServerConfig struct {
	Port         string        `yaml:"port"`          // /metrics listen address, e.g. ":9400"
	Timeout      time.Duration `yaml:"timeout"`       // whole-cycle collection timeout
	ReadTimeout  time.Duration `yaml:"read_timeout"`  // http.Server.ReadTimeout
	WriteTimeout time.Duration `yaml:"write_timeout"` // http.Server.WriteTimeout
}
