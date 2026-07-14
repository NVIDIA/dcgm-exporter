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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
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

// TestDCGMCleanupClosureBehavior verifies that the dcgmCleanup closure
// calls the CURRENT provider's Cleanup method, not a captured instance.
// This prevents memory leaks during GPU bind/unbind cycles.
func TestDCGMCleanupClosureBehavior(t *testing.T) {
	// Save original client
	originalClient := dcgmprovider.Client()
	defer dcgmprovider.SetClient(originalClient)

	// Create first mock provider
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProvider1 := mockdcgmprovider.NewMockDCGM(ctrl)
	mockProvider1.EXPECT().Cleanup().Times(0) // Should NOT be called

	mockProvider2 := mockdcgmprovider.NewMockDCGM(ctrl)
	mockProvider2.EXPECT().Cleanup().Times(1) // Should be called

	// Set first provider
	dcgmprovider.SetClient(mockProvider1)

	// Create cleanup closure (simulates line 461-463 in app.go)
	dcgmCleanup := func() {
		dcgmprovider.Client().Cleanup()
	}

	// Simulate DCGM reinitialization (like in handleGPUTopologyChange)
	dcgmprovider.SetClient(mockProvider2)

	// Call cleanup - should call mockProvider2.Cleanup(), NOT mockProvider1.Cleanup()
	dcgmCleanup()

	// Test passes if mockProvider2.Cleanup() was called and mockProvider1.Cleanup() was NOT called
	// (gomock verifies this automatically via EXPECT())
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
		CLIGPUDevices,
		CLISwitchDevices,
		CLICPUDevices,
		CLIDumpEnabled,
		CLIEnableGPUBindUnbindWatch,
		CLIEnablePprof,
		CLIWebSystemdSocket,
		CLIWebReadTimeout,
		CLIWebWriteTimeout,
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
		assert.Equal(t, defaults.NoHostname, c.Bool(CLINoHostname))
		assert.Equal(t, defaults.UseFakeGPUs, c.Bool(CLIUseFakeGPUs))
		assert.Equal(t, defaults.ConfigMapData, c.String(CLIConfigMapData))
		assert.Equal(t, defaults.WebSystemdSocket, c.Bool(CLIWebSystemdSocket))
		assert.Equal(t, defaults.WebConfigFile, c.String(CLIWebConfigFile))
		assert.Equal(t, defaults.WebReadTimeout.String(), c.String(CLIWebReadTimeout))
		assert.Equal(t, defaults.WebWriteTimeout.String(), c.String(CLIWebWriteTimeout))
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
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, appconfig.DefaultCollectorsFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, appconfig.DefaultCollectorsFile, watchFile)
}

func TestContextToConfigYAMLConfig(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collection:
  interval: 10s
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, configFile, cfg.ConfigFile)
	assert.Equal(t, 10000, cfg.CollectInterval)
	assert.Equal(t, appconfig.MetricSourceInline, cfg.MetricSource.Kind)
	require.Len(t, cfg.MetricSource.Fields, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", cfg.MetricSource.Fields[0].Name)
	_, watchFile := cfg.MetricFileWatcherPath()
	assert.False(t, watchFile)
}

func TestContextToConfigYAMLOmittedMetricsUsesDefaultMetricSource(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
collection:
  interval: 10s
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
version: 1
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
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collection:
  interval: 10s
`), 0o600))
	countersFile := filepath.Join(t.TempDir(), "override.csv")

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	require.NoError(t, ctx.Set(CLIFieldsFile, countersFile))
	require.NoError(t, ctx.Set(CLICollectInterval, "45000"))

	cfg, err := contextToConfig(ctx)

	require.NoError(t, err)
	assert.Equal(t, 45000, cfg.CollectInterval)
	assert.Equal(t, countersFile, cfg.CollectorsFile)
	assert.Equal(t, appconfig.MetricSourceFile, cfg.MetricSource.Kind)
	assert.Equal(t, countersFile, cfg.MetricSource.File)
	watchFile, ok := cfg.MetricFileWatcherPath()
	assert.True(t, ok)
	assert.Equal(t, countersFile, watchFile)
}

func TestContextToConfigLegacyConfigMapDataOverridesYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
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
version: 1
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

func TestContextToConfigLoadsWatchGroups(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
collection:
  watchGroups:
    - name: slow
      interval: 10m
      fields:
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
version: 1
collection:
  interval: 10s
`), 0o600))

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
	cfg, err := contextToConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 10000, cfg.CollectInterval)

	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
