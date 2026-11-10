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

package appconfig

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultCountWindow is the sampling window of the XID and clock event
// counters when no window override sets one.
const DefaultCountWindow = 5 * time.Minute

const (
	// DefaultMetricsName and DefaultOutputName name the single metrics entry
	// and output in a full config when the user's file gave no name. The names
	// carry no meaning at runtime.
	DefaultMetricsName = "default"
	DefaultOutputName  = "prometheus"
)

// ConfigNames are the names a full config gives its metrics entry and output.
// An empty name means the default.
type ConfigNames struct {
	Metrics string
	Output  string
}

// withDefaults fills in the names the user's file did not give.
func (n ConfigNames) withDefaults() ConfigNames {
	if n.Metrics == "" {
		n.Metrics = DefaultMetricsName
	}

	if n.Output == "" {
		n.Output = DefaultOutputName
	}

	return n
}

// NamesOf returns the metrics entry name and output name that a file
// uses, so a full config made from it keeps them. A version 1 file or no file
// gives the defaults.
func (y *YAMLConfig) NamesOf() ConfigNames {
	var names ConfigNames
	if y != nil && y.legacyV1 == nil {
		if len(y.Metrics) > 0 {
			names.Metrics = strings.TrimSpace(y.Metrics[0].Name)
		}

		for name := range y.Outputs {
			names.Output = name
		}
	}

	return names.withDefaults()
}

// NewFullConfig builds the config that config show prints, with every key this
// build supports set to its resolved value, including empty and zero values.
// Marshaled as YAML or JSON it loads back as the same settings. Settings that
// no config key can hold (for example --kubernetes) are not part of it.
//
// The config must be one that startup accepts: values the reader rejects, such
// as a zero interval, are written as they are and the result then does not
// load. A ConfigMap metric source has no config key, so the result names
// the default metric file.
func NewFullConfig(config *Config, names ConfigNames) *YAMLConfig {
	names = names.withDefaults()
	return &YAMLConfig{
		Version: yamlConfigVersion,
		Metrics: []YAMLMetricEntry{fullMetric(config, names.Metrics)},
		Outputs: map[string]YAMLOutput{
			names.Output: {Type: "prometheus", Schema: prometheusSchema, Listen: config.Address},
		},
		Sources:       fullSources(config),
		Server:        fullServer(config),
		Observability: fullObservability(config),
		DebugDump: &YAMLDebugDump{
			Enabled:       new(config.DumpConfig.Enabled),
			Directory:     config.DumpConfig.Directory,
			Retention:     FormatDuration(config.DumpConfig.Retention),
			Compression:   new(config.DumpConfig.Compression),
			MinInterval:   FormatDuration(config.DumpConfig.MinInterval),
			MaxFileBytes:  new(config.DumpConfig.MaxFileBytes),
			MaxTotalBytes: new(config.DumpConfig.MaxTotalBytes),
			MaxFiles:      new(config.DumpConfig.MaxFiles),
		},
	}
}

// fullMetric builds the metrics entry: source, interval, overrides and entities.
func fullMetric(config *Config, name string) YAMLMetricEntry {
	metric := YAMLMetricEntry{
		Name:       name,
		Enrichment: fullEnrichment(config),
		Every:      FormatDuration(time.Duration(config.CollectInterval) * time.Millisecond),
		Overrides:  fullOverrides(config),
		Entities: &YAMLEntities{
			GPU:    fullGPUEntities(config.GPUDeviceOptions),
			Switch: fullSwitchEntities(config.SwitchDeviceOptions),
			CPU:    fullCPUEntities(config.CPUDeviceOptions),
		},
	}

	switch config.MetricSource.Kind {
	case MetricSourceInline:
		for _, field := range config.MetricSource.Fields {
			metric.Fields = append(metric.Fields, YAMLMetricField(field))
		}
	case MetricSourceFile:
		metric.File = config.MetricSource.File
	default:
		metric.File = DefaultCollectorsFile
	}

	return metric
}

// fullEnrichment writes each switch the way the reader would read it back.
// A source turned on by a flag has no file mode; it is written as auto, which
// turns the same source on.
func fullEnrichment(config *Config) *YAMLEnrichment {
	modes := config.EnrichmentModes
	return &YAMLEnrichment{
		Kubernetes: enrichmentValue(config.Kubernetes, modes.Kubernetes),
		Container:  enrichmentValue(config.ContainerLabels, modes.Container),
		DRA:        enrichmentValue(config.KubernetesEnableDRA, modes.DRA),
		HPCJobs:    enrichmentValue(config.HPCJobMappingDir != "", modes.HPCJobs),
	}
}

