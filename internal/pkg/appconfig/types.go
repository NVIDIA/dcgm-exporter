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
	"slices"
	"strings"
	"time"
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
	DefaultDCGMCleanupTimeout   = 5 * time.Minute

	DefaultHPCJobMappingMaxFileBytes   = int64(65536)
	DefaultHPCJobMappingMaxJobIDBytes  = int64(256)
	DefaultHPCJobMappingMaxJobsPerFile = int64(64)
)

// MetricSourceKind identifies where dcgm-exporter should load metric definitions from.
type MetricSourceKind string

const (
	MetricSourceFile      MetricSourceKind = "file"
	MetricSourceInline    MetricSourceKind = "inline"
	MetricSourceConfigMap MetricSourceKind = "configMap"
)

// KubernetesGPUIDType names the key used to join exporter GPUs with Kubernetes
// pod-resource records. The selected key must match the identifier convention
// reported by the configured container runtime.
type KubernetesGPUIDType string

// DeviceOptions preserves the device-selector grammar until hardware discovery
// can apply it. Each collector family has one value, and reload snapshots copy
// its ranges so they never share mutable selection state with active settings.
type DeviceOptions struct {
	Flex       bool  // If true, then monitor all GPUs if MIG mode is disabled or all GPU instances if MIG is enabled.
	MajorRange []int // The indices of each GPU/NvSwitch to monitor, or -1 to monitor all
	MinorRange []int // The indices of each GPUInstance/NvLink to monitor, or -1 to monitor all
}

