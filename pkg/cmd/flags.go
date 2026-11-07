/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// contextToConfig resolves CLI, environment, and optional YAML input into one
// configuration snapshot. Startup calls it once; CSV reloads clone that result
// instead of rereading YAML or reevaluating command-line precedence.
func contextToConfig(c *cli.Context) (*appconfig.Config, error) {
	resolved, err := resolveConfig(c, "", nil, true)
	if err != nil {
		return nil, err
	}

	addContainerIgnoredWarning(resolved)
	for _, warning := range resolved.Warnings {
		slog.Warn(warning.Message, warning.Attrs...)
	}

	return resolved.Config, nil
}

// applyExplicitConfigOverrides applies startup flags and environment variables
// that were explicitly set. contextToConfig calls it after loading YAML so
// explicit command-line settings take precedence over the file.
func applyExplicitConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	applyBaseConfigOverrides(c, config)
	applyKubernetesConfigOverrides(c, config)
	applyExporterConfigOverrides(c, config)
	applyRuntimeConfigOverrides(c, config)
	// Each step that can fail reports its own problems, and a failure in one
	// does not hide the others.
	return errors.Join(
		applyDumpConfigOverrides(c, config),
		applyDeviceConfigOverrides(c, config),
		applyConfigMapOverride(c, config),
		applyWebConfigOverrides(c, config),
		applyDRAAndLifecycleConfigOverrides(c, config),
	)
}

// applyBaseConfigOverrides applies CLI values shared by all runtime modes.
func applyBaseConfigOverrides(c *cli.Context, config *appconfig.Config) {
	if c.IsSet(CLIFieldsFile) {
		config.MetricSource = appconfig.MetricSource{
			Kind: appconfig.MetricSourceFile,
			File: c.String(CLIFieldsFile),
		}
	}

	if c.IsSet(CLIAddress) {
		config.Address = c.String(CLIAddress)
	}

	if c.IsSet(CLICollectInterval) {
		config.CollectInterval = c.Int(CLICollectInterval)
	}

	if c.IsSet(CLIWatchMaxKeepAge) {
		config.WatchRetention.MaxAge = c.Duration(CLIWatchMaxKeepAge)
	}

	if c.IsSet(CLIWatchMaxKeepSamples) {
		config.WatchRetention.MaxSamples = c.Int64(CLIWatchMaxKeepSamples)
	}
}

// applyKubernetesConfigOverrides applies Kubernetes CLI settings to runtime config.
func applyKubernetesConfigOverrides(c *cli.Context, config *appconfig.Config) {
	if c.IsSet(CLIKubernetes) {
		config.Kubernetes = c.Bool(CLIKubernetes)
		config.ClearEnrichmentMode(appconfig.EnrichmentKubernetes)
	}

	if c.IsSet(CLIKubernetesEnablePodLabels) {
		config.KubernetesEnablePodLabels = c.Bool(CLIKubernetesEnablePodLabels)
	}

	if c.IsSet(CLIKubernetesEnablePodUID) {
		config.KubernetesEnablePodUID = c.Bool(CLIKubernetesEnablePodUID)
	}

	if c.IsSet(CLIKubernetesGPUIDType) {
		config.KubernetesGPUIdType = appconfig.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType))
	}

	if c.IsSet(CLIKubernetesPodLabelAllowlistRegex) {
		config.KubernetesPodLabelAllowlistRegex = c.StringSlice(CLIKubernetesPodLabelAllowlistRegex)
	}

	if c.IsSet(CLIUseOldNamespace) {
		config.UseOldNamespace = c.Bool(CLIUseOldNamespace)
	}

	if c.IsSet(CLIRemoteHEInfo) {
		config.UseRemoteHE = true
		config.RemoteHEInfo = c.String(CLIRemoteHEInfo)
	}
}