// enrichmentValue is true for a required source, false for one that is off,
// and auto for any other source that is on.
func enrichmentValue(on bool, mode EnrichmentMode) *YAMLEnrichmentMode {
	value := YAMLEnrichmentMode("auto")
	switch {
	case mode == EnrichmentRequired:
		value = "true"
	case !on:
		value = "false"
	}

	return &value
}

// fullOverrides lists the watch groups and the counter windows in the order
// OverridePositions gives. A window at its default is written only when it
// holds the place of an unnamed override (see layoutOverrides).
func fullOverrides(config *Config) []YAMLOverride {
	names, _ := overrideNames(config.WatchGroups)
	slots := layoutOverrides(config)
	overrides := make([]YAMLOverride, 0, len(slots))

	// A slot is either a counter window override or a watch group override.
	for _, slot := range slots {
		if slot.window != "" {
			countWindow, _ := CountWindowOf(slot.window)
			window := countWindow.Setting(config)
			overrides = append(overrides, YAMLOverride{
				Match:  []string{slot.window},
				Window: FormatDuration(time.Duration(*window) * time.Millisecond),
			})

			continue
		}

		group := config.WatchGroups[slot.group]
		override := YAMLOverride{
			Name:  names[slot.group],
			Match: yamlMatchItems(group),
			Every: FormatDuration(time.Duration(group.Interval) * time.Millisecond),
		}
		if retention := group.Retention; retention.MaxAge != nil || retention.MaxSamples != nil {
			watch := &YAMLDCGMWatch{MaxKeepSamples: retention.MaxSamples}
			if retention.MaxAge != nil {
				age := FormatDuration(*retention.MaxAge)
				watch.MaxKeepAge = &age
			}

			override.Sources = &YAMLOverrideSources{DCGM: &YAMLOverrideDCGMSource{Watch: watch}}
		}

		overrides = append(overrides, override)
	}

	return overrides
}

// yamlMatchItems writes a watch group's field items for the full config. A
// version 1 group read its items as path.Match patterns, so each is rewritten
// to select the same names under the version 2 rules.
func yamlMatchItems(group WatchGroup) []string {
	items := make([]string, len(group.Fields))
	for i, item := range group.Fields {
		if group.ExtendedMatch {
			items[i] = item
		} else {
			items[i] = versionTwoFieldItem(item)
		}
	}

	return items
}

// overrideSlot is one override of the full config: a watch group (by index) or a
// counter window (by counter name).
type overrideSlot struct {
	group  int
	window string
}

// layoutOverrides orders the overrides of the full config. Watch groups keep their
// order. A counter window is written only when it is not at its default,
// except when a default window must fill a place.
// The reader names an unnamed override after its position, so a group the
// reader named that way goes back to the position in its name, when enough
// windows exist to fill the places before it. That keeps the name after a
// show and reload. Windows that are not at their default fill those places
// first. A window at its default is then written too, but only when it
// brings the group all the way to its position.
func layoutOverrides(config *Config) []overrideSlot {
	windows, defaults := splitWindowCounters(config)

	slots := make([]overrideSlot, 0, len(config.WatchGroups)+len(windows))
	for index, group := range config.WatchGroups {
		if match := generatedGroupName.FindStringSubmatch(group.Name); match != nil {
			position, _ := strconv.Atoi(match[1])
			slots, windows, defaults = fillBeforePosition(slots, position, windows, defaults)
		}

		slots = append(slots, overrideSlot{group: index})
	}

	for _, counter := range windows {
		slots = append(slots, overrideSlot{group: -1, window: counter})
	}

	return slots
}

// splitWindowCounters returns the counters whose window is not at its default,
// then the counters whose window is at its default.
func splitWindowCounters(config *Config) (windows, defaults []string) {
	for _, countWindow := range CountWindows {
		window := countWindow.Setting(config)
		if time.Duration(*window)*time.Millisecond != DefaultCountWindow {
			windows = append(windows, countWindow.Counter)
		} else {
			defaults = append(defaults, countWindow.Counter)
		}
	}

	return windows, defaults
}

// fillBeforePosition adds counter windows to slots until a group named after
// position can sit there. Windows not at their default go first. Windows at their
// default are used only when they bring the group all the way to its position. The
// windows it uses are returned without the used ones, along with the slots.
func fillBeforePosition(slots []overrideSlot, position int, windows, defaults []string) ([]overrideSlot, []string, []string) {
	for len(slots) < position && len(windows) > 0 {
		slots = append(slots, overrideSlot{group: -1, window: windows[0]})
		windows = windows[1:]
	}

	missing := position - len(slots)
	if missing <= 0 || missing > len(defaults) {
		return slots, windows, defaults
	}

	for _, counter := range defaults[:missing] {
		slots = append(slots, overrideSlot{group: -1, window: counter})
	}

	return slots, windows, defaults[missing:]
}

