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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// TestMain uses goleak to catch goroutines that outlive their test. The
// reload coordinator, the metrics server goroutine, and the file/GPU
// watchers are all long-lived; any test that forgets to cancel a context
// fails loudly here rather than silently leaking into subsequent tests.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func Test_getDeviceWatchListManager(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE:         false,
		EnableDCGMLog:       true,
		DCGMLogLevel:        "DEBUG",
		GPUDeviceOptions:    appconfig.DeviceOptions{},
		SwitchDeviceOptions: appconfig.DeviceOptions{},
		CPUDeviceOptions:    appconfig.DeviceOptions{},
		UseFakeGPUs:         true,
	}

	tests := []struct {
		name       string
		counterSet *counters.CounterSet
		assertion  func(*testing.T, devicewatchlistmanager.Manager)
	}{
		{
			name: "When DCGM_FI_DEV_XID_ERRORS and DCGM_EXP_XID_ERRORS_COUNT enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   230,
						FieldName: "DCGM_FI_DEV_XID_ERRORS",
						PromType:  "gauge",
						Help:      "Value of the last XID error encountered.",
					},
				},
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9001,
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
						PromType:  "gauge",
						Help:      "Count of XID Errors within user-specified time window (see xid-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_FI_DEV_XID_ERRORS enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   230,
						FieldName: "DCGM_FI_DEV_XID_ERRORS",
						PromType:  "gauge",
						Help:      "Value of the last XID error encountered.",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_XID_ERRORS_COUNT enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9001,
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
						PromType:  "gauge",
						Help:      "Count of XID Errors within user-specified time window (see xid-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_XID_ERRORS_TOTAL enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   dcgm.Short(counters.DCGMXIDErrorsTotal),
						FieldName: counters.DCGMExpXIDErrorsTotal,
						PromType:  "counter",
						Help:      "cumulative XID errors observed since exporter start",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.DCGM_FI_DEV_XID_ERRORS, values[0].FieldID)
			},
		},
		{
			name:       "When no counters",
			counterSet: &counters.CounterSet{},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 0)
			},
		},
		{
			name: "When DCGM_FI_DEV_CLOCK_THROTTLE_REASON and DCGM_EXP_CLOCK_EVENTS_COUNT enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   112,
						FieldName: "DCGM_FI_DEV_CLOCK_THROTTLE_REASON",
						PromType:  "gauge",
					},
				},
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9002,
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
						PromType:  "gauge",
						Help:      "Count of clock events within the user-specified time window (see clock-events-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_FI_DEV_CLOCK_THROTTLE_REASON enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   112,
						FieldName: "DCGM_FI_DEV_CLOCK_THROTTLE_REASON",
						PromType:  "gauge",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_CLOCK_EVENTS_COUNT enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9002,
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
						PromType:  "gauge",
						Help:      "Count of clock events within the user-specified time window (see clock-events-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_CLOCK_EVENTS_TOTAL enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   dcgm.Short(counters.DCGMClockEventsTotal),
						FieldName: counters.DCGMExpClockEventsTotal,
						PromType:  "counter",
						Help:      "cumulative clock events observed since exporter start (edge-counted)",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, values[0].FieldID)
			},
		},
	}

	dcgmprovider.SmartDCGMInit(t, config)
	defer dcgmprovider.Client().Cleanup()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := startDeviceWatchListManager(tt.counterSet, config)
			require.NoError(t, err)
			if tt.assertion == nil {
				t.Skip(tt.name)
			}
			tt.assertion(t, got)
		})
	}
}

func TestStartDeviceWatchListManagerRejectsInvalidWatchGroups(t *testing.T) {
	tests := []struct {
		name        string
		watchGroups []appconfig.WatchGroup
		wantErr     string
	}{
		{
			name: "overlap",
			watchGroups: []appconfig.WatchGroup{
				{Name: "temperature", Interval: 10000, Fields: []string{"DCGM_FI_DEV_GPU_*"}},
				{Name: "gpu-temp", Interval: 30000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
			},
			wantErr: "matches multiple watch groups",
		},
		{
			name: "empty match",
			watchGroups: []appconfig.WatchGroup{
				{Name: "no-match", Interval: 10000, Fields: []string{"DCGM_FI_DEV_DOES_NOT_EXIST"}},
			},
			wantErr: "matched no configured fields",
		},
	}

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := startDeviceWatchListManager(counterSet, &appconfig.Config{WatchGroups: tt.watchGroups})

			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func Test_contextToConfig_DumpConfig(t *testing.T) {
	tests := []struct {
		name           string
		flags          map[string]string
		expectedConfig appconfig.DumpConfig
	}{
		{
			name: "Default dump config",
			flags: map[string]string{
				CLIGPUDevices: "f",
			},
			expectedConfig: appconfig.DumpConfig{
				Enabled:     false,
				Directory:   "/tmp/dcgm-exporter-debug",
				Retention:   24,
				Compression: true,
			},
		},
		{
			name: "Enabled dump config with custom settings",
			flags: map[string]string{
				CLIGPUDevices:      "f",
				CLIDumpEnabled:     "true",
				CLIDumpDirectory:   "/custom/debug/dir",
				CLIDumpRetention:   "48",
				CLIDumpCompression: "false",
			},
			expectedConfig: appconfig.DumpConfig{
				Enabled:     true,
				Directory:   "/custom/debug/dir",
				Retention:   48,
				Compression: false,
			},
		},
		{
			name: "Enabled dump config with no retention",
			flags: map[string]string{
				CLIGPUDevices:    "f",
				CLIDumpEnabled:   "true",
				CLIDumpRetention: "0",
			},
			expectedConfig: appconfig.DumpConfig{
				Enabled:     true,
				Directory:   "/tmp/dcgm-exporter-debug",
				Retention:   0,
				Compression: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			context := newTestCLIContext(t)
			for name, value := range tt.flags {
				require.NoError(t, context.Set(name, value))
			}

			// Call the real contextToConfig function to obtain the config
			config, err := contextToConfig(context)
			require.NoError(t, err)

			// Assert equality against the config returned by contextToConfig
			assert.Equal(t, tt.expectedConfig, config.DumpConfig)
		})
	}
}

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

func TestNewAppDefaultsMatchDefaultConfig(t *testing.T) {
	app := NewApp("test-version")
	unsetFlagEnvVars(t, app.Flags)

	var cfg *appconfig.Config
	app.Action = func(c *cli.Context) error {
		defaults, err := defaultConfig()
		require.NoError(t, err)

		assert.Equal(t, defaults.ConfigFile, c.String(CLIConfigFile))
		assert.Equal(t, defaults.CollectorsFile, c.String(CLIFieldsFile))
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
		assert.Equal(t, defaults.ConfigMapData, c.String(CLIConfigMapData))
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
		assert.Equal(t, defaults.DumpConfig.Retention, c.Int(CLIDumpRetention))
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

	defaults, err := defaultConfig()
	require.NoError(t, err)
	assert.Equal(t, defaults, cfg)
}

func TestGPUBindUnbindDefaults(t *testing.T) {
	config, err := defaultConfig()

	require.NoError(t, err)
	assert.False(t, config.EnableGPUBindUnbindWatch)
	assert.Equal(t, time.Second, config.GPUBindUnbindPollInterval)
}

func TestStartDCGMExporterWithSignalSource_RejectsInvalidConfigBeforeDCGM(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIGPUDevices, "x"))

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the only valid options")
}

func TestContextToConfigPreservesVsockRemoteHostengineInfo(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIRemoteHEInfo, "vsock://3:5555"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.UseRemoteHE)
	assert.Equal(t, "vsock://3:5555", cfg.RemoteHEInfo)
}

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

func TestContextToConfigNoYAMLUsesLegacyDefaultMetricSource(t *testing.T) {
	ctx := newTestCLIContext(t)

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Empty(t, cfg.ConfigFile)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.CollectorsFile)
	assert.Equal(t, undefinedConfigMapData, cfg.ConfigMapData)
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
      maxKeepAge: 5m
      maxKeepSamples: 0
collections:
  - name: latest-values
    every: 1s
    sources:
      dcgm:
        watch:
          maxKeepAge: 0s
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
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
      maxKeepAge: 0s
      maxKeepSamples: 0
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

func TestContextToConfigYAMLConfig(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  enableExporterMetrics: true
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collections:
  - name: scrape
    every: 10s
    metrics:
      include: ["*"]
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

func TestContextToConfigYAMLOmittedMetricsUsesDefaultMetricSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
collections:
  - name: scrape
    every: 10s
    metrics:
      include: ["*"]
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, 10000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.CollectorsFile)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, appconfig.DefaultCollectorsFile, watchFile)
}

func TestContextToConfigYAMLFileSourceIsWatched(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	countersFile := filepath.Join(t.TempDir(), "yaml-counters.csv")
	require.NoError(t, os.WriteFile(configFile, []byte(fmt.Sprintf(`
version: 2
metrics:
  file: %s
`, countersFile)), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, countersFile, cfg.CollectorsFile)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, countersFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, countersFile, watchFile)
}

func TestContextToConfigLegacyFlagsOverrideYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  enableExporterMetrics: true
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collections:
  - name: scrape
    every: 10s
    metrics:
      include: ["*"]
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
	assert.Equal(t, countersFile, cfg.CollectorsFile)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.False(t, cfg.EnableExporterMetrics)
	assert.Equal(t, countersFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, countersFile, watchFile)
}

func TestContextToConfigLegacyConfigMapDataOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIConfigMapData, "monitoring:legacy-metrics"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, "monitoring:legacy-metrics", cfg.ConfigMapData)
	assert.Equal(t, appconfig.MetricSourceConfigMap, cfg.MetricSource.Kind)
	assert.Equal(t, "monitoring", cfg.MetricSource.ConfigMap.Namespace)
	assert.Equal(t, "legacy-metrics", cfg.MetricSource.ConfigMap.Name)
	_, watchFile := cfg.MetricFileWatcherPath()
	assert.False(t, watchFile)
}

func TestContextToConfigRejectsYAMLConfigMapSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  configMap:
    namespace: monitoring
    name: exporter-metrics
    key: custom-metrics
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "field configMap not found")
}

func TestContextToConfigLoadsCollections(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
collections:
  - name: slow
    every: 10m
    metrics:
      include:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
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

func TestReloadConfigDoesNotRereadYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
collections:
  - name: scrape
    every: 10s
    metrics:
      include: ["*"]
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 10000, cfg.CollectInterval)

	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
collections:
  - name: scrape
    every: 1m
    metrics:
      include: ["*"]
`), 0o600))
	coord := newReloadCoordinator(ctx)
	coord.reloadConfig = cfg.Clone()

	reloadCfg, err := coord.buildReloadConfig()

	require.NoError(t, err)
	assert.Equal(t, 10000, reloadCfg.CollectInterval)
}

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

func TestContextToConfigRejectsPprofWithoutWebConfigFile(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIEnablePprof)
	assert.Contains(t, err.Error(), CLIWebConfigFile)
}

func TestContextToConfigContainerLabels(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, "/run/podman/podman.sock"))

	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)

	assert.True(t, cfg.ContainerLabels)
	assert.Equal(t, "/run/podman/podman.sock", cfg.ContainerRuntimeSocket)
}

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

func TestContextToConfigContainerLabelsRequiresRuntimeSocket(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIContainerLabels)
	assert.Contains(t, err.Error(), CLIContainerRuntimeSocket)
}

func TestContextToConfigContainerLabelsDoesNotRequireRuntimeSocketInKubernetes(t *testing.T) {
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
	assert.Empty(t, cfg.ContainerRuntimeSocket)
	assert.Contains(t, logs.String(), "container runtime labels are ignored when kubernetes mode is enabled")
}

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

func TestContextToConfigEnablesGPUBindUnbindDetectionFromYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
      pollInterval: 250ms
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.True(t, cfg.EnableGPUBindUnbindWatch)
	assert.Equal(t, 250*time.Millisecond, cfg.GPUBindUnbindPollInterval)
}

func TestContextToConfigGPUBindUnbindCLIOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: false
      pollInterval: 250ms
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

func TestContextToConfigGPUBindUnbindEnvironmentOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
      pollInterval: 250ms
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

func TestContextToConfigRejectsNonPositiveCompatibilityPollIntervalWhenEnabled(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "0s"))

	cfg, err := contextToConfig(ctx)

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), CLIGPUBindUnbindPollInterval)
}

func TestContextToConfigAllowsNonPositiveCompatibilityPollIntervalWhenDisabled(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "false"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "0s"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.False(t, cfg.EnableGPUBindUnbindWatch)
	assert.Zero(t, cfg.GPUBindUnbindPollInterval)
}

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

func TestContextToConfigRejectsNonPositiveGPUBindUnbindPollIntervalWhenEnabled(t *testing.T) {
	for _, interval := range []string{"0s", "-1s"} {
		t.Run(interval, func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
			require.NoError(t, os.WriteFile(configFile, []byte(fmt.Sprintf(`
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
      pollInterval: %s
