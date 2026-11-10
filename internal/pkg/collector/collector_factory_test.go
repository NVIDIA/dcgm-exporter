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

package collector

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

var deviceWatcher = devicewatcher.NewDeviceWatcher()

var mockGPU = deviceinfo.GPUInfo{
	DeviceInfo: dcgm.Device{
		GPU: uint(0),
	},
	GPUInstances: []deviceinfo.GPUInstanceInfo{},
}

// cleanupRecordingCollector records cleanup order for factory rollback tests.
// It models an already-created collector whose cleanup is owned by the factory
// until the candidate registry has been built successfully.
type cleanupRecordingCollector struct {
	name  string
	order *[]string
}

// delayedRollbackCollector models a cumulative collector whose stop request
// has returned but whose native watch cleanup has not completed yet.
type delayedRollbackCollector struct {
	cleanupCalled bool
	done          chan struct{}
}

// GetMetrics satisfies Collector; this test exercises failed-build cleanup.
func (c *delayedRollbackCollector) GetMetrics() (MetricsByCounter, error) {
	return MetricsByCounter{}, nil
}

// Cleanup records the rollback stop request without closing done.
func (c *delayedRollbackCollector) Cleanup() {
	c.cleanupCalled = true
}

// CleanupComplete reports when the test releases the simulated native watch.
func (c *delayedRollbackCollector) CleanupComplete() <-chan struct{} {
	return c.done
}

// GetMetrics satisfies Collector; rollback tests never scrape this collector.
func (c cleanupRecordingCollector) GetMetrics() (MetricsByCounter, error) {
	return MetricsByCounter{}, nil
}

// Cleanup records the factory's release order for TestCollectorFactoryRollbackCollectors.
func (c cleanupRecordingCollector) Cleanup() {
	*c.order = append(*c.order, c.name)
}

// TestCollectorFactoryRollbackCollectors verifies that a failed factory build
// releases prior successful collectors in reverse construction order.
func TestCollectorFactoryRollbackCollectors(t *testing.T) {
	order := []string{}
	first := EntityCollectorTuple{}
	first.SetCollector(cleanupRecordingCollector{name: "first", order: &order})
	second := EntityCollectorTuple{}
	second.SetCollector(cleanupRecordingCollector{name: "second", order: &order})
	wantErr := errors.New("third collector failed")

	collectors, err := rollbackCollectors([]EntityCollectorTuple{first, second}, wantErr)

	require.ErrorIs(t, err, wantErr)
	require.Empty(t, collectors)
	assert.Equal(t, []string{"second", "first"}, order)
}

// TestCollectorFactoryRollbackReportsDeferredCleanup verifies that a failed
// candidate build gives the application a completion signal instead of letting
// later provider cleanup race an asynchronous collector stop.
func TestCollectorFactoryRollbackReportsDeferredCleanup(t *testing.T) {
	delayed := &delayedRollbackCollector{done: make(chan struct{})}
	tuple := EntityCollectorTuple{}
	tuple.SetCollector(delayed)
	wantErr := errors.New("later collector failed")

	collectors, err := rollbackCollectors([]EntityCollectorTuple{tuple}, wantErr)

	require.Empty(t, collectors)
	require.ErrorIs(t, err, wantErr)
	require.True(t, delayed.cleanupCalled)
	var completionErr CleanupCompletionError
	require.ErrorAs(t, err, &completionErr)
	select {
	case <-completionErr.CleanupComplete():
		t.Fatal("rollback reported completion before native watch cleanup")
	default:
	}

	close(delayed.done)
	select {
	case <-completionErr.CleanupComplete():
	case <-time.After(time.Second):
		t.Fatal("rollback completion did not close after native watch cleanup")
	}
}

// registerCollectorsCase describes one counter set, its mocked watch lists and DCGM calls, and the collectors the factory must build.
type registerCollectorsCase struct {
	name string
	cs   *counters.CounterSet
	// getDeviceWatchListManager builds the watch-list manager the factory reads entity watch lists from.
	getDeviceWatchListManager func() devicewatchlistmanager.Manager
	hostname                  string
	config                    *appconfig.Config
	// setupDCGMMock declares the DCGM calls this case expects; nil means none.
	setupDCGMMock func(*mockdcgm.MockDCGM)
	// assert checks the collectors built when the factory succeeds; nil skips the check.
	assert func(*testing.T, []EntityCollectorTuple)
	// wantError reports whether the factory must fail and return no collectors.
	wantError bool
}

