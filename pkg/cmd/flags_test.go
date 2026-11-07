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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// Test_contextToConfig_DumpConfig checks dump mode settings when the CLI context becomes config.
func Test_contextToConfig_DumpConfig(t *testing.T) {
	tests := []struct {
		name   string
		flags  map[string]string
		expect func(*appconfig.DumpConfig)
	}{
		{
			name:   "Default dump config",
			flags:  map[string]string{CLIGPUDevices: "f"},
			expect: func(*appconfig.DumpConfig) {},
		},
		{
			name: "Enabled dump config with custom settings",
			flags: map[string]string{
				CLIGPUDevices:        "f",
				CLIDumpEnabled:       "true",
				CLIDumpDirectory:     "/custom/debug/dir",
				CLIDumpRetention:     "48",
				CLIDumpCompression:   "false",
				CLIDumpMinInterval:   "5s",
				CLIDumpMaxFileBytes:  "100",
				CLIDumpMaxTotalBytes: "1000",
				CLIDumpMaxFiles:      "3",
			},
			expect: func(c *appconfig.DumpConfig) {
				c.Enabled, c.Directory, c.Retention, c.Compression = true, "/custom/debug/dir", 48*time.Hour, false
				c.MinInterval, c.MaxFileBytes, c.MaxTotalBytes, c.MaxFiles = 5*time.Second, 100, 1000, 3
			},
		},
		{
			name: "Enabled dump config with no retention",
			flags: map[string]string{
				CLIGPUDevices:    "f",
				CLIDumpEnabled:   "true",
				CLIDumpRetention: "0",
			},
			expect: func(c *appconfig.DumpConfig) { c.Enabled, c.Retention = true, 0 },
		},
		{
			name: "Negative retention means no age cleanup",
			flags: map[string]string{
				CLIGPUDevices:    "f",
				CLIDumpEnabled:   "true",
				CLIDumpRetention: "-5",
			},
			expect: func(c *appconfig.DumpConfig) { c.Enabled, c.Retention = true, 0 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestCLIContext(t)
			for name, value := range tt.flags {
				require.NoError(t, context.Set(name, value))
			}

			config, err := contextToConfig(context)
			require.NoError(t, err)

			expected := appconfig.DefaultDumpConfig()
			tt.expect(&expected)
			assert.Equal(t, expected, config.DumpConfig)
		})
	}
}

// TestContextToConfigRejectsInvalidDumpLimits checks that each unusable dump
// limit fails config conversion with an error that names its flag.
func TestContextToConfigRejectsInvalidDumpLimits(t *testing.T) {
	tests := []struct {
		flag, value, want string
	}{
		{CLIDumpMinInterval, "0s", "dump-min-interval"},
		{CLIDumpMinInterval, "soon", "dump-min-interval"},
		{CLIDumpMaxFileBytes, "0", "dump-max-file-bytes"},
		{CLIDumpMaxFileBytes, "268435457", "must not exceed dump-max-total-bytes"},
		{CLIDumpMaxTotalBytes, "-1", "dump-max-total-bytes"},
		{CLIDumpMaxFiles, "0", "dump-max-files"},
		{CLIDumpRetention, "9223372036854775807", "dump-retention"},
		{CLIDumpDirectory, " ", "dump-directory must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.flag+"="+tt.value, func(t *testing.T) {
			context := newTestCLIContext(t)
			require.NoError(t, context.Set(CLIDumpEnabled, "true"))
			require.NoError(t, context.Set(tt.flag, tt.value))

			_, err := contextToConfig(context)

			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestNewApp_ConfiguresVersionFlagsAndAction checks the CLI exposes version flags and its startup action.
func TestNewApp_ConfiguresVersionFlagsAndAction(t *testing.T) {
	app := NewApp("test-version")

	require.NotNil(t, app)
	assert.Equal(t, "DCGM Exporter", app.Name)
	assert.Equal(t, "test-version", app.Version)
	assert.NotNil(t, app.Action)

	names := make(map[string]bool)
	for _, f := range app.Flags {
		for _, name := range f.Names() {
			names[name] = true
		}
	}

	for _, name := range []string{
		CLIConfigFile,
		CLIFieldsFile,
		CLIAddress,
		CLICollectInterval,
		CLIWatchMaxKeepAge,
		CLIWatchMaxKeepSamples,
		CLIGPUDevices,
		CLISwitchDevices,
		CLICPUDevices,
		CLIHealthRequireGPUs,
		CLIDumpEnabled,
		CLIEnablePprof,
		CLIWebSystemdSocket,
		CLIWebReadTimeout,
		CLIWebWriteTimeout,
		CLIMaxConcurrentScrapes,
		CLIEnableExporterMetrics,
		CLIEnableGPUBindUnbindWatch,
		CLIGPUBindUnbindPollInterval,
	} {
		assert.Truef(t, names[name], "expected flag %q to be registered", name)
	}
}

// TestNewAppDefaultsMatchDefaultConfig checks CLI defaults agree with the default runtime config.
func TestNewAppDefaultsMatchDefaultConfig(t *testing.T) {
	app := NewApp("test-version")
	unsetFlagEnvVars(t, app.Flags)

	var cfg *appconfig.Config
	app.Action = func(c *cli.Context) error {
		defaults := appconfig.DefaultConfig()

		assert.Equal(t, defaults.ConfigFile, c.String(CLIConfigFile))
		assert.Equal(t, defaults.MetricSource.File, c.String(CLIFieldsFile))
		assert.Equal(t, defaults.Address, c.String(CLIAddress))
		assert.Equal(t, defaults.CollectInterval, c.Int(CLICollectInterval))
		assert.Equal(t, defaults.WatchRetention.MaxAge, c.Duration(CLIWatchMaxKeepAge))
		assert.Equal(t, defaults.WatchRetention.MaxSamples, c.Int64(CLIWatchMaxKeepSamples))

		assert.Equal(t, defaults.Kubernetes, c.Bool(CLIKubernetes))
		assert.Equal(t, defaults.KubernetesEnablePodLabels, c.Bool(CLIKubernetesEnablePodLabels))
		assert.Equal(t, defaults.KubernetesEnablePodUID, c.Bool(CLIKubernetesEnablePodUID))
		assert.Equal(t, string(defaults.KubernetesGPUIdType), c.String(CLIKubernetesGPUIDType))
		assert.Empty(t, defaults.KubernetesPodLabelAllowlistRegex)
		assert.Empty(t, c.StringSlice(CLIKubernetesPodLabelAllowlistRegex))

		assert.Equal(t, defaults.UseOldNamespace, c.Bool(CLIUseOldNamespace))
		assert.Equal(t, defaults.RemoteHEInfo, c.String(CLIRemoteHEInfo))

		gpuDeviceOptions, err := parseDeviceOptions(c.String(CLIGPUDevices))
		require.NoError(t, err)
		assert.Equal(t, defaults.GPUDeviceOptions, gpuDeviceOptions)

		switchDeviceOptions, err := parseDeviceOptions(c.String(CLISwitchDevices))
		require.NoError(t, err)
		assert.Equal(t, defaults.SwitchDeviceOptions, switchDeviceOptions)

		cpuDeviceOptions, err := parseDeviceOptions(c.String(CLICPUDevices))
		require.NoError(t, err)
		assert.Equal(t, defaults.CPUDeviceOptions, cpuDeviceOptions)

		assert.Equal(t, defaults.HealthRequireGPUs, c.Bool(CLIHealthRequireGPUs))
		assert.Equal(t, defaults.NoHostname, c.Bool(CLINoHostname))
		assert.Equal(t, defaults.UseFakeGPUs, c.Bool(CLIUseFakeGPUs))
		assert.Equal(t, undefinedConfigMapData, c.String(CLIConfigMapData))

		assert.Equal(t, defaults.WebSystemdSocket, c.Bool(CLIWebSystemdSocket))
		assert.Equal(t, defaults.WebConfigFile, c.String(CLIWebConfigFile))
		assert.Equal(t, defaults.WebReadTimeout.String(), c.String(CLIWebReadTimeout))
		assert.Equal(t, defaults.WebWriteTimeout.String(), c.String(CLIWebWriteTimeout))
		assert.Equal(t, defaults.MaxConcurrentScrapes, c.Int(CLIMaxConcurrentScrapes))
		assert.Equal(t, defaults.EnableExporterMetrics, c.Bool(CLIEnableExporterMetrics))

		assert.Equal(t, defaults.XIDCountWindowSize, c.Int(CLIXIDCountWindowSize))
		assert.Equal(t, defaults.ReplaceBlanksInModelName, c.Bool(CLIReplaceBlanksInModelName))
		assert.Equal(t, defaults.Debug, c.Bool(CLIDebugMode))
		assert.Equal(t, defaults.ClockEventsCountWindowSize, c.Int(CLIClockEventsCountWindowSize))
		assert.Equal(t, defaults.EnableDCGMLog, c.Bool(CLIEnableDCGMLog))
		assert.Equal(t, defaults.DCGMLogLevel, c.String(CLIDCGMLogLevel))

		assert.Equal(t, defaults.PodResourcesKubeletSocket, c.String(CLIPodResourcesKubeletSocket))
		assert.Equal(t, defaults.HPCJobMappingDir, c.String(CLIHPCJobMappingDir))
		assert.Empty(t, defaults.NvidiaResourceNames)
		assert.Empty(t, c.StringSlice(CLINvidiaResourceNames))
		assert.Equal(t, defaults.KubernetesVirtualGPUs, c.Bool(CLIKubernetesVirtualGPUs))

		assert.Equal(t, defaults.DumpConfig.Enabled, c.Bool(CLIDumpEnabled))
		assert.Equal(t, defaults.DumpConfig.Directory, c.String(CLIDumpDirectory))
		assert.Equal(t, defaults.DumpConfig.Retention, time.Duration(c.Int(CLIDumpRetention))*time.Hour)
		assert.Equal(t, defaults.DumpConfig.Compression, c.Bool(CLIDumpCompression))

		assert.Equal(t, defaults.KubernetesEnableDRA, c.Bool(CLIKubernetesEnableDRA))
		assert.Equal(t, defaults.DisableStartupValidate, c.Bool(CLIDisableStartupValidate))
		assert.Equal(t, defaults.EnableGPUBindUnbindWatch, c.Bool(CLIEnableGPUBindUnbindWatch))
		assert.Equal(t, defaults.GPUBindUnbindPollInterval.String(), c.String(CLIGPUBindUnbindPollInterval))
		assert.Equal(t, defaults.EnablePprof, c.Bool(CLIEnablePprof))

		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	defaults := appconfig.DefaultConfig()
	assert.Equal(t, defaults, cfg)
}

// TestGPUBindUnbindDefaults checks default GPU lifecycle watch settings.
func TestGPUBindUnbindDefaults(t *testing.T) {
	config := appconfig.DefaultConfig()
	assert.False(t, config.EnableGPUBindUnbindWatch)
	assert.Equal(t, time.Second, config.GPUBindUnbindPollInterval)
}

// TestStartDCGMExporterWithSignalSource_RejectsInvalidConfigBeforeDCGM checks invalid settings fail before DCGM initialization.
func TestStartDCGMExporterWithSignalSource_RejectsInvalidConfigBeforeDCGM(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIGPUDevices, "x"))

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the only valid options")
}

// TestContextToConfigPreservesVsockRemoteHostengineInfo checks the CLI keeps remote vsock hostengine settings.
func TestContextToConfigPreservesVsockRemoteHostengineInfo(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIRemoteHEInfo, "vsock://3:5555"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.UseRemoteHE)
	assert.Equal(t, "vsock://3:5555", cfg.RemoteHEInfo)
}

// TestContextToConfigPreservesVsockRemoteHostengineInfoFromEnv checks environment values keep remote vsock hostengine settings.
func TestContextToConfigPreservesVsockRemoteHostengineInfoFromEnv(t *testing.T) {
	t.Setenv("DCGM_REMOTE_HOSTENGINE_INFO", "vsock://3:5555")

	var cfg *appconfig.Config
	app := NewApp("test-version")
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.UseRemoteHE)
	assert.Equal(t, "vsock://3:5555", cfg.RemoteHEInfo)
}

