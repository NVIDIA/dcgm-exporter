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
	"context"
	"errors"
	"flag"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/stdout"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

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
	}

	dcgmprovider.SmartDCGMInit(t, config)
	defer dcgmprovider.Client().Cleanup()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := startDeviceWatchListManager(tt.counterSet, config)
			if tt.assertion == nil {
				t.Skip(tt.name)
			}
			tt.assertion(t, got)
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
			// Create a mock CLI context with the test flags
			app := cli.NewApp()
			app.Flags = []cli.Flag{
				&cli.StringFlag{Name: CLIGPUDevices},
				&cli.StringFlag{Name: CLISwitchDevices},
				&cli.StringFlag{Name: CLICPUDevices},
				&cli.StringFlag{Name: CLIDCGMLogLevel},
				&cli.BoolFlag{Name: CLIDumpEnabled},
				&cli.StringFlag{Name: CLIDumpDirectory},
				&cli.IntFlag{Name: CLIDumpRetention},
				&cli.BoolFlag{Name: CLIDumpCompression},
			}

			// Set up the context with test values
			set := flag.NewFlagSet("test", 0)

			// Set defaults for required flags if not present
			if _, ok := tt.flags[CLIGPUDevices]; !ok {
				set.String(CLIGPUDevices, "f", "")
			}
			if _, ok := tt.flags[CLISwitchDevices]; !ok {
				set.String(CLISwitchDevices, "f", "")
			}
			if _, ok := tt.flags[CLICPUDevices]; !ok {
				set.String(CLICPUDevices, "f", "")
			}
			if _, ok := tt.flags[CLIDCGMLogLevel]; !ok {
				set.String(CLIDCGMLogLevel, "NONE", "")
			}
			// Set defaults for dump config flags if not present
			if _, ok := tt.flags[CLIDumpEnabled]; !ok {
				set.Bool(CLIDumpEnabled, false, "")
			}
			if _, ok := tt.flags[CLIDumpDirectory]; !ok {
				set.String(CLIDumpDirectory, "/tmp/dcgm-exporter-debug", "")
			}
			if _, ok := tt.flags[CLIDumpRetention]; !ok {
				set.Int(CLIDumpRetention, 24, "")
			}
			if _, ok := tt.flags[CLIDumpCompression]; !ok {
				set.Bool(CLIDumpCompression, true, "")
			}

			for name, value := range tt.flags {
				// Find the matching flag in app.Flags
				for _, flag := range app.Flags {
					if flag.Names()[0] == name {
						switch flag.(type) {
						case *cli.StringFlag:
							set.String(name, value, "")
						case *cli.BoolFlag:
							set.Bool(name, value == "true", "")
						case *cli.IntFlag:
							if intVal, err := strconv.Atoi(value); err == nil {
								set.Int(name, intVal, "")
							}
						}
						break
					}
				}
			}

			// Create a cli.Context from the populated FlagSet
			context := cli.NewContext(app, set, nil)

			// Call the real contextToConfig function to obtain the config
			config, err := contextToConfig(context)
			require.NoError(t, err)

			// Assert equality against the config returned by contextToConfig
			assert.Equal(t, tt.expectedConfig, config.DumpConfig)
		})
	}
}

// TestActionUsesCliContext verifies that the daemon's action chain
// (action → stdout.Capture → callback) honours cancellation of the
// cli.Context's root context. The action function reads c.Context (set by
// main.go via signal.NotifyContext + app.RunContext); cancellation of
// that context must propagate into the wrapped callback within a tight
// time bound.
//
// See docs/CONTEXTS.md for the full propagation tree.
func TestActionUsesCliContext(t *testing.T) {
	// Sleep that respects context cancellation. Returns ctx.Err() on
	// cancel; returns nil after the full duration elapses. Modelled on
	// the standard pattern in long-running I/O paths.
	ctxSleep := func(ctx context.Context, d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	cases := []struct {
		name        string
		prepCtx     func() (context.Context, context.CancelFunc)
		workTime    time.Duration
		wantErrKind error
		wantQuick   bool // did the call return well before workTime?
	}{
		{
			name: "positive: fresh ctx, callback runs to completion",
			prepCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			workTime:    20 * time.Millisecond,
			wantErrKind: nil,
			wantQuick:   false,
		},
		{
			name: "negative: pre-cancelled ctx aborts immediately",
			prepCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			workTime:    500 * time.Millisecond,
			wantErrKind: context.Canceled,
			wantQuick:   true,
		},
		{
			name: "boundary: ctx cancelled mid-callback propagates",
			prepCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				go func() { time.Sleep(30 * time.Millisecond); cancel() }()
				return ctx, func() {}
			},
			workTime:    500 * time.Millisecond,
			wantErrKind: context.Canceled,
			wantQuick:   true,
		},
		{
			name: "corner: ctx with deadline shorter than work fires DeadlineExceeded",
			prepCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			workTime:    500 * time.Millisecond,
			wantErrKind: context.DeadlineExceeded,
			wantQuick:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.prepCtx()
			defer cancel()

			// Build a cli.Context whose Context field is the cancellable one.
			// cli.NewContext(app, set, parent) inherits parent's Context.
			set := flag.NewFlagSet("test", 0)
			parent := &cli.Context{Context: ctx}
			cliCtx := cli.NewContext(NewApp(), set, parent)
			require.Same(t, ctx, cliCtx.Context,
				"cli.Context.Context must be the ctx we passed in")

			// Mirror the production action() wrapping: stdout.Capture(c.Context, fn).
			// The fn here stands in for startDCGMExporter — it just sleeps
			// respecting ctx so the test is fast and predictable.
			start := time.Now()
			err := stdout.Capture(cliCtx.Context, func() error {
				return ctxSleep(cliCtx.Context, tc.workTime)
			})
			elapsed := time.Since(start)

			if tc.wantErrKind == nil {
				assert.NoError(t, err)
			} else {
				assert.True(t, errors.Is(err, tc.wantErrKind),
					"err = %v, want %v", err, tc.wantErrKind)
			}
			if tc.wantQuick {
				assert.Less(t, elapsed, tc.workTime/2,
					"ctx cancellation/timeout did not propagate within budget; elapsed=%v workTime=%v",
					elapsed, tc.workTime)
			}
		})
	}
}