// Test_collectorFactory_Register verifies enabled counters create the expected collectors and setup errors return no partial collector set.
func Test_collectorFactory_Register(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil,
		deviceWatcher, int64(1))
	labelOnlyDeviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, nil,
		[]dcgm.Short{dcgm.DCGM_FI_DRIVER_VERSION}, deviceWatcher, int64(1))

	t.Run("DCGM fields", func(t *testing.T) {
		runRegisterCollectorsCases(t, registerDCGMFieldCases(ctrl, defaultDeviceWatchList, labelOnlyDeviceWatchList))
	})
	t.Run("clock events", func(t *testing.T) {
		runRegisterCollectorsCases(t, registerClockEventsCases(ctrl, defaultDeviceWatchList))
	})
	t.Run("XID errors", func(t *testing.T) {
		runRegisterCollectorsCases(t, registerXIDErrorsCases(ctrl, defaultDeviceWatchList))
	})
	t.Run("GPU health status", func(t *testing.T) {
		runRegisterCollectorsCases(t, registerGPUHealthStatusCases(ctrl, defaultDeviceWatchList))
	})
}

// registerDCGMFieldCases defines the DCGM field counter scenarios. The mocks are
// created on ctrl, so ctrl must outlive the subtests that use them.
func registerDCGMFieldCases(
	ctrl *gomock.Controller,
	defaultDeviceWatchList, labelOnlyDeviceWatchList devicewatchlistmanager.WatchList,
) []registerCollectorsCase {
	dcgmCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
		Help:      "",
	}

	return []registerCollectorsCase{
		{
			name: fmt.Sprintf("Collector enabled for the %s", dcgm.FE_GPU.String()),
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{dcgmCounter},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(42))
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU,
					mockGPU.DeviceInfo.GPU).Return(nil).AnyTimes()

				mockFieldHandle := dcgm.FieldHandle{}
				mockFieldHandle.SetHandle(uintptr(43))
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Eq([]dcgm.Short{42})).Return(
					mockFieldHandle, nil,
				).AnyTimes()

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(
					gomock.Eq(mockFieldHandle),
					gomock.Eq(mockGroupHandle),
					gomock.Any(),
					gomock.Any(),
					gomock.Any(),
				).Return(nil).AnyTimes()
			},
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &DCGMCollector{}, entityCollectorTuples[0].Collector())
			},
		},
		{
			name: fmt.Sprintf("Collector enabled for the %s even when DCGM returns error", dcgm.FE_GPU.String()),
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{dcgmCounter},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config: &appconfig.Config{
				DisableStartupValidate: true,
			},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			wantError: false,
		},
		{
			name: "DCGM label-only counters do not create collectors",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   dcgm.DCGM_FI_DRIVER_VERSION,
						FieldName: "DCGM_FI_DRIVER_VERSION",
						PromType:  "label",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(labelOnlyDeviceWatchList,
					true).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Empty(t, entityCollectorTuples)
			},
		},
	}
}

// registerClockEventsCases defines the clock events count and total collector
// scenarios. The mocks are created on ctrl, so ctrl must outlive the subtests.
func registerClockEventsCases(
	ctrl *gomock.Controller,
	defaultDeviceWatchList devicewatchlistmanager.WatchList,
) []registerCollectorsCase {
	return []registerCollectorsCase{
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector is enabled",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{112}),
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &clockEventsCollector{}, entityCollectorTuples[0].Collector())
			},
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_TOTAL collector is enabled",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: counters.DCGMExpClockEventsTotal,
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS}, true),
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &clockEventsTotalCollector{}, entityCollectorTuples[0].Collector())
				collector := entityCollectorTuples[0].Collector()
				collector.Cleanup()
				cleanupCompleter, ok := collector.(CleanupCompleter)
				require.True(t, ok)
				select {
				case <-cleanupCompleter.CleanupComplete():
				case <-time.After(time.Second):
					t.Fatal("clock events watch cleanup did not complete")
				}
			},
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector can not be initialized",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.
					WatchList{}, false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:  "testhost",
			config:    &appconfig.Config{},
			wantError: true,
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 0)
			},
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector can not be created by DCGM",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			hostname:  "testhost",
			config:    &appconfig.Config{},
			wantError: true,
		},
	}
}