// TestContextToConfigNoYAMLUsesLegacyDefaultMetricSource checks the legacy metric source when no YAML file is given.
func TestContextToConfigNoYAMLUsesLegacyDefaultMetricSource(t *testing.T) {
	ctx := newTestCLIContext(t)

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Empty(t, cfg.ConfigFile)
	assert.Equal(t, 30000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DefaultWatchRetention(), cfg.WatchRetention)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, appconfig.DefaultCollectorsFile, watchFile)
}

// TestContextToConfigAppliesFieldWatchRetentionOverrides verifies explicit CLI values replace YAML global bounds.
func TestContextToConfigAppliesFieldWatchRetentionOverrides(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIWatchMaxKeepAge, "2m"))
	require.NoError(t, ctx.Set(CLIWatchMaxKeepSamples, "4"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, appconfig.WatchRetention{
		MaxAge:     2 * time.Minute,
		MaxSamples: 4,
	}, cfg.WatchRetention)
}

// TestContextToConfigResolvesWatchGroupRetentionAfterCLIOverrides guards late property-level group inheritance.
func TestContextToConfigResolvesWatchGroupRetentionAfterCLIOverrides(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    watch:
      max_keep_age: 5m
      max_keep_samples: 0
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    overrides:
      - name: latest-values
        match: [DCGM_FI_DEV_GPU_TEMP]
        every: 1s
        sources:
          dcgm:
            watch:
              max_keep_age: 0s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIWatchMaxKeepSamples, "7"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, appconfig.WatchRetention{
		MaxAge:     5 * time.Minute,
		MaxSamples: 7,
	}, cfg.WatchRetention)
	require.Len(t, cfg.WatchGroups, 1)
	assert.Equal(t, appconfig.WatchRetention{
		MaxAge:     0,
		MaxSamples: 7,
	}, cfg.WatchGroups[0].Retention.Resolve(cfg.WatchRetention))
}

// TestContextToConfigRejectsUnboundedYAMLFieldWatchRetention verifies final startup validation rejects zero bounds.
func TestContextToConfigRejectsUnboundedYAMLFieldWatchRetention(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    watch:
      max_keep_age: 0s
      max_keep_samples: 0
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "cannot both be zero")
}

