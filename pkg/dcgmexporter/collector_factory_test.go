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

package dcgmexporter

import (
	"errors"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	dcgmmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

var mockGPU = deviceinfo.GPUInfo{
	DeviceInfo: dcgm.Device{
		GPU: uint(0),
	},
	GPUInstances: []deviceinfo.GPUInstanceInfo{},
}

func Test_collectorFactory_Register(t *testing.T) {
	dcgmCounter := appconfig.Counter{
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

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil, deviceWatcher,
		int64(1))

	tests := []struct {
		name                      string
		cs                        *CounterSet
		getDeviceWatchListManager func() devicewatchlistmanager.Manager
		hostname                  string
		config                    *appconfig.Config
		setupDCGMMock             func(*dcgmmock.MockDCGM)
		assert                    func(*testing.T, *Registry)
		wantsPanic                bool
	}{
		{
			name: fmt.Sprintf("Collector enabled for the %s", dcgm.FE_GPU.String()),
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{dcgmCounter},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *dcgmmock.MockDCGM) {
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
			assert: func(t *testing.T, registry *Registry) {
				require.Len(t, registry.collectorGroups, 1)
				require.Contains(t, registry.collectorGroups, dcgm.FE_GPU)
				require.Len(t, registry.collectorGroups[dcgm.FE_GPU], 1)
				require.IsType(t, &DCGMCollector{}, registry.collectorGroups[dcgm.FE_GPU][0])
			},
		},
		{
			name: fmt.Sprintf("Collector enabled for the %s but DCGM returns error", dcgm.FE_GPU.String()),
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{dcgmCounter},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *dcgmmock.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			wantsPanic: true,
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector is enabled",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{112}),
			assert: func(t *testing.T, registry *Registry) {
				require.Len(t, registry.collectorGroups, 1)
				require.Contains(t, registry.collectorGroups, dcgm.FE_GPU)
				require.Len(t, registry.collectorGroups[dcgm.FE_GPU], 1)
				require.IsType(t, &clockEventsCollector{}, registry.collectorGroups[dcgm.FE_GPU][0])
			},
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector can not be initialized",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.
					WatchList{}, false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
			assert: func(t *testing.T, registry *Registry) {
				require.Len(t, registry.collectorGroups, 0)
			},
		},
		{
			name: "DCGM_EXP_CLOCK_EVENTS_COUNT collector can not be created by DCGM",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			setupDCGMMock: func(mockDCGM *dcgmmock.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
		},
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector is enabled",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:      "testhost",
			config:        &appconfig.Config{},
			setupDCGMMock: setupDCGMMockForDCGMExpMetrics([]dcgm.Short{230}),
			assert: func(t *testing.T, registry *Registry) {
				require.Len(t, registry.collectorGroups, 1)
				require.Contains(t, registry.collectorGroups, dcgm.FE_GPU)
				require.Len(t, registry.collectorGroups[dcgm.FE_GPU], 1)
				require.IsType(t, &xidCollector{}, registry.collectorGroups[dcgm.FE_GPU][0])
			},
		},
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector can not be initialized",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.
					WatchList{}, false).AnyTimes()
				return mockDeviceWatchListManager
			},
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
			assert: func(t *testing.T, registry *Registry) {
				require.Len(t, registry.collectorGroups, 0)
			},
		},
		{
			name: "DCGM_EXP_XID_ERRORS_COUNT collector can not be created by DCGM",
			cs: &CounterSet{
				DCGMCounters: []appconfig.Counter{},
				ExporterCounters: []appconfig.Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			getDeviceWatchListManager: func() devicewatchlistmanager.Manager {
				mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
				mockDeviceWatchListManager.EXPECT().WatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList,
					true)
				mockDeviceWatchListManager.EXPECT().WatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
					false).AnyTimes()
				return mockDeviceWatchListManager
			},
			setupDCGMMock: func(mockDCGM *dcgmmock.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, errors.New("boom")).AnyTimes()
			},
			hostname:   "testhost",
			config:     &appconfig.Config{},
			wantsPanic: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logrus.StandardLogger().ExitFunc = func(i int) {
				panic("logrus.Fatal")
			}

			defer func() {
				logrus.StandardLogger().ExitFunc = nil
			}()

			mockDCGMProvider := dcgmmock.NewMockDCGM(ctrl)

			realDCGM := dcgmprovider.Client()
			defer func() {
				dcgmprovider.SetClient(realDCGM)
			}()

			dcgmprovider.SetClient(mockDCGMProvider)
			if tt.setupDCGMMock != nil {
				tt.setupDCGMMock(mockDCGMProvider)
			}

			registry := NewRegistry()
			if tt.wantsPanic {
				require.PanicsWithValue(t, "logrus.Fatal", func() {
					InitCollectorFactory(tt.cs, tt.getDeviceWatchListManager(), tt.hostname, tt.config,
						registry).Register()
				})
				return
			}
			InitCollectorFactory(tt.cs, tt.getDeviceWatchListManager(), tt.hostname, tt.config,
				registry).Register()
			if tt.assert != nil {
				tt.assert(t, registry)
			}
		})
	}
}

func setupDCGMMockForDCGMExpMetrics(fields []dcgm.Short) func(mockDCGM *dcgmmock.MockDCGM) {
	return func(mockDCGM *dcgmmock.MockDCGM) {
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