// registerXIDErrorsCases defines the XID errors count and total collector
// scenarios. The mocks are created on ctrl, so ctrl must outlive the subtests.
func registerXIDErrorsCases(
	ctrl *gomock.Controller,
	defaultDeviceWatchList devicewatchlistmanager.WatchList,
) []registerCollectorsCase {
	return []registerCollectorsCase{
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector is enabled",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{230}),
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &xidCollector{}, entityCollectorTuples[0].Collector())
			},
		},
		{
			name: "DCGM_EXP_XID_ERRORS_TOTAL collector is enabled",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: counters.DCGMExpXIDErrorsTotal,
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}, true),
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &xidTotalCollector{}, entityCollectorTuples[0].Collector())
				collector := entityCollectorTuples[0].Collector()
				collector.Cleanup()
				cleanupCompleter, ok := collector.(CleanupCompleter)
				require.True(t, ok)
				select {
				case <-cleanupCompleter.CleanupComplete():
				case <-time.After(time.Second):
					t.Fatal("XID watch cleanup did not complete")
				}
			},
		},
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector can not be initialized",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.
					WatchList{}, false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:  "testhost",
			config:    &appconfig.Config{},
			wantError: true,
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 0)
			},
		},
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector can not be created by DCGM",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			hostname:  "testhost",
			config:    &appconfig.Config{},
			wantError: true,
		},
	}
}

// registerGPUHealthStatusCases defines the GPU health status collector
// scenarios, including each setup step that can fail. The mocks are created on
// ctrl, so ctrl must outlive the subtests.
func registerGPUHealthStatusCases(
	ctrl *gomock.Controller,
	defaultDeviceWatchList devicewatchlistmanager.WatchList,
) []registerCollectorsCase {
	return []registerCollectorsCase{
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector is enabled",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil).AnyTimes()
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(1), nil).AnyTimes()
				mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{}, nil).AnyTimes()
				mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil).AnyTimes()
				mockDCGM.EXPECT().AddLinkEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockDCGM.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(dcgm.FieldHandle{}, nil).AnyTimes()
				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil).AnyTimes()
				setupDCGMMockForDCGMExpMetrics([]dcgm.Short{230})(mockDCGM)
			},
			assert: func(t *testing.T, entityCollectorTuples []EntityCollectorTuple) {
				require.Len(t, entityCollectorTuples, 1)
				require.Equal(t, entityCollectorTuples[0].Entity(), dcgm.FE_GPU)
				require.IsType(t, &gpuHealthStatusCollector{}, entityCollectorTuples[0].Collector())
			},
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{}, errors.New("boom!")).AnyTimes()
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when zero supported devices",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{}, nil)
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when entity group can not be created",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu_health_monitor_")
				})).Return(dcgm.GroupHandle{}, errors.New("boom!"))
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when entity can not be added to the group",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu_health_monitor_")
				})).Return(dcgm.GroupHandle{}, nil)
				mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(errors.New("boom!"))
				mockDCGM.EXPECT().DestroyGroup(dcgm.GroupHandle{}).Return(nil)
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when enable healthcheck returns an error",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu_health_monitor_")
				})).Return(dcgm.GroupHandle{}, nil)
				mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(nil)
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL&^dcgm.DCGM_HEALTH_WATCH_IMEX)).Return(errors.New("boom!"))
				mockDCGM.EXPECT().DestroyGroup(dcgm.GroupHandle{}).Return(nil)
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when deviceinfo.Initialize returns an error",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu_health_monitor_")
				})).Return(dcgm.GroupHandle{}, nil)
				mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(nil)
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL&^dcgm.DCGM_HEALTH_WATCH_IMEX)).Return(nil)
				mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("boom!"))
				mockDCGM.EXPECT().DestroyGroup(dcgm.GroupHandle{}).Return(nil)
			},
			wantError: true,
		},
		{
			name: "DCGM_EXP_GPU_HEALTH_STATUS collector can not be initialized when device watch returns an error",
			cs: &counters.CounterSet{
				DCGMCounters: []counters.Counter{},
				ExporterCounters: []counters.Counter{
					{
						FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockDCGM.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu_health_monitor_")
				})).Return(dcgm.GroupHandle{}, nil)
				mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(nil)
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL&^dcgm.DCGM_HEALTH_WATCH_IMEX)).Return(nil)
				mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{}, nil)
				mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
				mockDCGM.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu-collector-group")
				})).Return(dcgm.GroupHandle{}, errors.New("boom!"))
				mockDCGM.EXPECT().DestroyGroup(dcgm.GroupHandle{}).Return(nil)
			},
			wantError: true,
		},
	}
}

// runRegisterCollectorsCases runs each case as a subtest against a fresh DCGM
// mock and restores the real DCGM client afterwards.
func runRegisterCollectorsCases(t *testing.T, tests []registerCollectorsCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
			mockDCGMProvider.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()

			realDCGM := dcgmprovider.Client()
			defer func() {
				dcgmprovider.SetClient(realDCGM)
			}()

			dcgmprovider.SetClient(mockDCGMProvider)
			if tt.setupDCGMMock != nil {
				tt.setupDCGMMock(mockDCGMProvider)
			}

			entityCollectors, err := InitCollectorFactory(tt.cs, tt.getDeviceWatchListManager(), tt.hostname,
				tt.config).NewCollectors()
			if tt.wantError {
				require.Error(t, err)
				require.Empty(t, entityCollectors)
				return
			}

			require.NoError(t, err)
			if tt.assert != nil {
				tt.assert(t, entityCollectors)
			}
		})
	}
}