// TestContextToConfigRejectsInvalidFieldWatchRetention covers CLI range failures before exporter startup.
func TestContextToConfigRejectsInvalidFieldWatchRetention(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		value   string
		wantErr string
	}{
		{name: "negative age", flag: CLIWatchMaxKeepAge, value: "-1s", wantErr: "maxAge must not be negative"},
		{name: "negative samples", flag: CLIWatchMaxKeepSamples, value: "-1", wantErr: "maxSamples must not be negative"},
		{name: "samples exceed DCGM range", flag: CLIWatchMaxKeepSamples, value: "2147483648", wantErr: "must not exceed 2147483647"},
		{name: "both bounds disabled", flag: CLIWatchMaxKeepAge, value: "0s", wantErr: "cannot both be zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(tt.flag, tt.value))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestNewAppRejectsMalformedFieldWatchMaxAge verifies the CLI duration parser reports malformed user input.
func TestNewAppRejectsMalformedFieldWatchMaxAge(t *testing.T) {
	app := NewApp("test-version")
	app.Action = func(*cli.Context) error { return nil }

	err := app.Run([]string{"dcgm-exporter", "--watch-max-keep-age=not-a-duration"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
}

// TestContextToConfigLoadsFieldWatchRetentionFromEnvironment verifies environment values use CLI override precedence.
func TestContextToConfigLoadsFieldWatchRetentionFromEnvironment(t *testing.T) {
	app := NewApp("test-version")
	unsetFlagEnvVars(t, app.Flags)
	t.Setenv("DCGM_EXPORTER_WATCH_MAX_KEEP_AGE", "90s")
	t.Setenv("DCGM_EXPORTER_WATCH_MAX_KEEP_SAMPLES", "8")

	var cfg *appconfig.Config
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	require.NoError(t, app.Run([]string{"dcgm-exporter"}))
	require.NotNil(t, cfg)
	assert.Equal(t, appconfig.WatchRetention{
		MaxAge:     90 * time.Second,
		MaxSamples: 8,
	}, cfg.WatchRetention)
}

// TestContextToConfigYAMLConfig checks YAML values become runtime configuration.
func TestContextToConfigYAMLConfig(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: scrape
    every: 10s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP
        prometheus_type: gauge
        help: GPU temperature.
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
observability:
  metrics:
    enabled: true
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, configFile, cfg.ConfigFile)
	assert.Equal(t, 10000, cfg.CollectInterval)
	assert.Equal(t, appconfig.MetricSourceInline, cfg.MetricSource.Kind)
	assert.True(t, cfg.EnableExporterMetrics)
	require.Len(t, cfg.MetricSource.Fields, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", cfg.MetricSource.Fields[0].Name)
	_, watchFile := cfg.MetricFileWatcherPath()
	assert.False(t, watchFile)
}

// TestContextToConfigYAMLOmittedMetricsUsesDefaultMetricSource checks omitted YAML metrics select the default source.
func TestContextToConfigYAMLOmittedMetricsUsesDefaultMetricSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("version: 2\nserver:\n  max_concurrent_scrapes: 8\n"), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, 8, cfg.MaxConcurrentScrapes)
	assert.Equal(t, 30000, cfg.CollectInterval)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, appconfig.DefaultCollectorsFile, watchFile)
}

// TestContextToConfigYAMLFileSourceIsWatched checks a YAML metric file is added to reload watches.
func TestContextToConfigYAMLFileSourceIsWatched(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	countersFile := filepath.Join(t.TempDir(), "yaml-counters.csv")
	require.NoError(t, os.WriteFile(configFile, []byte(fmt.Sprintf(`
version: 2
metrics:
  - name: gpu
    file: %s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`, countersFile)), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, countersFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, countersFile, watchFile)
}

// TestContextToConfigLegacyFlagsOverrideYAML checks explicit legacy flags take precedence over YAML values.
func TestContextToConfigLegacyFlagsOverrideYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: scrape
    every: 10s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP
        prometheus_type: gauge
        help: GPU temperature.
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
observability:
  metrics:
    enabled: true
`), 0o600))
	countersFile := filepath.Join(t.TempDir(), "override.csv")

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIFieldsFile, countersFile))
	require.NoError(t, ctx.Set(CLICollectInterval, "45000"))
	require.NoError(t, ctx.Set(CLIEnableExporterMetrics, "false"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, 45000, cfg.CollectInterval)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.False(t, cfg.EnableExporterMetrics)
	assert.Equal(t, countersFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, countersFile, watchFile)
}

// TestContextToConfigLegacyConfigMapDataOverridesYAML checks explicit ConfigMap data takes precedence over YAML metrics.
func TestContextToConfigLegacyConfigMapDataOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP
        prometheus_type: gauge
        help: GPU temperature.
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIConfigMapData, "monitoring:legacy-metrics"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, appconfig.MetricSourceConfigMap, cfg.MetricSource.Kind)
	assert.Equal(t, "monitoring", cfg.MetricSource.ConfigMap.Namespace)
	assert.Equal(t, "legacy-metrics", cfg.MetricSource.ConfigMap.Name)
	_, watchFile := cfg.MetricFileWatcherPath()
	assert.False(t, watchFile)
}

// TestContextToConfigUnsetConfigMapPreservesMetricSource checks an unset ConfigMap flag leaves the chosen metric source alone.
func TestContextToConfigUnsetConfigMapPreservesMetricSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`version: 2
metrics:
  - name: gpu
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP
        prometheus_type: gauge
        help: GPU temperature.
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
	for _, value := range []string{"", undefinedConfigMapData} {
		for _, tc := range []struct {
			name string
			yaml bool
			file string
		}{
			{name: "defaults"},
			{name: "inline YAML", yaml: true},
			{name: "collectors override", yaml: true, file: "/custom/counters.csv"},
		} {
			t.Run(tc.name+"/"+value, func(t *testing.T) {
				ctx := newTestCLIContext(t)
				if tc.yaml {
					require.NoError(t, ctx.Set(CLIConfigFile, configFile))
				}
				if tc.file != "" {
					require.NoError(t, ctx.Set(CLIFieldsFile, tc.file))
				}
				before, err := contextToConfig(ctx)
				require.NoError(t, err)
				require.NoError(t, ctx.Set(CLIConfigMapData, value))
				after, err := contextToConfig(ctx)
				require.NoError(t, err)
				assert.Equal(t, before.MetricSource, after.MetricSource)
			})
		}
	}
}

// TestContextToConfigRejectsYAMLConfigMapSource checks YAML cannot select the legacy ConfigMap source.
func TestContextToConfigRejectsYAMLConfigMapSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    configMap:
      namespace: monitoring
      name: exporter-metrics
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "metrics[0].configMap: unknown key at line 5")
}

