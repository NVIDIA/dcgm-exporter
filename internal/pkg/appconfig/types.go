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
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

const (
	DefaultWebReadTimeout  = 10 * time.Second
	DefaultWebWriteTimeout = 30 * time.Second
	DefaultCollectorsFile  = "/etc/dcgm-exporter/default-counters.csv"
	UndefinedConfigMapData = "none"
	DefaultConfigMapKey    = "metrics"
)

// MetricSourceKind identifies where dcgm-exporter should load metric definitions from.
type MetricSourceKind string

const (
	MetricSourceFile      MetricSourceKind = "file"
	MetricSourceInline    MetricSourceKind = "inline"
	MetricSourceConfigMap MetricSourceKind = "configMap"
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

// MetricField is one inline metric definition in the same shape as a three-column metric CSV row.
type MetricField struct {
	Name           string
	PrometheusType string
	Help           string
}

// ConfigMapMetricSource identifies the compatibility API-backed ConfigMap metric source.
type ConfigMapMetricSource struct {
	Namespace string
	Name      string
}

// MetricSource is the resolved metric definition source used after config parsing and overrides.
type MetricSource struct {
	Kind      MetricSourceKind
	File      string
	Fields    []MetricField
	ConfigMap ConfigMapMetricSource
}

// WatchGroup assigns a set of metric fields to a collection interval.
type WatchGroup struct {
	Name     string
	Interval int
	Fields   []string
}

type Config struct {
	ConfigFile                       string
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
	MetricSource                     MetricSource
	WatchGroups                      []WatchGroup
	MetricGroups                     []dcgm.MetricGroup
	WebSystemdSocket                 bool
	WebConfigFile                    string
	WebReadTimeout                   time.Duration
	WebWriteTimeout                  time.Duration
	XIDCountWindowSize               int
	ReplaceBlanksInModelName         bool
	Debug                            bool
	ClockEventsCountWindowSize       int
	EnableDCGMLog                    bool
	DCGMLogLevel                     string
	PodResourcesKubeletSocket        string
	HPCJobMappingDir                 string
	ContainerLabels                  bool
	ContainerRuntimeSocket           string
	NvidiaResourceNames              []string
	KubernetesVirtualGPUs            bool
	DumpConfig                       DumpConfig // Configuration for file-based dumps
	KubernetesEnableDRA              bool
	DisableStartupValidate           bool
	EnableGPUBindUnbindWatch         bool          // Enable GPU bind/unbind event monitoring
	GPUBindUnbindPollInterval        time.Duration // Poll interval for GPU bind/unbind events
	EnablePprof                      bool          // Enable /debug/pprof/ HTTP endpoints
}

// Clone returns a copy of Config with slices duplicated for reload snapshots.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	clone := *c
	clone.KubernetesPodLabelAllowlistRegex = append([]string(nil), c.KubernetesPodLabelAllowlistRegex...)
	clone.NvidiaResourceNames = append([]string(nil), c.NvidiaResourceNames...)
	clone.MetricSource.Fields = append([]MetricField(nil), c.MetricSource.Fields...)
	clone.WatchGroups = append([]WatchGroup(nil), c.WatchGroups...)
	for i := range clone.WatchGroups {
		clone.WatchGroups[i].Fields = append([]string(nil), c.WatchGroups[i].Fields...)
	}
	clone.MetricGroups = append([]dcgm.MetricGroup(nil), c.MetricGroups...)
	for i := range clone.MetricGroups {
		clone.MetricGroups[i].FieldIds = append([]uint(nil), c.MetricGroups[i].FieldIds...)
	}

	return &clone
}

// MetricFileWatcherPath returns the resolved metrics file path when file reloads should be watched.
func (c *Config) MetricFileWatcherPath() (string, bool) {
	if c == nil {
		return "", false
	}

	source := c.MetricSource
	if source.Kind == "" {
		if c.ConfigMapData != "" && c.ConfigMapData != UndefinedConfigMapData {
			return "", false
		}
		source = MetricSource{Kind: MetricSourceFile, File: c.CollectorsFile}
	}

	if source.Kind != MetricSourceFile || strings.TrimSpace(source.File) == "" {
		return "", false
	}
	return source.File, true
}