// TestCollectorFactoryExplainsRefusedFieldsForEveryEntity checks that when the
// hostengine refuses the GPU watch, the one error names the refused fields of
// the GPU and link watch lists and lists a field shared by both once. The
// factory returns before the CPU collector installs a watch: the mock has no
// WatchFieldsWithGroupExNoRefresh expectation, so any watch fails the test.
func TestCollectorFactoryExplainsRefusedFieldsForEveryEntity(t *testing.T) {
	const (
		gpuRefusedField  = dcgm.Short(1001)
		linkRefusedField = dcgm.Short(1002)
		acceptedField    = dcgm.Short(1003)
		cpuField         = dcgm.Short(1004)
		badParamText     = "error creating DCGM fields group: Bad parameter passed to function"
	)
	refusedFields := map[dcgm.Short]string{gpuRefusedField: "gpu_refused_field", linkRefusedField: "link_refused_field"}

	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(client)

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(42))
	client.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	client.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil).Times(1)
	client.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil).AnyTimes()
	client.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
			for _, field := range fields {
				if _, refused := refusedFields[field]; refused {
					return dcgm.FieldHandle{}, errors.New(badParamText)
				}
			}
			return dcgm.FieldHandle{}, nil
		}).AnyTimes()
	client.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(field dcgm.Short) (dcgm.FieldMeta, error) {
		return dcgm.FieldMeta{Tag: refusedFields[field]}, nil
	}).AnyTimes()
	// No WatchFieldsWithGroupExNoRefresh expectation: any watch installed fails the test.

	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	watcher := devicewatcher.NewDeviceWatcherWithClient(client)
	gpuWatchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo, []dcgm.Short{gpuRefusedField, acceptedField}, nil, watcher, 1)
	linkWatchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo, []dcgm.Short{linkRefusedField, gpuRefusedField, acceptedField}, nil, watcher, 1)
	cpuWatchList := *devicewatchlistmanager.NewWatchList(deviceInfo, []dcgm.Short{cpuField}, nil, watcher, 1)

	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	manager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(gpuWatchList, true).AnyTimes()
	manager.EXPECT().EntityWatchList(dcgm.FE_LINK).Return(linkWatchList, true).AnyTimes()
	manager.EXPECT().EntityWatchList(dcgm.FE_CPU).Return(cpuWatchList, true).AnyTimes()
	manager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{}, false).AnyTimes()

	counterSet := &counters.CounterSet{DCGMCounters: []counters.Counter{
		{FieldID: gpuRefusedField, FieldName: "GPU_REFUSED", PromType: "gauge"},
	}}
	collectors, err := InitCollectorFactory(counterSet, manager, "testhost", &appconfig.Config{}).NewCollectors()

	require.Error(t, err)
	require.Empty(t, collectors)
	assert.ErrorContains(t, err, "the DCGM hostengine refused these fields")
	assert.ErrorContains(t, err, "Run the DCGM version this exporter supports")
	assert.ErrorContains(t, err, "gpu_refused_field (field ID 1001)")
	assert.ErrorContains(t, err, "link_refused_field (field ID 1002)")
	assert.ErrorContains(t, err, badParamText)
	assert.ErrorContains(t, err, `watch group "GPU default"`)
	assert.ErrorContains(t, err, `watch group "NvLink default"`)
	assert.Equal(t, 1, strings.Count(err.Error(), "gpu_refused_field"), "a shared field must be listed once")
	assert.Equal(t, 1, strings.Count(err.Error(), "refused these fields"), "the message must appear once")
	assert.True(t, strings.HasPrefix(err.Error(), "build DCGM collectors: the DCGM hostengine refused these fields"),
		"a message that covers every entity must not name one entity type: %v", err)

	// With startup validation off, every entity collector fails, but the explanation is logged once.
	client.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil).AnyTimes()
	bestEffort := mockdevicewatchlistmanager.NewMockManager(ctrl)
	bestEffort.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(gpuWatchList, true).AnyTimes()
	bestEffort.EXPECT().EntityWatchList(dcgm.FE_LINK).Return(linkWatchList, true).AnyTimes()
	bestEffort.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{}, false).AnyTimes()
	logs := captureSlogOutput(t)
	collectors, err = InitCollectorFactory(counterSet, bestEffort, "testhost",
		&appconfig.Config{DisableStartupValidate: true}).NewCollectors()

	require.NoError(t, err)
	assert.Empty(t, collectors)
	assert.Equal(t, 1, strings.Count(logs.String(), "refused these fields"), "the explanation must be logged once")
	assert.Contains(t, logs.String(), "entity type 'NvLink' cannot be initialized; see the refused-fields error above")
}

