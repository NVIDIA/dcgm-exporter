/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

var sampleCounters = []Counter{
	{dcgm.DCGM_FI_DEV_GPU_TEMP, "DCGM_FI_DEV_GPU_TEMP", "gauge", "Temperature Help info"},
	{dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "gauge", "Energy help info"},
	{dcgm.DCGM_FI_DEV_POWER_USAGE, "DCGM_FI_DEV_POWER_USAGE", "gauge", "Power help info"},
	{dcgm.DCGM_FI_DRIVER_VERSION, "DCGM_FI_DRIVER_VERSION", "label", "Driver version"},
	/* test that switch and link metrics are filtered out automatically when devices are not detected */
	{
		dcgm.DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT,
		"DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT",
		"gauge",
		"switch temperature",
	},
	{
		dcgm.DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS,
		"DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS",
		"gauge",
		"per-link flit errors",
	},
	/* test that vgpu metrics are not filtered out */
	{dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS, "DCGM_FI_DEV_VGPU_LICENSE_STATUS", "gauge", "vgpu license status"},
	/* test that cpu and cpu core metrics are filtered out automatically when devices are not detected */
	{dcgm.DCGM_FI_DEV_CPU_UTIL_TOTAL, "DCGM_FI_DEV_CPU_UTIL_TOTAL", "gauge", "Total CPU utilization"},
}

var expectedMetrics = map[string]bool{
	"DCGM_FI_DEV_GPU_TEMP":                 true,
	"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION": true,
	"DCGM_FI_DEV_POWER_USAGE":              true,
	"DCGM_FI_DEV_VGPU_LICENSE_STATUS":      true,
}

var expectedCPUMetrics = map[string]bool{
	"DCGM_FI_DEV_CPU_UTIL_TOTAL": true,
}

func mockDCGM(ctrl *gomock.Controller) *mockdcgm.MockDCGM {
	// Mock results outputs
	mockDevice := dcgm.Device{
		GPU:  0,
		UUID: "fake1",
	}

	mockMigHierarchy := dcgm.MigHierarchy_v2{
		Count: 0,
	}

	mockCPUHierarchy := dcgm.CpuHierarchy_v1{
		Version: 0,
		NumCpus: 1,
		Cpus: [dcgm.MAX_NUM_CPUS]dcgm.CpuHierarchyCpu_v1{
			{
				CpuId:      0,
				OwnedCores: []uint64{0, 18446744073709551360, 65535},
			},
		},
	}

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(1)

	mockFieldHandle := dcgm.FieldHandle{}
	mockFieldHandle.SetHandle(1)

	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).AnyTimes()
	mockDCGMProvider.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetGpuInstanceHierarchy().Return(mockMigHierarchy, nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetCpuHierarchy().Return(mockCPUHierarchy, nil).AnyTimes()
	mockDCGMProvider.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()
	mockDCGMProvider.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldHandle, nil).AnyTimes()
	mockDCGMProvider.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(mockDevice, nil).AnyTimes()

	return mockDCGMProvider
}

func TestDCGMCollector(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE: false,
	}
	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	collector := testDCGMGPUCollector(t, sampleCounters)
	collector.Cleanup()

	collector = testDCGMCPUCollector(t, sampleCounters)
	collector.Cleanup()
}

func testDCGMGPUCollector(t *testing.T, counters []Counter) *DCGMCollector {
	dOpt := appconfig.DeviceOptions{
		Flex:       true,
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	}
	config := appconfig.Config{
		GPUDevices:      dOpt,
		NoHostname:      false,
		UseOldNamespace: false,
		UseFakeGPUs:     false,
		CollectInterval: 1,
	}

	// Store actual dcgm provider
	realDCGMProvider := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGMProvider)

	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockDCGM(ctrl)

	// Calls where actual API calls and results are desirable
	mockDCGMProvider.EXPECT().FieldGetById(gomock.Any()).
		DoAndReturn(func(fieldID dcgm.Short) dcgm.FieldMeta {
			return realDCGMProvider.FieldGetById(fieldID)
		}).AnyTimes()

	mockDCGMProvider.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
			error,
		) {
			return realDCGMProvider.EntityGetLatestValues(entityGroup, entityId, fields)
		}).AnyTimes()

	// Set mock DCGM provider
	dcgmprovider.SetClient(mockDCGMProvider)

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(counters, &config)

	err := fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	gpuItem, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
	require.True(t, exists)

	g, err := NewDCGMCollector(counters, "", &config, gpuItem)
	require.NoError(t, err)

	/* Test for error when no switches are available to monitor. */
	switchItem, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_SWITCH)
	assert.False(t, exists, "dcgm.FE_SWITCH should not be available")

	_, err = NewDCGMCollector(counters, "", &config, switchItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	/* Test for error when no cpus are available to monitor. */
	cpuItem, exist := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_CPU)
	require.False(t, exist, "dcgm.FE_CPU should not be available")

	_, err = NewDCGMCollector(counters, "", &config, cpuItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	out, err := g.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that you have a GPU on this node")
	require.Len(t, out, len(expectedMetrics), fmt.Sprintf("Expected: %+v \nGot: %+v", expectedMetrics, out))

	seenMetrics := map[string]bool{}
	for _, metrics := range out {
		for _, metric := range metrics {
			seenMetrics[metric.Counter.FieldName] = true
			require.NotEmpty(t, metric.GPU)

			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, FailedToConvert)
		}
	}
	require.Equal(t, seenMetrics, expectedMetrics)

	return g
}