// OverridePositions says where the full config writes each override: the index of
// every watch group, and the index of each counter window that is written.
func OverridePositions(config *Config) (groups []int, windows map[string]int) {
	groups = make([]int, len(config.WatchGroups))
	windows = map[string]int{}
	for position, slot := range layoutOverrides(config) {
		if slot.window != "" {
			windows[slot.window] = position
			continue
		}

		groups[slot.group] = position
	}

	return groups, windows
}

// OverrideRename is a watch group name that an override cannot hold,
// and the name the full config writes for it instead.
type OverrideRename struct {
	Group    int
	From, To string
}

// RenamedOverrides lists the watch groups whose names the full config changes.
func RenamedOverrides(config *Config) []OverrideRename {
	_, renames := overrideNames(config.WatchGroups)
	return renames
}

// overrideNames gives the name each watch group has in the full config: ""
// for an override the reader named itself, the group's name when a version 2
// override can hold it, and otherwise a changed name. A version 1 watch group
// may have any name, but an override name may contain only letters, digits, _, -
// and . . Each other character becomes -, repeated dashes collapse, and dashes at
// the ends go. A changed name that another name already uses gets -2, -3 and so on.
func overrideNames(groups []WatchGroup) (names []string, renames []OverrideRename) {
	names = make([]string, len(groups))
	used := map[string]bool{}
	for _, group := range groups {
		if validOverrideName(group.Name) {
			used[group.Name] = true
		}
	}

	for index, group := range groups {
		switch {
		case IsGeneratedOverrideName(group.Name):
			// User names cannot contain brackets, so the unnamed override is written without one.
		case validOverrideName(group.Name):
			names[index] = group.Name
		default:
			name := uniqueName(sanitizeOverrideName(group.Name), used)
			used[name] = true
			names[index] = name
			renames = append(renames, OverrideRename{Group: index, From: group.Name, To: name})
		}
	}

	return names, renames
}

// sanitizeOverrideName turns a name into one that validOverrideName accepts.
func sanitizeOverrideName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if validOverrideName(string(r)) {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('-')
		}
	}

	collapsed := strings.Trim(dashRun.ReplaceAllString(builder.String(), "-"), "-")
	if collapsed == "" {
		return "override"
	}

	return collapsed
}

// dashRun matches a run of dashes.
var dashRun = regexp.MustCompile(`-{2,}`)

// uniqueName is name, or name-2, name-3 and so on, whichever is not in used.
func uniqueName(name string, used map[string]bool) string {
	candidate := name
	for suffix := 2; used[candidate]; suffix++ {
		candidate = name + "-" + strconv.Itoa(suffix)
	}

	return candidate
}

// generatedGroupName matches the name the reader gives an unnamed override.
var generatedGroupName = regexp.MustCompile(`^metrics\[0\]\.overrides\[([0-9]+)\]$`)

// IsGeneratedOverrideName says the name is the path the reader gave an unnamed override.
func IsGeneratedOverrideName(name string) bool { return generatedGroupName.MatchString(name) }

// fullGPUEntities is the inverse of YAMLGPUEntities.deviceOptions.
func fullGPUEntities(options DeviceOptions) *YAMLGPUEntities {
	if options.Flex {
		return &YAMLGPUEntities{Policy: "flex"}
	}

	return &YAMLGPUEntities{
		Policy:    "explicit",
		GPUs:      fullSelection(options.MajorRange),
		Instances: fullSelection(options.MinorRange),
	}
}

// fullSwitchEntities is the inverse of the switch case of majorMinorOptions.
func fullSwitchEntities(options DeviceOptions) *YAMLSwitchEntities {
	if options.Flex {
		return &YAMLSwitchEntities{Indices: "all", Links: "all"}
	}

	return &YAMLSwitchEntities{
		Indices: fullSelection(options.MajorRange),
		Links:   fullSelection(options.MinorRange),
	}
}

// fullCPUEntities is the inverse of the CPU case of majorMinorOptions.
func fullCPUEntities(options DeviceOptions) *YAMLCPUEntities {
	if options.Flex {
		return &YAMLCPUEntities{Indices: "all", Cores: "all"}
	}

	return &YAMLCPUEntities{
		Indices: fullSelection(options.MajorRange),
		Cores:   fullSelection(options.MinorRange),
	}
}

