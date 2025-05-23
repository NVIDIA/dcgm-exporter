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

package integration_test

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"
	"k8s.io/utils/ptr"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

var deviceWatcher = devicewatcher.NewDeviceWatcher()

var expectedGPUMetrics = map[string]bool{
	testutils.SampleGPUTempCounter.FieldName:           true,
	testutils.SampleGPUTotalEnergyCounter.FieldName:    true,
	testutils.SampleGPUPowerUsageCounter.FieldName:     true,
	testutils.SampleVGPULicenseStatusCounter.FieldName: true,
}

var expectedCPUMetrics = map[string]bool{
	testutils.SampleCPUUtilTotalCounter.FieldName: true,
}

func setupTest() func() {
	config := &appconfig.Config{
		UseRemoteHE: false,
	}

	dcgmprovider.Initialize(config)

	return func() {
		defer dcgmprovider.Client().Cleanup()
	}
}

func runOnlyWithLiveGPUs(t *testing.T) {
	t.Helper()
	gpus, err := dcgmprovider.Client().GetSupportedDevices()
	assert.NoError(t, err)
	if len(gpus) < 1 {
		t.Skip("Skipping test that requires live GPUs. None were found")
	}
}

func mockDCGM(ctrl *gomock.Controller) *mockdcgmprovider.MockDCGM {
	// Mock results outputs
	mockDevice := dcgm.Device{
		GPU:  0,
		UUID: "fake1",
		PCI: dcgm.PCIInfo{
			BusID: "00000000:0000:0000.0",
		},
	}

	mockMigHierarchy := dcgm.MigHierarchy_v2{
		Count: 0,
	}

	mockCPUHierarchy := dcgm.CPUHierarchy_v1{
		Version: 0,
		NumCPUs: 1,
		CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v1{
			{
				CPUID:      0,
				OwnedCores: []uint64{0, 18446744073709551360, 65535},
			},
		},
	}

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(1)

	mockFieldHandle := dcgm.FieldHandle{}
	mockFieldHandle.SetHandle(1)

	mockDCGMProvider := mockdcgmprovider.NewMockDCGM(ctrl)
	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).AnyTimes()
	mockDCGMProvider.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockMigHierarchy, nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil).AnyTimes()
	mockDCGMProvider.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil).AnyTimes()
	mockDCGMProvider.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldHandle, nil).AnyTimes()
	mockDCGMProvider.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(mockDevice, nil).AnyTimes()

	return mockDCGMProvider
}

func TestClockEventsCollector_NewClocksThrottleReasonsCollector(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE: false,
		GPUDeviceOptions: appconfig.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
	}

	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)
	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	t.Run("Should Return Error When DCGM_EXP_CLOCK_EVENTS_COUNT is not present", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
		}
		cc, err := counters.ExtractCounters(records, config)
		require.NoError(t, err)
		require.Len(t, cc.ExporterCounters, 0)
		require.Len(t, cc.DCGMCounters, 1)
		clockEventCollector, err := collector.NewClockEventsCollector(cc.DCGMCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, clockEventCollector)
	})

	t.Run("Should Return Error When Counter Param Is Empty", func(t *testing.T) {
		counterList := make([]counters.Counter, 0)
		clockEventCollector, err := collector.NewClockEventsCollector(counterList, "", config, item)
		require.Error(t, err)
		require.Nil(t, clockEventCollector)
	})

	t.Run("Should Not Return Error When DCGM_EXP_CLOCK_EVENTS_COUNT Present More Than Once", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
			{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
		}
		cc, err := counters.ExtractCounters(records, config)
		require.NoError(t, err)
		for i := range cc.DCGMCounters {
			if cc.DCGMCounters[i].PromType == "label" {
				cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
			}
		}
		clockEventCollector, err := collector.NewClockEventsCollector(cc.ExporterCounters, "", config, item)
		require.NoError(t, err)
		require.NotNil(t, clockEventCollector)
	})
}