`, interval)), 0o600))
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIConfigFile, configFile))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "sources.dcgm.detectBindUnbind.pollInterval")
		})
	}
}

func TestRunDCGMExporter_RegistersGPULifecycleWatchBeforeInitialRegistry(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	watchRegistered := false
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before DCP discovery")
		return nil, errors.New("profiling unavailable")
	})
	mock.EXPECT().
		WatchFieldValue(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			time.Second,
			time.Duration(0),
			2,
		).
		DoAndReturn(func(uint, dcgm.Short, time.Duration, time.Duration, int) error {
			watchRegistered = true
			return nil
		})
	mock.EXPECT().Cleanup()

	stopAfterBuild := errors.New("stop after initial registry build")
	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before NVML discovery")
		return nil
	}
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *cli.Context, *appconfig.Config) (
		*registry.Registry,
		devicewatchlistmanager.Manager,
		error,
	) {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before topology discovery")
		return nil, nil, stopAfterBuild
	}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	setGPUBindUnbindTestConfig(t, ctx)

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.ErrorIs(t, err, stopAfterBuild)
}

func TestRunDCGMExporter_FailsBeforeRegistryBuildWhenGPULifecycleWatchCannotStart(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	setupErr := errors.New("bind/unbind field unsupported")
	mock.EXPECT().
		WatchFieldValue(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			time.Second,
			time.Duration(0),
			2,
		).
		Return(setupErr)
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error {
		t.Fatal("NVML must not initialize after the explicitly enabled lifecycle watch fails")
		return nil
	}
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *cli.Context, *appconfig.Config) (
		*registry.Registry,
		devicewatchlistmanager.Manager,
		error,
	) {
		t.Fatal("registry must not build without the explicitly enabled lifecycle watch")
		return nil, nil, nil
	}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	setGPUBindUnbindTestConfig(t, ctx)

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.ErrorIs(t, err, setupErr)
	assert.Contains(t, err.Error(), "start GPU bind/unbind watcher")
}

func TestRunDCGMExporter_RejectsPprofWithoutWebConfigFileBeforeStartup(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))

	initializeDCGMProviderFunc = func(*appconfig.Config) {
		t.Fatal("DCGM must not initialize after pprof config validation fails")
	}

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), CLIEnablePprof)
	assert.Contains(t, err.Error(), CLIWebConfigFile)
}

func TestRunDCGMExporter_NVMLStartupPolicy(t *testing.T) {
	buildStop := errors.New("stop after initial registry build")
	tests := []struct {
		name                   string
		kubernetes             bool
		disableStartupValidate bool
		useFakeGPUs            bool
		initializeError        error
		wantContinue           bool
		wantInitializeCalls    int32
		wantCleanupCalls       int32
	}{
		{
			name:                "non-Kubernetes success",
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
		{
			name:                "non-Kubernetes failure falls back",
			initializeError:     errors.New("NVML unavailable"),
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
		{
			name:                "Kubernetes validated failure remains fatal",
			kubernetes:          true,
			initializeError:     errors.New("NVML unavailable"),
			wantInitializeCalls: 1,
		},
		{
			name:                   "Kubernetes failure with validation disabled falls back",
			kubernetes:             true,
			disableStartupValidate: true,
			initializeError:        errors.New("NVML unavailable"),
			wantContinue:           true,
			wantInitializeCalls:    1,
			wantCleanupCalls:       1,
		},
		{
			name:                "fake-GPU mode uses the optional NVML lifecycle",
			useFakeGPUs:         true,
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				Return(nil, errors.New("profiling unsupported")).
				AnyTimes()
			mock.EXPECT().Cleanup()

			initializeDCGMProviderFunc = func(*appconfig.Config) {}

			var initializeCalls atomic.Int32
			initializeNVMLProviderFunc = func() error {
				initializeCalls.Add(1)
				return tt.initializeError
			}
			var cleanupCalls atomic.Int32
			cleanupNVMLProviderFunc = func() {
				cleanupCalls.Add(1)
			}

			var buildCalls atomic.Int32
			buildRegistryFunc = func(
				context.Context,
				*cli.Context,
				*appconfig.Config,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				buildCalls.Add(1)
				return nil, nil, buildStop
			}

			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIKubernetes, fmt.Sprintf("%t", tt.kubernetes)))
			require.NoError(t, ctx.Set(CLIDisableStartupValidate, fmt.Sprintf("%t", tt.disableStartupValidate)))
			require.NoError(t, ctx.Set(CLIUseFakeGPUs, fmt.Sprintf("%t", tt.useFakeGPUs)))

			err := runDCGMExporter(context.Background(), ctx, nil)

			if tt.wantContinue {
				require.ErrorIs(t, err, buildStop)
				assert.Equal(t, int32(1), buildCalls.Load())
			} else {
				require.ErrorIs(t, err, tt.initializeError)
				assert.Zero(t, buildCalls.Load())
			}
			assert.Equal(t, tt.wantInitializeCalls, initializeCalls.Load())
			assert.Equal(t, tt.wantCleanupCalls, cleanupCalls.Load())
		})
	}
}

func TestRunDCGMExporter_InitialRegistryBuildFailure(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *cli.Context, *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return nil, nil, errors.New("registry build failed")
	}
	newMetricsServerFunc = func(*appconfig.Config, devicewatchlistmanager.Manager, *registry.Registry) (*server.MetricsServer, func(), error) {
		t.Fatal("metrics server must not start after registry build fails")
		return nil, nil, nil
	}

	err := runDCGMExporter(context.Background(), newTestCLIContext(t), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry build failed")
}

func TestRunDCGMExporter_MetricsServerFailureCleansRegistry(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	reg := registry.NewRegistry()
	buildRegistryFunc = func(context.Context, *cli.Context, *appconfig.Config) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return reg, topologyManager(), nil
	}
	newMetricsServerFunc = func(*appconfig.Config, devicewatchlistmanager.Manager, *registry.Registry) (*server.MetricsServer, func(), error) {
		return nil, nil, errors.New("listen setup failed")
	}

	err := runDCGMExporter(context.Background(), newTestCLIContext(t), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen setup failed")
}

func TestConfigureLogger(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		debug   bool
		wantErr string
	}{
		{name: "text", format: "text"},
		{name: "json with debug", format: "json", debug: true},
		{name: "invalid", format: "yaml", wantErr: "invalid log-format parameter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := cli.NewApp()
			app.Flags = []cli.Flag{
				&cli.StringFlag{Name: CLILogFormat},
				&cli.BoolFlag{Name: CLIDebugMode},
			}
			set := flag.NewFlagSet("logger", 0)
			set.String(CLILogFormat, tt.format, "")
			set.Bool(CLIDebugMode, tt.debug, "")

			err := configureLogger(cli.NewContext(app, set, nil))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
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

func TestDeviceUsageMIGNoteIsGPUOnly(t *testing.T) {
	usages := map[string]string{}
	for _, rawFlag := range NewApp().Flags {
		if stringFlag, ok := rawFlag.(*cli.StringFlag); ok {
			usages[stringFlag.Name] = stringFlag.Usage
		}
	}

	assert.Contains(t, usages[CLIGPUDevices], "unless MIG mode is enabled")
	assert.NotContains(t, usages[CLISwitchDevices], "unless MIG mode is enabled")
	assert.NotContains(t, usages[CLICPUDevices], "unless MIG mode is enabled")
}

func TestParseDeviceOptions_Table(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    appconfig.DeviceOptions
		wantErr string
	}{
		{name: "flex", input: "f", want: appconfig.DeviceOptions{Flex: true}},
		{name: "all major", input: "g", want: appconfig.DeviceOptions{MajorRange: []int{-1}}},
		{name: "all minor", input: "i", want: appconfig.DeviceOptions{MinorRange: []int{-1}}},
		{name: "major list and range", input: "g:0,2-4", want: appconfig.DeviceOptions{MajorRange: []int{0, 2, 3, 4}}},
		{name: "minor singleton range", input: "i:7-7", want: appconfig.DeviceOptions{MinorRange: []int{7}}},
		{
			name:  "all major and all minor",
			input: "g+i",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		},
		{
			name:  "all minor and all major",
			input: "i+g",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		},
		{
			name:  "major range and all minor",
			input: "g:0,2-4+i",
			want:  appconfig.DeviceOptions{MajorRange: []int{0, 2, 3, 4}, MinorRange: []int{-1}},
		},
		{
			name:  "all major and minor range",
			input: "g+i:7-7",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{7}},
		},
		{
			name:  "ranged minor and ranged major",
			input: "i:1-2+g:3",
			want:  appconfig.DeviceOptions{MajorRange: []int{3}, MinorRange: []int{1, 2}},
		},
		{name: "too many separators", input: "g:0:1", wantErr: "there can only be one specified range"},
		{name: "too many separators in combined selector", input: "g:0:1+i", wantErr: "there can only be one specified range"},
		{name: "flex cannot have range", input: "f:0", wantErr: "no range can be specified"},
		{name: "flex cannot combine before major", input: "f+g", wantErr: "cannot be combined"},
		{name: "flex cannot combine after minor", input: "i+f", wantErr: "cannot be combined"},
		{name: "duplicate major selector", input: "g+g:0", wantErr: "duplicate device option 'g'"},
		{name: "duplicate minor selector", input: "i:0+i:1", wantErr: "duplicate device option 'i'"},
		{name: "empty trailing selector", input: "g+", wantErr: "empty selector"},
		{name: "empty leading selector", input: "+i", wantErr: "empty selector"},
		{name: "unknown selector", input: "x:0", wantErr: "only valid options"},
		{name: "bad number", input: "g:nope", wantErr: "invalid syntax"},
		{name: "empty major range", input: "g:", wantErr: "invalid syntax"},
		{name: "empty range element", input: "g:0,,1", wantErr: "invalid syntax"},
		{name: "bad range start", input: "g:a-2", wantErr: "invalid syntax"},
		{name: "bad range end", input: "g:1-b", wantErr: "invalid syntax"},
		{name: "descending range", input: "g:4-2", wantErr: "must not exceed"},
		{name: "bad range shape", input: "g:1-2-3", wantErr: "range can only be"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDeviceOptions(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDeviceOptionsExpansionLimitPerSelector(t *testing.T) {
	got, err := parseDeviceOptions("g:0+i:0-1023")
	require.NoError(t, err)
	assert.Equal(t, []int{0}, got.MajorRange)
	assert.Len(t, got.MinorRange, 1024)
	assert.Equal(t, 0, got.MinorRange[0])
	assert.Equal(t, 1023, got.MinorRange[1023])

	_, err = parseDeviceOptions("i:0-1024")
	require.ErrorContains(t, err, "more than 1024 indices")
}

func FuzzParseDeviceOptions(f *testing.F) {
	for _, seed := range []string{
		"f",
		"g",
		"i",
		"g:0,2-4+i:7",
		"g:0-1023",
		"i:0-1024",
		"g:9223372036854775807-9223372036854775807",
		"f+g",
		"g:1-2-3",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseDeviceOptions(input)
		if err != nil {
			return
		}

		if got.Flex {
			if got.MajorRange != nil || got.MinorRange != nil {
				t.Fatalf("flex selector returned ranges: %#v", got)
			}
		} else if got.MajorRange == nil && got.MinorRange == nil {
			t.Fatalf("successful non-flex selector returned no ranges: %q", input)
		}

		for _, indices := range [][]int{got.MajorRange, got.MinorRange} {
			if indices == nil {
				continue
			}
			if len(indices) == 0 {
				t.Fatalf("successful selector returned an empty range: %q", input)
			}
			if len(indices) > int(dcgm.MAX_NUM_CPU_CORES) {
				t.Fatalf("selector returned %d indices, limit is %d", len(indices), dcgm.MAX_NUM_CPU_CORES)
			}
			if len(indices) == 1 && indices[0] == -1 {
				continue
			}
			for _, index := range indices {
				if index < 0 {
					t.Fatalf("selector returned negative index %d outside the all-devices sentinel", index)
				}
			}
		}

		again, err := parseDeviceOptions(input)
		if err != nil {
			t.Fatalf("selector result changed between identical parses: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("selector parsing is not deterministic:\nfirst:  %#v\nsecond: %#v", got, again)
		}
	})
}

func TestContextToConfigHonorsConfigurationFlags(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIFieldsFile, "/tmp/custom-counters.csv"))
	require.NoError(t, ctx.Set(CLIAddress, ":19500"))
	require.NoError(t, ctx.Set(CLICollectInterval, "5000"))
	require.NoError(t, ctx.Set(CLIGPUDevices, "g:2-3"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-counters.csv", cfg.CollectorsFile)
	assert.Equal(t, ":19500", cfg.Address)
	assert.Equal(t, 5000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DeviceOptions{MajorRange: []int{2, 3}}, cfg.GPUDeviceOptions)
}

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
			// Neutralise ambient flag env vars first, so an exported
			// DCGM_EXPORTER_HEALTH_REQUIRE_GPUS cannot make the "off" cases
			// pass or fail for the wrong reason. t.Setenv below still wins.
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
	assert.Equal(t, "/tmp/env-counters.csv", cfg.CollectorsFile)
	assert.Equal(t, ":19501", cfg.Address)
	assert.Equal(t, 7000, cfg.CollectInterval)
	assert.Equal(t, appconfig.DeviceOptions{MinorRange: []int{9}}, cfg.GPUDeviceOptions)
}

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

func TestParseDuration(t *testing.T) {
	assert.Equal(t, 3*time.Second, parseDuration("", 3*time.Second))
	assert.Equal(t, 250*time.Millisecond, parseDuration("250ms", time.Second))
	assert.Equal(t, time.Second, parseDuration("not-a-duration", time.Second))
}

func TestContextToConfigParsesWebTimeouts(t *testing.T) {
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIWebReadTimeout, "3s"))
	require.NoError(t, ctx.Set(CLIWebWriteTimeout, "45s"))

	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)

	assert.Equal(t, 3*time.Second, cfg.WebReadTimeout)
	assert.Equal(t, 45*time.Second, cfg.WebWriteTimeout)
}

func TestContextToConfigDefaultsMalformedWebTimeouts(t *testing.T) {
	tests := []struct {
		name       string
		readValue  string
		writeValue string
		wantRead   time.Duration
		wantWrite  time.Duration
	}{
		{
			name:       "read timeout",
			readValue:  "bogus",
			writeValue: "45s",
			wantRead:   appconfig.DefaultWebReadTimeout,
			wantWrite:  45 * time.Second,
		},
		{
			name:       "write timeout",
			readValue:  "3s",
			writeValue: "bogus",
			wantRead:   3 * time.Second,
			wantWrite:  appconfig.DefaultWebWriteTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIWebReadTimeout, tt.readValue))
			require.NoError(t, ctx.Set(CLIWebWriteTimeout, tt.writeValue))

			cfg, err := contextToConfig(ctx)
			require.NoError(t, err)

			assert.Equal(t, tt.wantRead, cfg.WebReadTimeout)
			assert.Equal(t, tt.wantWrite, cfg.WebWriteTimeout)
		})
	}
}

type fakeWatcher struct {
	err     error
	started chan struct{}
	release chan struct{}
}

func (f *fakeWatcher) Watch(ctx context.Context, onChange func()) error {
	close(f.started)
	onChange()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return f.err
	}
}

func TestRunWatcher(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "watcher failure is contained", err: errors.New("watch failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fw := &fakeWatcher{err: tt.err, started: make(chan struct{}), release: make(chan struct{})}
			var wg sync.WaitGroup
			var called atomic.Bool

			runWatcher(ctx, fw, func() { called.Store(true) }, &wg)

			select {
			case <-fw.started:
			case <-time.After(time.Second):
				t.Fatal("watcher did not start")
			}
			require.Eventually(t, called.Load, time.Second, 10*time.Millisecond)
			if errors.Is(tt.err, context.Canceled) {
				cancel()
			} else {
				close(fw.release)
			}

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("runWatcher did not stop")
			}
		})
	}
}

func TestNewOSWatcher(t *testing.T) {
	ch, cleanup := newOSWatcher(os.Interrupt)
	require.NotNil(t, ch)
	cleanup()
	_, ok := <-ch
	assert.False(t, ok)
}

func restoreStartupSeams(t *testing.T) {
	t.Helper()
	prevInitializeDCGMProvider := initializeDCGMProviderFunc
	prevInitializeNVMLProvider := initializeNVMLProviderFunc
	prevCleanupNVMLProvider := cleanupNVMLProviderFunc
	prevBuildRegistry := buildRegistryFunc
	prevGetCounters := getCountersFunc
	prevStartWatchListManager := startWatchListManagerFunc
	prevGetHostname := getHostnameFunc
	prevInitCollectorFactory := initCollectorFactoryFunc
	prevNewMetricsServer := newMetricsServerFunc
	prevNewFileWatcher := newFileWatcherFunc
	prevNewGPUBindUnbindWatcher := newGPUBindUnbindWatcherFunc
	t.Cleanup(func() {
		initializeDCGMProviderFunc = prevInitializeDCGMProvider
		initializeNVMLProviderFunc = prevInitializeNVMLProvider
		cleanupNVMLProviderFunc = prevCleanupNVMLProvider
		buildRegistryFunc = prevBuildRegistry
		getCountersFunc = prevGetCounters
		startWatchListManagerFunc = prevStartWatchListManager
		getHostnameFunc = prevGetHostname
		initCollectorFactoryFunc = prevInitCollectorFactory
		newMetricsServerFunc = prevNewMetricsServer
		newFileWatcherFunc = prevNewFileWatcher
		newGPUBindUnbindWatcherFunc = prevNewGPUBindUnbindWatcher
	})
}

type staticWatchListManager map[dcgm.Field_Entity_Group]devicewatchlistmanager.WatchList

func (s staticWatchListManager) CreateEntityWatchList(dcgm.Field_Entity_Group, devicewatcher.Watcher, int64) error {
	return nil
}

func (s staticWatchListManager) EntityWatchList(group dcgm.Field_Entity_Group) (devicewatchlistmanager.WatchList, bool) {
	watchList, ok := s[group]
	return watchList, ok
}

type topologyDeviceInfo struct {
	gpuCount uint
	switches []deviceinfo.SwitchInfo
	cpus     []deviceinfo.CPUInfo
}

func (t topologyDeviceInfo) GPUCount() uint                    { return t.gpuCount }
func (t topologyDeviceInfo) GPUs() []deviceinfo.GPUInfo        { return nil }
func (t topologyDeviceInfo) GPU(uint) deviceinfo.GPUInfo       { return deviceinfo.GPUInfo{} }
func (t topologyDeviceInfo) Switches() []deviceinfo.SwitchInfo { return t.switches }
func (t topologyDeviceInfo) Switch(uint) deviceinfo.SwitchInfo { return deviceinfo.SwitchInfo{} }
func (t topologyDeviceInfo) CPUs() []deviceinfo.CPUInfo        { return t.cpus }
func (t topologyDeviceInfo) CPU(uint) deviceinfo.CPUInfo       { return deviceinfo.CPUInfo{} }
func (t topologyDeviceInfo) GOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (t topologyDeviceInfo) SOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (t topologyDeviceInfo) COpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (t topologyDeviceInfo) InfoType() dcgm.Field_Entity_Group { return dcgm.FE_NONE }
func (t topologyDeviceInfo) IsCPUWatched(uint) bool            { return false }
func (t topologyDeviceInfo) IsCoreWatched(uint, uint) bool     { return false }
func (t topologyDeviceInfo) IsSwitchWatched(uint) bool         { return false }
func (t topologyDeviceInfo) IsLinkWatched(uint, uint) bool     { return false }

func topologyManager() devicewatchlistmanager.Manager {
	return staticWatchListManager{
		dcgm.FE_GPU: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{gpuCount: 2}, nil, nil, nil, 0,
		),
		dcgm.FE_SWITCH: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{switches: []deviceinfo.SwitchInfo{{EntityId: 7}, {EntityId: 8}}}, nil, nil, nil, 0,
		),
		dcgm.FE_CPU: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{cpus: []deviceinfo.CPUInfo{{EntityId: 0}, {EntityId: 1}, {EntityId: 2}}}, nil, nil, nil, 0,
		),
	}
}

func TestActionRecoversStartupPanic(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	initializeDCGMProviderFunc = func(*appconfig.Config) {
		panic("synthetic startup panic")
	}

	err := action(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "synthetic startup panic")
}

func TestNewAppRunReturnsErrorWhenRemoteHostengineStartupPanics(t *testing.T) {
	restoreStartupSeams(t)
	initializeDCGMProviderFunc = func(config *appconfig.Config) {
		assert.True(t, config.UseRemoteHE)
		assert.Equal(t, "127.0.0.1:1", config.RemoteHEInfo)
		panic("remote hostengine connection failed")
	}

	err := NewApp("test-version").Run([]string{
		"dcgm-exporter",
		"--disable-startup-validate",
		"--remote-hostengine-info",
		"127.0.0.1:1",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote hostengine connection failed")
}

func TestRunDCGMExporter_HotReloadAndShutdown(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	countersFile := filepath.Join(t.TempDir(), "counters.csv")
	require.NoError(t, os.WriteFile(countersFile, []byte("DCGM_FI_DEV_GPU_TEMP,gauge,temp\n"), 0o600))

	var buildCalls atomic.Int32
	buildRegistryFunc = func(
		_ context.Context,
		_ *cli.Context,
		cfg *appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		assert.Equal(t, countersFile, cfg.CollectorsFile)
		buildCalls.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIFieldsFile, countersFile))
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	reloadRequests := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		done <- runDCGMExporter(lifecycleCtx, ctx, reloadRequests)
	}()

	require.Eventually(t, func() bool { return buildCalls.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)
	reloadRequests <- struct{}{}
	require.Eventually(t, func() bool { return buildCalls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("exporter did not shut down after SIGTERM")
	}
}

func TestRunDCGMExporter_ShutdownCleansCurrentNVMLClient(t *testing.T) {
	tests := []struct {
		name       string
		kubernetes bool
	}{
		{name: "non-Kubernetes"},
		{name: "Kubernetes", kubernetes: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			ctrl := gomock.NewController(t)

			originalNVML := nvmlprovider.Client()
			t.Cleanup(func() { nvmlprovider.SetClient(originalNVML) })
			initialNVML := mocknvmlprovider.NewMockNVML(ctrl)
			currentNVML := mocknvmlprovider.NewMockNVML(ctrl)
			initialNVML.EXPECT().Cleanup().Times(0)
			currentNVML.EXPECT().Cleanup().Times(1)
			nvmlprovider.SetClient(initialNVML)

			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				Return(nil, errors.New("profiling unsupported"))
			mock.EXPECT().Cleanup()

			var buildCalls atomic.Int32
			buildRegistryFunc = func(
				context.Context,
				*cli.Context,
				*appconfig.Config,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				buildCalls.Add(1)
				return registry.NewRegistry(), topologyManager(), nil
			}
			initializeDCGMProviderFunc = func(*appconfig.Config) {}
			initializeNVMLProviderFunc = func() error { return nil }

			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
			require.NoError(t, ctx.Set(CLIKubernetes, fmt.Sprintf("%t", tt.kubernetes)))
			require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))

			lifecycleCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- runDCGMExporter(lifecycleCtx, ctx, nil)
			}()

			require.Eventually(t, func() bool {
				return buildCalls.Load() == 1
			}, 2*time.Second, 10*time.Millisecond)
			nvmlprovider.SetClient(currentNVML)
			cancel()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("exporter did not shut down")
			}
		})
	}
}

func TestRunDCGMExporter_ShutdownCleansCurrentDCGMClient(t *testing.T) {
	restoreStartupSeams(t)
	ctrl := gomock.NewController(t)

	originalDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(originalDCGM) })
	initialDCGM := mockdcgmprovider.NewMockDCGM(ctrl)
	currentDCGM := mockdcgmprovider.NewMockDCGM(ctrl)
	initialDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	initialDCGM.EXPECT().Cleanup().Times(0)
	currentDCGM.EXPECT().Cleanup().Times(1)
	dcgmprovider.SetClient(initialDCGM)

	var buildCalls atomic.Int32
	buildRegistryFunc = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		buildCalls.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	initializeDCGMProviderFunc = func(*appconfig.Config) {}
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))

	lifecycleCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runDCGMExporter(lifecycleCtx, ctx, nil)
	}()

	require.Eventually(t, func() bool {
		return buildCalls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
	dcgmprovider.SetClient(currentDCGM)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("exporter did not shut down")
	}
}

func TestDoConfigReloadSuccessAndFailure(t *testing.T) {
	t.Run("success installs rebuilt registry", func(t *testing.T) {
		coord := newTestCoordinator(t)
		initialRegistry := registry.NewRegistry()
		coord.server.SetRegistry(initialRegistry)
		newRegistry := registry.NewRegistry()
		coord.buildRegistry = func(
			context.Context,
			*cli.Context,
			*appconfig.Config,
		) (*registry.Registry, devicewatchlistmanager.Manager, error) {
			return newRegistry, topologyManager(), nil
		}

		coord.doConfigReload(context.Background(), &appconfig.Config{}, 1)

		assert.Same(t, newRegistry, coord.server.GetRegistry())
	})

	t.Run("failure keeps serving last-good registry", func(t *testing.T) {
		coord := newTestCoordinator(t)
		initialRegistry := registry.NewRegistry()
		coord.server.SetRegistry(initialRegistry)
		coord.buildRegistry = func(
			context.Context,
			*cli.Context,
			*appconfig.Config,
		) (*registry.Registry, devicewatchlistmanager.Manager, error) {
			return nil, nil, errors.New("build failed")
		}

		coord.doConfigReload(context.Background(), &appconfig.Config{}, 1)

		assert.Same(t, initialRegistry, coord.server.GetRegistry())
	})
}

func TestGetCountersCopiesLabelCounters(t *testing.T) {
	countersFile := filepath.Join(t.TempDir(), "counters.csv")
	require.NoError(t, os.WriteFile(countersFile, []byte(
		"DCGM_FI_DEV_GPU_TEMP,gauge,temp\nDCGM_FI_DRIVER_VERSION,label,driver\n",
	), 0o600))

	got, err := getCounters(context.Background(), &appconfig.Config{
		CollectorsFile: countersFile,
		ConfigMapData:  undefinedConfigMapData,
		CollectDCP:     false,
	})

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 2)
	require.Len(t, got.ExporterCounters, 1)
	assert.Equal(t, "DCGM_FI_DRIVER_VERSION", got.ExporterCounters[0].FieldName)
}

func TestReloadEventForGPUState(t *testing.T) {
	tests := []struct {
		state dcgm.BindUnbindEventState
		want  reloadEvent
		ok    bool
	}{
		{state: dcgm.DcgmBUEventStateSystemReinitializing, want: evNone, ok: false},
		{state: dcgm.DcgmBUEventStateSystemReinitializationCompleted, want: evGPUReinitialized, ok: true},
		{state: dcgm.BindUnbindEventState(99), want: evNone, ok: false},
	}

	for _, tt := range tests {
		got, ok := reloadEventForGPUState(tt.state)
		assert.Equal(t, tt.want, got)
		assert.Equal(t, tt.ok, ok)
	}
}

func sampleMetricGroups() []dcgm.MetricGroup {
	return []dcgm.MetricGroup{
		{Major: 0, Minor: 0, FieldIds: []uint{1001, 1002, 1003}},
		{Major: 1, Minor: 0, FieldIds: []uint{1010, 1011}},
	}
}

// newTestCoordinator builds a coordinator backed by a zero-value
// *server.MetricsServer (sufficient for SetReloadInProgress/IsReloadInProgress)
// and a minimal cli.Context. Tests replace only the dependency seam relevant to the behavior under test.
func newTestCoordinator(t *testing.T) *reloadCoordinator {
	t.Helper()
	coord := newReloadCoordinator(newTestCLIContext(t))
	coord.cleanupDCGM = func() {}
	coord.initializeDCGM = func(*appconfig.Config) {}
	coord.cleanupNVML = func() {}
	coord.initializeNVML = func() error { return nil }
	coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
		return func() {}
	}
	cfg, err := defaultConfig()
	require.NoError(t, err)
	coord.reloadConfig = cfg.Clone()
	coord.setServer(&server.MetricsServer{})
	return coord
}

// newTestCLIContext returns a cli.Context with the minimum flags needed for
// contextToConfig to succeed — it sets the device-option strings and the DCGM
// log level. Other flags default to their zero value.
func newTestCLIContext(t *testing.T) *cli.Context {
	t.Helper()
	app := cli.NewApp()
	app.Flags = []cli.Flag{
		&cli.StringFlag{Name: CLIConfigFile},
		&cli.StringFlag{Name: CLIFieldsFile},
		&cli.StringFlag{Name: CLIAddress},
		&cli.IntFlag{Name: CLICollectInterval},
		&cli.DurationFlag{Name: CLIWatchMaxKeepAge},
		&cli.Int64Flag{Name: CLIWatchMaxKeepSamples},
		&cli.StringFlag{Name: CLIConfigMapData},
		&cli.StringFlag{Name: CLIGPUDevices},
		&cli.StringFlag{Name: CLISwitchDevices},
		&cli.StringFlag{Name: CLICPUDevices},
		&cli.StringFlag{Name: CLIDCGMLogLevel},
		&cli.StringFlag{Name: CLILogFormat},
		&cli.StringFlag{Name: CLIRemoteHEInfo},
		&cli.BoolFlag{Name: CLIDisableStartupValidate},
		&cli.BoolFlag{Name: CLIKubernetes},
		&cli.BoolFlag{Name: CLIKubernetesVirtualGPUs},
		&cli.BoolFlag{Name: CLIUseFakeGPUs},
		&cli.BoolFlag{Name: CLIContainerLabels},
		&cli.StringFlag{Name: CLIContainerRuntimeSocket},
		&cli.BoolFlag{Name: CLIDumpEnabled},
		&cli.StringFlag{Name: CLIDumpDirectory},
		&cli.IntFlag{Name: CLIDumpRetention},
		&cli.BoolFlag{Name: CLIDumpCompression},
		&cli.StringFlag{Name: CLIWebReadTimeout},
		&cli.StringFlag{Name: CLIWebWriteTimeout},
		&cli.BoolFlag{Name: CLIEnableExporterMetrics},
		&cli.StringFlag{Name: CLIWebConfigFile},
		&cli.BoolFlag{Name: CLIEnableGPUBindUnbindWatch},
		&cli.StringFlag{Name: CLIGPUBindUnbindPollInterval},
		&cli.BoolFlag{Name: CLIEnablePprof},
	}
	set := flag.NewFlagSet("test", 0)
	set.String(CLIFieldsFile, filepath.Join(t.TempDir(), "counters.csv"), "")
	set.String(CLIConfigFile, "", "")
	set.String(CLIAddress, "127.0.0.1:0", "")
	set.Int(CLICollectInterval, 1, "")
	set.Duration(CLIWatchMaxKeepAge, appconfig.DefaultWatchMaxKeepAge, "")
	set.Int64(CLIWatchMaxKeepSamples, appconfig.DefaultWatchMaxSamples, "")
	set.String(CLIConfigMapData, undefinedConfigMapData, "")
	set.String(CLIGPUDevices, "f", "")
	set.String(CLISwitchDevices, "f", "")
	set.String(CLICPUDevices, "f", "")
	set.String(CLIDCGMLogLevel, "NONE", "")
	set.String(CLILogFormat, "text", "")
	set.String(CLIRemoteHEInfo, "localhost:5555", "")
	set.Bool(CLIDisableStartupValidate, false, "")
	set.Bool(CLIKubernetes, false, "")
	set.Bool(CLIKubernetesVirtualGPUs, false, "")
	set.Bool(CLIUseFakeGPUs, false, "")
	set.Bool(CLIContainerLabels, false, "")
	set.String(CLIContainerRuntimeSocket, "", "")
	set.Bool(CLIDumpEnabled, false, "")
	set.String(CLIDumpDirectory, "/tmp/dcgm-exporter-debug", "")
	set.Int(CLIDumpRetention, 24, "")
	set.Bool(CLIDumpCompression, true, "")
	set.String(CLIWebReadTimeout, appconfig.DefaultWebReadTimeout.String(), "")
	set.String(CLIWebWriteTimeout, appconfig.DefaultWebWriteTimeout.String(), "")
	set.Int(CLIMaxConcurrentScrapes, appconfig.DefaultMaxConcurrentScrapes, "")
	set.Bool(CLIEnableExporterMetrics, false, "")
	set.String(CLIWebConfigFile, "", "")
	set.Bool(CLIEnableGPUBindUnbindWatch, false, "")
	set.String(CLIGPUBindUnbindPollInterval, time.Second.String(), "")
	set.Bool(CLIEnablePprof, false, "")
	return cli.NewContext(app, set, nil)
}

func setGPUBindUnbindTestConfig(t *testing.T, ctx *cli.Context) {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
`), 0o600))
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
}

