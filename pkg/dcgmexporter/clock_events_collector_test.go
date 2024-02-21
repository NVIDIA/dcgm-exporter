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

package dcgmexporter

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestClockEventsCollector_Gather(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)
	runOnlyWithLiveGPUs(t)
	testutils.RequireLinux(t)

	hostname := "local-test"
	config := &Config{
		GPUDevices: DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
		ClockEventsCountWindowSize: int(time.Duration(5) * time.Minute),
	}

	records := [][]string{
		{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
		{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
	}

	cc, err := extractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	for i := range cc.DCGMCounters {
		if cc.DCGMCounters[i].PromType == "label" {
			cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
		}
	}

	// Create fake GPU
	numGPUs, err := dcgm.GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgm.CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	type clockEventsCountExpectation map[string]string
	expectations := map[string]clockEventsCountExpectation{}

	for i, gpuID := range gpuIDs {
		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
		)
		require.NoError(t, err)

		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
		)
		require.NoError(t, err)

		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE),
		)
		require.NoError(t, err)

		expectations[fmt.Sprint(gpuID)] = clockEventsCountExpectation{
			DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL.String(): "2",
			DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL.String(): "2",
			DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE.String():   "1",
		}
	}

	// Create a fake K8S to emulate work on K8S environment
	tmpDir, cleanup := CreateTmpDir(t)
	defer cleanup()
	socketPath = tmpDir + "/kubelet.sock"
	server := grpc.NewServer()

	gpuIDsAsString := make([]string, len(gpuIDs))

	for i, g := range gpuIDs {
		gpuIDsAsString[i] = fmt.Sprint(g)
	}

	podresourcesapi.RegisterPodResourcesListerServer(server, NewPodResourcesMockServer(gpuIDsAsString))
	// Tell that the app is running on K8S
	config.Kubernetes = true

	allCounters := []Counter{
		Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(allCounters, config)
	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	item, _ := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)

	collector, err := NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		collector.Cleanup()
	}()

	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(Counter)]

	for i := 0; i < len(metricValues); i++ {
		gpuID, err := strconv.ParseUint(metricValues[i].GPU, 10, 64)
		if err == nil {
			if !slices.Contains(gpuIDs, uint(gpuID)) {
				metricValues = append(metricValues[:i], metricValues[i+1:]...)
			}
		}
	}

	// We expect 9 records, because we have 3 fake GPU and each GPU experienced 3 CLOCK_EVENTS
	require.Len(t, metricValues, 9)
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.ClockEventsCountWindowSize), val.Labels["window_size_in_ms"])
		expected, exists := expectations[val.GPU]
		require.True(t, exists)
		actualReason, exists := val.Labels["clock_event"]
		require.True(t, exists)
		expectedVal, exists := expected[actualReason]
		require.True(t, exists)
		require.Equal(t, expectedVal, val.Value)
	}
}

func TestClockEventsCollector_NewClocksThrottleReasonsCollector(t *testing.T) {
	config := &Config{
		GPUDevices: DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
	}

	teardownTest := setupTest(t)
	defer teardownTest(t)

	allCounters := []Counter{
		Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(allCounters, config)
	err := fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)
	item, _ := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)

	t.Run("Should Return Error When DCGM_EXP_CLOCK_EVENTS_COUNT is not present", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
		}
		cc, err := extractCounters(records, config)
		require.NoError(t, err)
		require.Len(t, cc.ExporterCounters, 0)
		require.Len(t, cc.DCGMCounters, 1)
		collector, err := NewClockEventsCollector(cc.DCGMCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, collector)
	})

	t.Run("Should Return Error When Counter Param Is Empty", func(t *testing.T) {
		counters := make([]Counter, 0)
		collector, err := NewClockEventsCollector(counters, "", config, item)
		require.Error(t, err)
		require.Nil(t, collector)
	})

	t.Run("Should Not Return Error When DCGM_EXP_CLOCK_EVENTS_COUNT Present More Than Once", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
		}
		cc, err := extractCounters(records, config)
		require.NoError(t, err)
		for i := range cc.DCGMCounters {
			if cc.DCGMCounters[i].PromType == "label" {
				cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
			}
		}
		collector, err := NewClockEventsCollector(cc.ExporterCounters, "", config, item)
		require.NoError(t, err)
		require.NotNil(t, collector)
	})
}

