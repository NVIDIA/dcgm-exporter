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

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	osmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/os"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

var deviceWatcher = devicewatcher.NewDeviceWatcher()

var mockGPU = deviceinfo.GPUInfo{
	DeviceInfo: dcgm.Device{
		GPU: uint(0),
	},
	GPUInstances: []deviceinfo.GPUInstanceInfo{},
}

func Test_collectorFactory_Register(t *testing.T) {
	dcgmCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
		Help:      "",
	}

	ctrl := gomock.NewController(t)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil,
		deviceWatcher, int64(1))

	tests := []struct {
		name                      string
		cs                        *counters.CounterSet
		getDeviceWatchListManager func() devicewatchlistmanager.Manager
		hostname                  string
		config                    *appconfig.Config
		setupDCGMMock             func(*mockdcgm.MockDCGM)
		assert                    func(*testing.T, []EntityCollectorTuple)
		wantsPanic                bool
	}{
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
					mockFieldHandle, nil).AnyTimes()

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(gomock.Eq(mockFieldHandle),
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
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *mockdcgm.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			wantsPanic: false,
		},
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
					true).AnyTimes()
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
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
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
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
		},
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
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
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
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
		},
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
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(dcgm.FieldHandle{}, nil)
				mockDCGM.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
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
			wantsPanic: true,
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
			wantsPanic: true,
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
			wantsPanic: true,
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
			},
			wantsPanic: true,
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
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL)).Return(errors.New("boom!"))
			},
			wantsPanic: true,
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
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL)).Return(nil)
				mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("boom!"))
			},
			wantsPanic: true,
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
				mockDCGM.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL)).Return(nil)
				mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{}, nil)
				mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
					return strings.HasPrefix(x.(string), "gpu-collector-group")
				})).Return(dcgm.GroupHandle{}, errors.New("boom!"))
			},
			wantsPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

			realDCGM := dcgmprovider.Client()
			defer func() {
				dcgmprovider.SetClient(realDCGM)
			}()

			mOS := osmock.NewMockOS(ctrl)
			mOS.EXPECT().Exit(gomock.Eq(1)).Do(func(code int) {
				panic("os.Exit")
			}).AnyTimes()
			os = mOS
			defer func() {
				os = osinterface.RealOS{}
			}()

			dcgmprovider.SetClient(mockDCGMProvider)
			if tt.setupDCGMMock != nil {
				tt.setupDCGMMock(mockDCGMProvider)
			}

			if tt.wantsPanic {
				require.PanicsWithValue(t, "os.Exit", func() {
					InitCollectorFactory(tt.cs, tt.getDeviceWatchListManager(), tt.hostname,
						tt.config).NewCollectors()
				})
				return
			}
			entityCollectors := InitCollectorFactory(tt.cs, tt.getDeviceWatchListManager(), tt.hostname,
				tt.config).NewCollectors()
			if tt.assert != nil {
				tt.assert(t, entityCollectors)
			}
		})
	}
}

func setupDCGMMockForDCGMExpMetrics(fields []dcgm.Short) func(mockDCGM *mockdcgm.MockDCGM) {
	return func(mockDCGM *mockdcgm.MockDCGM) {
		mockGroupHandle := dcgm.GroupHandle{}
		mockGroupHandle.SetHandle(uintptr(42))
		mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()
		mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU,
			mockGPU.DeviceInfo.GPU).Return(nil).AnyTimes()

		mockFieldHandle := dcgm.FieldHandle{}
		mockFieldHandle.SetHandle(uintptr(43))
		mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Eq(fields)).Return(
			mockFieldHandle, nil).AnyTimes()

		mockDCGM.EXPECT().WatchFieldsWithGroupEx(gomock.Eq(mockFieldHandle),
			gomock.Eq(mockGroupHandle),
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).Return(nil).AnyTimes()
	}
}