func unsetFlagEnvVars(t *testing.T, flags []cli.Flag) {
	t.Helper()

	seen := make(map[string]struct{})
	for _, flag := range flags {
		docFlag, ok := flag.(cli.DocGenerationFlag)
		if !ok {
			continue
		}
		for _, envVar := range docFlag.GetEnvVars() {
			if _, ok := seen[envVar]; ok {
				continue
			}
			seen[envVar] = struct{}{}

			name := envVar
			value, wasSet := os.LookupEnv(name)
			require.NoError(t, os.Unsetenv(name))
			t.Cleanup(func() {
				if wasSet {
					require.NoError(t, os.Setenv(name, value))
					return
				}
				require.NoError(t, os.Unsetenv(name))
			})
		}
	}
}

func newInvalidTestCLIContext(t *testing.T) *cli.Context {
	t.Helper()
	app := cli.NewApp()
	app.Flags = []cli.Flag{
		&cli.StringFlag{Name: CLIGPUDevices},
		&cli.StringFlag{Name: CLISwitchDevices},
		&cli.StringFlag{Name: CLICPUDevices},
		&cli.StringFlag{Name: CLIDCGMLogLevel},
	}
	set := flag.NewFlagSet("test-invalid", 0)
	set.String(CLIGPUDevices, "bad", "")
	set.String(CLISwitchDevices, "f", "")
	set.String(CLICPUDevices, "f", "")
	set.String(CLIDCGMLogLevel, "NONE", "")
	ctx := cli.NewContext(app, set, nil)
	if err := ctx.Set(CLIGPUDevices, "bad"); err != nil {
		t.Fatalf("set invalid GPU devices: %v", err)
	}
	return ctx
}