collection:
  interval: 1m
`), 0o600))
	coord := newReloadCoordinator(ctx, func() {})
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

func TestContextToConfigRejectsInvalidGPUBindUnbindPollInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-1s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
			require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, tt.value))

			cfg, err := contextToConfig(ctx)

			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "invalid gpu-bind-unbind-poll-interval")
			assert.Contains(t, err.Error(), tt.value)
			assert.Contains(t, err.Error(), "must be greater than 0")
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

func TestContextToConfigGPUBindUnbindPollInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "valid custom interval", value: "250ms", want: 250 * time.Millisecond},
		{name: "fractional interval", value: "0.5s", want: 500 * time.Millisecond},
		{name: "minimum positive interval", value: "1ns", want: time.Nanosecond},
		{name: "large positive interval", value: "24h", want: 24 * time.Hour},
		{name: "empty falls back to default", value: "", want: time.Second},
		{name: "malformed falls back to default", value: "not-a-duration", want: time.Second},
		{name: "whitespace falls back to default", value: " 1s ", want: time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
			require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, tt.value))

			cfg, err := contextToConfig(ctx)

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.GPUBindUnbindPollInterval)
		})
	}
}

func TestContextToConfigAllowsNonPositiveGPUBindUnbindPollIntervalWhenWatchDisabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "zero", value: "0s", want: 0},
		{name: "negative", value: "-1s", want: -1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "false"))
			require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, tt.value))

			cfg, err := contextToConfig(ctx)

			require.NoError(t, err)
			assert.False(t, cfg.EnableGPUBindUnbindWatch)
			assert.Equal(t, tt.want, cfg.GPUBindUnbindPollInterval)
		})
	}
}

func TestContextToConfigRejectsInvalidGPUBindUnbindPollIntervalFromEnv(t *testing.T) {
	t.Setenv("DCGM_EXPORTER_ENABLE_GPU_BIND_UNBIND_WATCH", "true")
	t.Setenv("DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL", "0s")

	var cfg *appconfig.Config
	app := NewApp()
	app.Action = func(c *cli.Context) error {
		var err error
		cfg, err = contextToConfig(c)
		return err
	}

	err := app.Run([]string{"dcgm-exporter"})

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "invalid gpu-bind-unbind-poll-interval")
	assert.Contains(t, err.Error(), "0s")
	assert.Contains(t, err.Error(), "must be greater than 0")
}

func TestRunDCGMExporter_RejectsPprofWithoutWebConfigFileBeforeStartup(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))

	validatePrerequisitesFunc = func() error {
		t.Fatal("prerequisite validation must not run after pprof config validation fails")
		return nil
	}
	initializeDCGMProviderFunc = func(*appconfig.Config) {
		t.Fatal("DCGM must not initialize after pprof config validation fails")
	}

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), CLIEnablePprof)
	assert.Contains(t, err.Error(), CLIWebConfigFile)
}

func TestRunDCGMExporter_PrerequisiteFailureStopsStartup(t *testing.T) {
	restoreStartupSeams(t)
	validatePrerequisitesFunc = func() error {
		return errors.New("missing runtime capability")
	}
	initializeDCGMProviderFunc = func(*appconfig.Config) {
		t.Fatal("DCGM must not initialize after prerequisite validation fails")
	}

	err := runDCGMExporter(context.Background(), newTestCLIContext(t), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing runtime capability")
}

func TestRunDCGMExporter_RejectsInvalidGPUBindUnbindPollIntervalBeforeStartup(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnableGPUBindUnbindWatch, "true"))
	require.NoError(t, ctx.Set(CLIGPUBindUnbindPollInterval, "0s"))

	validatePrerequisitesFunc = func() error {
		t.Fatal("prerequisite validation must not run after config validation fails")
		return nil
	}
	initializeDCGMProviderFunc = func(*appconfig.Config) {
		t.Fatal("DCGM must not initialize after config validation fails")
	}

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid gpu-bind-unbind-poll-interval")
	assert.Contains(t, err.Error(), "0s")
	assert.Contains(t, err.Error(), "must be greater than 0")
}

func TestRunDCGMExporter_InitialRegistryBuildFailure(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	validatePrerequisitesFunc = func() error { return nil }
	initializeDCGMProviderFunc = func(*appconfig.Config) {}
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

	validatePrerequisitesFunc = func() error { return nil }
	initializeDCGMProviderFunc = func(*appconfig.Config) {}
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
	prevValidatePrerequisites := validatePrerequisitesFunc
	prevInitializeDCGMProvider := initializeDCGMProviderFunc
	prevInitializeNVMLProvider := initializeNVMLProviderFunc
	prevBuildRegistry := buildRegistryFunc
	prevGetCounters := getCountersFunc
	prevStartWatchListManager := startWatchListManagerFunc
	prevGetHostname := getHostnameFunc
	prevInitCollectorFactory := initCollectorFactoryFunc
	prevNewMetricsServer := newMetricsServerFunc
	prevNewFileWatcher := newFileWatcherFunc
	prevNewGPUBindUnbindWatcher := newGPUBindUnbindWatcherFunc
	t.Cleanup(func() {
		validatePrerequisitesFunc = prevValidatePrerequisites
		initializeDCGMProviderFunc = prevInitializeDCGMProvider
		initializeNVMLProviderFunc = prevInitializeNVMLProvider
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
	validatePrerequisitesFunc = func() error {
		t.Fatal("startup validation should be disabled for this lifecycle test")
		return nil
	}

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

func TestGPUWatcherLifecycle(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		FieldGroupCreate(gomock.Any(), gomock.Any()).
		Return(dcgm.FieldHandle{}, errors.New("NVML doesn't exist")).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	controller := newGPUWatcherLifecycle(ctx, func() *watcher.GPUBindUnbindWatcher {
		return watcher.NewGPUBindUnbindWatcher(watcher.WithPollInterval(time.Millisecond))
	}, func() {
		t.Fatal("NVML unavailable path should not report topology changes")
	})

	var wg sync.WaitGroup
	runGPUWatcher(controller, &wg)
	require.Eventually(t, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return controller.cancel != nil
	}, time.Second, 10*time.Millisecond)

	controller.Start()
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GPU watcher lifecycle did not stop after context cancel")
	}

	controller.Stop()
	controller.Start()
}

func sampleMetricGroups() []dcgm.MetricGroup {
	return []dcgm.MetricGroup{
		{Major: 0, Minor: 0, FieldIds: []uint{1001, 1002, 1003}},
		{Major: 1, Minor: 0, FieldIds: []uint{1010, 1011}},
	}
}

// newTestCoordinator builds a coordinator backed by a zero-value
// *server.MetricsServer (sufficient for SetReloadInProgress/IsReloadInProgress)
// and a minimal cli.Context that contextToConfig accepts. Real apply functions
// would need fuller DCGM/NVML state; tests that drive handle() through the
// coordinator therefore inject their own applyConfigReload / applyTopologyChange
// stubs.
func newTestCoordinator(t *testing.T) *reloadCoordinator {
	t.Helper()
	coord := newReloadCoordinator(newTestCLIContext(t), func() {})
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
		&cli.BoolFlag{Name: CLIContainerLabels},
		&cli.StringFlag{Name: CLIContainerRuntimeSocket},
		&cli.BoolFlag{Name: CLIDumpEnabled},
		&cli.StringFlag{Name: CLIDumpDirectory},
		&cli.IntFlag{Name: CLIDumpRetention},
		&cli.BoolFlag{Name: CLIDumpCompression},
		&cli.BoolFlag{Name: CLIEnableGPUBindUnbindWatch},
		&cli.StringFlag{Name: CLIGPUBindUnbindPollInterval},
		&cli.StringFlag{Name: CLIWebReadTimeout},
		&cli.StringFlag{Name: CLIWebWriteTimeout},
		&cli.StringFlag{Name: CLIWebConfigFile},
		&cli.BoolFlag{Name: CLIEnablePprof},
	}
	set := flag.NewFlagSet("test", 0)
	set.String(CLIFieldsFile, filepath.Join(t.TempDir(), "counters.csv"), "")
	set.String(CLIConfigFile, "", "")
	set.String(CLIAddress, "127.0.0.1:0", "")
	set.Int(CLICollectInterval, 1, "")
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
	set.Bool(CLIContainerLabels, false, "")
	set.String(CLIContainerRuntimeSocket, "", "")
	set.Bool(CLIDumpEnabled, false, "")
	set.String(CLIDumpDirectory, "/tmp/dcgm-exporter-debug", "")
	set.Int(CLIDumpRetention, 24, "")
	set.Bool(CLIDumpCompression, true, "")
	set.Bool(CLIEnableGPUBindUnbindWatch, false, "")
	set.String(CLIGPUBindUnbindPollInterval, "1s", "")
	set.String(CLIWebReadTimeout, appconfig.DefaultWebReadTimeout.String(), "")
	set.String(CLIWebWriteTimeout, appconfig.DefaultWebWriteTimeout.String(), "")
	set.String(CLIWebConfigFile, "", "")
	set.Bool(CLIEnablePprof, false, "")
	return cli.NewContext(app, set, nil)
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
	coord := newReloadCoordinator(newTestCLIContext(t), func() {})

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

type fakeGPUWatcherLifecycle struct {
	startCalls atomic.Int32
	stopCalls  atomic.Int32
	running    atomic.Bool
}

func (f *fakeGPUWatcherLifecycle) Start() {
	f.startCalls.Add(1)
	f.running.Store(true)
}

func (f *fakeGPUWatcherLifecycle) Stop() {
	f.stopCalls.Add(1)
	f.running.Store(false)
}

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

// runCoordinator starts coord.Run in a goroutine and returns a cleanup func
// that cancels it and waits for exit. Test-body code Triggers events on coord
// and the coordinator drains them in Run.
func runCoordinator(t *testing.T, coord *reloadCoordinator) context.CancelFunc {
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
			t.Fatal("coordinator did not exit after context cancel")
		}
	})
	return cancel
}

// TestReloadCoordinator_CoalescesBurstOfSameEvent verifies that a burst of
// identical events enqueued before Run drains yields a single apply call.
func TestReloadCoordinator_CoalescesBurstOfSameEvent(t *testing.T) {
	coord := newTestCoordinator(t)

	var configCalls atomic.Int32
	done := make(chan struct{})
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		if configCalls.Add(1) == 1 {
			close(done)
		}
	}
	coord.applyTopologyChange = func(context.Context, uint64) {
		t.Fatal("topology apply must not run for a config-only burst")
	}

	// Trigger several events before starting Run — all should collapse into
	// one mailbox value.
	for i := 0; i < 10; i++ {
		coord.Trigger(evConfigChanged)
	}

	runCoordinator(t, coord)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not process the coalesced event")
	}

	// Give the coordinator a moment to race into a second call if it were going to.
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int32(1), configCalls.Load(),
		"identical events must coalesce into exactly one apply")
}

// TestReloadCoordinator_TopologyDominatesConfigBurst pins the explicit
// regression for the reviewer's "stronger event lost behind weaker burst"
// concern: Triggering N config events followed by one topology event must
// yield exactly one topology apply and zero config applies.
func TestReloadCoordinator_TopologyDominatesConfigBurst(t *testing.T) {
	coord := newTestCoordinator(t)

	var topoCalls atomic.Int32
	done := make(chan struct{})
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("config apply must not run when topology dominates the burst")
	}
	coord.applyTopologyChange = func(context.Context, uint64) {
		if topoCalls.Add(1) == 1 {
			close(done)
		}
	}

	// Fire a bunch of config events, then upgrade to topology. All this
	// happens before Run starts — so the mailbox sees the CAS-upgrade
	// sequence and ends holding evTopologyChanged.
	for i := 0; i < 50; i++ {
		coord.Trigger(evConfigChanged)
	}
	coord.Trigger(evTopologyChanged)

	runCoordinator(t, coord)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not process the dominating topology event")
	}
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int32(1), topoCalls.Load())
}

// TestReloadCoordinator_TopologyDuringActiveHandlerIsNotDropped is the load-
// bearing behaviour from the pre-refactor design: a topology event arriving
// while a config reload is running must be processed afterward. With the
// mailbox coordinator this happens without a rate limiter to defeat.
func TestReloadCoordinator_TopologyDuringActiveHandlerIsNotDropped(t *testing.T) {
	coord := newTestCoordinator(t)

	configStarted := make(chan struct{})
	configBlock := make(chan struct{})
	topoDone := make(chan struct{})

	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		close(configStarted)
		<-configBlock // block until the test releases us
	}
	coord.applyTopologyChange = func(context.Context, uint64) {
		close(topoDone)
	}

	runCoordinator(t, coord)

	coord.Trigger(evConfigChanged)

	// Wait until the config apply is mid-flight.
	select {
	case <-configStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("config apply did not start")
	}

	// Topology event arrives while the config handler is running.
	coord.Trigger(evTopologyChanged)

	// Release the config handler. Coordinator should now pick up the queued
	// topology event from the mailbox.
	close(configBlock)

	select {
	case <-topoDone:
	case <-time.After(2 * time.Second):
		t.Fatal("topology event queued during active handler was never processed")
	}
}

// TestReloadCoordinator_StartupSeedsDCPForFirstHotReload pins the startup-
// seeding invariant: queryDCPMetrics called before any reload populates
// coord.dcp so the first buildReloadConfig produces a cfg with the seeded
// MetricGroups.
func TestReloadCoordinator_StartupSeedsDCPForFirstHotReload(t *testing.T) {
	mock := withMockDCGMClient(t)
	groups := sampleMetricGroups()
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groups, nil)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	coord := newTestCoordinator(t)
	// Seed exactly as runDCGMExporter does at startup.
	coord.queryDCPMetrics(&appconfig.Config{CollectDCP: true}, 0)

	cfg, err := coord.buildReloadConfig()
	require.NoError(t, err)

	assert.True(t, cfg.CollectDCP,
		"first reload after startup sees CollectDCP=true from the seeded snapshot")
	assert.Equal(t, groups, cfg.MetricGroups,
		"first reload after startup sees the seeded MetricGroups")
}

// TestReloadCoordinator_SurvivesHandlerPanic verifies that a panic in an
// apply handler is recovered inside handle() and the coordinator processes
// the next event normally.
func TestReloadCoordinator_SurvivesHandlerPanic(t *testing.T) {
	coord := newTestCoordinator(t)

	var topoCalls atomic.Int32
	okDone := make(chan struct{})
	coord.applyTopologyChange = func(context.Context, uint64) {
		n := topoCalls.Add(1)
		if n == 1 {
			panic("synthetic handler panic")
		}
		close(okDone)
	}

	runCoordinator(t, coord)

	// First event: panics, is recovered.
	coord.Trigger(evTopologyChanged)

	// Wait until the first handler call has actually happened — otherwise
	// the second Trigger could coalesce with the first.
	require.Eventually(t, func() bool { return topoCalls.Load() >= 1 },
		2*time.Second, 5*time.Millisecond, "first handler did not run")

	// Second event: should be processed normally, proving the coordinator
	// outlived the panic.
	coord.Trigger(evTopologyChanged)

	select {
	case <-okDone:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not process a second event after a handler panic")
	}
	assert.Equal(t, int32(2), topoCalls.Load())
}

// TestReloadCoordinator_ShutsDownOnContextCancel verifies that Run returns
// promptly when its context is cancelled. runCoordinator's cleanup asserts
// this directly; this test makes the contract explicit.
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
		t.Fatal("coordinator did not exit within 2s of context cancel")
	}
}

// TestReloadCoordinator_TriggerIsNonBlocking pins the non-blocking producer
// contract. Without Run running, many Triggers must all return quickly; the
// mailbox+wake design guarantees this without relying on any buffer capacity.
func TestReloadCoordinator_TriggerIsNonBlocking(t *testing.T) {
	coord := newTestCoordinator(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			coord.Trigger(evConfigChanged)
			coord.Trigger(evTopologyChanged)
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Trigger blocked — mailbox+wake contract violated")
	}
	// Mailbox should end up holding the strongest event seen.
	assert.Equal(t, int32(evTopologyChanged), coord.mailbox.Load())
}

// TestReloadCoordinator_MailboxCASUpgradeTable exhaustively verifies the
// monotonic-upgrade rule: Trigger(ev) stores max(current, ev) — it never
// downgrades. The table covers every (start, trigger) combination.
func TestReloadCoordinator_MailboxCASUpgradeTable(t *testing.T) {
	assert.Equal(t, "reloadEvent(99)", reloadEvent(99).String())

	cases := []struct {
		start, trigger, want reloadEvent
	}{
		{evNone, evConfigChanged, evConfigChanged},
		{evNone, evTopologyChanged, evTopologyChanged},
		{evConfigChanged, evConfigChanged, evConfigChanged},
		{evConfigChanged, evTopologyChanged, evTopologyChanged},
		{evTopologyChanged, evConfigChanged, evTopologyChanged}, // must NOT downgrade
		{evTopologyChanged, evTopologyChanged, evTopologyChanged},
	}

	for _, tc := range cases {
		t.Run(tc.start.String()+"_then_"+tc.trigger.String(), func(t *testing.T) {
			coord := newTestCoordinator(t)
			coord.mailbox.Store(int32(tc.start))

			coord.Trigger(tc.trigger)

			got := reloadEvent(coord.mailbox.Load())
			assert.Equalf(t, tc.want, got,
				"Trigger(%s) from %s must land on %s, got %s",
				tc.trigger, tc.start, tc.want, got)
		})
	}
}

// TestReloadCoordinator_StressManyProducers hammers Trigger from many
// producer goroutines and verifies the coordinator processes events cleanly,
// no CAS livelock, no lost wakeups that leave the mailbox stuck non-empty.
// Under -race this exercises the paths that the single-producer tests do
// not: contention on the CAS loop and on the 1-slot wake channel.
func TestReloadCoordinator_StressManyProducers(t *testing.T) {
	coord := newTestCoordinator(t)

	var applies atomic.Int32
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		applies.Add(1)
	}
	coord.applyTopologyChange = func(context.Context, uint64) {
		applies.Add(1)
	}

	runCoordinator(t, coord)

	const producers = 64
	const perProducer = 2000

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				ev := evConfigChanged
				if (i+p)%7 == 0 {
					ev = evTopologyChanged
				}
				coord.Trigger(ev)
			}
		}(p)
	}
	wg.Wait()

	// Total fired events: producers * perProducer. Coalescing means we
	// expect many fewer than that many apply calls, but at least one.
	maxFired := int32(producers * perProducer)

	// Wait for the coordinator to drain to a stable state. The mailbox
	// should eventually settle to evNone.
	require.Eventually(t, func() bool {
		return reloadEvent(coord.mailbox.Load()) == evNone
	}, 5*time.Second, 10*time.Millisecond, "mailbox did not drain after producer burst")

	// One more Trigger to flush any pending apply and force at least one
	// observable invocation if the coordinator had somehow gotten wedged.
	coord.Trigger(evConfigChanged)
	require.Eventually(t, func() bool {
		return applies.Load() > 0 && reloadEvent(coord.mailbox.Load()) == evNone
	}, 2*time.Second, 10*time.Millisecond)

	// Sanity: apply count cannot exceed total fired + 1 (the flush).
	assert.LessOrEqual(t, applies.Load(), maxFired+1,
		"apply count exceeded fired events — coalescing broken")
	assert.Greater(t, applies.Load(), int32(0),
		"no events were processed despite many producers firing")
}

// TestInitReloadCoordinator_SeedsDCP pins the startup wiring. The test calls
// the exact function runDCGMExporter uses to construct and
// seed the coordinator, so a future refactor that accidentally removes the
// seeding step will turn this test red rather than silently dropping profiling
// metrics after reload.
func TestInitReloadCoordinator_SeedsDCP(t *testing.T) {
	mock := withMockDCGMClient(t)
	groups := sampleMetricGroups()
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groups, nil)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	cfg := &appconfig.Config{CollectDCP: true}
	coord := initReloadCoordinator(newTestCLIContext(t), func() {}, cfg)

	require.NotNil(t, coord.dcp,
		"initReloadCoordinator must publish a DCP snapshot before returning")
	assert.True(t, coord.dcp.collectDCP)
	assert.Equal(t, groups, coord.dcp.metricGroups)
	assert.Equal(t, groups, cfg.MetricGroups,
		"the caller's config must also reflect the queried capabilities")
}

// TestDoTopologyChange_PessimisticallyInvalidatesDCP is the regression for
// the reviewer's "stale capabilities survive an early topology panic"
// concern. doTopologyChange must invalidate r.dcp before touching DCGM, so a
// later panic cannot leave the next config reload applying capabilities from
// the pre-change hardware.
//
// This test calls handle() directly on the test goroutine so there is no
// concurrency between the coordinator's write of r.dcp and the test's read.
func TestDoTopologyChange_PessimisticallyInvalidatesDCP(t *testing.T) {
	coord := newTestCoordinator(t)

	// Seed a stale snapshot that *would* survive into the next reload if the
	// invalidation at the start of doTopologyChange were missing.
	coord.dcp = &dcpCapabilities{collectDCP: true, metricGroups: sampleMetricGroups()}

	// Simulate an early panic inside doTopologyChange by having dcgmCleanup
	// panic. handle()'s own deferred recover swallows it.
	coord.dcgmCleanup = func() { panic("synthetic failure before queryDCPMetrics") }

	// Drive handle() directly — it runs the full panic-recover path in the
	// current goroutine and returns when its defer completes.
	coord.handle(context.Background(), evTopologyChanged)

	require.NotNil(t, coord.dcp, "dcp must be set to the invalidated snapshot, not left as stale")
	assert.False(t, coord.dcp.collectDCP,
		"a topology change must invalidate stale DCP capabilities even if it panics early")
	assert.Nil(t, coord.dcp.metricGroups)
}

func TestDoTopologyChange_MissingSnapshotIsNonDestructive(t *testing.T) {
	coord := newReloadCoordinator(newInvalidTestCLIContext(t), func() {
		t.Fatal("dcgmCleanup must not run after config build failure")
	})
	coord.setServer(&server.MetricsServer{})

	stale := newDCPCapabilities(&appconfig.Config{
		CollectDCP:   true,
		MetricGroups: sampleMetricGroups(),
	})
	coord.dcp = stale

	gpuWatcher := &fakeGPUWatcherLifecycle{}
	gpuWatcher.running.Store(true)
	coord.setGPUWatcher(gpuWatcher)

	coord.handle(context.Background(), evTopologyChanged)

	assert.Same(t, stale, coord.dcp,
		"config parse failure should return before invalidating DCP")
	assert.Equal(t, int32(0), gpuWatcher.stopCalls.Load(),
		"config parse failure should not stop the GPU watcher")
	assert.True(t, gpuWatcher.running.Load(),
		"config parse failure should leave the GPU watcher running")
}

func TestDoTopologyChangeDoesNotRereadYAML(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
collection:
  interval: 10s
`), 0o600))

	cliCtx := newTestCLIContext(t)
	require.NoError(t, cliCtx.Set(CLIConfigFile, configFile))
	startupConfig, err := contextToConfig(cliCtx)
	require.NoError(t, err)
	require.Equal(t, 10000, startupConfig.CollectInterval)

	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