// TestContextToConfigLoadsWatchGroupOverride checks a slow field override loads as a watch group.
func TestContextToConfigLoadsWatchGroupOverride(t *testing.T) {
	dir := t.TempDir()
	csvFile := filepath.Join(dir, "metrics.csv")
	require.NoError(t, os.WriteFile(csvFile, []byte("DCGM_FI_DEV_NVLINK_PPCNT_TOTAL,counter,Total\n"), 0o600))
	configFile := filepath.Join(dir, "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    file: `+csvFile+`
    overrides:
      - name: slow
        match: [DCGM_FI_DEV_NVLINK_PPCNT_*]
        every: 10m
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	require.Len(t, cfg.WatchGroups, 1)
	assert.Equal(t, "slow", cfg.WatchGroups[0].Name)
	assert.Equal(t, 600000, cfg.WatchGroups[0].Interval)
	assert.Equal(t, []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"}, cfg.WatchGroups[0].Fields)
}

// TestContextToConfigRejectsOverrideOverlapAndEmptyMatch checks a config file
// whose overrides overlap or match nothing fails at load, naming the YAML paths.
func TestContextToConfigRejectsOverrideOverlapAndEmptyMatch(t *testing.T) {
	dir := t.TempDir()
	csvFile := filepath.Join(dir, "metrics.csv")
	require.NoError(t, os.WriteFile(csvFile, []byte("DCGM_FI_DEV_POWER_USAGE,gauge,Power\n"), 0o600))
	tests := []struct {
		name      string
		overrides string
		wantErr   string
	}{
		{
			name: "named overrides overlap",
			overrides: `      - {name: a, match: [DCGM_FI_DEV_POWER_USAGE], every: 1s}
      - {name: b, match: ["DCGM_FI_DEV_POWER_*"], every: 2s}`,
			wantErr: "field DCGM_FI_DEV_POWER_USAGE matches multiple overrides: a (metrics[0].overrides[0]) and b (metrics[0].overrides[1])",
		},
		{
			name: "unnamed overrides overlap by regex",
			overrides: `      - {match: ["/DCGM_FI_DEV_.*/"], every: 1s}
      - {match: [DCGM_FI_DEV_POWER_USAGE], every: 2s}`,
			wantErr: "matches multiple overrides: metrics[0].overrides[0] and metrics[0].overrides[1]",
		},
		{
			name:      "named override matches nothing",
			overrides: `      - {name: none, match: [DCGM_FI_NOT_IN_FILE], every: 1s}`,
			wantErr:   `override "none" (metrics[0].overrides[0]) matched no configured fields`,
		},
		{
			name:      "wildcard override matches nothing",
			overrides: `      - {match: ["NOTHING_*"], every: 1s}`,
			wantErr:   `override "metrics[0].overrides[0]" matched no configured fields`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := filepath.Join(dir, "dcgm-exporter.yaml")
			require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    file: `+csvFile+`
    overrides:
`+tt.overrides+`
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIConfigFile, configFile))

			cfg, err := contextToConfig(ctx)

			require.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, cfg)
		})
	}
}

// TestContextToConfigChecksWindowOverridesAgainstTheResolvedFile checks a window
// override that selects a normal field is rejected, and that a relative metric
// file is read next to the config file whatever the working directory holds.
func TestContextToConfigChecksWindowOverridesAgainstTheResolvedFile(t *testing.T) {
	const normalRow = "DCGM_FI_DEV_GPU_TEMP_CELSIUS,gauge,Temperature\n"
	const counterRow = "DCGM_EXP_XID_ERRORS_COUNT,gauge,Xid\n"
	load := func(t *testing.T, nextToConfig, inWorkingDir string) error {
		t.Helper()
		dir, cwd := t.TempDir(), t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "metrics.csv"), []byte(nextToConfig), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "metrics.csv"), []byte(inWorkingDir), 0o600))
		t.Chdir(cwd)
		configFile := filepath.Join(dir, "dcgm-exporter.yaml")
		require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    file: metrics.csv
    overrides:
      - {match: ["/DCGM_.*/"], window: 5m}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
		ctx := newTestCLIContext(t)
		require.NoError(t, ctx.Set(CLIConfigFile, configFile))
		_, err := contextToConfig(ctx)
		return err
	}

	t.Run("decoy in the working directory is not read", func(t *testing.T) {
		require.NoError(t, load(t, counterRow, normalRow))
	})
	t.Run("normal field next to the config is found", func(t *testing.T) {
		err := load(t, normalRow, counterRow)
		require.ErrorContains(t, err,
			"metrics[0].overrides[0].match[0]: selects DCGM_FI_DEV_GPU_TEMP_CELSIUS, which is not a window counter")
	})
	t.Run("inline fields", func(t *testing.T) {
		configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
		require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    fields:
      - {name: DCGM_FI_DEV_POWER_USAGE, prometheus_type: gauge, help: p}
    overrides:
      - {match: [DCGM_EXP_XID_ERRORS_COUNT, "DCGM_FI_DEV_POWER_*"], window: 5m}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
		ctx := newTestCLIContext(t)
		require.NoError(t, ctx.Set(CLIConfigFile, configFile))
		_, err := contextToConfig(ctx)
		require.ErrorContains(t, err,
			"metrics[0].overrides[0].match[1]: selects DCGM_FI_DEV_POWER_USAGE, which is not a window counter")
	})
}

// TestContextToConfigDoesNotReadAMetricFileThatIsNotRegular checks the override
// check refuses a device path quickly instead of reading it.
func TestContextToConfigDoesNotReadAMetricFileThatIsNotRegular(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    file: /dev/zero
    overrides:
      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 5m}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	_, readErr := counters.ReadCSVFile("/dev/zero")
	require.ErrorContains(t, readErr, "not a regular file")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = contextToConfig(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading the metric file did not stop")
	}
}

// TestContextToConfigRejectsInlineOverrideOverlap checks inline fields are read for the same check.
func TestContextToConfigRejectsInlineOverrideOverlap(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    fields:
      - {name: DCGM_FI_DEV_POWER_USAGE, prometheus_type: gauge, help: p}
    overrides:
      - {match: [DCGM_FI_DEV_POWER_USAGE], every: 1s}
      - {match: ["DCGM_FI_DEV_POWER_*"], every: 2s}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	_, err := contextToConfig(ctx)

	require.ErrorContains(t, err, "metrics[0].overrides[0] and metrics[0].overrides[1]")
}

// TestReloadConfigDoesNotRereadYAML checks reload uses the startup YAML snapshot.
func TestReloadConfigDoesNotRereadYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: scrape
    file: /etc/dcgm-exporter/default-counters.csv
    every: 10s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 10000, cfg.CollectInterval)

	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: scrape
    file: /etc/dcgm-exporter/default-counters.csv
    every: 1m
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
	coord := newReloadCoordinator()
	coord.reloadConfig = cfg.Clone()

	reloadCfg, err := coord.buildConfigReload()

	require.NoError(t, err)
	assert.Equal(t, 10000, reloadCfg.CollectInterval)
}