func TestClockEventsCollector_Gather_AllTheThings(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &Config{
		GPUDevices: DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
		ClockEventsCountWindowSize: int(time.Duration(5) * time.Minute),
	}

	records := [][]string{
		{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
		{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
	}

	cc, err := extractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	for i := range cc.DCGMCounters {
		if cc.DCGMCounters[i].PromType == "label" {
			cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
		}
	}

	// Create fake GPU
	numGPUs, err := dcgm.GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgm.CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	type clockThrottleReasonExpectation map[string]string
	expectations := map[string]clockThrottleReasonExpectation{}

	require.Len(t, gpuIDs, 1)
	gpuID := gpuIDs[0]
	err = dcgm.InjectFieldValue(gpuID,
		dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		dcgm.DCGM_FT_INT64,
		0,
		time.Now().Add(-time.Duration(1)*time.Second).UnixMicro(),
		int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|
			DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING|
			DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP|
			DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN|
			DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST|
			DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|
			DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL|
			DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE|
			DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS),
	)

	require.NoError(t, err)

	expectations[fmt.Sprint(gpuID)] = clockThrottleReasonExpectation{
		DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE.String():       "1",
		DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING.String(): "1",
		DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP.String():   "1",
		DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN.String():    "1",
		DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST.String():     "1",
		DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL.String():     "1",
		DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL.String():     "1",
		DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE.String(): "1",
		DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS.String(): "1",
	}

	allCounters := []Counter{
		Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(allCounters, config)

	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	item, _ := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)

	collector, err := NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		collector.Cleanup()
	}()

	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(Counter)]

	metricValues = getFakeGPUMetrics(metricValues, gpuIDs)

	// Expected 9 metric values, because we injected 9 reasons
	require.Len(t, metricValues, 9)
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.ClockEventsCountWindowSize), val.Labels["window_size_in_ms"])
		expected, exists := expectations[val.GPU]
		require.True(t, exists)
		actualReason, exists := val.Labels["clock_event"]
		require.True(t, exists)
		expectedVal, exists := expected[actualReason]
		require.True(t, exists)
		require.Equal(t, expectedVal, val.Value)
	}
}

func TestClockEventsCollector_Gather_AllTheThings_WhenNoLabels(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &Config{
		GPUDevices: DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
		ClockEventsCountWindowSize: int(time.Duration(5) * time.Minute),
	}

	records := [][]string{
		{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
	}

	cc, err := extractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 0)

	// Create fake GPU
	numGPUs, err := dcgm.GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgm.CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	gpuID := gpuIDs[0]
	err = dcgm.InjectFieldValue(gpuID,
		dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		dcgm.DCGM_FT_INT64,
		0,
		time.Now().Add(-time.Duration(1)*time.Second).UnixMicro(),
		int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|
			DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING|
			DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP|
			DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN|
			DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST|
			DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|
			DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL|
			DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE|
			DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS),
	)

	require.NoError(t, err)

	allCounters := []Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(allCounters, config)

	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	item, _ := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)

	collector, err := NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		collector.Cleanup()
	}()

	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(Counter)]
	// Exclude the real GPU from the test
	metricValues = getFakeGPUMetrics(metricValues, gpuIDs)
	// Expected 9 metric values, because we injected 9 reasons
	require.Len(t, metricValues, 9)
}

func getFakeGPUMetrics(metricValues []Metric, gpuIDs []uint) []Metric {
	for i := 0; i < len(metricValues); i++ {
		gpuID, err := strconv.ParseUint(metricValues[i].GPU, 10, 64)
		if err == nil {
			if !slices.Contains(gpuIDs, uint(gpuID)) {
				metricValues = append(metricValues[:i], metricValues[i+1:]...)
			}
		}
	}
	return metricValues
}
