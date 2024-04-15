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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	dcgmmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

func Test_collectorFactory_Register(t *testing.T) {
	dcgmCounter := Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
		Help:      "",
	}

	ctrl := gomock.NewController(t)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()

	defaultFieldEntityGroupTypeSystemInfoItem := FieldEntityGroupTypeSystemInfoItem{
		DeviceFields: []dcgm.Short{42},
		DeviceInfo:   mockDeviceInfo,
	}

	tests := []struct {
		name                           string
		cs                             *CounterSet
		fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo
		hostname                       string
		config                         *appconfig.Config
		setupDCGMMock                  func(*dcgmmock.MockDCGM)
		assert                         func(*testing.T, *Registry)
		wantsPanic                     bool
	}{
		{
			name: fmt.Sprintf("Collector enabled for the %s", dcgm.FE_GPU.String()),
			cs: &CounterSet{
				DCGMCounters: []Counter{dcgmCounter},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
			},
			hostname: "testhost",
			config:   &appconfig.Config{},
			setupDCGMMock: func(mockDCGM *dcgmmock.MockDCGM) {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(42))
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()

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
				DCGMCounters: []Counter{dcgmCounter},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{},
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
				DCGMCounters: []Counter{},
				ExporterCounters: []Counter{
					{
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
					},
				},
			},
			fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
				items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
					dcgm.FE_GPU: defaultFieldEntityGroupTypeSystemInfoItem,
				},
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

			deviceinfo.DcgmCreateGroup = dcgmprovider.Client().CreateGroup
			defer func() {
				deviceinfo.DcgmCreateGroup = dcgm.CreateGroup
			}()

			registry := NewRegistry()
			if tt.wantsPanic {
				require.PanicsWithValue(t, "logrus.Fatal", func() {
					InitCollectorFactory().Register(tt.cs, tt.fieldEntityGroupTypeSystemInfo, tt.hostname, tt.config, registry)
				})
				return
			}
			InitCollectorFactory().Register(tt.cs, tt.fieldEntityGroupTypeSystemInfo, tt.hostname, tt.config, registry)
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