// TestContextToConfigRejectsInvalidOptions checks invalid CLI options fail config conversion.
func TestContextToConfigRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		value   string
		wantErr string
	}{
		{
			name:    "switch devices",
			flag:    CLISwitchDevices,
			value:   "bad",
			wantErr: "the only valid options",
		},
		{
			name:    "cpu devices",
			flag:    CLICPUDevices,
			value:   "bad",
			wantErr: "the only valid options",
		},
		{
			name:    "dcgm log level",
			flag:    CLIDCGMLogLevel,
			value:   "TRACE",
			wantErr: "invalid dcgm-log-level parameter value",
		},
		{
			name:    "configmap data",
			flag:    CLIConfigMapData,
			value:   "not-a-namespace-name-pair",
			wantErr: "malformed configmap-data",
		},
		{
			name:    "configmap data without a name",
			flag:    CLIConfigMapData,
			value:   "monitoring:",
			wantErr: "namespace and name are required",
		},
		{
			name:    "configmap data without a namespace",
			flag:    CLIConfigMapData,
			value:   ":metrics",
			wantErr: "namespace and name are required",
		},
		{
			name:    "blank collectors file",
			flag:    CLIFieldsFile,
			value:   "  ",
			wantErr: "collectors must not be empty",
		},
		{
			name:    "zero collect interval",
			flag:    CLICollectInterval,
			value:   "0",
			wantErr: "invalid collect-interval parameter value: 0, must be greater than 0",
		},
		{
			name:    "negative collect interval",
			flag:    CLICollectInterval,
			value:   "-5",
			wantErr: "invalid collect-interval parameter value: -5, must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(tt.flag, tt.value))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestContextToConfigPodLabelSelection checks that pod labels need an explicit
// allowlist from a flag or the environment, and that a flag replaces the
// environment value.
func TestContextToConfigPodLabelSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		env       map[string]string
		want      []string
		wantError string
	}{
		{name: "pod labels off"},
		{name: "explicit all labels", args: []string{"--kubernetes-enable-pod-labels", "--kubernetes-pod-label-allowlist-regex=.*"}, want: []string{".*"}},
		{
			name: "environment selection",
			env:  map[string]string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS": "true", "DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX": "^app$,^tier$"},
			want: []string{"^app$", "^tier$"},
		},
		{
			name:      "environment without selection",
			env:       map[string]string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS": "true"},
			wantError: "requires kubernetes-pod-label-allowlist-regex",
		},
		{
			name: "flag replaces invalid environment",
			args: []string{"--kubernetes-pod-label-allowlist-regex=^app$"},
			env:  map[string]string{"DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS": "true", "DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX": "[invalid"},
			want: []string{"^app$"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp("test").Flags)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			var cfg *appconfig.Config
			app := NewApp("test-version")
			app.Action = func(c *cli.Context) error {
				var err error
				cfg, err = contextToConfig(c)
				return err
			}

			err := app.Run(append([]string{"dcgm-exporter"}, tc.args...))
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.KubernetesPodLabelAllowlistRegex)
		})
	}
}

// TestContextToConfigRejectsPprofWithoutWebConfigFile checks profiling requires a web config file.
func TestContextToConfigRejectsPprofWithoutWebConfigFile(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIEnablePprof)
	assert.Contains(t, err.Error(), CLIWebConfigFile)
}

// TestContextToConfigContainerLabels checks container label flags enter runtime config.
func TestContextToConfigContainerLabels(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, "/run/podman/podman.sock"))

	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)

	assert.True(t, cfg.ContainerLabels)
	assert.Equal(t, "/run/podman/podman.sock", cfg.ContainerRuntimeSocket)
}

// TestContextToConfigContainerLabelsFromEnv checks container label environment values enter runtime config.
func TestContextToConfigContainerLabelsFromEnv(t *testing.T) {
	t.Setenv("DCGM_EXPORTER_CONTAINER_LABELS", "true")
	t.Setenv("DCGM_CONTAINER_RUNTIME_SOCKET", "/run/runtime.sock")

	var cfg *appconfig.Config
	app := NewApp("test-version")
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.ContainerLabels)
	assert.Equal(t, "/run/runtime.sock", cfg.ContainerRuntimeSocket)
}

// TestContextToConfigContainerLabelsUseTheDefaultRuntimeSocket checks container labels without a named socket use the default one.
func TestContextToConfigContainerLabelsUseTheDefaultRuntimeSocket(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, "/var/run/docker.sock", cfg.ContainerRuntimeSocket)
}

// TestContextToConfigContainerLabelsIgnoredInKubernetes checks container labels are ignored, with a notice, when Kubernetes mode is on.
func TestContextToConfigContainerLabelsIgnoredInKubernetes(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIKubernetes, "true"))
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.Kubernetes)
	assert.True(t, cfg.ContainerLabels)
	assert.Contains(t, logs.String(), "container runtime labels are ignored when kubernetes mode is enabled")
}

// TestContextToConfigAllowsPprofWithWebConfigFile checks profiling is allowed with a web config file.
func TestContextToConfigAllowsPprofWithWebConfigFile(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))
	require.NoError(t, ctx.Set(CLIWebConfigFile, filepath.Join(t.TempDir(), "web-config.yml")))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.EnablePprof)
	assert.Equal(t, ctx.String(CLIWebConfigFile), cfg.WebConfigFile)
}

// TestContextToConfigEnablesGPUBindUnbindDetectionFromYAML checks YAML can enable GPU lifecycle detection.
func TestContextToConfigEnablesGPUBindUnbindDetectionFromYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: true
      every: 250ms
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.EnableGPUBindUnbindWatch)
	assert.Equal(t, 250*time.Millisecond, cfg.GPUBindUnbindPollInterval)
}

// TestContextToConfigGPUBindUnbindCLIOverridesYAML checks CLI lifecycle settings take precedence over YAML.
func TestContextToConfigGPUBindUnbindCLIOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: false
      every: 250ms
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "2s"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.EnableGPUBindUnbindWatch)
	assert.Equal(t, 2*time.Second, cfg.GPUBindUnbindPollInterval)
}

// TestContextToConfigGPUBindUnbindEnvironmentOverridesYAML checks environment lifecycle settings take precedence over YAML.
func TestContextToConfigGPUBindUnbindEnvironmentOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: true
      every: 250ms
`), 0o600))
	t.Setenv("DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH", "false")
	t.Setenv("DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL", "2s")

	var cfg *appconfig.Config
	app := NewApp()
	app.Action = func(ctx *cli.Context) error {
		var err error
		cfg, err = contextToConfig(ctx)
		return err
	}

	require.NoError(t, app.Run([]string{"dcgm-exporter", "--config-file", configFile}))
	require.NotNil(t, cfg)
	assert.False(t, cfg.EnableGPUBindUnbindWatch)
	assert.Equal(t, 2*time.Second, cfg.GPUBindUnbindPollInterval)
}

// TestContextToConfigRejectsNonPositiveCompatibilityPollIntervalWhenEnabled checks enabled compatibility polling requires a positive interval.
func TestContextToConfigRejectsNonPositiveCompatibilityPollIntervalWhenEnabled(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "0s"))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIGPUBindUnbindPollInterval)
}

// TestContextToConfigAllowsNonPositiveCompatibilityPollIntervalWhenDisabled checks disabled compatibility polling ignores its interval.
func TestContextToConfigAllowsNonPositiveCompatibilityPollIntervalWhenDisabled(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "false"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "0s"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.False(t, cfg.EnableGPUBindUnbindWatch)
	assert.Zero(t, cfg.GPUBindUnbindPollInterval)
}

// TestContextToConfigRejectsNonPositiveCompatibilityPollIntervalFromEnvironment checks invalid polling intervals from the environment are rejected.
func TestContextToConfigRejectsNonPositiveCompatibilityPollIntervalFromEnvironment(t *testing.T) {
	t.Setenv("DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH", "true")
	t.Setenv("DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL", "0s")

	var cfg *appconfig.Config
	app := NewApp()
	app.Action = func(ctx *cli.Context) error {
		var err error
		cfg, err = contextToConfig(ctx)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIGPUBindUnbindPollInterval)
}

// TestContextToConfigRejectsNonPositiveGPUBindUnbindPollIntervalWhenEnabled checks enabled GPU lifecycle polling requires a positive interval.
func TestContextToConfigRejectsNonPositiveGPUBindUnbindPollIntervalWhenEnabled(t *testing.T) {
	for _, interval := range []string{"0s", "-1s"} {
		t.Run(interval, func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
			require.NoError(t, os.WriteFile(configFile, []byte(fmt.Sprintf(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: true
      every: %s
`, interval)), 0o600))
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIConfigFile, configFile))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "sources.dcgm.detect_bind_unbind.every")
		})
	}
}

