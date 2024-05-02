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
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/utils/ptr"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

func TestXIDCollector_Gather_Encode(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)
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

	cc, err := extractCounters(records, config)
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

	allCounters := []appconfig.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	allCounters = append(allCounters, cc.ExporterCounters.LabelCounters()...)

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err = deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	item, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	xidCollector, err := NewXIDCollector(cc.ExporterCounters, hostname, config, item)
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
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(appconfig.Counter)]

	fakeGPUIDMap := map[string]struct{}{}
	for _, fakeGPUID := range fakeGPUIDs {
		fakeGPUIDMap[fmt.Sprint(fakeGPUID)] = struct{}{}
	}

	conditionFakeGPUOnly := func(m Metric) bool {
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
	metricValues = metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(appconfig.Counter)]
	// We want to filter out physical GPU and keep fake only
	metricValues = filterMetrics(metricValues, conditionFakeGPUOnly)
	// We update metrics with slice, that doesn't contain physical GPU
	metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(appconfig.Counter)] = metricValues

	// We have 3 fake GPU and each GPU experienced 3 XID errors: 42, 46, 19 to GPU0
	require.Len(t, metricValues, 1+(len(fakeGPUIDs)*2))
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.XIDCountWindowSize), val.Labels["window_size_in_ms"])
	}

	// Now we check the metric rendering
	var b bytes.Buffer
	err = renderGroup(&b, dcgm.FE_GPU, metrics)
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
		assert.Len(t, mv.Label, 8)
		assert.Equal(t, "gpu", *mv.Label[0].Name)
		assert.Equal(t, "UUID", *mv.Label[1].Name)
		assert.Equal(t, "device", *mv.Label[2].Name)
		assert.Equal(t, "modelName", *mv.Label[3].Name)
		assert.Equal(t, "Hostname", *mv.Label[4].Name)
		assert.Equal(t, "DCGM_FI_DRIVER_VERSION", *mv.Label[5].Name)
		assert.Equal(t, "window_size_in_ms", *mv.Label[6].Name)
		assert.Equal(t, "xid", *mv.Label[7].Name)
		assert.NotEmpty(t, *mv.Label[7].Value)
	}
}

func filterMetrics(metricValues []Metric, condition func(Metric) bool) []Metric {
	var result []Metric
	for _, metricValue := range metricValues {
		if condition(metricValue) {
			result = append(result, metricValue)
		}
	}
	return result
}

func TestXIDCollector_NewXIDCollector(t *testing.T) {
	config := &appconfig.Config{
		GPUDeviceOptions: appconfig.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
	}

	teardownTest := setupTest(t)
	defer teardownTest(t)

	allCounters := []appconfig.Counter{
		{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(allCounters, config)
	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher, int64(config.CollectInterval))
	require.NoError(t, err)

	item, _ := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)

	t.Run("Should Return Error When DCGM_EXP_XID_ERRORS_COUNT is not present", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
		}
		cc, err := extractCounters(records, config)
		require.NoError(t, err)
		require.Len(t, cc.ExporterCounters, 0)
		require.Len(t, cc.DCGMCounters, 1)

		xidCollector, err := NewXIDCollector(cc.DCGMCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, xidCollector)
	})

	t.Run("Should Return Error When Counters Param Is Empty", func(t *testing.T) {
		counters := make([]appconfig.Counter, 0)
		xidCollector, err := NewXIDCollector(counters, "", config, item)
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
		cc, err := extractCounters(records, config)
		require.NoError(t, err)
		for i := range cc.DCGMCounters {
			if cc.DCGMCounters[i].PromType == "label" {
				cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
			}
		}
		xidCollector, err := NewXIDCollector(cc.ExporterCounters, "", config, item)
		require.NoError(t, err)
		require.NotNil(t, xidCollector)
	})
}