func TestBuildReloadConfigRequiresStartupSnapshot(t *testing.T) {
	coord := newReloadCoordinator(newTestCLIContext(t))

	cfg, err := coord.buildReloadConfig()

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "no startup config snapshot")
}

// TestDCPCapabilities_RestoresPublishedState verifies that buildReloadConfig
// overlays a seeded snapshot onto the fresh config.
func TestDCPCapabilities_RestoresPublishedState(t *testing.T) {
	coord := newTestCoordinator(t)
	source := &appconfig.Config{CollectDCP: true, MetricGroups: sampleMetricGroups()}
	coord.dcp = newDCPCapabilities(source)

	cfg, err := coord.buildReloadConfig()
	require.NoError(t, err)

	assert.True(t, cfg.CollectDCP)
	assert.Equal(t, sampleMetricGroups(), cfg.MetricGroups,
		"fresh config picks up the seeded metric groups")
}

// TestDCPCapabilities_DeepCopyGuardsAliasing is the load-bearing aliasing
// regression. applyTo writes into public fields of appconfig.Config and
// dcgm.MetricGroup, so a future refactor that drops cloning would corrupt
// the stored snapshot via the apply-site.
func TestDCPCapabilities_DeepCopyGuardsAliasing(t *testing.T) {
	coord := newTestCoordinator(t)
	source := &appconfig.Config{CollectDCP: true, MetricGroups: sampleMetricGroups()}
	original := sampleMetricGroups()
	coord.dcp = newDCPCapabilities(source)

	// Apply into config B, then mutate both the slice and a nested FieldIds
	// slice via B's public fields.
	configB, err := coord.buildReloadConfig()
	require.NoError(t, err)
	configB.MetricGroups[0].FieldIds[0] = 0xDEADBEEF
	configB.MetricGroups = append(configB.MetricGroups, dcgm.MetricGroup{Major: 99})

	// Also mutate the source to prove the snapshot is decoupled from publish
	// input as well.
	source.MetricGroups[0].FieldIds[1] = 0xDEADBEEF
	source.MetricGroups = append(source.MetricGroups, dcgm.MetricGroup{Major: 77})

	// Apply into a fresh config C and assert it matches the original inputs.
	configC, err := coord.buildReloadConfig()
	require.NoError(t, err)
	assert.Equal(t, original, configC.MetricGroups,
		"snapshot survives mutation of both publish input and apply output")
}