// TestContextToConfigParsesCombinedDeviceOptions verifies combined selectors flow through all device flags.
func TestContextToConfigParsesCombinedDeviceOptions(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIGPUDevices, "g+i"))
	require.NoError(t, ctx.Set(CLISwitchDevices, "i+g:0"))
	require.NoError(t, ctx.Set(CLICPUDevices, "g:0-1+i:2,4"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		cfg.GPUDeviceOptions)
	assert.Equal(t, appconfig.DeviceOptions{MajorRange: []int{0}, MinorRange: []int{-1}},
		cfg.SwitchDeviceOptions)
	assert.Equal(t, appconfig.DeviceOptions{MajorRange: []int{0, 1}, MinorRange: []int{2, 4}},
		cfg.CPUDeviceOptions)
}

// TestDeviceUsageMIGNoteIsGPUOnly checks MIG guidance appears only with GPU selection help.
func TestDeviceUsageMIGNoteIsGPUOnly(t *testing.T) {
	usages := map[string]string{}
	for _, rawFlag := range NewApp().Flags {
		if stringFlag, ok := rawFlag.(cli.DocGenerationFlag); ok {
			usages[stringFlag.Names()[0]] = stringFlag.GetUsage()
		}
	}

	assert.Contains(t, usages[CLIGPUDevices], "unless MIG mode is enabled")
	assert.NotContains(t, usages[CLISwitchDevices], "unless MIG mode is enabled")
	assert.NotContains(t, usages[CLICPUDevices], "unless MIG mode is enabled")
}

// TestContextToConfigHonorsConfigurationFlags checks each configuration flag reaches runtime config.
func TestContextToConfigHonorsConfigurationFlags(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIFieldsFile, "/tmp/custom-counters.csv"))
	require.NoError(t, ctx.Set(CLIAddress, ":19500"))
	require.NoError(t, ctx.Set(CLICollectInterval, "5000"))
	require.NoError(t, ctx.Set(CLIGPUDevices, "g:2-3"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-counters.csv", cfg.MetricSource.File)
	assert.Equal(t, ":19500", cfg.Address)
	assert.Equal(t, 5000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DeviceOptions{MajorRange: []int{2, 3}}, cfg.GPUDeviceOptions)
}

// TestContextToConfigHonorsHealthRequireGPUs checks flag and environment precedence for the opt-in health gate.
func TestContextToConfigHonorsHealthRequireGPUs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{name: "default off", args: []string{"dcgm-exporter"}, want: false},
		{name: "flag on", args: []string{"dcgm-exporter", "--health-require-gpus"}, want: true},
		{name: "flag explicitly off", args: []string{"dcgm-exporter", "--health-require-gpus=false"}, want: false},
		{name: "env on", args: []string{"dcgm-exporter"}, env: "true", want: true},
		{name: "env off", args: []string{"dcgm-exporter"}, env: "false", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg *appconfig.Config
			app := NewApp("test-version")
			unsetFlagEnvVars(t, app.Flags)
			if tc.env != "" {
				t.Setenv("DCGM_EXPORTER_HEALTH_REQUIRE_GPUS", tc.env)
			}

			app.Action = func(c *cli.Context) error {
				var err error
				cfg, err = contextToConfig(c)
				return err
			}

			require.NoError(t, app.Run(tc.args))
			require.NotNil(t, cfg)
			assert.Equal(t, tc.want, cfg.HealthRequireGPUs)
		})
	}
}

// TestContextToConfigHonorsConfigurationEnvironment checks each configuration environment value reaches runtime config.
func TestContextToConfigHonorsConfigurationEnvironment(t *testing.T) {
	t.Setenv("DCGM_EXPORTER_COLLECTORS", "/tmp/env-counters.csv")
	t.Setenv("DCGM_EXPORTER_LISTEN", ":19501")
	t.Setenv("DCGM_EXPORTER_INTERVAL", "7000")
	t.Setenv("DCGM_EXPORTER_DEVICES_STR", "i:9")

	var cfg *appconfig.Config
	app := NewApp("test-version")
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "/tmp/env-counters.csv", cfg.MetricSource.File)
	assert.Equal(t, ":19501", cfg.Address)
	assert.Equal(t, 7000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DeviceOptions{MinorRange: []int{9}}, cfg.GPUDeviceOptions)
}

// TestContextToConfigMaxConcurrentScrapes checks the scrape concurrency limit reaches runtime config.
func TestContextToConfigMaxConcurrentScrapes(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		cli     string
		want    int
		wantErr string
	}{
		{
			name: "default",
			want: appconfig.DefaultMaxConcurrentScrapes,
		},
		{
			name: "environment",
			env:  "32",
			want: 32,
		},
		{
			name: "CLI",
			cli:  "24",
			want: 24,
		},
		{
			name: "CLI overrides environment",
			env:  "32",
			cli:  "8",
			want: 8,
		},
		{
			name: "value above default",
			cli:  "64",
			want: 64,
		},
		{
			name:    "zero",
			cli:     "0",
			wantErr: CLIMaxConcurrentScrapes,
		},
		{
			name:    "negative",
			cli:     "-1",
			wantErr: CLIMaxConcurrentScrapes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp("test-version")
			unsetFlagEnvVars(t, app.Flags)
			if tt.env != "" {
				t.Setenv("DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES", tt.env)
			}

			var cfg *appconfig.Config
			app.Action = func(c *cli.Context) error {
				var err error
				cfg, err = contextToConfig(c)
				return err
			}

			args := []string{"dcgm-exporter"}
			if tt.cli != "" {
				args = append(args, "--"+CLIMaxConcurrentScrapes+"="+tt.cli)
			}
			err := app.Run(args)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, tt.want, cfg.MaxConcurrentScrapes)
		})
	}
}

// TestContextToConfigEnableExporterMetrics verifies the default, environment, and CLI opt-in contract.
func TestContextToConfigEnableExporterMetrics(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		cli     string
		want    bool
		wantErr bool
	}{
		{name: "disabled by default"},
		{name: "enabled by environment", env: "true", want: true},
		{name: "enabled by CLI", cli: "true", want: true},
		{name: "CLI disables over environment", env: "true", cli: "false"},
		{name: "rejects invalid environment", env: "invalid", wantErr: true},
		{name: "rejects invalid CLI", cli: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp("test-version")
			unsetFlagEnvVars(t, app.Flags)
			if tt.env != "" {
				t.Setenv("DCGM_EXPORTER_ENABLE_EXPORTER_METRICS", tt.env)
			}

			var cfg *appconfig.Config
			app.Action = func(c *cli.Context) error {
				var err error
				cfg, err = contextToConfig(c)
				return err
			}

			args := []string{"dcgm-exporter"}
			if tt.cli != "" {
				args = append(args, "--"+CLIEnableExporterMetrics+"="+tt.cli)
			}
			err := app.Run(args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, tt.want, cfg.EnableExporterMetrics)
		})
	}
}

// TestContextToConfigParsesWebTimeouts checks valid web timeouts become durations in runtime config.
func TestContextToConfigParsesWebTimeouts(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIWebReadTimeout, "3s"))
	require.NoError(t, ctx.Set(CLIWebWriteTimeout, "45s"))

	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)

	assert.Equal(t, 3*time.Second, cfg.WebReadTimeout)
	assert.Equal(t, 45*time.Second, cfg.WebWriteTimeout)
}

