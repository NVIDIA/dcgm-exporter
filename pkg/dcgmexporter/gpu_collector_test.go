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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

var expectedGPUMetrics = map[string]bool{
	testutils.SampleGPUTempCounter.FieldName:           true,
	testutils.SampleGPUTotalEnergyCounter.FieldName:    true,
	testutils.SampleGPUPowerUsageCounter.FieldName:     true,
	testutils.SampleVGPULicenseStatusCounter.FieldName: true,
}

var expectedCPUMetrics = map[string]bool{
	testutils.SampleCPUUtilTotalCounter.FieldName: true,
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

	collector := testDCGMGPUCollector(t, testutils.SampleCounters)
	collector.Cleanup()

	collector = testDCGMCPUCollector(t, testutils.SampleCounters)
	collector.Cleanup()
}

func testDCGMGPUCollector(t *testing.T, counters []appconfig.Counter) *DCGMCollector {
	dOpt := appconfig.DeviceOptions{
		Flex:       true,
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	}
	config := appconfig.Config{
		GPUDeviceOptions: dOpt,
		NoHostname:       false,
		UseOldNamespace:  false,
		UseFakeGPUs:      false,
		CollectInterval:  1,
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

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(counters, &config)

	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	gpuItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	g, err := NewDCGMCollector(counters, "", &config, gpuItem)
	require.NoError(t, err)

	/* Test for error when no switches are available to monitor. */
	switchItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_SWITCH)
	assert.False(t, exists, "dcgm.FE_SWITCH should not be available")

	_, err = NewDCGMCollector(counters, "", &config, switchItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	/* Test for error when no cpus are available to monitor. */
	cpuItem, exist := deviceWatchListManager.EntityWatchList(dcgm.FE_CPU)
	require.False(t, exist, "dcgm.FE_CPU should not be available")

	_, err = NewDCGMCollector(counters, "", &config, cpuItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	out, err := g.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that you have a GPU on this node")
	require.Len(t, out, len(expectedGPUMetrics), fmt.Sprintf("Expected: %+v \nGot: %+v", expectedGPUMetrics, out))

	seenMetrics := map[string]bool{}
	for _, metrics := range out {
		for _, metric := range metrics {
			seenMetrics[metric.Counter.FieldName] = true
			require.NotEmpty(t, metric.GPU)

			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, FailedToConvert)
		}
	}
	require.Equal(t, seenMetrics, expectedGPUMetrics)

	return g
}

func testDCGMCPUCollector(t *testing.T, counters []appconfig.Counter) *DCGMCollector {
	dOpt := appconfig.DeviceOptions{Flex: true, MajorRange: []int{-1}, MinorRange: []int{-1}}
	config := appconfig.Config{
		CPUDeviceOptions: dOpt,
		NoHostname:       false,
		UseOldNamespace:  false,
		UseFakeGPUs:      false,
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
	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(counters, &config)
	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_CPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_CPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	cpuItem, cpuItemExist := deviceWatchListManager.EntityWatchList(dcgm.FE_CPU)
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

	c := []appconfig.Counter{
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
			metrics := make(map[appconfig.Counter][]Metric)
			ToMetric(metrics, values, c, d, instanceInfo, false, "", tc.replaceBlanksInModelName)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(appconfig.Counter)]
			assert.Equal(t, "42", metricValues[0].Value)
			assert.Equal(t, tc.expectedGPUModelName, metricValues[0].GPUModelName)
		})
	}
}

func TestToMetricWhenDCGM_FI_DEV_XID_ERRORSField(t *testing.T) {
	c := []appconfig.Counter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
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
		name        string
		fieldValue  byte
		expectedErr string
	}

	testCases := []testCase{
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has no error",
			fieldValue:  0,
			expectedErr: xidErrCodeToText[0],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has known value",
			fieldValue:  42,
			expectedErr: xidErrCodeToText[42],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has unknown value",
			fieldValue:  255,
			expectedErr: unknownErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fieldValue := [4096]byte{}
			fieldValue[0] = tc.fieldValue
			values := []dcgm.FieldValue_v1{
				{
					FieldId:   dcgm.DCGM_FI_DEV_XID_ERRORS,
					FieldType: dcgm.DCGM_FT_INT64,
					Value:     fieldValue,
				},
			}

			metrics := make(map[appconfig.Counter][]Metric)
			ToMetric(metrics, values, c, d, instanceInfo, false, "", false)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(appconfig.Counter)]
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Value)
			assert.Contains(t, metricValues[0].Attributes, "err_code")
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Attributes["err_code"])
			assert.Contains(t, metricValues[0].Attributes, "err_msg")
			assert.Equal(t, tc.expectedErr, metricValues[0].Attributes["err_msg"])
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

	counters := []appconfig.Counter{
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
		GPUDeviceOptions: dOpt,
		NoHostname:       false,
		UseOldNamespace:  false,
		UseFakeGPUs:      false,
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(counters, &config)
	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	gpuItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
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