// withMockDCGMClient installs a fresh MockDCGM for the duration of the test
// and restores the previous client on cleanup.
func withMockDCGMClient(t *testing.T) *mockdcgmprovider.MockDCGM {
	t.Helper()
	ctrl := gomock.NewController(t)
	mock := mockdcgmprovider.NewMockDCGM(ctrl)
	prev := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(prev) })
	dcgmprovider.SetClient(mock)
	return mock
}

// TestQueryDCPMetrics_PublishesSuccess proves the success path writes an
// enabled snapshot containing the queried metric groups.
func TestQueryDCPMetrics_PublishesSuccess(t *testing.T) {
	mock := withMockDCGMClient(t)
	groups := sampleMetricGroups()
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groups, nil)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	coord := newTestCoordinator(t)
	cfg := &appconfig.Config{CollectDCP: true}
	coord.queryDCPMetrics(cfg, 0)

	require.NotNil(t, coord.dcp, "queryDCPMetrics must publish a snapshot")
	assert.True(t, coord.dcp.collectDCP)
	assert.Equal(t, groups, coord.dcp.metricGroups)
	assert.True(t, cfg.CollectDCP, "config reflects success")
	assert.Equal(t, groups, cfg.MetricGroups)
}

// TestQueryDCPMetrics_PublishesDisabledOnError proves the error path
// overwrites any prior enabled snapshot with a disabled one. This is the
// property that prevents stale enabled capabilities from surviving a failed
// topology-change query into a later hot reload.
func TestQueryDCPMetrics_PublishesDisabledOnError(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))

	coord := newTestCoordinator(t)
	// Seed a prior enabled snapshot to ensure the error path overwrites it.
	coord.dcp = &dcpCapabilities{collectDCP: true, metricGroups: sampleMetricGroups()}

	cfg := &appconfig.Config{CollectDCP: true}
	coord.queryDCPMetrics(cfg, 0)

	require.NotNil(t, coord.dcp)
	assert.False(t, coord.dcp.collectDCP, "error path publishes disabled snapshot")
	assert.Nil(t, coord.dcp.metricGroups)
	assert.False(t, cfg.CollectDCP)
	assert.Nil(t, cfg.MetricGroups)
}

// TestQueryDCPMetrics_PublishesDisabledOnPanic proves the panic-recovery path
// also overwrites any prior enabled snapshot — specifically exercising the
// single-defer epilogue ordering (recover mutates cfg, then publish captures
// the mutated state).
func TestQueryDCPMetrics_PublishesDisabledOnPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).DoAndReturn(
		func(uint) ([]dcgm.MetricGroup, error) { panic("profiling API segfault") },
	)

	coord := newTestCoordinator(t)
	coord.dcp = &dcpCapabilities{collectDCP: true, metricGroups: sampleMetricGroups()}

	cfg := &appconfig.Config{CollectDCP: true}
	require.NotPanics(t, func() { coord.queryDCPMetrics(cfg, 0) },
		"queryDCPMetrics must recover from profiling API panics")

	require.NotNil(t, coord.dcp)
	assert.False(t, coord.dcp.collectDCP, "panic path publishes disabled snapshot")
	assert.Nil(t, coord.dcp.metricGroups)
	assert.False(t, cfg.CollectDCP)
	assert.Nil(t, cfg.MetricGroups)
}

// countingFactory is a minimal collector.Factory that records how many times
// NewCollectors is invoked and returns a fixed slice of distinct tuples.
type countingFactory struct {
	calls      atomic.Int32
	collectors []collector.EntityCollectorTuple
}

func (f *countingFactory) NewCollectors() []collector.EntityCollectorTuple {
	f.calls.Add(1)
	return f.collectors
}

type fakeMetricCollector struct{}

func (fakeMetricCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

func (fakeMetricCollector) Cleanup() {}

// TestPopulateRegistry_CallsNewCollectorsOnce ensures each registry calls
// NewCollectors once. Extra calls install field watches on collectors that are
// never registered or cleaned up.
func TestPopulateRegistry_CallsNewCollectorsOnce(t *testing.T) {
	tuples := []collector.EntityCollectorTuple{{}, {}, {}}
	tuples[0].SetEntity(dcgm.FE_GPU)
	tuples[1].SetEntity(dcgm.FE_SWITCH)
	tuples[2].SetEntity(dcgm.FE_CPU)

	f := &countingFactory{collectors: tuples}
	r := registry.NewRegistry()

	got := populateRegistry(f, r)

	assert.Equal(t, int32(1), f.calls.Load(),
		"NewCollectors must be invoked exactly once per registry lifecycle")
	assert.Equal(t, len(tuples), got,
		"returned count equals the number of collectors registered")
}

func TestBuildRegistrySuccessAndHostnameFailure(t *testing.T) {
	restoreStartupSeams(t)

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}
	manager := topologyManager()
	tuples := []collector.EntityCollectorTuple{{}}
	tuples[0].SetEntity(dcgm.FE_GPU)
	tuples[0].SetCollector(fakeMetricCollector{})

	getCountersFunc = func(context.Context, *appconfig.Config) (*counters.CounterSet, error) {
		return counterSet, nil
	}
	startWatchListManagerFunc = func(got *counters.CounterSet, _ *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		assert.Same(t, counterSet, got)
		return manager, nil
	}
	getHostnameFunc = func(*appconfig.Config) (string, error) {
		return "host-a", nil
	}
	factory := &countingFactory{collectors: tuples}
	initCollectorFactoryFunc = func(
		gotCounters *counters.CounterSet,
		gotManager devicewatchlistmanager.Manager,
		hostname string,
		_ *appconfig.Config,
	) collector.Factory {
		assert.Same(t, counterSet, gotCounters)
		assert.Equal(t, manager, gotManager)
		assert.Equal(t, "host-a", hostname)
		return factory
	}

	reg, gotManager, err := buildRegistry(context.Background(), newTestCLIContext(t), &appconfig.Config{})
	require.NoError(t, err)
	require.NotNil(t, reg)
	t.Cleanup(reg.Cleanup)
	assert.Equal(t, manager, gotManager)
	assert.Equal(t, int32(1), factory.calls.Load())

	getHostnameFunc = func(*appconfig.Config) (string, error) {
		return "", errors.New("no hostname")
	}
	initCollectorFactoryFunc = func(
		*counters.CounterSet,
		devicewatchlistmanager.Manager,
		string,
		*appconfig.Config,
	) collector.Factory {
		t.Fatal("collector factory should not be initialized when hostname lookup fails")
		return nil
	}

	reg, gotManager, err = buildRegistry(context.Background(), newTestCLIContext(t), &appconfig.Config{})
	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "failed to get hostname")

	getHostnameFunc = func(*appconfig.Config) (string, error) {
		t.Fatal("hostname lookup should be skipped when NoHostname is enabled")
		return "", nil
	}
	factory = &countingFactory{collectors: tuples}
	initCollectorFactoryFunc = func(
		gotCounters *counters.CounterSet,
		gotManager devicewatchlistmanager.Manager,
		hostname string,
		_ *appconfig.Config,
	) collector.Factory {
		assert.Same(t, counterSet, gotCounters)
		assert.Equal(t, manager, gotManager)
		assert.Empty(t, hostname)
		return factory
	}

	reg, gotManager, err = buildRegistry(context.Background(), newTestCLIContext(t), &appconfig.Config{NoHostname: true})
	require.NoError(t, err)
	require.NotNil(t, reg)
	t.Cleanup(reg.Cleanup)
	assert.Equal(t, manager, gotManager)
	assert.Equal(t, int32(1), factory.calls.Load())
}