// TestContextToConfigRejectsMalformedWebTimeouts checks malformed explicit web timeouts fail config conversion.
func TestContextToConfigRejectsMalformedWebTimeouts(t *testing.T) {
	tests := []struct {
		name       string
		readValue  string
		writeValue string
		wantFlag   string
	}{
		{
			name:       "read timeout",
			readValue:  "bogus",
			writeValue: "45s",
			wantFlag:   CLIWebReadTimeout,
		},
		{
			name:       "empty read timeout",
			readValue:  "",
			writeValue: "45s",
			wantFlag:   CLIWebReadTimeout,
		},
		{
			name:       "write timeout",
			readValue:  "3s",
			writeValue: "bogus",
			wantFlag:   CLIWebWriteTimeout,
		},
		{
			name:       "empty write timeout",
			readValue:  "3s",
			writeValue: "",
			wantFlag:   CLIWebWriteTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIWebReadTimeout, tt.readValue))
			require.NoError(t, ctx.Set(CLIWebWriteTimeout, tt.writeValue))

			cfg, err := contextToConfig(ctx)
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.wantFlag)
		})
	}
}

// TestContextToConfigRejectsMalformedGPUBindUnbindPollInterval ensures an
// operator typo cannot quietly replace the explicit watcher interval.
func TestContextToConfigRejectsMalformedGPUBindUnbindPollInterval(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "malformed", value: "bogus"},
		{name: "empty", value: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, test.value))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), CLIGPUBindUnbindPollInterval)
		})
	}
}

// TestContextToConfigRejectsNonPositiveWebTimeouts checks zero and negative web timeouts fail config conversion.
func TestContextToConfigRejectsNonPositiveWebTimeouts(t *testing.T) {
	for _, flag := range []string{CLIWebReadTimeout, CLIWebWriteTimeout} {
		for _, value := range []string{"0s", "-5s"} {
			t.Run(flag+"/"+value, func(t *testing.T) {
				ctx := newTestCLIContext(t)
				require.NoError(t, ctx.Set(flag, value))
				cfg, err := contextToConfig(ctx)
				require.Error(t, err)
				assert.Nil(t, cfg)
				assert.Contains(t, err.Error(), flag)
				assert.Contains(t, err.Error(), "must be greater than 0")
			})
		}
	}
}

// configMapSelector is a NAMESPACE:NAME value for --configmap-data in tests.
const configMapSelector = "ns:name"

// deprecationMessages holds the exact warning text for each deprecated flag.
var deprecationMessages = map[string]string{
	CLIConfigMapData: "flag --configmap-data is deprecated and still supported; mount the ConfigMap as a file and pass its path with --collectors instead",
}

// TestStartupDeprecationWarnings checks startup logs one warning for each
// deprecated flag set by argv or environment, and none when left unset or when
// only the flags that are not deprecated (--use-old-namespace, --no-hostname,
// --replace-blanks-in-model-name) are set.
func TestStartupDeprecationWarnings(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want []string
	}{
		{name: "unset"},
		{name: "other flags only", args: []string{"--kubernetes", "--collectors", "x.csv"}},
		{name: "short alias", args: []string{"-m", configMapSelector}, want: []string{CLIConfigMapData}},
		{name: "long flag", args: []string{"--configmap-data", configMapSelector}, want: []string{CLIConfigMapData}},
		{name: "flags that are not deprecated", args: []string{"-o", "-n", "-rbmn"}},
		{name: "env var", env: map[string]string{"DCGM_EXPORTER_CONFIGMAP_DATA": configMapSelector}, want: []string{CLIConfigMapData}},
		{name: "env vars of flags that are not deprecated", env: map[string]string{
			"DCGM_EXPORTER_USE_OLD_NAMESPACE":            "true",
			"DCGM_EXPORTER_NO_HOSTNAME":                  "true",
			"DCGM_EXPORTER_REPLACE_BLANKS_IN_MODEL_NAME": "true",
		}},
		{name: "env and argv warn once", args: []string{"-m", configMapSelector}, env: map[string]string{"DCGM_EXPORTER_CONFIGMAP_DATA": "ns:other"}, want: []string{CLIConfigMapData}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			app := NewApp("test")
			app.Action = func(c *cli.Context) error {
				_, err := contextToConfig(c)
				return err
			}
			require.NoError(t, app.Run(append([]string{"dcgm-exporter"}, tt.args...)))

			wanted := map[string]bool{}
			for _, name := range tt.want {
				wanted[name] = true
			}
			for name, message := range deprecationMessages {
				count := bytes.Count(logs.Bytes(), []byte(fmt.Sprintf("%q flag=%s", message, name)))
				if wanted[name] {
					assert.Equal(t, 1, count, "warning for %s", name)
				} else {
					assert.Zero(t, count, "warning for %s", name)
				}
			}
			assert.Equal(t, len(tt.want), bytes.Count(logs.Bytes(), []byte("level=WARN")))
		})
	}
}

// TestInspectionDeprecationWarnings checks inspection reports the same
// deprecation warnings from the root flags, the override tail and environment.
func TestInspectionDeprecationWarnings(t *testing.T) {
	const configMapData = "flag --configmap-data is deprecated and still supported; mount the ConfigMap as a file and pass its path with --collectors instead"

	// Inline metric definitions keep validation free of host CSV files.
	path := "testdata/inspection.yaml"
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want []string
	}{
		{name: "unset", args: []string{"config", "explain", path, "--output", "json"}, want: []string{}},
		{name: "root flag", args: []string{"-m", configMapSelector, "config", "explain", path, "--output", "json"}, want: []string{configMapData}},
		{name: "override tail", args: []string{"config", "explain", path, "--output", "json", "--", "-m", configMapSelector}, want: []string{configMapData}},
		{name: "root and tail warn once", args: []string{"-m", configMapSelector, "config", "explain", path, "--output", "json", "--", "-m=ns:other"}, want: []string{configMapData}},
		{name: "env var", args: []string{"config", "explain", path, "--output", "json"}, env: map[string]string{"DCGM_EXPORTER_CONFIGMAP_DATA": configMapSelector}, want: []string{configMapData}},
		{name: "flags that are not deprecated", args: []string{"-o", "config", "explain", path, "--output", "json", "--", "-rbmn", "-n"}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			stdout, stderr, err := inspectionRun(t, tt.args...)
			require.NoError(t, err)
			var data explainData
			require.NoError(t, json.Unmarshal(inspectionEnvelopeOf(t, stdout, "config.explain"), &data))

			got := []string{}
			for _, warning := range data.Warnings {
				got = append(got, warning.Summary)
				assert.Contains(t, stderr, warning.Summary)
			}

			assert.Equal(t, tt.want, got)
			assert.Equal(t, len(tt.want), strings.Count(stderr, "warning  "))
		})
	}
}

// TestDeprecatedFlagsHelp checks root help shows the deprecated flag's
// description followed by its deprecation notice, and no notice on the flags
// that are not deprecated.
func TestDeprecatedFlagsHelp(t *testing.T) {
	want := map[string]string{
		CLIConfigMapData: "Read metrics from ConfigMap NAMESPACE:NAME. " +
			"Deprecated: still supported; mount the ConfigMap as a file and pass its path with --collectors instead.",
		CLIUseOldNamespace: "Use old 1.x namespace.",
		CLINoHostname:      "Omit the hostname information from the output, matching older versions.",
		CLIReplaceBlanksInModelName: "Replace every blank space in the GPU model name with a dash, " +
			"ensuring a continuous, space-free identifier.",
	}
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "--help")
	require.NoError(t, err)
	for name, description := range want {
		found := false
		for _, section := range strings.Split(stdout, "\n\n") {
			if strings.HasPrefix(strings.TrimSpace(section), "--"+name+",") {
				found = true
				assert.Contains(t, strings.Join(strings.Fields(section), " "), description+" Default:")
			}
		}
		assert.True(t, found, "help entry for %s", name)
	}
}

