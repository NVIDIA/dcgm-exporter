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

package integration

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	config2 "github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/server"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/utils"
)

func TestXIDCollector_Gather_Encode(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)
	runOnlyWithLiveGPUs(t)

	hostname := "local-test"
	config := &config2.Config{
		GPUDevices: config2.DeviceOptions{
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

	cc, err := utils.ExtractCounters(records, config)
	require.NoError(t, err)
	require.Len(t, cc.ExporterCounters, 1)
	require.Len(t, cc.DCGMCounters, 1)

	for i := range cc.DCGMCounters {
		if cc.DCGMCounters[i].PromType == "label" {
			cc.ExporterCounters = append(cc.ExporterCounters, cc.DCGMCounters[i])
		}
	}

	// Create fake GPU
	numGPUs, err := dcgmClient.Client().GetAllDeviceCount()
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

	for i, gpuID := range gpuIDs {
		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(42),
		)
		require.NoError(t, err)

		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(42),
		)
		require.NoError(t, err)

		err = dcgm.InjectFieldValue(gpuID,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FT_INT64,
			0,
			time.Now().Add(-time.Duration(i)*time.Second).UnixMicro(),
			int64(46),
		)
		require.NoError(t, err)

	}

	allCounters := []config2.Counter{
		config2.Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := sysinfo.NewEntityGroupTypeSystemInfo(allCounters, config)
	err = fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
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
	metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(config2.Counter)]
	// We expect 6 records, because we have 3 fake GPU and each GPU experienced 2 XID errors: 42 and 46
	require.Len(t, metricValues, 6)
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.XIDCountWindowSize), val.Labels["window_size_in_ms"])
	}

	// We inject new error
	err = dcgm.InjectFieldValue(gpuIDs[0],
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
	metricValues = metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(config2.Counter)]
	require.Len(t, metricValues, 6+1)
	for _, val := range metricValues {
		require.Contains(t, val.Labels, "window_size_in_ms")
		require.Equal(t, fmt.Sprint(config.XIDCountWindowSize), val.Labels["window_size_in_ms"])
	}

	// Now we check the metric rendering
	var b bytes.Buffer
	// TODO
	err = server.EncodeExpMetrics(&b, metrics)
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
	require.Len(t, metricFamily.Metric, 6+1)
	assert.Len(t, metricFamily.Metric[0].Label, 8)
	assert.Equal(t, "gpu", *metricFamily.Metric[0].Label[0].Name)
	assert.Equal(t, "UUID", *metricFamily.Metric[0].Label[1].Name)
	assert.Equal(t, "device", *metricFamily.Metric[0].Label[2].Name)
	assert.Equal(t, "modelName", *metricFamily.Metric[0].Label[3].Name)
	assert.Equal(t, "Hostname", *metricFamily.Metric[0].Label[4].Name)
	assert.Equal(t, "DCGM_FI_DRIVER_VERSION", *metricFamily.Metric[0].Label[5].Name)
	assert.Equal(t, "window_size_in_ms", *metricFamily.Metric[0].Label[6].Name)
	assert.Equal(t, "xid", *metricFamily.Metric[0].Label[7].Name)
	assert.NotEmpty(t, *metricFamily.Metric[0].Label[7].Value)
}

func TestXIDCollector_NewXIDCollector(t *testing.T) {
	config := &config2.Config{
		GPUDevices: config2.DeviceOptions{
			Flex:       true,
			MajorRange: []int{-1},
			MinorRange: []int{-1},
		},
	}

	teardownTest := setupTest(t)
	defer teardownTest(t)

	allCounters := []config2.Counter{
		config2.Counter{
			FieldID: dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS,
		},
	}

	fieldEntityGroupTypeSystemInfo := sysinfo.NewEntityGroupTypeSystemInfo(allCounters, config)
	err := fieldEntityGroupTypeSystemInfo.Load(dcgm.FE_GPU)
	require.NoError(t, err)

	item, _ := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)

	t.Run("Should Return Error When DCGM_EXP_XID_ERRORS_COUNT is not present", func(t *testing.T) {
		records := [][]string{
			{"DCGM_FI_DRIVER_VERSION", "label", "Driver Version"},
		}
		cc, err := utils.ExtractCounters(records, config)
		require.NoError(t, err)
		require.Len(t, cc.ExporterCounters, 0)
		require.Len(t, cc.DCGMCounters, 1)

		xidCollector, err := collector.NewXIDCollector(cc.DCGMCounters, "", config, item)
		require.Error(t, err)
		require.Nil(t, xidCollector)
	})

	t.Run("Should Return Error When Counters Param Is Empty", func(t *testing.T) {
		counters := make([]config2.Counter, 0)
		xidCollector, err := collector.NewXIDCollector(counters, "", config, item)
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
		cc, err := utils.ExtractCounters(records, config)
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