// DumpConfig carries the user-approved file-dump policy. Collection code reads
// it before writing diagnostics, so storage location, retention, and compression
// remain configuration rather than collector-specific decisions.
type DumpConfig struct {
	Enabled       bool          `yaml:"enabled" json:"enabled"`                 // Enable file-based dumps
	Directory     string        `yaml:"directory" json:"directory"`             // Directory to store dump files
	Retention     time.Duration `yaml:"retention" json:"retention"`             // How long dump files are kept (0 = no age cleanup)
	Compression   bool          `yaml:"compression" json:"compression"`         // Use gzip compression for dump files
	MinInterval   time.Duration `yaml:"min_interval" json:"min_interval"`       // Minimum time between attempts per dump kind and entity group
	MaxFileBytes  int64         `yaml:"max_file_bytes" json:"max_file_bytes"`   // Largest dump file on disk, compressed size included
	MaxTotalBytes int64         `yaml:"max_total_bytes" json:"max_total_bytes"` // Total bytes of exporter-owned dump files
	MaxFiles      int64         `yaml:"max_files" json:"max_files"`             // Number of exporter-owned dump files
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

// WindowOverride is a counter window override from the config file, kept so
// its match items can be checked against the metric source once it is read.
type WindowOverride struct {
	// Path is the YAML location of the override.
	Path  string
	Match FieldMatcher
}

// WatchGroup assigns a set of metric fields to a collection interval and optional retention policy.
type WatchGroup struct {
	Name string
	// Path is the YAML location of the override that became this group. It is
	// empty for groups from the older config format.
	Path      string
	Interval  int
	Fields    []string
	Retention WatchRetentionOverride
	// ExtendedMatch lets Fields items be /regular expressions/. Overrides in the
	// config file set it; groups from the older format read names and wildcards only.
	ExtendedMatch bool
}

// Describe names the group for a message: an override when it came from the
// config file, a watch group when it came from the older config format.
func (g WatchGroup) Describe() string {
	if g.Path != "" {
		return fmt.Sprintf("override %q", g.Name)
	}

	return fmt.Sprintf("watch group %q", g.Name)
}

// OTLPWatchCollection retains source selection until watch settings are resolved.
type OTLPWatchCollection struct {
	Name      string
	Sources   []WatchSource
	Interval  int
	Retention WatchRetentionOverride
}

// Config is the complete resolved exporter configuration.
// Command-line and YAML inputs are merged into this value once during startup;
// registry builds and reload snapshots only read its resolved settings. Runtime
// discoveries and callbacks deliberately live outside this type.
type Config struct {
	ConfigFile        string
	ServiceInstanceID string
	DisablePrometheus bool
	EnableOTLP        bool
	Address           string
	CollectInterval   int

	Kubernetes                       bool
	KubernetesEnablePodLabels        bool
	KubernetesEnablePodUID           bool
	KubernetesGPUIdType              KubernetesGPUIDType
	KubernetesPodLabelAllowlistRegex []string // Regex patterns for filtering pod labels
	KubernetesPodLabelCacheSize      int      // Maximum number of label keys to cache (<=0 means default size)
	UseOldNamespace                  bool

	UseRemoteHE  bool
	RemoteHEInfo string

	GPUDeviceOptions    DeviceOptions
	SwitchDeviceOptions DeviceOptions
	CPUDeviceOptions    DeviceOptions
	NoHostname          bool
	UseFakeGPUs         bool
	HealthRequireGPUs   bool

	MetricSource         MetricSource
	WatchRetention       WatchRetention
	WatchGroups          []WatchGroup
	OTLPWatchCollections []OTLPWatchCollection
	// WindowOverrides are the config file overrides that set a counter window.
	WindowOverrides []WindowOverride

	WebSystemdSocket      bool
	WebConfigFile         string
	WebReadTimeout        time.Duration
	WebWriteTimeout       time.Duration
	MaxConcurrentScrapes  int
	EnableExporterMetrics bool

	XIDCountWindowSize         int
	ReplaceBlanksInModelName   bool
	Debug                      bool
	LogFormat                  string
	ClockEventsCountWindowSize int
	EnableDCGMLog              bool
	DCGMLogLevel               string
	// DCGMCleanupTimeout bounds each wait for DCGM: to release watches on
	// reload or GPU reset recovery, at shutdown, after a failed or canceled
	// registry build, and to start or stop the GPU bind/unbind watch.
	DCGMCleanupTimeout time.Duration

	PodResourcesKubeletSocket   string
	HPCJobMappingDir            string
	HPCJobMappingMaxFileBytes   int64
	HPCJobMappingMaxJobIDBytes  int64
	HPCJobMappingMaxJobsPerFile int64
	ContainerLabels             bool
	ContainerRuntimeSocket      string
	NvidiaResourceNames         []string
	KubernetesVirtualGPUs       bool

	DumpConfig DumpConfig // Configuration for file-based dumps

	KubernetesEnableDRA       bool
	EnrichmentModes           EnrichmentModes // How the config file asked for each workload-label source
	DisableStartupValidate    bool
	EnableGPUBindUnbindWatch  bool          // Enable GPU bind/unbind event monitoring
	GPUBindUnbindPollInterval time.Duration // Interval between GPU bind/unbind event reads
	EnablePprof               bool          // Enable /debug/pprof/ HTTP endpoints
}

// Clone copies all slices and retention pointers into an independent config.
// Reloads use the copy to build collectors without changing the saved startup
// settings or another reload snapshot.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	clone := *c
	clone.KubernetesPodLabelAllowlistRegex = slices.Clone(c.KubernetesPodLabelAllowlistRegex)
	clone.NvidiaResourceNames = slices.Clone(c.NvidiaResourceNames)
	clone.GPUDeviceOptions = cloneDeviceOptions(c.GPUDeviceOptions)
	clone.SwitchDeviceOptions = cloneDeviceOptions(c.SwitchDeviceOptions)
	clone.CPUDeviceOptions = cloneDeviceOptions(c.CPUDeviceOptions)
	clone.MetricSource.Fields = slices.Clone(c.MetricSource.Fields)

	clone.WindowOverrides = slices.Clone(c.WindowOverrides)
	clone.WatchGroups = slices.Clone(c.WatchGroups)

	// Each watch group holds a slice and retention pointers, so copy them too.
	for i := range clone.WatchGroups {
		clone.WatchGroups[i].Fields = slices.Clone(c.WatchGroups[i].Fields)
		clone.WatchGroups[i].Retention = c.WatchGroups[i].Retention.clone()
	}
	clone.OTLPWatchCollections = slices.Clone(c.OTLPWatchCollections)
	for i := range clone.OTLPWatchCollections {
		clone.OTLPWatchCollections[i].Sources = slices.Clone(c.OTLPWatchCollections[i].Sources)
		clone.OTLPWatchCollections[i].Retention = c.OTLPWatchCollections[i].Retention.clone()
	}

	return &clone
}

// clone copies retention pointers so reload configurations do not share mutable overrides.
func (o WatchRetentionOverride) clone() WatchRetentionOverride {
	if o.MaxAge != nil {
		age := *o.MaxAge
		o.MaxAge = &age
	}
	if o.MaxSamples != nil {
		samples := *o.MaxSamples
		o.MaxSamples = &samples
	}
	return o
}

// cloneDeviceOptions returns an independent copy of a device selector.
// Config.Clone uses it for each collector family so reload snapshots cannot
// share mutable selector ranges with the startup configuration.
func cloneDeviceOptions(options DeviceOptions) DeviceOptions {
	clone := options
	clone.MajorRange = slices.Clone(options.MajorRange)
	clone.MinorRange = slices.Clone(options.MinorRange)
	return clone
}

// MetricFileWatcherPath returns the resolved metrics file path when file reloads should be watched.
func (c *Config) MetricFileWatcherPath() (string, bool) {
	if c == nil {
		return "", false
	}

	if c.MetricSource.Kind != MetricSourceFile || strings.TrimSpace(c.MetricSource.File) == "" {
		return "", false
	}
	return c.MetricSource.File, true
}