// TestContextToConfigYAMLEveryAndWindowPrecedence checks defaults < YAML < env < argv for the v2 interval and counter windows.
func TestContextToConfigYAMLEveryAndWindowPrecedence(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    every: 10s
    overrides:
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 2m
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))

	for _, tc := range []struct {
		name         string
		env          map[string]string
		args         []string
		wantInterval int
		wantWindow   int
	}{
		{name: "YAML only", wantInterval: 10000, wantWindow: 120000},
		{
			name:         "environment beats YAML",
			env:          map[string]string{"DCGM_EXPORTER_INTERVAL": "20000", "DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE": "60000"},
			wantInterval: 20000, wantWindow: 60000,
		},
		{
			name:         "argv beats environment and YAML",
			env:          map[string]string{"DCGM_EXPORTER_INTERVAL": "20000"},
			args:         []string{"-c", "30000", "-x", "90000"},
			wantInterval: 30000, wantWindow: 90000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			var cfg *appconfig.Config
			app := NewApp()
			app.Action = func(ctx *cli.Context) error {
				var err error
				cfg, err = contextToConfig(ctx)
				return err
			}
			args := append([]string{"dcgm-exporter", "--config-file", configFile}, tc.args...)
			require.NoError(t, app.Run(args))
			assert.Equal(t, tc.wantInterval, cfg.CollectInterval)
			assert.Equal(t, tc.wantWindow, cfg.XIDCountWindowSize)
		})
	}
}

// TestResolveConfigReportsVersion1Warning checks a version 1 file carries the deprecation notice and a version 2 file does not.
func TestResolveConfigReportsVersion1Warning(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, tc := range []struct {
		version int
		want    []string
	}{
		{version: 1, want: []string{appconfig.LegacyConfigVersionWarning}},
		{version: 2, want: nil},
	} {
		t.Run(fmt.Sprintf("version %d", tc.version), func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
			require.NoError(t, os.WriteFile(configFile, []byte(fmt.Sprintf("version: %d\n", tc.version)), 0o600))
			var got []string
			app := NewApp()
			app.Action = func(ctx *cli.Context) error {
				resolved, err := resolveConfig(ctx, "", nil, true)
				if err == nil {
					for _, warning := range resolved.Warnings {
						got = append(got, warning.Message)
					}
				}

				return err
			}

			require.NoError(t, app.Run([]string{"dcgm-exporter", "--config-file", configFile}))
			assert.Equal(t, tc.want, got)
		})
	}
}

// captureStartupLogs runs startup config resolution with the given arguments and returns the log text.
func captureStartupLogs(t *testing.T, args ...string) string {
	t.Helper()
	unsetFlagEnvVars(t, NewApp().Flags)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	app := NewApp("test")
	app.Action = func(c *cli.Context) error {
		_, err := contextToConfig(c)
		return err
	}
	require.NoError(t, app.Run(append([]string{"dcgm-exporter"}, args...)))
	return logs.String()
}

// The container-labels warning keeps its flag and mode attributes.
func TestStartupContainerLabelsWarningKeepsAttributes(t *testing.T) {
	logs := captureStartupLogs(t, "--kubernetes", "--container-labels")
	assert.Equal(t, 1, strings.Count(logs, `msg="container runtime labels are ignored when kubernetes mode is enabled" flag=`+CLIContainerLabels+" mode="+CLIKubernetes))
	assert.Equal(t, 1, strings.Count(logs, "level=WARN"))
}

// A version 1 file logs the deprecation warning exactly once, without flag attributes.
func TestStartupLogsVersion1WarningOnce(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("version: 1\n"), 0o600))
	logs := captureStartupLogs(t, "--config-file", configFile)
	assert.Equal(t, 1, strings.Count(logs, fmt.Sprintf("%q", appconfig.LegacyConfigVersionWarning)))
	assert.Equal(t, 1, strings.Count(logs, "level=WARN"))
	assert.NotContains(t, logs, "flag=")

	assert.NotContains(t, captureStartupLogs(t), "deprecated and will be removed")
}

// runHPCConfig resolves the config the way startup does. It inherits the
// process environment, so callers clear flag environment variables with
// unsetFlagEnvVars and set the ones they test with t.Setenv.
func runHPCConfig(t *testing.T, args ...string) (*appconfig.Config, error) {
	t.Helper()

	app := NewApp("test-version")
	app.Writer, app.ErrWriter = io.Discard, io.Discard

	var cfg *appconfig.Config
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run(append([]string{"dcgm-exporter"}, args...))
	return cfg, err
}

// TestContextToConfigHPCJobMappingLimits checks the HPC mapping limits come
// from the file, then the environment, then flags, and that a limit that is
// not positive, equals the maximum int64, or is not an integer is refused.
func TestContextToConfigHPCJobMappingLimits(t *testing.T) {
	unsetFlagEnvVars(t, NewApp("test-version").Flags)

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`version: 2
sources:
  hpc_jobs: {mapping_dir: /jobs, max_file_bytes: 100, max_job_id_bytes: 20, max_jobs_per_file: 3}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`), 0o600))
	fileFlag := "--" + CLIConfigFile + "=" + configFile

	cfg, err := runHPCConfig(t)
	require.NoError(t, err)
	assert.Equal(t, appconfig.DefaultHPCJobMappingMaxFileBytes, cfg.HPCJobMappingMaxFileBytes)
	assert.Equal(t, appconfig.DefaultHPCJobMappingMaxJobIDBytes, cfg.HPCJobMappingMaxJobIDBytes)
	assert.Equal(t, appconfig.DefaultHPCJobMappingMaxJobsPerFile, cfg.HPCJobMappingMaxJobsPerFile)

	cfg, err = runHPCConfig(t, fileFlag)
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 20, 3},
		[]int64{cfg.HPCJobMappingMaxFileBytes, cfg.HPCJobMappingMaxJobIDBytes, cfg.HPCJobMappingMaxJobsPerFile})

	t.Setenv("DCGM_HPC_JOB_MAPPING_MAX_FILE_BYTES", "200")
	t.Setenv("DCGM_HPC_JOB_MAPPING_MAX_JOB_ID_BYTES", "40")
	t.Setenv("DCGM_HPC_JOB_MAPPING_MAX_JOBS_PER_FILE", "6")

	cfg, err = runHPCConfig(t, fileFlag, "--"+CLIHPCJobMappingMaxJobsPerFile+"=9")
	require.NoError(t, err)
	assert.Equal(t, []int64{200, 40, 9},
		[]int64{cfg.HPCJobMappingMaxFileBytes, cfg.HPCJobMappingMaxJobIDBytes, cfg.HPCJobMappingMaxJobsPerFile})
	assert.Equal(t, "/jobs", cfg.HPCJobMappingDir)

	for _, flag := range []string{CLIHPCJobMappingMaxFileBytes, CLIHPCJobMappingMaxJobIDBytes, CLIHPCJobMappingMaxJobsPerFile} {
		for _, value := range []string{"0", "-1", "9223372036854775807", "1.5"} {
			_, err := runHPCConfig(t, "--"+flag+"="+value)
			assert.Error(t, err, "--%s=%s", flag, value)
		}
	}
}