// applyDeviceConfigOverrides applies device selection CLI settings to runtime config.
func applyDeviceConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLINoHostname) {
		config.NoHostname = c.Bool(CLINoHostname)
	}

	if c.IsSet(CLIUseFakeGPUs) {
		config.UseFakeGPUs = c.Bool(CLIUseFakeGPUs)
	}

	return errors.Join(
		applyDeviceOptionOverride(c, CLIGPUDevices, "GPUDeviceOptions", &config.GPUDeviceOptions),
		applyDeviceOptionOverride(c, CLISwitchDevices, "SwitchDeviceOptions", &config.SwitchDeviceOptions),
		applyDeviceOptionOverride(c, CLICPUDevices, "CPUDeviceOptions", &config.CPUDeviceOptions),
	)
}

// applyDeviceOptionOverride applies one explicit device selector over earlier settings.
func applyDeviceOptionOverride(c *cli.Context, flag, field string, target *appconfig.DeviceOptions) error {
	if !c.IsSet(flag) {
		return nil
	}

	options, err := parseDeviceOptions(c.String(flag))
	if err != nil {
		return &appconfig.SettingError{Err: err, Field: field}
	}

	*target = options
	return nil
}

// applyConfigMapOverride applies an explicitly set ConfigMap source during CLI config conversion.
func applyConfigMapOverride(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLIConfigMapData) {
		if err := applyConfigMapDataSource(config, c.String(CLIConfigMapData)); err != nil {
			return &appconfig.SettingError{Err: err, Field: "MetricSource"}
		}
	}

	return nil
}

// applyWebConfigOverrides applies web settings and rejects invalid explicit timeouts during CLI config conversion.
func applyWebConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLIHealthRequireGPUs) {
		config.HealthRequireGPUs = c.Bool(CLIHealthRequireGPUs)
	}

	if c.IsSet(CLIWebSystemdSocket) {
		config.WebSystemdSocket = c.Bool(CLIWebSystemdSocket)
	}

	if c.IsSet(CLIWebConfigFile) {
		config.WebConfigFile = c.String(CLIWebConfigFile)
	}

	return errors.Join(
		applyDurationOverride(c, CLIWebReadTimeout, "WebReadTimeout", &config.WebReadTimeout),
		applyDurationOverride(c, CLIWebWriteTimeout, "WebWriteTimeout", &config.WebWriteTimeout),
	)
}

// applyDurationOverride applies an explicit duration flag; a value that is not
// a duration leaves the target unchanged.
func applyDurationOverride(c *cli.Context, flag, field string, target *time.Duration) error {
	if !c.IsSet(flag) {
		return nil
	}

	duration, err := parseExplicitDuration(c, flag)
	if err != nil {
		return &appconfig.SettingError{Err: err, Field: field}
	}

	*target = duration
	return nil
}

// applyExporterConfigOverrides applies exporter CLI settings to runtime config.
func applyExporterConfigOverrides(c *cli.Context, config *appconfig.Config) {
	if c.IsSet(CLIMaxConcurrentScrapes) {
		config.MaxConcurrentScrapes = c.Int(CLIMaxConcurrentScrapes)
	}

	if c.IsSet(CLIEnableExporterMetrics) {
		config.EnableExporterMetrics = c.Bool(CLIEnableExporterMetrics)
	}

	if c.IsSet(CLIXIDCountWindowSize) {
		config.XIDCountWindowSize = c.Int(CLIXIDCountWindowSize)
	}

	if c.IsSet(CLIReplaceBlanksInModelName) {
		config.ReplaceBlanksInModelName = c.Bool(CLIReplaceBlanksInModelName)
	}

	if c.IsSet(CLIDebugMode) {
		config.Debug = c.Bool(CLIDebugMode)
	}

	if c.IsSet(CLILogFormat) {
		config.LogFormat = c.String(CLILogFormat)
	}

	if c.IsSet(CLIClockEventsCountWindowSize) {
		config.ClockEventsCountWindowSize = c.Int(CLIClockEventsCountWindowSize)
	}

	if c.IsSet(CLIEnableDCGMLog) {
		config.EnableDCGMLog = c.Bool(CLIEnableDCGMLog)
	}

	if c.IsSet(CLIDCGMLogLevel) {
		config.DCGMLogLevel = c.String(CLIDCGMLogLevel)
	}
}