// TestCollectorFactoryKeepsEachEntityExplanationWhenWideningFails checks that
// when the start-up explanation cannot be widened to every watch list, it is not
// stored, so a later entity failure still logs its own refused fields.
func TestCollectorFactoryKeepsEachEntityExplanationWhenWideningFails(t *testing.T) {
	const (
		gpuRefusedField  = dcgm.Short(1001)
		linkRefusedField = dcgm.Short(1002)
		probeFails       = dcgm.Short(1004)
		badParamText     = "error creating DCGM fields group: Bad parameter passed to function"
	)
	refusedFields := map[dcgm.Short]string{gpuRefusedField: "gpu_refused_field", linkRefusedField: "link_refused_field"}

	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(client)

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(42))
	client.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	client.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil).AnyTimes()
	client.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil).AnyTimes()
	client.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
			for _, field := range fields {
				if _, refused := refusedFields[field]; refused {
					return dcgm.FieldHandle{}, errors.New(badParamText)
				}
				if field == probeFails {
					return dcgm.FieldHandle{}, &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}
				}
			}
			return dcgm.FieldHandle{}, nil
		}).AnyTimes()
	client.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(field dcgm.Short) (dcgm.FieldMeta, error) {
		return dcgm.FieldMeta{Tag: refusedFields[field]}, nil
	}).AnyTimes()

	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	watcher := devicewatcher.NewDeviceWatcherWithClient(client)
	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	manager.EXPECT().EntityWatchList(dcgm.FE_GPU).
		Return(*devicewatchlistmanager.NewWatchList(deviceInfo, []dcgm.Short{gpuRefusedField}, nil, watcher, 1), true).AnyTimes()
	manager.EXPECT().EntityWatchList(dcgm.FE_LINK).
		Return(*devicewatchlistmanager.NewWatchList(deviceInfo, []dcgm.Short{linkRefusedField}, nil, watcher, 1), true).AnyTimes()
	// The CPU list's probe fails for another reason, so the widened explanation cannot be built.
	manager.EXPECT().EntityWatchList(dcgm.FE_CPU).
		Return(*devicewatchlistmanager.NewWatchList(deviceInfo, []dcgm.Short{probeFails}, nil, watcher, 1), true).AnyTimes()
	manager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{}, false).AnyTimes()

	counterSet := &counters.CounterSet{DCGMCounters: []counters.Counter{
		{FieldID: gpuRefusedField, FieldName: "GPU_REFUSED", PromType: "gauge"},
	}}
	logs := captureSlogOutput(t)
	collectors, err := InitCollectorFactory(counterSet, manager, "testhost",
		&appconfig.Config{DisableStartupValidate: true}).NewCollectors()

	require.NoError(t, err)
	assert.Empty(t, collectors)
	assert.Contains(t, logs.String(), "gpu_refused_field (field ID 1001)")
	assert.Contains(t, logs.String(), "entity type 'GPU' cannot be initialized; err: the DCGM hostengine refused these fields")
	assert.Contains(t, logs.String(), "entity type 'NvLink' cannot be initialized; err: the DCGM hostengine refused these fields")
	assert.Contains(t, logs.String(), "link_refused_field (field ID 1002)", "the NvLink failure must log its own fields")
	assert.NotContains(t, logs.String(), "see the refused-fields error above")
	assert.NotContains(t, logs.String(), "DCGM collectors cannot be initialized")
}

// recordingWatchListManager records entity watch-list lookups made by a collector factory.
type recordingWatchListManager struct {
	requested []dcgm.Field_Entity_Group
}

// CreateEntityWatchList satisfies Manager; these tests inspect lookups rather than constructing lists.
func (r *recordingWatchListManager) CreateEntityWatchList(
	dcgm.Field_Entity_Group, devicewatcher.WatchInstaller, int64,
) error {
	return nil
}

// EntityWatchList records the requested scope and reports that no list is configured.
func (r *recordingWatchListManager) EntityWatchList(entityType dcgm.Field_Entity_Group) (
	devicewatchlistmanager.WatchList, bool,
) {
	r.requested = append(r.requested, entityType)
	return devicewatchlistmanager.WatchList{}, false
}