func TestClockEventsCollector_Gather(t *testing.T) {
	teardownTest := setupTest()
	defer teardownTest()
	runOnlyWithLiveGPUs(t)
	testutils.RequireLinux(t)

	hostname := "local-test"
	config := &appconfig.Config{
		GPUDeviceOptions: appconfig.DeviceOptions{
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

	cc, err := counters.ExtractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters.LabelCounters()...)

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

	type clockEventsCountExpectation map[string]string
	expectations := map[string]clockEventsCountExpectation{}

	for i, gpuID := range gpuIDs {
		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
		)
		require.NoError(t, err)

		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
		)
		require.NoError(t, err)

		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(collector.DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE),
		)
		require.NoError(t, err)

		expectations[fmt.Sprint(gpuID)] = clockEventsCountExpectation{
			collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL.String(): "2",
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL.String(): "2",
			collector.DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE.String():   "1",
		}
	}

	// Create a fake K8S to emulate work on K8S environment
	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := tmpDir + "/kubelet.sock"
	server := grpc.NewServer()

	gpuIDsAsString := make([]string, len(gpuIDs))

	for i, g := range gpuIDs {
		gpuIDsAsString[i] = fmt.Sprint(g)
	}

	v1.RegisterPodResourcesListerServer(server,
		testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, gpuIDsAsString))
	// Tell that the app is running on K8S
	config.Kubernetes = true
	config.PodResourcesKubeletSocket = socketPath

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	allCounters = append(allCounters, cc.ExporterCounters.LabelCounters()...)

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	clockEventCollector, err := collector.NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		clockEventCollector.Cleanup()
	}()

	metrics, err := clockEventCollector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]

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

func TestClockEventsCollector_Gather_AllTheThings(t *testing.T) {
	teardownTest := setupTest()
	defer teardownTest()
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &appconfig.Config{
		GPUDeviceOptions: appconfig.DeviceOptions{
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

	cc, err := counters.ExtractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	for i := range cc.DCGMCounters {
		if cc.DCGMCounters[i].PromType == "label" {
			cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
		}
	}

	// Create fake GPU
	numGPUs, err := dcgmprovider.Client().GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgmprovider.Client().CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	type clockThrottleReasonExpectation map[string]string
	expectations := map[string]clockThrottleReasonExpectation{}

	require.Len(t, gpuIDs, 1)
	gpuID := gpuIDs[0]
	err = dcgmprovider.Client().InjectFieldValue(gpuID,
		dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		dcgm.DCGM_FT_INT64,
		0,
		time.Now().Add(-time.Duration(1)*time.Second).UnixMicro(),
		int64(collector.DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|
			collector.DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE|
			collector.DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS),
	)

	require.NoError(t, err)

	expectations[fmt.Sprint(gpuID)] = clockThrottleReasonExpectation{
		collector.DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE.String():       "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING.String(): "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP.String():   "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN.String():    "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST.String():     "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL.String():     "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL.String():     "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE.String(): "1",
		collector.DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS.String(): "1",
	}

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	allCounters = append(allCounters, cc.ExporterCounters.LabelCounters()...)

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)

	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	clockEventCollector, err := collector.NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		clockEventCollector.Cleanup()
	}()

	metrics, err := clockEventCollector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]

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
	teardownTest := setupTest()
	defer teardownTest()
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &appconfig.Config{
		GPUDeviceOptions: appconfig.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
		ClockEventsCountWindowSize: int(time.Duration(5) * time.Minute),
	}

	records := [][]string{
		{"DCGM_EXP_CLOCK_EVENTS_COUNT", "gauge", ""},
	}

	cc, err := counters.ExtractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 0)

	// Create fake GPU
	numGPUs, err := dcgmprovider.Client().GetAllDeviceCount()
	require.NoError(t, err)

	if numGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	gpuIDs, err := dcgmprovider.Client().CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)

	gpuID := gpuIDs[0]
	err = dcgmprovider.Client().InjectFieldValue(gpuID,
		dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		dcgm.DCGM_FT_INT64,
		0,
		time.Now().Add(-time.Duration(1)*time.Second).UnixMicro(),
		int64(collector.DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|
			collector.DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST|
			collector.DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL|
			collector.DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE|
			collector.DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS),
	)

	require.NoError(t, err)

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)

	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	clockEventCollector, err := collector.NewClockEventsCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		clockEventCollector.Cleanup()
	}()

	metrics, err := clockEventCollector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_CLOCK_EVENTS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
	// Exclude the real GPU from the test
	metricValues = getFakeGPUMetrics(metricValues, gpuIDs)
	// Expected 9 metric values, because we injected 9 reasons
	require.Len(t, metricValues, 9)
}