// applyRuntimeConfigOverrides applies runtime and socket CLI settings to runtime config.
func applyRuntimeConfigOverrides(c *cli.Context, config *appconfig.Config) {
	if c.IsSet(CLIPodResourcesKubeletSocket) {
		config.PodResourcesKubeletSocket = c.String(CLIPodResourcesKubeletSocket)
	}

	// A flag that sets a source replaces the config file's choice for it.
	if c.IsSet(CLIHPCJobMappingDir) {
		config.HPCJobMappingDir = c.String(CLIHPCJobMappingDir)
		config.ClearEnrichmentMode(appconfig.EnrichmentHPCJobs)
	}

	if c.IsSet(CLIHPCJobMappingMaxFileBytes) {
		config.HPCJobMappingMaxFileBytes = c.Int64(CLIHPCJobMappingMaxFileBytes)
	}

	if c.IsSet(CLIHPCJobMappingMaxJobIDBytes) {
		config.HPCJobMappingMaxJobIDBytes = c.Int64(CLIHPCJobMappingMaxJobIDBytes)
	}

	if c.IsSet(CLIHPCJobMappingMaxJobsPerFile) {
		config.HPCJobMappingMaxJobsPerFile = c.Int64(CLIHPCJobMappingMaxJobsPerFile)
	}

	if c.IsSet(CLIContainerLabels) {
		config.ContainerLabels = c.Bool(CLIContainerLabels)
		config.ClearEnrichmentMode(appconfig.EnrichmentContainer)
	}

	if c.IsSet(CLIContainerRuntimeSocket) {
		config.ContainerRuntimeSocket = c.String(CLIContainerRuntimeSocket)
	}

	if c.IsSet(CLINvidiaResourceNames) {
		config.NvidiaResourceNames = trimmedNonBlank(c.StringSlice(CLINvidiaResourceNames))
	}

	if c.IsSet(CLIKubernetesVirtualGPUs) {
		config.KubernetesVirtualGPUs = c.Bool(CLIKubernetesVirtualGPUs)
	}
}

// trimmedNonBlank returns the values without surrounding spaces and without
// blank entries. The version 2 file trims the same spaces but rejects a blank
// entry as an error; the flag keeps its older best-effort meaning and drops it.
func trimmedNonBlank(values []string) []string {
	kept := make([]string, 0, len(values))

	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			kept = append(kept, value)
		}
	}

	return kept
}

// applyDumpConfigOverrides applies dump mode CLI settings to runtime config.
func applyDumpConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLIDumpEnabled) {
		config.DumpConfig.Enabled = c.Bool(CLIDumpEnabled)
	}

	if c.IsSet(CLIDumpDirectory) {
		config.DumpConfig.Directory = c.String(CLIDumpDirectory)
	}

	if c.IsSet(CLIDumpRetention) {
		retention, err := appconfig.DumpRetentionFromHours(c.Int(CLIDumpRetention))
		if err != nil {
			return &appconfig.SettingError{Err: err, Field: "DumpConfig.Retention"}
		}

		config.DumpConfig.Retention = retention
	}

	if c.IsSet(CLIDumpCompression) {
		config.DumpConfig.Compression = c.Bool(CLIDumpCompression)
	}

	if c.IsSet(CLIDumpMaxFileBytes) {
		config.DumpConfig.MaxFileBytes = c.Int64(CLIDumpMaxFileBytes)
	}

	if c.IsSet(CLIDumpMaxTotalBytes) {
		config.DumpConfig.MaxTotalBytes = c.Int64(CLIDumpMaxTotalBytes)
	}

	if c.IsSet(CLIDumpMaxFiles) {
		config.DumpConfig.MaxFiles = c.Int64(CLIDumpMaxFiles)
	}

	return applyDurationOverride(c, CLIDumpMinInterval, "DumpConfig.MinInterval", &config.DumpConfig.MinInterval)
}