func TestBuildRegistryReturnsCounterLoadError(t *testing.T) {
	restoreStartupSeams(t)

	getCountersFunc = func(context.Context, *appconfig.Config) (*counters.CounterSet, error) {
		return nil, errors.New("bad collectors")
	}
	startWatchListManagerFunc = func(*counters.CounterSet, *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		t.Fatal("watch list manager should not start when counter loading fails")
		return nil, nil
	}

	reg, gotManager, err := buildRegistry(context.Background(), newTestCLIContext(t), &appconfig.Config{})

	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "failed to get counters")
	assert.Contains(t, err.Error(), "bad collectors")
}

func TestBuildRegistryReturnsWatchListManagerError(t *testing.T) {
	restoreStartupSeams(t)

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}

	getCountersFunc = func(context.Context, *appconfig.Config) (*counters.CounterSet, error) {
		return counterSet, nil
	}
	startWatchListManagerFunc = func(got *counters.CounterSet, _ *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		assert.Same(t, counterSet, got)
		return nil, errors.New("bad watch groups")
	}
	getHostnameFunc = func(*appconfig.Config) (string, error) {
		t.Fatal("hostname should not be read when watch list manager startup fails")
		return "", nil
	}

	reg, gotManager, err := buildRegistry(context.Background(), newTestCLIContext(t), &appconfig.Config{})

	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "bad watch groups")
}

// ---- Reload coordinator tests ----

func runCoordinator(t *testing.T, coord *reloadCoordinator) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coord.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("coordinator did not exit after context cancellation")
		}
	})
}

type cleanupTrackingCollector struct {
	cleanup func()
}

// trackingGPUWatcherLifecycle records lifecycle calls made by the coordinator.
type trackingGPUWatcherLifecycle struct {
	start func()
	stop  func()
}

// Start records a watcher start when the test configured one.
func (w trackingGPUWatcherLifecycle) Start(context.Context) error {
	if w.start != nil {
		w.start()
	}
	return nil
}

// Stop records a watcher stop when the test configured one.
func (w trackingGPUWatcherLifecycle) Stop() {
	if w.stop != nil {
		w.stop()
	}
}

type scriptedGPUWatcherLifecycle struct {
	startErrors []error
	starts      int
	stops       int
}

func (w *scriptedGPUWatcherLifecycle) Start(context.Context) error {
	w.starts++
	if w.starts <= len(w.startErrors) {
		return w.startErrors[w.starts-1]
	}
	return nil
}

func (w *scriptedGPUWatcherLifecycle) Stop() {
	w.stops++
}

func (*cleanupTrackingCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

func (c *cleanupTrackingCollector) Cleanup() {
	c.cleanup()
}

func registryWithCleanup(cleanup func()) *registry.Registry {
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(&cleanupTrackingCollector{cleanup: cleanup})
	r := registry.NewRegistry()
	r.Register(tuple)
	return r
}

func TestReloadCoordinator_PendingReloadPreservesLatestGPUState(t *testing.T) {
	coord := newTestCoordinator(t)

	coord.Trigger(evConfigChanged)
	coord.Trigger(evGPUReinitialized)

	pending := coord.takePending()
	assert.True(t, pending.configChanged)
	assert.Equal(t, evGPUReinitialized, pending.latestGPUEvent)
	assert.True(t, coord.takePending().empty())
}

func TestReloadCoordinator_CoalescesBurstOfConfigChanged(t *testing.T) {
	coord := newTestCoordinator(t)
	var calls atomic.Int32
	applied := make(chan struct{})
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		if calls.Add(1) == 1 {
			close(applied)
		}
	}

	for range 50 {
		coord.Trigger(evConfigChanged)
	}
	runCoordinator(t, coord)

	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced config event was not delivered")
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestReloadCoordinator_DRAResourceSliceChangeUsesRegistryReload(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)

	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		builds.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	coord.cleanupDCGM = func() { t.Fatal("DRA topology reload must not reset DCGM") }
	coord.cleanupNVML = func() { t.Fatal("DRA topology reload must not reset NVML") }

	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("a coalesced DRA event must use the outcome-aware registry reload path")
	}
	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(1), builds.Load())
	assert.NotSame(t, initialRegistry, coord.server.GetRegistry())
}

func TestReloadCoordinator_DRAResourceSliceChangeRetriesFailedRegistryBuild(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	refreshedRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		return refreshedRegistry, topologyManager(), nil
	}
	coord.cleanupDCGM = func() { t.Fatal("DRA topology reload must not reset DCGM") }
	coord.cleanupNVML = func() { t.Fatal("DRA topology reload must not reset NVML") }

	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Same(t, initialRegistry, coord.server.GetRegistry())
	pending := coord.takePending()
	assert.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load(), "the failed DRA reload must be replayed once")
	assert.Same(t, refreshedRegistry, coord.server.GetRegistry())
	assert.True(t, coord.takePending().empty(), "a successful retry must not leave more DRA work pending")
}

func TestReloadCoordinator_DRAResourceSliceChangeRetryIsBounded(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		builds.Add(1)
		return nil, nil, errors.New("registry build failed")
	}

	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())
	pending := coord.takePending()
	require.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load(), "one notification permits one retry")
	assert.Same(t, initialRegistry, coord.server.GetRegistry(), "both failed builds must preserve the last-good registry")
	assert.True(t, coord.takePending().empty(), "a failed retry must not schedule another retry")
}

func TestReloadCoordinator_DRAResourceSliceChangeRetriesAfterPanic(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	refreshedRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			panic("synthetic DRA registry build panic")
		}
		return refreshedRegistry, topologyManager(), nil
	}

	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Same(t, initialRegistry, coord.server.GetRegistry())
	pending := coord.takePending()
	require.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load())
	assert.Same(t, refreshedRegistry, coord.server.GetRegistry())
	assert.False(t, coord.server.IsReloadInProgress())
	assert.True(t, coord.takePending().empty(), "a successful panic retry must not leave more DRA work pending")
}

// TestReloadCoordinator_CoalescedLifecycleResetsProvidersOnce checks the full reset order.
func TestReloadCoordinator_CoalescedLifecycleResetsProvidersOnce(t *testing.T) {
	mock := withMockDCGMClient(t)
	var order []string
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
			order = append(order, "dcp")
			return nil, errors.New("profiling unavailable")
		})

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registryWithCleanup(func() {
		order = append(order, "registry-cleanup")
	}))
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		stop:  func() { order = append(order, "watcher-stop") },
		start: func() { order = append(order, "watcher-start") },
	}
	coord.cleanupNVML = func() { order = append(order, "nvml-cleanup") }
	coord.cleanupDCGM = func() { order = append(order, "dcgm-cleanup") }
	coord.initializeDCGM = func(*appconfig.Config) { order = append(order, "dcgm-init") }
	coord.initializeNVML = func() error {
		order = append(order, "nvml-init")
		return nil
	}
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		order = append(order, "build")
		return newRegistry, topologyManager(), nil
	}
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("lifecycle reset must consume a coalesced config reload")
	}

	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, []string{
		"watcher-stop",
		"registry-cleanup",
		"nvml-cleanup",
		"dcgm-cleanup",
		"dcgm-init",
		"watcher-start",
		"nvml-init",
		"dcp",
		"build",
	}, order)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_RetriesFailedRegistryBuildWithoutAnotherEvent checks self-recovery.
func TestReloadCoordinator_RetriesFailedRegistryBuildWithoutAnotherEvent(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	coord.server.SetRegistry(registry.NewRegistry())
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	var cleanups atomic.Int32
	coord.cleanupNVML = func() { cleanups.Add(1) }
	coord.cleanupDCGM = func() { cleanups.Add(1) }
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) <= 2 {
			return nil, nil, errors.New("registry build failed")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(1), builds.Load())
	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())
	assert.Equal(t, int32(2), builds.Load())
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(3), builds.Load())
	assert.Equal(t, int32(2), cleanups.Load(), "a registry retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

func TestReloadCoordinator_RetriesRegistryBuildAfterPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			panic("synthetic registry build panic")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry, "a recovered panic must leave one retry pending")
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), builds.Load())
	assert.Equal(t, int32(2), providerCleanups.Load(), "a panic retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

func TestReloadCoordinator_RetriesWatcherStartAfterPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	var retry func()
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		assert.Equal(t, time.Second, delay)
		retry = callback
		return func() {}
	}
	var watcherStarts atomic.Int32
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		start: func() {
			if watcherStarts.Add(1) == 1 {
				panic("synthetic watcher start panic")
			}
		},
	}
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Equal(t, gpuRecoveryRestartWatcher, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry, "a watcher panic must leave one retry pending")
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), watcherStarts.Load())
	assert.Equal(t, int32(2), providerCleanups.Load(), "a watcher panic retry must not repeat provider teardown")
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