func getFakeGPUMetrics(metricValues []collector.Metric, gpuIDs []uint) []collector.Metric {
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

func TestXIDCollector_Gather_Encode(t *testing.T) {
	teardownTest := setupTest()
	defer teardownTest()
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &appconfig.Config{
		GPUDeviceOptions: appconfig.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
		XIDCountWindowSize: int(time.Duration(5) * time.Minute),
	}

	records := [][]string{
		{
			"DCGM_EXP_XID_ERRORS_COUNT",
			"gauge",
			"Count of XID Errors within user-specified time window (see xid-count-window-size param).",
		},
		{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
	}

	cc, err := counters.ExtractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	for i := range cc.DCGMCounters {
		if cc.DCGMCounters[i].PromType == "label" {
			cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
		}
	}

	// Get a number of hardware GPUs
	hardwareGPUs, err := dcgmprovider.Client().GetAllDeviceCount()
	require.NoError(t, err)

	if hardwareGPUs+1 > dcgm.MAX_NUM_DEVICES {
		t.Skipf("Unable to add fake GPU with more than %d gpus", dcgm.MAX_NUM_DEVICES)
	}

	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}

	// Create fake GPU
	fakeGPUIDs, err := dcgmprovider.Client().CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, fakeGPUIDs)

	for i, gpuID := range fakeGPUIDs {
		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(42),
		)
		require.NoError(t, err)

		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(42),
		)
		require.NoError(t, err)

		err = dcgmprovider.Client().InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(46),
		)
		require.NoError(t, err)

	}

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	allCounters = append(allCounters, cc.ExporterCounters.LabelCounters()...)

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	item, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	xidCollector, err := collector.NewXIDCollector(cc.ExporterCounters, hostname, config, item)
	require.NoError(t, err)

	defer func() {
		xidCollector.Cleanup()
	}()

	metrics, err := xidCollector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)
	// We expect 1 metric: DCGM_EXP_XID_ERRORS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with 0 index
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]

	fakeGPUIDMap := map[string]struct{}{}
	for _, fakeGPUID := range fakeGPUIDs {
		fakeGPUIDMap[fmt.Sprint(fakeGPUID)] = struct{}{}
	}

	conditionFakeGPUOnly := func(m collector.Metric) bool {
		_, exists := fakeGPUIDMap[m.GPU]
		return exists
	}

	// We want to filter out physical GPU and keep fake only
	metricValues = filterMetrics(metricValues, conditionFakeGPUOnly)
	require.Len(t, metricValues, len(fakeGPUIDs)*2)

	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.XIDCountWindowSize), val.Labels["window_size_in_ms"])
	}

	// We inject new error
	err = dcgmprovider.Client().InjectFieldValue(fakeGPUIDs[0],
		dcgm.DCGM_FI_DEV_XID_ERRORS,
		dcgm.DCGM_FT_INT64,
		0,
		time.Now().UnixMicro(),
		int64(19),
	)
	require.NoError(t, err)

	// Wait for 1 second
	time.Sleep(1 * time.Second)

	metrics, err = xidCollector.GetMetrics()
	require.NoError(t, err)
	require.NotEmpty(t, metrics)

	// We expect 1 metric: DCGM_EXP_XID_ERRORS_COUNT
	require.Len(t, metrics, 1)
	// We get metric value with the last index
	metricValues = metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
	// We want to filter out physical GPU and keep fake only
	metricValues = filterMetrics(metricValues, conditionFakeGPUOnly)
	// We update metrics with slice, that doesn't contain physical GPU
	metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)] = metricValues

	// We have 3 fake GPU and each GPU experienced 3 XID errors: 42, 46, 19 to GPU0
	require.Len(t, metricValues, 1+(len(fakeGPUIDs)*2))
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.XIDCountWindowSize), val.Labels["window_size_in_ms"])
	}

	// Now we check the metric rendering
	var b bytes.Buffer
	err = rendermetrics.RenderGroup(&b, dcgm.FE_GPU, metrics)
	require.NoError(t, err)
	require.NotEmpty(t, b)

	var parser expfmt.TextParser
	mf, err := parser.TextToMetricFamilies(&b)
	require.NoError(t, err)
	require.NotEmpty(t, mf)
	require.Len(t, mf, 1)
	metricFamily := mf[reflect.ValueOf(mf).MapKeys()[0].Interface().(string)]
	require.NotNil(t, metricFamily.Name)
	assert.Equal(t, "DCGM_EXP_XID_ERRORS_COUNT", *metricFamily.Name)
	assert.Equal(t, "Count of XID Errors within user-specified time window (see xid-count-window-size param).",
		*metricFamily.Help)
	assert.Equal(t, io_prometheus_client.MetricType_GAUGE, *metricFamily.Type)
	// We have 3 fake GPU and each GPU, except the one experienced XID errors: 42, 46, 19
	require.Len(t, metricFamily.Metric, 1+(len(fakeGPUIDs)*2))
	for _, mv := range metricFamily.Metric {
		require.NotNil(t, mv.Gauge.Value)
		if *(mv.Gauge.Value) == 0 {
			// We don't inject XID errors into the hardware GPU, so we do not expect XID label
			assert.Len(t, mv.Label, 7)
			assert.False(t, slices.ContainsFunc(mv.Label, func(lp *io_prometheus_client.LabelPair) bool {
				return ptr.Deref(lp.Name, "") == "xid"
			}))
			continue
		}
		assert.Len(t, mv.Label, 9)
		assert.Equal(t, "gpu", *mv.Label[0].Name)
		assert.Equal(t, "UUID", *mv.Label[1].Name)
		assert.Equal(t, "pci_bus_id", *mv.Label[2].Name)
		assert.NotEmpty(t, *mv.Label[2].Value)
		assert.Equal(t, "device", *mv.Label[3].Name)
		assert.Equal(t, "modelName", *mv.Label[4].Name)
		assert.Equal(t, "Hostname", *mv.Label[5].Name)
		assert.Equal(t, "DCGM_FI_DRIVER_VERSION", *mv.Label[6].Name)
		assert.Equal(t, "window_size_in_ms", *mv.Label[7].Name)
		assert.Equal(t, "xid", *mv.Label[8].Name)
		assert.NotEmpty(t, *mv.Label[8].Value)
	}
}

