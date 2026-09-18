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
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

const (
	DefaultWebReadTimeout       = 10 * time.Second
	DefaultWebWriteTimeout      = 30 * time.Second
	DefaultMaxConcurrentScrapes = 16
	DefaultCollectorsFile       = "/etc/dcgm-exporter/default-counters.csv"
	UndefinedConfigMapData      = "none"
	DefaultConfigMapKey         = "metrics"
	DefaultWatchMaxKeepAge      = 10 * time.Minute
	DefaultWatchMaxSamples      = int64(0)
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

// WatchRetention controls how much field history DCGM retains for a watch.
// Startup resolves and validates this policy before watch-list construction converts it to DCGM arguments.
type WatchRetention struct {
	MaxAge     time.Duration
	MaxSamples int64
}

// DefaultWatchRetention returns the compatibility field-watch retention policy.
// Startup and legacy constructors use it when no explicit global policy is available.
func DefaultWatchRetention() WatchRetention {
	return WatchRetention{
		MaxAge:     DefaultWatchMaxKeepAge,
		MaxSamples: DefaultWatchMaxSamples,
	}
}

// Validate rejects retention values that DCGM cannot accept or that leave a watch unbounded.
// Call it after configuration precedence and per-group inheritance have produced a concrete policy.
func (r WatchRetention) Validate() error {
	if r.MaxAge < 0 {
		return fmt.Errorf("maxAge must not be negative")
	}
	if r.MaxSamples < 0 {
		return fmt.Errorf("maxSamples must not be negative")
	}
	if r.MaxSamples > math.MaxInt32 {
		return fmt.Errorf("maxSamples must not exceed %d", math.MaxInt32)
	}
	if r.MaxAge == 0 && r.MaxSamples == 0 {
		return fmt.Errorf("maxAge and maxSamples cannot both be zero")
	}
	return nil
}

// DCGMMaxKeepSamples validates and converts the sample limit to the type expected by DCGM.
// Watch-list construction uses it immediately before storing retention on a devicewatcher field group.
func (r WatchRetention) DCGMMaxKeepSamples() (int32, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	return int32(r.MaxSamples), nil //nolint:gosec // G115: Validate bounds MaxSamples to int32.
}

// WatchRetentionOverride contains presence-aware per-watch-group overrides.
// Pointer fields preserve omitted values so Resolve can inherit each property from the final global policy.
type WatchRetentionOverride struct {
	MaxAge     *time.Duration
	MaxSamples *int64
}

// Resolve applies explicitly configured values to the global retention policy.
// Watch-group validation and construction call it after YAML and CLI or environment precedence is complete.
func (o WatchRetentionOverride) Resolve(global WatchRetention) WatchRetention {
	resolved := global
	if o.MaxAge != nil {
		resolved.MaxAge = *o.MaxAge
	}
	if o.MaxSamples != nil {
		resolved.MaxSamples = *o.MaxSamples
	}
	return resolved
}

// WatchGroup assigns a set of metric fields to a collection interval and optional retention policy.
type WatchGroup struct {
	Name      string
	Interval  int
	Fields    []string
	Retention WatchRetentionOverride
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
	WatchRetention                   WatchRetention
	WatchGroups                      []WatchGroup
	MetricGroups                     []dcgm.MetricGroup
	WebSystemdSocket                 bool
	WebConfigFile                    string
	WebReadTimeout                   time.Duration
	WebWriteTimeout                  time.Duration
	MaxConcurrentScrapes             int
	EnableExporterMetrics            bool
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
	GPUBindUnbindPollInterval        time.Duration // Interval between GPU bind/unbind event reads
	EnablePprof                      bool          // Enable /debug/pprof/ HTTP endpoints

	// draResourceSliceChangeCallback is runtime-only wiring for the DRA
	// informer. It is deliberately private so it cannot become a YAML, JSON, or
	// command-line configuration surface.
	draResourceSliceChangeCallback func()
}

// SetDRAResourceSliceChangeCallback installs the in-memory notification used
// when a complete DRA ResourceSlice generation changes.
func (c *Config) SetDRAResourceSliceChangeCallback(callback func()) {
	c.draResourceSliceChangeCallback = callback
}

// DRAResourceSliceChangeCallback returns the in-memory DRA generation notification.
func (c *Config) DRAResourceSliceChangeCallback() func() {
	return c.draResourceSliceChangeCallback
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
		if c.WatchGroups[i].Retention.MaxAge != nil {
			maxAge := *c.WatchGroups[i].Retention.MaxAge
			clone.WatchGroups[i].Retention.MaxAge = &maxAge
		}
		if c.WatchGroups[i].Retention.MaxSamples != nil {
			maxSamples := *c.WatchGroups[i].Retention.MaxSamples
			clone.WatchGroups[i].Retention.MaxSamples = &maxSamples
		}
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
