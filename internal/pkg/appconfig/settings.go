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
	"fmt"
	"strconv"
)

// SettingKey ties one setting to the flag that sets it and the key that
// holds it.
type SettingKey struct {
	// Field is the name that origin tracking uses for the setting.
	Field string
	// Flag is the root flag that sets the setting, empty when none does.
	Flag string
	// Key is the key that holds the setting, empty when only a flag
	// can set it. The key of a setting can depend on the config it belongs to.
	Key string
}

// fixedSettingKeys maps each Config field to the flag that sets it and to the
// key of the config file that holds it. Origin tracking uses it to record where
// each value came from, flags after "--" use it to replace earlier values, and
// the explain rows use it to name each setting.
//
// Config holds the values, YAMLConfig is the file, and Resolved is a Config
// together with where each of its values came from. The order here is the order
// of the explain rows. Rows with no key are set only by flags. A row whose flag
// or key depends on the config is filled in by SettingKeys.
var fixedSettingKeys = []SettingKey{
	{Field: "MetricSource"},
	{Field: "CollectInterval", Flag: FlagCollectInterval, Key: "metrics[0].every"},
	{Field: FieldXIDCountWindowSize, Flag: FlagXIDCountWindowSize},
	{Field: FieldClockEventsCountWindowSize, Flag: FlagClockEventsCountWindowSize},
	{Field: "GPUDeviceOptions", Flag: FlagGPUDevices, Key: "metrics[0].entities.gpu"},
	{Field: "SwitchDeviceOptions", Flag: FlagSwitchDevices, Key: "metrics[0].entities.switch"},
	{Field: "CPUDeviceOptions", Flag: FlagCPUDevices, Key: "metrics[0].entities.cpu"},
	{Field: "Address", Flag: FlagAddress},
	{Field: "RemoteHEInfo", Flag: FlagRemoteHEInfo, Key: "sources.dcgm.hostengine"},
	{Field: FieldWatchMaxAge, Flag: FlagWatchMaxKeepAge, Key: "sources.dcgm.watch.max_keep_age"},
	{Field: FieldWatchMaxSamples, Flag: FlagWatchMaxKeepSamples, Key: "sources.dcgm.watch.max_keep_samples"},
	{Field: "EnableGPUBindUnbindWatch", Flag: FlagEnableGPUBindUnbindWatch, Key: "sources.dcgm.detect_bind_unbind.enabled"},
	{Field: "GPUBindUnbindPollInterval", Flag: FlagGPUBindUnbindPollInterval, Key: "sources.dcgm.detect_bind_unbind.every"},
	{Field: FieldDCGMCleanupTimeout, Flag: FlagDCGMCleanupTimeout, Key: DCGMCleanupTimeoutKey},
	{Field: "DCGMLogLevel", Flag: FlagDCGMLogLevel, Key: "sources.dcgm.embedded_log.level"},
	{Field: "EnableDCGMLog", Flag: FlagEnableDCGMLog, Key: "sources.dcgm.embedded_log.file"},
	{Field: "PodResourcesKubeletSocket", Flag: FlagPodResourcesKubeletSocket, Key: "sources.kubernetes.pod_resources_socket"},
	{Field: "KubernetesGPUIdType", Flag: FlagKubernetesGPUIDType, Key: "sources.kubernetes.gpu_id_type"},
	{Field: "NvidiaResourceNames", Flag: FlagNvidiaResourceNames, Key: "sources.kubernetes.additional_resource_names"},
	{Field: "KubernetesVirtualGPUs", Flag: FlagKubernetesVirtualGPUs, Key: "sources.kubernetes.virtual_gpus"},
	{Field: "KubernetesEnablePodUID", Flag: FlagKubernetesEnablePodUID, Key: "sources.kubernetes.pod_uid"},
	{Field: "KubernetesEnablePodLabels", Flag: FlagKubernetesEnablePodLabels, Key: "sources.kubernetes.pod_labels.enabled"},
	{
		Field: "KubernetesPodLabelAllowlistRegex", Flag: FlagKubernetesPodLabelAllowlistRegex,
		Key: "sources.kubernetes.pod_labels.allow",
	},
	{Field: "Kubernetes", Flag: FlagKubernetes, Key: EnrichmentKubernetes.Key()},
	{Field: "ContainerLabels", Flag: FlagContainerLabels, Key: EnrichmentContainer.Key()},
	{Field: "KubernetesEnableDRA", Flag: FlagKubernetesEnableDRA, Key: EnrichmentDRA.Key()},
	// The switch has its own field name because its origin differs from the
	// mapping directory's: a file switch that clears the directory is not a
	// value the file gave the directory.
	{Field: HPCJobsSwitchField, Flag: FlagHPCJobMappingDir, Key: EnrichmentHPCJobs.Key()},
	{Field: "ContainerRuntimeSocket", Flag: FlagContainerRuntimeSocket, Key: "sources.container.runtime_socket"},
	{Field: "HPCJobMappingDir", Flag: FlagHPCJobMappingDir, Key: "sources.hpc_jobs.mapping_dir"},
	{Field: "HPCJobMappingMaxFileBytes", Flag: FlagHPCJobMappingMaxFileBytes, Key: "sources.hpc_jobs.max_file_bytes"},
	{Field: "HPCJobMappingMaxJobIDBytes", Flag: FlagHPCJobMappingMaxJobIDBytes, Key: "sources.hpc_jobs.max_job_id_bytes"},
	{Field: "HPCJobMappingMaxJobsPerFile", Flag: FlagHPCJobMappingMaxJobsPerFile, Key: "sources.hpc_jobs.max_jobs_per_file"},
	{Field: "WebReadTimeout", Flag: FlagWebReadTimeout, Key: "server.read_timeout"},
	{Field: "WebWriteTimeout", Flag: FlagWebWriteTimeout, Key: "server.write_timeout"},
	{Field: "MaxConcurrentScrapes", Flag: FlagMaxConcurrentScrapes, Key: "server.max_concurrent_scrapes"},
	{Field: "WebSystemdSocket", Flag: FlagWebSystemdSocket, Key: "server.systemd_socket"},
	{Field: "WebConfigFile", Flag: FlagWebConfigFile, Key: "server.web_config_file"},
	{Field: "EnablePprof", Flag: FlagEnablePprof, Key: "server.profiling"},
	{Field: "EnableExporterMetrics", Flag: FlagEnableExporterMetrics, Key: "observability.metrics.enabled"},
	{Field: "LogFormat", Flag: FlagLogFormat, Key: "observability.logs.format"},
	{Field: "Debug", Flag: FlagDebugMode, Key: "observability.logs.level"},
	{Field: "DumpConfig.Enabled", Flag: FlagDumpEnabled, Key: "debug_dump.enabled"},
	{Field: "DumpConfig.Directory", Flag: FlagDumpDirectory, Key: "debug_dump.directory"},
	{Field: "DumpConfig.Retention", Flag: FlagDumpRetention, Key: "debug_dump.retention"},
	{Field: "DumpConfig.Compression", Flag: FlagDumpCompression, Key: "debug_dump.compression"},
	{Field: "DumpConfig.MinInterval", Flag: FlagDumpMinInterval, Key: "debug_dump.min_interval"},
	{Field: "DumpConfig.MaxFileBytes", Flag: FlagDumpMaxFileBytes, Key: "debug_dump.max_file_bytes"},
	{Field: "DumpConfig.MaxTotalBytes", Flag: FlagDumpMaxTotalBytes, Key: "debug_dump.max_total_bytes"},
	{Field: "DumpConfig.MaxFiles", Flag: FlagDumpMaxFiles, Key: "debug_dump.max_files"},
	{Field: "UseOldNamespace", Flag: FlagUseOldNamespace},
	{Field: "NoHostname", Flag: FlagNoHostname},
	{Field: "ReplaceBlanksInModelName", Flag: FlagReplaceBlanksInModelName},
	{Field: "UseFakeGPUs", Flag: FlagUseFakeGPUs},
	{Field: "DisableStartupValidate", Flag: FlagDisableStartupValidate},
}

