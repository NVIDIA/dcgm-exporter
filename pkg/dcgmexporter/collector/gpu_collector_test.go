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

package collector

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mock_dcgmprovider "github.com/NVIDIA/dcgm-exporter/mocks/pkg/dcgmexporter/dcgmprovider"
	mock_sysinfo "github.com/NVIDIA/dcgm-exporter/mocks/pkg/dcgmexporter/sysinfo"
	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

var sampleCounters = []common.Counter{
	{1, "COUNTER_SAMPLE_1", "gauge", "Sample Counter 1"},
	{2, "COUNTER_SAMPLE_2", "gauge", "Sample Counter 2"},
}

func Test_NewDCGMCollector(t *testing.T) {

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSysInfo := mock_sysinfo.NewMockSystemInfoInterface(ctrl)
	mockSysInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).MinTimes(0)
	mockSysInfo.EXPECT().GOpts().Return(common.DeviceOptions{
		Flex:       false,
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	})
	mockSysInfo.EXPECT().GPUCount().Return(1).MinTimes(0)
	mockSysInfo.EXPECT().GPU(0).Return(sysinfo.GPUInfo{
		DeviceInfo:   dcgm.Device{GPU: 1},
		GPUInstances: []sysinfo.GPUInstanceInfo{},
		MigEnabled:   false,
	}).MinTimes(0)

	// Mock DCGM calls
	mockDCGMProvider := mock_dcgmprovider.NewMockDCGMProvider(ctrl)
	dcgmprovider.SetClient(mockDCGMProvider)
	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(1))
	mockDCGMProvider.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
	mockDCGMProvider.EXPECT().AddEntityToGroup(mockGroupHandle, gomock.Any(), gomock.Any()).Return(nil).MinTimes(0)
	mockDCGMProvider.EXPECT().DestroyGroup(mockGroupHandle).MinTimes(0)

	mockFieldHandle := dcgm.FieldHandle{}
	mockFieldHandle.SetHandle(uintptr(1))
	mockDCGMProvider.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldHandle, nil).MinTimes(0)
	mockDCGMProvider.EXPECT().FieldGroupDestroy(mockFieldHandle).MinTimes(0).MinTimes(0)

	mockDCGMProvider.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).Return(nil).MinTimes(0)

	config := &common.Config{}

	// Set up the expected collector
	expectedCollector := &DCGMCollector{
		Counters: sampleCounters,
		SysInfo:  mockSysInfo,
		Hostname: "test-hostname",
	}

	fieldEntity := sysinfo.FieldEntityGroupTypeSystemInfoItem{
		SystemInfo:   mockSysInfo,
		DeviceFields: []dcgm.Short{1, 2, 3},
	}

	collector, cleanups, err := NewDCGMCollector(sampleCounters, "test-hostname", config, fieldEntity)

	require.NoError(t, err)

	assert.Equal(t, expectedCollector, collector)

	assert.NotNil(t, cleanups)
	cleanups()
}