func TestXIDCollector_NewXIDCollector(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE: false,
		GPUDeviceOptions: appconfig.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
	}

	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	allCounters := []counters.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		},
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	t.Run("Should Return Error When DCGM_EXP_XID_ERRORS_COUNT is not present", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
		}
		cc, err := counters.ExtractCounters(records, config)
		require.NoError(t, err)
		require.Len(t, cc.ExporterCounters, 0)
		require.Len(t, cc.DCGMCounters, 1)

		xidCollector, err := collector.NewXIDCollector(cc.DCGMCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, xidCollector)
	})

	t.Run("Should Return Error When Counters Param Is Empty", func(t *testing.T) {
		emptyCounters := make([]counters.Counter, 0)
		xidCollector, err := collector.NewXIDCollector(emptyCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, xidCollector)
	})

	t.Run("Should Not Return Error When DCGM_EXP_XID_ERRORS_COUNT Present More Than Once", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
			{
				"DCGM_EXP_XID_ERRORS_COUNT",
				"gauge",
				"Count of XID Errors within user-specified time window (see xid-count-window-size param).",
			},
			{
				"DCGM_EXP_XID_ERRORS_COUNT",
				"gauge",
				"Count of XID Errors within user-specified time window (see xid-count-window-size param).",
			},
			{
				"DCGM_EXP_XID_ERRORS_COUNT",
				"gauge",
				"Count of XID Errors within user-specified time window (see xid-count-window-size param).",
			},
		}
		cc, err := counters.ExtractCounters(records, config)
		require.NoError(t, err)
		for i := range cc.DCGMCounters {
			if cc.DCGMCounters[i].PromType == "label" {
				cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
			}
		}
		xidCollector, err := collector.NewXIDCollector(cc.ExporterCounters, "", config, item)
		require.NoError(t, err)
		require.NotNil(t, xidCollector)
	})
}