// TestOTLPCollectorSelection checks that only selected metric sources create native collectors.
func TestOTLPCollectorSelection(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		metricSources bool
		wantCount     int
		watchErr      error
		skipValidate  bool
	}{
		{name: "OTLP only", enabled: true, metricSources: true, wantCount: 1},
		{name: "failed OTLP watch rejects candidate", enabled: true, metricSources: true, wantCount: 1, watchErr: assert.AnError},
		{name: "failed OTLP watch still rejects without validation", enabled: true, metricSources: true, wantCount: 1, watchErr: assert.AnError, skipValidate: true},
		{name: "OTLP without watch collections", enabled: true},
		{name: "disabled", metricSources: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(native)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			native.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
			native.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			native.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{GPU: 0, UUID: "GPU-0"}, nil)
			native.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			native.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			native.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil).AnyTimes()
			config := &appconfig.Config{EnableOTLP: tt.enabled, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, WatchRetention: appconfig.DefaultWatchRetention()}
			config.DisableStartupValidate = tt.skipValidate
			if tt.metricSources {
				sources, err := signalcatalog.WatchSourcesFor("gpu.power.usage", signalcatalog.ProfileNVIDIA)
				require.NoError(t, err)
				config.OTLPWatchCollections = []appconfig.OTLPWatchCollection{{Name: "otel", Sources: sources, Interval: 5000}}
			}
			manager := devicewatchlistmanager.NewWatchListManager(nil, config)
			require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_GPU, devicewatcher.NewDeviceWatcherWithClient(native), 30000))
			group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
			group.SetHandle(1)
			fieldGroup.SetHandle(2)
			native.EXPECT().CreateGroup(gomock.Any()).Return(group, nil).Times(tt.wantCount)
			native.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil).Times(tt.wantCount)
			native.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{
				dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS,
				dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN,
				dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH,
				dcgm.DCGM_FI_DEV_PCIE_LINK_GEN,
				dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH,
			}).Return(fieldGroup, nil).Times(tt.wantCount)
			native.EXPECT().WatchFieldsWithGroupExNoRefresh(fieldGroup, group, int64(5000000), float64(600), int32(0)).Return(tt.watchErr).Times(tt.wantCount)
			watched := tt.wantCount
			if tt.watchErr != nil {
				watched = 0
			}
			native.EXPECT().UnwatchFields(fieldGroup, group).Return(nil).Times(watched)
			native.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil).Times(tt.wantCount)
			native.EXPECT().DestroyGroup(group).Return(nil).Times(tt.wantCount)
			got, err := InitCollectorFactory(&counters.CounterSet{}, manager, "host", config).NewCollectors()
			if tt.watchErr != nil {
				require.ErrorIs(t, err, tt.watchErr)
				require.Empty(t, got)
				return
			}

			require.NoError(t, err)
			require.Len(t, got, tt.wantCount)
			for _, entity := range got {
				entity.Collector().Cleanup()
			}
		})
	}
}

// TestCollectorFactoryDoesNotRequestGPUCIWatchLists checks that a plain GPU field builds no collectors here and
// never asks the watch list manager for a compute instance watch list.
func TestCollectorFactoryDoesNotRequestGPUCIWatchLists(t *testing.T) {
	manager := &recordingWatchListManager{}
	counterSet := &counters.CounterSet{
		DCGMCounters: []counters.Counter{
			{
				FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
				FieldName: "DCGM_FI_DEV_GPU_TEMP",
				PromType:  "gauge",
			},
		},
	}

	got, err := InitCollectorFactory(counterSet, manager, "testhost", &appconfig.Config{}).NewCollectors()

	require.NoError(t, err)
	require.Empty(t, got)
	require.NotContains(t, manager.requested, dcgm.FE_GPU_CI)
}

// TestCollectorFactoryAddsFieldPolicyCollectorWithoutADeviceWatchList checks that the field policy metrics alone
// produce one field policy collector with no entity type, and no watch list is requested from the manager.
func TestCollectorFactoryAddsFieldPolicyCollectorWithoutADeviceWatchList(t *testing.T) {
	manager := &recordingWatchListManager{}
	got, err := InitCollectorFactory(
		&counters.CounterSet{ExporterCounters: fieldPolicyCounters()}, manager, "testhost", &appconfig.Config{},
	).NewCollectors()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, dcgm.FE_NONE, got[0].Entity())
	require.IsType(t, &fieldPolicyCollector{}, got[0].Collector())
	require.Empty(t, manager.requested)
}