// applyDRAAndLifecycleConfigOverrides applies DRA, GPU lifecycle and DCGM
// cleanup timeout settings during CLI config conversion.
func applyDRAAndLifecycleConfigOverrides(c *cli.Context, config *appconfig.Config) error {
	if c.IsSet(CLIKubernetesEnableDRA) {
		config.KubernetesEnableDRA = c.Bool(CLIKubernetesEnableDRA)
		config.ClearEnrichmentMode(appconfig.EnrichmentDRA)
	}

	if c.IsSet(CLIDisableStartupValidate) {
		config.DisableStartupValidate = c.Bool(CLIDisableStartupValidate)
	}

	if c.IsSet(CLIEnableGPUBindUnbindWatch) {
		config.EnableGPUBindUnbindWatch = c.Bool(CLIEnableGPUBindUnbindWatch)
	}

	if c.IsSet(CLIEnablePprof) {
		config.EnablePprof = c.Bool(CLIEnablePprof)
	}

	if c.IsSet(CLIDCGMCleanupTimeout) {
		config.DCGMCleanupTimeout = c.Duration(CLIDCGMCleanupTimeout)
	}

	return applyDurationOverride(c, CLIGPUBindUnbindPollInterval, "GPUBindUnbindPollInterval", &config.GPUBindUnbindPollInterval)
}

// applyConfigMapDataSource selects the compatibility API-backed ConfigMap metric source.
func applyConfigMapDataSource(config *appconfig.Config, configMapData string) error {
	if configMapData == "" || configMapData == undefinedConfigMapData {
		// An unset ConfigMap does not override defaults, YAML, or --collectors.
		return nil
	}

	parts := strings.Split(configMapData, ":")
	if len(parts) != 2 {
		return fmt.Errorf("malformed configmap-data %q", configMapData)
	}

	namespace := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if namespace == "" || name == "" {
		return fmt.Errorf("malformed configmap-data %q: namespace and name are required", configMapData)
	}

	config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceConfigMap,
		ConfigMap: appconfig.ConfigMapMetricSource{
			Namespace: namespace,
			Name:      name,
		},
	}

	return nil
}

// parseExplicitDuration parses an explicitly set CLI duration and names the setting in errors.
func parseExplicitDuration(c *cli.Context, flag string) (time.Duration, error) {
	duration, err := time.ParseDuration(c.String(flag))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", flag, err)
	}

	return duration, nil
}

// deprecatedFlags lists released flags that keep working but have a better
// replacement, which help and warnings name.
var deprecatedFlags = []struct{ name, replacement string }{
	{CLIConfigMapData, "mount the ConfigMap as a file and pass its path with --" + CLIFieldsFile + " instead"},
}

// deprecatedUsage appends the deprecation notice for name to a flag's help
// description. NewApp uses it so help and warnings share one replacement text.
func deprecatedUsage(name, description string) string {
	for _, f := range deprecatedFlags {
		if f.name == name {
			return description + " Deprecated: still supported; " + f.replacement + "."
		}
	}
	panic("flag --" + name + " is not in deprecatedFlags")
}

// deprecationWarnings returns one warning for each deprecated flag set by argv
// or environment in any of the given layers. A deprecated flag is a string
// flag, and any explicit value warns, including an empty one, because the flag
// itself is going away. Flag behavior is unchanged.
func deprecationWarnings(layers ...*cli.Context) []appconfig.ResolutionWarning {
	var warnings []appconfig.ResolutionWarning
	for _, f := range deprecatedFlags {
		for _, c := range layers {
			if c != nil && c.IsSet(f.name) {
				warnings = append(warnings, appconfig.ResolutionWarning{
					Message: fmt.Sprintf("flag --%s is deprecated and still supported; %s", f.name, f.replacement),
					Attrs:   []any{slog.String("flag", f.name)},
					Origin:  flagOrigin(c, f.name),
				})

				break
			}
		}
	}

	return warnings
}