func filterMetrics(metricValues []collector.Metric, condition func(metric collector.Metric) bool) []collector.Metric {
	var result []collector.Metric
	for _, metricValue := range metricValues {
		if condition(metricValue) {
			result = append(result, metricValue)
		}
	}
	return result
}

func TestDCGMCollector(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE: false,
	}
	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	dcgmCollector := testDCGMGPUCollector(t, testutils.SampleCounters)
	dcgmCollector.Cleanup()

	dcgmCollector = testDCGMCPUCollector(t, testutils.SampleCounters)
	dcgmCollector.Cleanup()
}

func testDCGMGPUCollector(t *testing.T, counters []counters.Counter) *collector.DCGMCollector {
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
	mockDCGMProvider.EXPECT().FieldGetByID(gomock.Any()).
		DoAndReturn(func(fieldID dcgm.Short) dcgm.FieldMeta {
			return realDCGMProvider.FieldGetByID(fieldID)
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

	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	gpuItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	g, err := collector.NewDCGMCollector(counters, "", &config, gpuItem)
	require.NoError(t, err)

	/* Test for error when no switches are available to monitor. */
	switchItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_SWITCH)
	assert.False(t, exists, "dcgm.FE_SWITCH should not be available")

	_, err = collector.NewDCGMCollector(counters, "", &config, switchItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	/* Test for error when no cpus are available to monitor. */
	cpuItem, exist := deviceWatchListManager.EntityWatchList(dcgm.FE_CPU)
	require.False(t, exist, "dcgm.FE_CPU should not be available")

	_, err = collector.NewDCGMCollector(counters, "", &config, cpuItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	out, err := g.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that you have a GPU on this node")
	require.Len(t, out, len(expectedGPUMetrics),
		fmt.Sprintf("Expected: %+v \nGot: %+v", expectedGPUMetrics, out))

	seenMetrics := map[string]bool{}
	for _, metrics := range out {
		for _, metric := range metrics {
			seenMetrics[metric.Counter.FieldName] = true
			require.NotEmpty(t, metric.GPU)
			require.NotEmpty(t, metric.GPUUUID)
			require.NotEmpty(t, metric.GPUPCIBusID)
			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, collector.FailedToConvert)
		}
	}
	require.Equal(t, seenMetrics, expectedGPUMetrics)

	return g
}

func testDCGMCPUCollector(t *testing.T, counters []counters.Counter) *collector.DCGMCollector {
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
	mockDCGMProvider.EXPECT().FieldGetByID(gomock.Any()).
		DoAndReturn(func(fieldID dcgm.Short) dcgm.FieldMeta {
			return realDCGMProvider.FieldGetByID(fieldID)
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
	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_CPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_CPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	cpuItem, cpuItemExist := deviceWatchListManager.EntityWatchList(dcgm.FE_CPU)
	require.True(t, cpuItemExist)

	c, err := collector.NewDCGMCollector(counters, "", &config, cpuItem)
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
			require.NotEqual(t, metric.Value, collector.FailedToConvert)
		}
		require.Equal(t, seenMetrics, expectedCPUMetrics)
	}

	return c
}

func TestGPUCollector_GetMetrics(t *testing.T) {
	teardownTest := setupTest()
	defer teardownTest()

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

	intputCounters := []counters.Counter{
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

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(intputCounters, &config)
	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	gpuItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	c, err := collector.NewDCGMCollector(intputCounters, "", &config, gpuItem)
	require.NoError(t, err)

	defer c.Cleanup()

	out, err := c.GetMetrics()
	require.NoError(t, err)
	require.Len(t, out, 1)

	values := out[intputCounters[0]]

	require.Equal(t, numGPUs, uint(len(values)))
}