// setupDCGMMockForDCGMExpMetrics builds watch expectations for exporter-backed field tests.
func setupDCGMMockForDCGMExpMetrics(fields []dcgm.Short, expectCleanup ...bool) func(mockDCGM *mockdcgm.MockDCGM) {
	return func(mockDCGM *mockdcgm.MockDCGM) {
		mockGroupHandle := dcgm.GroupHandle{}
		mockGroupHandle.SetHandle(uintptr(42))
		mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()
		mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU,
			mockGPU.DeviceInfo.GPU).Return(nil).AnyTimes()

		mockFieldHandle := dcgm.FieldHandle{}
		mockFieldHandle.SetHandle(uintptr(43))
		mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Eq(fields)).Return(
			mockFieldHandle, nil,
		).AnyTimes()

		mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(
			gomock.Eq(mockFieldHandle),
			gomock.Eq(mockGroupHandle),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).Return(nil).AnyTimes()
		if len(expectCleanup) > 0 && expectCleanup[0] {
			mockDCGM.EXPECT().UnwatchFields(mockFieldHandle, mockGroupHandle).Return(nil).Times(1)
			mockDCGM.EXPECT().FieldGroupDestroy(mockFieldHandle).Return(nil).Times(1)
			mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil).Times(1)
			return
		}
		mockDCGM.EXPECT().UnwatchFields(mockFieldHandle, mockGroupHandle).Return(nil).AnyTimes()
		mockDCGM.EXPECT().FieldGroupDestroy(mockFieldHandle).Return(nil).AnyTimes()
		mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil).AnyTimes()
	}
}

// TestCollectorFactorySystemHealthStatus checks that the factory adds the
// system health collector as an FE_NONE collector without asking for a device
// watch list, and that every construction failure stops the build.
func TestCollectorFactorySystemHealthStatus(t *testing.T) {
	tests := []struct {
		name           string
		counterSet     *counters.CounterSet
		setUp          func(*mockdcgm.MockDCGM)
		wantCollectors int
		wantError      bool
		wantErrorIs    error
		cleanup        bool
	}{
		{
			name:       "metric absent",
			counterSet: &counters.CounterSet{},
		},
		{
			name: "supported runtime registers FE_NONE collector without watch list",
			counterSet: &counters.CounterSet{
				ExporterCounters: systemHealthTestCounterList(),
			},
			setUp: func(client *mockdcgm.MockDCGM) {
				groupID := systemHealthTestGroupHandle()
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				expectSystemHealthGroupInitialization(client, groupID)
				client.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{}, nil)
				client.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)
			},
			wantCollectors: 1,
			cleanup:        true,
		},
		{
			name: "unsupported runtime is fatal",
			counterSet: &counters.CounterSet{
				ExporterCounters: systemHealthTestCounterList(),
			},
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_PCIE, nil,
				)
			},
			wantError:   true,
			wantErrorIs: errIMEXHealthUnsupported,
		},
		{
			name: "unavailable version evidence is fatal",
			counterSet: &counters.CounterSet{
				ExporterCounters: systemHealthTestCounterList(),
			},
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.HealthSystem(0), errors.New("capability query failed"),
				)
			},
			wantError: true,
		},
		{
			name: "supported runtime group setup failure remains fatal",
			counterSet: &counters.CounterSet{
				ExporterCounters: systemHealthTestCounterList(),
			},
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				client.EXPECT().GetSupportedDevices().Return(nil, errors.New("group setup failed"))
			},
			wantError: true,
		},
		{
			name: "supported runtime priming failure remains fatal",
			counterSet: &counters.CounterSet{
				ExporterCounters: systemHealthTestCounterList(),
			},
			setUp: func(client *mockdcgm.MockDCGM) {
				groupID := systemHealthTestGroupHandle()
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				expectSystemHealthGroupInitialization(client, groupID)
				client.EXPECT().HealthCheck(groupID).Return(
					dcgm.HealthResponse{}, errors.New("prime failed"),
				)
				client.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			originalClient := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(originalClient) })
			dcgmprovider.SetClient(client)

			manager := &recordingWatchListManager{}
			if tt.setUp != nil {
				tt.setUp(client)
			}

			createCollectors := func() ([]EntityCollectorTuple, error) {
				return InitCollectorFactory(
					tt.counterSet,
					manager,
					"test-host",
					&appconfig.Config{},
				).NewCollectors()
			}

			got, err := createCollectors()
			if tt.wantError {
				require.Error(t, err)
				if tt.wantErrorIs != nil {
					require.ErrorIs(t, err, tt.wantErrorIs)
				}
				require.Empty(t, manager.requested)
				return
			}
			require.NoError(t, err)
			require.Empty(t, manager.requested)
			checkSystemHealthCollectors(t, got, tt.wantCollectors, tt.cleanup)
		})
	}
}

// checkSystemHealthCollectors checks that the factory built want collectors and
// that a single one is the FE_NONE system health collector. With cleanup set it
// cleans that collector up, so the mock's DestroyGroup expectation is met.
func checkSystemHealthCollectors(t *testing.T, got []EntityCollectorTuple, want int, cleanup bool) {
	t.Helper()
	require.Len(t, got, want)

	if want == 1 {
		require.Equal(t, dcgm.FE_NONE, got[0].Entity())
		require.IsType(t, &systemHealthStatusCollector{}, got[0].Collector())
	}

	if cleanup {
		got[0].Collector().Cleanup()
	}
}