// SettingKeys returns fixedSettingKeys for c, with the keys that depend on c
// filled in: the metric source, the counter windows, one group of rows for each
// watch group, and the output name.
func SettingKeys(c *Config, names ConfigNames) []SettingKey {
	names = names.withDefaults()
	positions, windows := OverridePositions(c)
	keys := make([]SettingKey, 0, len(fixedSettingKeys))
	for _, k := range fixedSettingKeys {
		switch k.Field {
		case "MetricSource":
			k.Flag, k.Key = metricSourceKey(c.MetricSource.Kind)
		case FieldXIDCountWindowSize:
			k.Key = countWindowKey(ExporterXIDErrorsCountField, windows)
		case FieldClockEventsCountWindowSize:
			k.Key = countWindowKey(ExporterClockEventsCountField, windows)
		case "Address":
			k.Key = "outputs." + names.Output + ".listen"
		}

		keys = append(keys, k)
		if k.Field == FieldClockEventsCountWindowSize {
			keys = append(keys, watchGroupSettingKeys(c, positions)...)
		}
	}

	return keys
}

// metricSourceKey is the flag and key that set the metric source of the given kind.
func metricSourceKey(kind MetricSourceKind) (flag, key string) {
	switch kind {
	case MetricSourceInline:
		return FlagFieldsFile, "metrics[0].fields"
	case MetricSourceConfigMap:
		return FlagConfigMapData, ""
	}

	return FlagFieldsFile, "metrics[0].file"
}

// countWindowKey is the key of one counter's sampling window. The document
// writes the window as an override, at the position the counter's override has,
// and writes no override while the window is at its default; then the key is empty.
func countWindowKey(counter string, positions map[string]int) string {
	position, written := positions[counter]
	if !written {
		return ""
	}

	return "metrics[0].overrides[" + strconv.Itoa(position) + "].window"
}

// watchGroupSettingKeys lists the settings of each watch group. The retention
// settings appear only for a group that sets its own retention.
func watchGroupSettingKeys(c *Config, positions []int) []SettingKey {
	var settings []SettingKey
	for i, group := range c.WatchGroups {
		field, key := WatchGroupField(i), fmt.Sprintf("metrics[0].overrides[%d]", positions[i])
		if !IsGeneratedOverrideName(group.Name) {
			// An unnamed override has no name key to report: the reader made the
			// name up from the override's key, and no input set it.
			settings = append(settings, SettingKey{Field: field + ".Name", Key: key + ".name"})
		}

		settings = append(settings,
			SettingKey{Field: field + ".Interval", Key: key + ".every"},
			SettingKey{Field: field + ".Fields", Key: key + ".match"},
		)
		if group.Retention.MaxAge != nil {
			settings = append(settings, SettingKey{Field: WatchGroupMaxAgeField(i), Key: key + ".sources.dcgm.watch.max_keep_age"})
		}

		if group.Retention.MaxSamples != nil {
			settings = append(settings, SettingKey{
				Field: WatchGroupMaxSamplesField(i), Key: key + ".sources.dcgm.watch.max_keep_samples",
			})
		}
	}

	return settings
}