collection:
  watchGroups:
    - name: slow
      interval: 10m
      fields:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
`), 0o600))

	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))

	coord := newReloadCoordinator(cliCtx, func() {})
	coord.setServer(&server.MetricsServer{})
	coord.reloadConfig = startupConfig.Clone()

	var initialized atomic.Bool
	coord.initializeDCGM = func(got *appconfig.Config) {
		assert.Equal(t, 10000, got.CollectInterval,
			"topology reset must reuse startup YAML instead of re-reading the changed file")
		initialized.Store(true)
	}

	var rebuilt atomic.Bool
	coord.buildRegistry = func(
		_ context.Context,
		_ *cli.Context,
		got *appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		assert.True(t, initialized.Load())
		assert.Equal(t, 10000, got.CollectInterval)
		assert.False(t, got.CollectDCP,
			"topology reset must publish the post-reset DCP result")
		assert.Nil(t, got.MetricGroups)
		rebuilt.Store(true)
		return nil, nil, errors.New("stop before real registry rebuild")
	}

	coord.handle(context.Background(), evTopologyChanged)

	assert.True(t, initialized.Load(),
		"invalid edited YAML must not prevent DCGM reinitialization")
	assert.True(t, rebuilt.Load(),
		"invalid edited YAML must not prevent registry rebuild")
	require.NotNil(t, coord.dcp)
	assert.False(t, coord.dcp.collectDCP)
	assert.Nil(t, coord.dcp.metricGroups)
}

func TestDoTopologyChange_StopsGPUWatcherBeforeCleanup(t *testing.T) {
	coord := newTestCoordinator(t)
	gpuWatcher := &fakeGPUWatcherLifecycle{}
	gpuWatcher.running.Store(true)
	coord.setGPUWatcher(gpuWatcher)

	coord.dcgmCleanup = func() {
		assert.False(t, gpuWatcher.running.Load(),
			"GPU watcher must be stopped before DCGM cleanup starts")
		panic("synthetic cleanup failure")
	}

	coord.handle(context.Background(), evTopologyChanged)

	assert.Equal(t, int32(1), gpuWatcher.stopCalls.Load())
	assert.Equal(t, int32(0), gpuWatcher.startCalls.Load(),
		"watcher should not restart if cleanup panics before DCGM is reinitialized")
	require.NotNil(t, coord.dcp)
	assert.False(t, coord.dcp.collectDCP)
}

func TestDoTopologyChange_RestartsGPUWatcherAfterDCGMInitialize(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))

	coord := newTestCoordinator(t)
	gpuWatcher := &fakeGPUWatcherLifecycle{}
	gpuWatcher.running.Store(true)
	coord.setGPUWatcher(gpuWatcher)

	var initialized atomic.Bool
	coord.initializeDCGM = func(*appconfig.Config) {
		assert.False(t, gpuWatcher.running.Load(),
			"GPU watcher should still be stopped while DCGM is being initialized")
		assert.Equal(t, int32(1), gpuWatcher.stopCalls.Load())
		assert.Equal(t, int32(0), gpuWatcher.startCalls.Load())
		initialized.Store(true)
	}
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		assert.True(t, initialized.Load())
		assert.True(t, gpuWatcher.running.Load(),
			"GPU watcher should be recreated against the initialized DCGM client")
		return nil, nil, errors.New("stop before real registry rebuild")
	}

	coord.handle(context.Background(), evTopologyChanged)

	assert.Equal(t, int32(1), gpuWatcher.stopCalls.Load())
	assert.Equal(t, int32(1), gpuWatcher.startCalls.Load())
}

func TestDoTopologyChangeSuccessInstallsRegistry(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))

	coord := newTestCoordinator(t)
	oldRegistry := registry.NewRegistry()
	coord.server.SetRegistry(oldRegistry)

	gpuWatcher := &fakeGPUWatcherLifecycle{}
	gpuWatcher.running.Store(true)
	coord.setGPUWatcher(gpuWatcher)

	var initialized atomic.Bool
	coord.dcgmCleanup = func() {}
	coord.initializeDCGM = func(*appconfig.Config) {
		initialized.Store(true)
	}
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		assert.True(t, initialized.Load())
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evTopologyChanged)

	assert.Same(t, newRegistry, coord.server.GetRegistry())
	assert.Equal(t, int32(1), gpuWatcher.stopCalls.Load())
	assert.Equal(t, int32(1), gpuWatcher.startCalls.Load())
	require.NotNil(t, coord.dcp)
	assert.False(t, coord.dcp.collectDCP)
}

// TestReloadCoordinator_RepublishedDCPReplacesPreviousSnapshot proves the
// load-bearing refresh invariant for the topology-change path: once a later
// query publishes a new DCP snapshot, the next config reload must use that
// latest snapshot rather than stale capabilities from an earlier topology.
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
	assert.Equal(t, groupsA, cfgA.MetricGroups)

	// A later topology change republishes the supported groups. The next
	// config reload must see the new snapshot, not the earlier one.
	coord.queryDCPMetrics(&appconfig.Config{CollectDCP: true}, 1)

	cfgB, err := coord.buildReloadConfig()
	require.NoError(t, err)
	assert.Equal(t, groupsB, cfgB.MetricGroups)
	assert.NotEqual(t, cfgA.MetricGroups, cfgB.MetricGroups)
}

// TestReloadCoordinator_ConfigBuildFailureDoesNotPoisonNextReload verifies
// that a failed config reload clears the in-progress flag and that a later
// successful reload still reaches applyConfigReload with the published DCP
// snapshot intact.
func TestReloadCoordinator_ConfigBuildFailureDoesNotPoisonNextReload(t *testing.T) {
	coord := newReloadCoordinator(newInvalidTestCLIContext(t), func() {})
	coord.setServer(&server.MetricsServer{})

	var (
		configCalls atomic.Int32
		gotConfig   *appconfig.Config
	)
	coord.applyConfigReload = func(_ context.Context, cfg *appconfig.Config, _ uint64) {
		configCalls.Add(1)
		gotConfig = cfg
	}

	// First reload fails before a startup snapshot is available.
	coord.handle(context.Background(), evConfigChanged)
	assert.False(t, coord.server.IsReloadInProgress(),
		"failed config build must clear reload-in-progress before returning")
	assert.Equal(t, int32(0), configCalls.Load(),
		"applyConfigReload must not run when buildReloadConfig fails")

	// Second reload uses a valid startup snapshot and should succeed normally.
	cfg, err := defaultConfig()
	require.NoError(t, err)
	coord.reloadConfig = cfg.Clone()
	coord.dcp = newDCPCapabilities(&appconfig.Config{
		CollectDCP:   true,
		MetricGroups: sampleMetricGroups(),
	})

	coord.handle(context.Background(), evConfigChanged)

	assert.False(t, coord.server.IsReloadInProgress(),
		"successful retry must also clear reload-in-progress before returning")
	assert.Equal(t, int32(1), configCalls.Load(),
		"a successful retry should still reach applyConfigReload")
	require.NotNil(t, gotConfig)
	assert.True(t, gotConfig.CollectDCP)
	assert.Equal(t, sampleMetricGroups(), gotConfig.MetricGroups,
		"successful retry should still inherit the latest published DCP snapshot")
}