func TestReloadCoordinator_RetriesGPUWatcherRegistrationAfterProviderReset(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	gpuWatcher := &scriptedGPUWatcherLifecycle{startErrors: []error{context.DeadlineExceeded}}
	coord.gpuWatcher = gpuWatcher
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return newRegistry, topologyManager(), nil
	}

	coord.doGPULifecycleReset(context.Background(), 1)

	assert.Equal(t, 1, gpuWatcher.stops)
	assert.Equal(t, 1, gpuWatcher.starts)
	assert.Equal(t, gpuRecoveryRestartWatcher, coord.gpuRecoveryStage)
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, 2, gpuWatcher.starts)
	assert.Equal(t, int32(2), providerCleanups.Load(), "a watcher retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

func TestReloadCoordinator_NewLifecycleSupersedesQueuedRecoveryRetry(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable")).
		Times(2)

	coord := newTestCoordinator(t)
	var retry func()
	var retryCanceled atomic.Int32
	coord.scheduleRetry = func(_ context.Context, _ time.Duration, callback func()) context.CancelFunc {
		retry = callback
		return func() { retryCanceled.Add(1) }
	}
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())
	require.NotNil(t, retry)
	retry()
	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), builds.Load())
	assert.Positive(t, retryCanceled.Load())
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

func TestNextLifecycleRetryDelayCapsAtThirtySeconds(t *testing.T) {
	assert.Equal(t, 2*time.Second, nextLifecycleRetryDelay(time.Second))
	assert.Equal(t, 30*time.Second, nextLifecycleRetryDelay(16*time.Second))
	assert.Equal(t, 30*time.Second, nextLifecycleRetryDelay(30*time.Second))
}

// TestReloadCoordinator_CleanupPanicDoesNotBlockLaterLifecycleReset checks recovery after a panic.
func TestReloadCoordinator_CleanupPanicDoesNotBlockLaterLifecycleReset(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.reloadConfig.Kubernetes = true
	coord.server.SetRegistry(registryWithCleanup(func() {
		panic("synthetic collector cleanup failure")
	}))

	var nvmlCleanups atomic.Int32
	var watcherStarts atomic.Int32
	var watcherStops atomic.Int32
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		start: func() { watcherStarts.Add(1) },
		stop:  func() { watcherStops.Add(1) },
	}
	coord.cleanupNVML = func() { nvmlCleanups.Add(1) }
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return newRegistry, topologyManager(), nil
	}

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())
	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	// The later lifecycle reset retries after the first reset panics.
	assert.Same(t, newRegistry, coord.server.GetRegistry())
	assert.Equal(t, int32(1), nvmlCleanups.Load())
	assert.Equal(t, int32(2), watcherStarts.Load())
	assert.Equal(t, int32(2), watcherStops.Load())
}

// TestDoGPULifecycleReset_ProviderOrder checks provider order and NVML failure policy.
func TestDoGPULifecycleReset_ProviderOrder(t *testing.T) {
	wantOrder := []string{
		"stale-cleanup",
		"nvml-cleanup",
		"dcgm-cleanup",
		"dcgm-init",
		"nvml",
		"dcp",
		"build",
	}
	tests := []struct {
		name                   string
		kubernetes             bool
		virtualGPUs            bool
		disableStartupValidate bool
		useFakeGPUs            bool
		initializeError        error
		wantFailureLogLevel    string
	}{
		{
			name: "non-Kubernetes real GPUs",
		},
		{
			name:                "Kubernetes validated failure logs error and still rebuilds",
			kubernetes:          true,
			initializeError:     errors.New("NVML unavailable"),
			wantFailureLogLevel: "ERROR",
		},
		{
			name:                "non-Kubernetes failure warns and still rebuilds",
			initializeError:     errors.New("NVML unavailable"),
			wantFailureLogLevel: "WARN",
		},
		{
			name:                   "Kubernetes validation-disabled failure warns and still rebuilds",
			kubernetes:             true,
			disableStartupValidate: true,
			initializeError:        errors.New("NVML unavailable"),
			wantFailureLogLevel:    "WARN",
		},
		{
			name:        "Kubernetes virtual GPUs use the same provider lifecycle",
			kubernetes:  true,
			virtualGPUs: true,
		},
		{
			name:        "fake-GPU mode uses the same provider lifecycle",
			useFakeGPUs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			var order []string

			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
					order = append(order, "dcp")
					return nil, errors.New("profiling unavailable")
				})

			coord := newTestCoordinator(t)
			coord.reloadConfig.Kubernetes = tt.kubernetes
			coord.reloadConfig.KubernetesVirtualGPUs = tt.virtualGPUs
			coord.reloadConfig.DisableStartupValidate = tt.disableStartupValidate
			coord.reloadConfig.UseFakeGPUs = tt.useFakeGPUs
			coord.cleanupNVML = func() { order = append(order, "nvml-cleanup") }
			coord.cleanupDCGM = func() { order = append(order, "dcgm-cleanup") }
			coord.initializeDCGM = func(*appconfig.Config) { order = append(order, "dcgm-init") }
			coord.initializeNVML = func() error {
				order = append(order, "nvml")
				return tt.initializeError
			}
			coord.server.SetRegistry(registryWithCleanup(func() {
				order = append(order, "stale-cleanup")
			}))
			newRegistry := registry.NewRegistry()
			coord.buildRegistry = func(
				context.Context,
				*cli.Context,
				*appconfig.Config,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				order = append(order, "build")
				return newRegistry, topologyManager(), nil
			}

			coord.handle(context.Background(), evGPUReinitialized)

			assert.Equal(t, wantOrder, order)
			if tt.wantFailureLogLevel != "" {
				assert.Contains(t, logs.String(),
					"level="+tt.wantFailureLogLevel+" msg=\"Failed to reinitialize NVML\"")
			}
			assert.Same(t, newRegistry, coord.server.GetRegistry())
		})
	}
}

// TestDoGPULifecycleReset_BuildFailureAllowsConfigRecovery checks the next config reload.
func TestDoGPULifecycleReset_BuildFailureAllowsConfigRecovery(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	var retry func()
	coord.scheduleRetry = func(_ context.Context, _ time.Duration, callback func()) context.CancelFunc {
		retry = callback
		return func() {}
	}
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		_ context.Context,
		_ *cli.Context,
		cfg *appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		assert.Equal(t, "updated-counters.csv", cfg.CollectorsFile)
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Nil(t, coord.server.ClearRegistry())
	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())

	coord.reloadConfig.CollectorsFile = "updated-counters.csv"
	var configCalls atomic.Int32
	coord.applyConfigReload = func(_ context.Context, cfg *appconfig.Config, _ uint64) {
		configCalls.Add(1)
		assert.Equal(t, "updated-counters.csv", cfg.CollectorsFile)
	}
	require.NotNil(t, retry)
	retry()
	coord.Trigger(evConfigChanged)
	coord.handlePending(context.Background(), coord.takePending())
	assert.Equal(t, int32(1), configCalls.Load())
	assert.Equal(t, int32(2), builds.Load())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
}

func TestReloadCoordinator_TriggerIsConcurrentSafe(t *testing.T) {
	coord := newTestCoordinator(t)
	events := []reloadEvent{evConfigChanged, evGPUReinitialized}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < 1000; j++ {
					coord.Trigger(events[(offset+j)%len(events)])
				}
			}(i)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger did not complete under concurrent producers")
	}

	pending := coord.takePending()
	assert.True(t, pending.configChanged)
	assert.Equal(t, evGPUReinitialized, pending.latestGPUEvent)
}

func TestReloadCoordinator_ShutsDownOnContextCancel(t *testing.T) {
	coord := newTestCoordinator(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coord.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not exit after context cancellation")
	}
}

func TestInitReloadCoordinatorStoresStartupConfig(t *testing.T) {
	cfg := &appconfig.Config{CollectDCP: true}
	coord := initReloadCoordinator(newTestCLIContext(t), cfg)

	require.NotNil(t, coord.reloadConfig)
	assert.NotSame(t, cfg, coord.reloadConfig)
	assert.True(t, coord.reloadConfig.CollectDCP)
	assert.Nil(t, coord.dcp, "DCP discovery must wait until the lifecycle watch is registered")

	callback := cfg.DRAResourceSliceChangeCallback()
	require.NotNil(t, callback)
	callback()
	assert.Equal(t, evDRAResourceSliceChanged, coord.takePending().draResourceSliceEvent)
}

func TestReloadCoordinator_RepublishedDCPReplacesPreviousSnapshot(t *testing.T) {
	mock := withMockDCGMClient(t)
	groupsA := []dcgm.MetricGroup{{Major: 0, Minor: 0, FieldIds: []uint{1001, 1002}}}
	groupsB := []dcgm.MetricGroup{{Major: 7, Minor: 1, FieldIds: []uint{1077, 1078}}}
	gomock.InOrder(
		mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groupsA, nil),
		mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groupsB, nil),
	)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	coord := newTestCoordinator(t)
	coord.queryDCPMetrics(&appconfig.Config{CollectDCP: true}, 0)
	cfgA, err := coord.buildReloadConfig()
	require.NoError(t, err)

	coord.queryDCPMetrics(&appconfig.Config{CollectDCP: true}, 1)
	cfgB, err := coord.buildReloadConfig()
	require.NoError(t, err)

	assert.Equal(t, groupsA, cfgA.MetricGroups)
	assert.Equal(t, groupsB, cfgB.MetricGroups)
}

func TestReloadCoordinator_ConfigBuildFailureDoesNotPoisonNextReload(t *testing.T) {
	coord := newReloadCoordinator(newInvalidTestCLIContext(t))
	coord.setServer(&server.MetricsServer{})

	var calls atomic.Int32
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		calls.Add(1)
	}

	coord.handle(context.Background(), evConfigChanged)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Equal(t, int32(0), calls.Load())

	cfg, err := defaultConfig()
	require.NoError(t, err)
	coord.reloadConfig = cfg.Clone()
	coord.handle(context.Background(), evConfigChanged)

	assert.False(t, coord.server.IsReloadInProgress())
	assert.Equal(t, int32(1), calls.Load())
}
