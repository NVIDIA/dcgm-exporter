/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package transformation

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

type fakeContainerRuntime struct {
	byGPU map[string][]containerInfo
	err   error
}

func (f fakeContainerRuntime) ContainersByGPU(context.Context, deviceinfo.Provider) (map[string][]containerInfo, error) {
	return f.byGPU, f.err
}

func TestContainerMapperProcessAddsContainerMetrics(t *testing.T) {
	counter := counters.Counter{FieldID: 155, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
		{GPU: "1", GPUUUID: "GPU-1", GPUDevice: "nvidia1", Attributes: map[string]string{}},
		{GPU: "2", GPUUUID: "GPU-2", GPUDevice: "nvidia2", Attributes: map[string]string{}},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
		"1":     {{Name: "indexer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 5)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
	assert.NotContains(t, got[2].Attributes, containerAttribute)
	assert.Equal(t, "indexer", got[3].Attributes[containerAttribute])
	assert.NotContains(t, got[4].Attributes, containerAttribute)
}

func TestContainerMapperProcessAddsContainerMetricsForDistinctSamples(t *testing.T) {
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_EXP_XID_ERRORS_TOTAL", PromType: "counter"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{
			GPU:        "0",
			GPUUUID:    "GPU-0",
			GPUDevice:  "nvidia0",
			Labels:     map[string]string{"xid": "42"},
			Attributes: map[string]string{hpcJobAttribute: "job-1"},
		},
		{
			GPU:        "0",
			GPUUUID:    "GPU-0",
			GPUDevice:  "nvidia0",
			Labels:     map[string]string{"xid": "46"},
			Attributes: map[string]string{hpcJobAttribute: "job-2"},
		},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 4)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
	assert.Equal(t, "42", got[1].Labels["xid"])
	assert.Equal(t, "job-1", got[1].Attributes[hpcJobAttribute])
	assert.NotContains(t, got[2].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[3].Attributes[containerAttribute])
	assert.Equal(t, "46", got[3].Labels["xid"])
	assert.Equal(t, "job-2", got[3].Attributes[hpcJobAttribute])
}

func TestContainerMapperProcessAddsMetricForEachContainerOnGPU(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer-a"}, {Name: "trainer-b"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 3)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer-a", got[1].Attributes[containerAttribute])
	assert.Equal(t, "trainer-b", got[2].Attributes[containerAttribute])
}

func TestContainerMapperProcessAddsContainerMetricForMIGInstance(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{
			GPU:           "0",
			GPUUUID:       "GPU-0",
			GPUDevice:     "nvidia0",
			MigProfile:    "1g.10gb",
			GPUInstanceID: "8",
			Attributes:    map[string]string{},
		},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"0.8": {{Name: "mig-container"}},
	}}}

	err := mapper.Process(metrics, testMIGDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 2)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "mig-container", got[1].Attributes[containerAttribute])
}

func TestContainerMapperProcessInitializesNilAttributes(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0"},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 2)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
}

func TestContainerMapperProcessSkipsRuntimeWithoutGPUInventory(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
	}}
	runtime := &countingContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}
	mapper := &containerMapper{runtime: runtime}

	err := mapper.Process(metrics, runtimeTestDeviceInfo{})
	require.NoError(t, err)

	assert.Zero(t, runtime.calls)
	require.Len(t, metrics[counter], 1)
	assert.NotContains(t, metrics[counter][0].Attributes, containerAttribute)
}

func TestContainerMapperProcessPreservesAttributesAndAvoidsDuplicateLabels(t *testing.T) {
	counter := counters.Counter{FieldID: 156, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{hpcJobAttribute: "job-1"}},
		{GPU: "1", GPUUUID: "GPU-1", Attributes: map[string]string{containerAttribute: "existing"}},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}, {Name: "trainer"}},
		"GPU-1": {{Name: "should-not-overwrite"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 3)
	assert.Equal(t, "job-1", got[1].Attributes[hpcJobAttribute])
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
	assert.Equal(t, "existing", got[2].Attributes[containerAttribute])
}

func TestContainerMapperProcessLeavesMetricsOnRuntimeError(t *testing.T) {
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{}}}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{err: fmt.Errorf("runtime unavailable")}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	require.Len(t, metrics[counter], 1)
	assert.NotContains(t, metrics[counter][0].Attributes, containerAttribute)
}

func TestContainerMapperName(t *testing.T) {
	assert.Equal(t, "containerMapper", (&containerMapper{}).Name())
}

type countingContainerRuntime struct {
	byGPU map[string][]containerInfo
	calls int
}

func (f *countingContainerRuntime) ContainersByGPU(context.Context, deviceinfo.Provider) (map[string][]containerInfo, error) {
	f.calls++
	return f.byGPU, nil
}