// TestCollectorFactorySystemHealthStatusUsesFactoryClient checks that the
// system health collector is built, scraped and cleaned up on the factory's
// DCGM client, not on the process client. The process client is a strict mock
// with no expectations, so any call to it fails the test.
func TestCollectorFactorySystemHealthStatusUsesFactoryClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	ownClient := mockdcgm.NewMockDCGM(ctrl)
	processClient := mockdcgm.NewMockDCGM(ctrl)
	previous := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(previous) })
	dcgmprovider.SetClient(processClient)

	groupID := systemHealthTestGroupHandle()
	ownClient.EXPECT().HealthGetSupportedSystems().Return(dcgm.DCGM_HEALTH_WATCH_IMEX, nil)
	expectSystemHealthGroupInitialization(ownClient, groupID)
	ownClient.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{}, nil).Times(2)
	ownClient.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)

	got, err := InitCollectorFactoryWithClient(
		ownClient,
		&counters.CounterSet{ExporterCounters: systemHealthTestCounterList()},
		&recordingWatchListManager{},
		"test-host",
		&appconfig.Config{},
	).NewCollectors()
	require.NoError(t, err)
	require.Len(t, got, 1)

	metrics, err := got[0].Collector().GetMetrics()
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	got[0].Collector().Cleanup()
}

// TestCollectorFactorySystemHealthRollback checks that a system health
// construction failure after the GPU health collector was built destroys every
// health group already created, each exactly once.
func TestCollectorFactorySystemHealthRollback(t *testing.T) {
	gpuGroup := dcgm.GroupHandle{}
	gpuGroup.SetHandle(uintptr(7))
	systemGroup := systemHealthTestGroupHandle()

	tests := []struct {
		name  string
		setUp func(*mockdcgm.MockDCGM)
	}{
		{
			name: "unsupported hostengine destroys the GPU health group",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(dcgm.DCGM_HEALTH_WATCH_PCIE, nil)
				client.EXPECT().DestroyGroup(gpuGroup).Return(nil).Times(1)
			},
		},
		{
			name: "priming failure destroys the system and GPU health groups",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(dcgm.DCGM_HEALTH_WATCH_IMEX, nil)
				expectSystemHealthGroupInitialization(client, systemGroup)
				client.EXPECT().HealthCheck(systemGroup).Return(dcgm.HealthResponse{}, errors.New("prime failed"))
				client.EXPECT().DestroyGroup(systemGroup).Return(nil).Times(1)
				client.EXPECT().DestroyGroup(gpuGroup).Return(nil).Times(1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			previous := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			dcgmprovider.SetClient(client)

			expectGPUHealthCollectorBuild(client, gpuGroup)
			tt.setUp(client)

			exporterCounters := append(
				counters.CounterList{{FieldName: counters.DCGMExpGPUHealthStatus}},
				systemHealthTestCounterList()...,
			)
			got, err := InitCollectorFactory(
				&counters.CounterSet{ExporterCounters: exporterCounters},
				singleGPUWatchListManager(ctrl),
				"test-host",
				&appconfig.Config{},
			).NewCollectors()

			require.Error(t, err)
			require.Empty(t, got)
		})
	}
}

// expectGPUHealthCollectorBuild expects a successful GPU health collector
// build on client for one GPU, returning gpuGroup as its health group. It
// does not expect the group to be destroyed.
func expectGPUHealthCollectorBuild(client *mockdcgm.MockDCGM, gpuGroup dcgm.GroupHandle) {
	client.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
	client.EXPECT().CreateGroup(gomock.Cond(func(value any) bool {
		name, ok := value.(string)
		return ok && strings.HasPrefix(name, "gpu_health_monitor_")
	})).Return(gpuGroup, nil)
	client.EXPECT().AddEntityToGroup(gpuGroup, dcgm.FE_GPU, uint(0)).Return(nil)
	client.EXPECT().HealthSet(gpuGroup, dcgm.DCGM_HEALTH_WATCH_ALL&^dcgm.DCGM_HEALTH_WATCH_IMEX).Return(nil)
	client.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	client.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{}, nil)
	client.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
	client.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()
}

// singleGPUWatchListManager returns a manager whose GPU watch list monitors
// one GPU and watches no fields, so exporter families build without watches.
func singleGPUWatchListManager(ctrl *gomock.Controller) devicewatchlistmanager.Manager {
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	watchList := *devicewatchlistmanager.NewWatchList(deviceInfo, nil, nil, deviceWatcher, int64(1))
	manager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(watchList, true).AnyTimes()

	return manager
}