func testDCGMCPUCollector(t *testing.T, counters []Counter) *DCGMCollector {
	dOpt := appconfig.DeviceOptions{Flex: true, MajorRange: []int{-1}, MinorRange: []int{-1}}
	config := appconfig.Config{
		CPUDevices:      dOpt,
		NoHostname:      false,
		UseOldNamespace: false,
		UseFakeGPUs:     false,
	}

	realDCGMProvider := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGMProvider)

	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockDCGM(ctrl)

	// Calls where actual API calls and results are desirable
	mockDCGMProvider.EXPECT().FieldGetById(gomock.Any()).
		DoAndReturn(func(fieldID dcgm.Short) dcgm.FieldMeta {
			return realDCGMProvider.FieldGetById(fieldID)
		}).AnyTimes()

	mockDCGMProvider.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
			error,
		) {
			return realDCGMProvider.EntityGetLatestValues(entityGroup, entityId, fields)
		}).AnyTimes()

	dcgmprovider.SetClient(mockDCGMProvider)

	/* Test that only cpu metrics are collected for cpu entities. */

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(counters, &config)
	err := fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_CPU)
	require.NoError(t, err)

	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_CPU)
	require.NoError(t, err)

	cpuItem, cpuItemExist := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_CPU)
	require.True(t, cpuItemExist)

	c, err := NewDCGMCollector(counters, "", &config, cpuItem)
	require.NoError(t, err)

	out, err := c.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that the fake CPU has been registered")

	for _, dev := range out {
		seenMetrics := map[string]bool{}
		for _, metric := range dev {
			seenMetrics[metric.Counter.FieldName] = true
			require.NotEmpty(t, metric.GPU)

			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, FailedToConvert)
		}
		require.Equal(t, seenMetrics, expectedCPUMetrics)
	}

	return c
}

func TestToMetric(t *testing.T) {
	fieldValue := [4096]byte{}
	fieldValue[0] = 42
	values := []dcgm.FieldValue_v1{
		{
			FieldId:   150,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     fieldValue,
		},
	}

	c := []Counter{
		{
			FieldID:   150,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		},
	}

	d := dcgm.Device{
		UUID: "fake0",
		Identifiers: dcgm.DeviceIdentifiers{
			Model: "NVIDIA T400 4GB",
		},
	}

	var instanceInfo *deviceinfo.GPUInstanceInfo = nil

	type testCase struct {
		replaceBlanksInModelName bool
		expectedGPUModelName     string
	}

	testCases := []testCase{
		{
			replaceBlanksInModelName: true,
			expectedGPUModelName:     "NVIDIA-T400-4GB",
		},
		{
			replaceBlanksInModelName: false,
			expectedGPUModelName:     "NVIDIA T400 4GB",
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("When replaceBlanksInModelName is %t", tc.replaceBlanksInModelName), func(t *testing.T) {
			metrics := make(map[Counter][]Metric)
			ToMetric(metrics, values, c, d, instanceInfo, false, "", tc.replaceBlanksInModelName)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(Counter)]
			assert.Equal(t, "42", metricValues[0].Value)
			assert.Equal(t, tc.expectedGPUModelName, metricValues[0].GPUModelName)
		})
	}
}

func TestGPUCollector_GetMetrics(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	runOnlyWithLiveGPUs(t)
	// Create fake GPU
	numGPUs, err := dcgmprovider.Client().GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgmprovider.Client().CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	numGPUs, err = dcgmprovider.Client().GetAllDeviceCount()
	require.NoError(t, err)

	counters := []Counter{
		{
			FieldID:   100,
			FieldName: "DCGM_FI_DEV_SM_CLOCK",
			PromType:  "gauge",
			Help:      "SM clock frequency (in MHz).",
		},
	}

	dOpt := appconfig.DeviceOptions{
		Flex:       true,
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	}
	config := appconfig.Config{
		GPUDevices:      dOpt,
		NoHostname:      false,
		UseOldNamespace: false,
		UseFakeGPUs:     false,
	}

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(counters, &config)
	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	gpuItem, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
	require.True(t, exists)

	c, err := NewDCGMCollector(counters, "", &config, gpuItem)
	require.NoError(t, err)

	defer c.Cleanup()

	out, err := c.GetMetrics()
	require.NoError(t, err)
	require.Len(t, out, 1)

	values := out[counters[0]]

	require.Equal(t, numGPUs, uint(len(values)))
}