// fullSelection turns a runtime index range into "all", "none" or a list.
func fullSelection(indices []int) any {
	switch {
	case len(indices) == 0:
		return "none"
	case isAllSelection(indices):
		return "all"
	default:
		return append([]int{}, indices...)
	}
}

// fullSources builds every source block.
func fullSources(config *Config) *YAMLSources {
	maxAge := FormatDuration(config.WatchRetention.MaxAge)
	maxSamples := config.WatchRetention.MaxSamples

	dcgm := &YAMLDCGMSource{
		Watch: &YAMLDCGMWatch{MaxKeepAge: &maxAge, MaxKeepSamples: &maxSamples},
		DetectBindUnbind: &YAMLDetectBindUnbind{
			Enabled: new(config.EnableGPUBindUnbindWatch),
			Every:   fullPollInterval(config),
		},
		CleanupTimeout: FormatDuration(config.DCGMCleanupTimeout),
	}
	if config.UseRemoteHE {
		dcgm.HostEngine = config.RemoteHEInfo
	}

	if config.EnableDCGMLog {
		dcgm.EmbeddedLog = &YAMLEmbeddedLog{Level: EmbeddedLogLevelName(config.DCGMLogLevel), File: "stdout"}
	}

	return &YAMLSources{
		DCGM: dcgm,
		Kubernetes: &YAMLKubernetesSource{
			PodResourcesSocket:      config.PodResourcesKubeletSocket,
			GPUIDType:               string(config.KubernetesGPUIdType),
			AdditionalResourceNames: append([]string{}, config.NvidiaResourceNames...),
			VirtualGPUs:             new(config.KubernetesVirtualGPUs),
			PodUID:                  new(config.KubernetesEnablePodUID),
			PodLabels: &YAMLPodLabels{
				Enabled: new(config.KubernetesEnablePodLabels),
				Allow:   append([]string{}, config.KubernetesPodLabelAllowlistRegex...),
			},
		},
		Container: &YAMLContainerSource{RuntimeSocket: valueOrDefault(config.ContainerRuntimeSocket, defaultContainerRuntimeSocket)},
		DRA:       &YAMLDRASource{Mode: "kubernetes"},
		HPCJobs: &YAMLHPCJobsSource{
			MappingDir:     valueOrDefault(config.HPCJobMappingDir, defaultHPCJobMappingDir),
			MaxFileBytes:   new(config.HPCJobMappingMaxFileBytes),
			MaxJobIDBytes:  new(config.HPCJobMappingMaxJobIDBytes),
			MaxJobsPerFile: new(config.HPCJobMappingMaxJobsPerFile),
		},
	}
}

// fullPollInterval writes the poll interval. When detection is off and the
// interval is one the reader would reject, it writes the default instead, which
// has no effect while detection is off.
func fullPollInterval(config *Config) string {
	if !config.EnableGPUBindUnbindWatch && config.GPUBindUnbindPollInterval <= 0 {
		return FormatDuration(time.Second)
	}

	return FormatDuration(config.GPUBindUnbindPollInterval)
}

// valueOrDefault is value, or fallback when value is empty. The full config shows the
// path a file would use, even while its source is switched off.
func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// EmbeddedLogLevelName is the name of a DCGM log level.
func EmbeddedLogLevelName(dcgmLevel string) string {
	for name, level := range embeddedLogLevels {
		if level == dcgmLevel {
			return name
		}
	}

	return strings.ToLower(dcgmLevel)
}

// fullServer builds the server block.
func fullServer(config *Config) *YAMLServer {
	return &YAMLServer{
		ReadTimeout:          FormatDuration(config.WebReadTimeout),
		WriteTimeout:         FormatDuration(config.WebWriteTimeout),
		MaxConcurrentScrapes: new(config.MaxConcurrentScrapes),
		SystemdSocket:        new(config.WebSystemdSocket),
		WebConfigFile:        config.WebConfigFile,
		Profiling:            new(config.EnablePprof),
	}
}

// fullObservability builds the observability block.
func fullObservability(config *Config) *YAMLObservability {
	level := "info"
	if config.Debug {
		level = "debug"
	}

	return &YAMLObservability{
		Metrics: &YAMLObservabilityMetrics{Enabled: new(config.EnableExporterMetrics)},
		Logs:    &YAMLObservabilityLogs{Format: config.LogFormat, Level: level},
	}
}

// FormatDuration writes a duration the way a config file would: "10m", not "10m0s".
func FormatDuration(duration time.Duration) string {
	text := duration.String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}

	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}

	return text
}
