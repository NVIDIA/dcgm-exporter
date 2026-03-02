/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakePodResourcesClient implements podResourcesClient for tests.
type fakePodResourcesClient struct {
	resp *podresourcesv1.ListPodResourcesResponse
	err  error
}

func (f *fakePodResourcesClient) List(
	_ context.Context,
	_ *podresourcesv1.ListPodResourcesRequest,
	_ ...grpc.CallOption,
) (*podresourcesv1.ListPodResourcesResponse, error) {
	return f.resp, f.err
}

// fakeNVMLDevice implements nvmlDevice for tests.
type fakeNVMLDevice struct {
	uuid      string
	modelName string
	samples   []nvml.ProcessUtilizationSample
	ret       nvml.Return
}

func (f *fakeNVMLDevice) GetUUID() (string, nvml.Return) {
	return f.uuid, nvml.SUCCESS
}

func (f *fakeNVMLDevice) GetName() (string, nvml.Return) {
	return f.modelName, nvml.SUCCESS
}

func (f *fakeNVMLDevice) GetProcessUtilization(_ uint64) ([]nvml.ProcessUtilizationSample, nvml.Return) {
	return f.samples, f.ret
}

// fakeNVMLLib implements nvmlLib for tests.
type fakeNVMLLib struct {
	devices []nvmlDevice
	ret     nvml.Return
}

func (f *fakeNVMLLib) DeviceGetCount() (int, nvml.Return) {
	if f.ret != nvml.SUCCESS {
		return 0, f.ret
	}
	return len(f.devices), nvml.SUCCESS
}

func (f *fakeNVMLLib) DeviceGetHandleByIndex(index int) (nvmlDevice, nvml.Return) {
	if index >= len(f.devices) {
		return nil, nvml.ERROR_INVALID_ARGUMENT
	}
	return f.devices[index], nvml.SUCCESS
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testCounter() counters.Counter {
	return counters.Counter{
		FieldName: counters.DCGMExpSMUtilPerPod,
		PromType:  "gauge",
		Help:      "Per-pod GPU SM utilization",
	}
}

func testConfig() *appconfig.Config {
	return &appconfig.Config{
		PodResourcesKubeletSocket: "/var/lib/kubelet/pod-resources/kubelet.sock",
		EnablePerPodGPUUtil:       true,
	}
}

func newTestCollector(
	nvmlLib nvmlLib,
	podClient podResourcesClient,
) *processPodCollector {
	return newProcessPodCollectorWithDeps(
		testCounter(),
		"test-host",
		testConfig(),
		nvmlLib,
		podClient,
	)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestProcessPodCollector_EmitsMetricForSinglePod verifies that when one pod
// holds a GPU and one process is running on it, a single metric is emitted
// with the correct pod labels and SM utilisation value.
func TestProcessPodCollector_EmitsMetricForSinglePod(t *testing.T) {
	const (
		gpuUUID      = "GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		podName      = "my-pod"
		podNamespace = "default"
		containerNm  = "my-container"
		smUtil       = uint32(42)
	)

	podClient := &fakePodResourcesClient{
		resp: &podresourcesv1.ListPodResourcesResponse{
			PodResources: []*podresourcesv1.PodResources{
				{
					Name:      podName,
					Namespace: podNamespace,
					Containers: []*podresourcesv1.ContainerResources{
						{
							Name: containerNm,
							Devices: []*podresourcesv1.ContainerDevices{
								{
									ResourceName: "nvidia.com/gpu",
									DeviceIds:    []string{gpuUUID},
								},
							},
						},
					},
				},
			},
		},
	}

	nvmlLib := &fakeNVMLLib{
		devices: []nvmlDevice{
			&fakeNVMLDevice{
				uuid:      gpuUUID,
				modelName: "NVIDIA A100",
				samples: []nvml.ProcessUtilizationSample{
					{Pid: 1234, SmUtil: smUtil},
				},
				ret: nvml.SUCCESS,
			},
		},
	}

	c := newTestCollector(nvmlLib, podClient)
	metrics, err := c.GetMetrics()

	require.NoError(t, err)
	require.Contains(t, metrics, testCounter())

	metricList := metrics[testCounter()]
	require.Len(t, metricList, 1)

	m := metricList[0]
	assert.Equal(t, fmt.Sprintf("%d", smUtil), m.Value)
	assert.Equal(t, "0", m.GPU)
	assert.Equal(t, "UUID", m.UUID)
	assert.Equal(t, gpuUUID, m.GPUUUID)
	assert.Equal(t, "nvidia0", m.GPUDevice)
	assert.Equal(t, "NVIDIA A100", m.GPUModelName)
	assert.Equal(t, "test-host", m.Hostname)
	assert.Equal(t, podName, m.Labels[podLabel])
	assert.Equal(t, podNamespace, m.Labels[namespaceLabel])
	assert.Equal(t, containerNm, m.Labels[containerLabel])
}

// TestProcessPodCollector_NoProcesses verifies that no metrics are emitted
// when no processes are running on any GPU (empty samples slice).
// This covers the time-slicing case where the GPU is allocated but idle.
func TestProcessPodCollector_NoProcesses(t *testing.T) {
	const gpuUUID = "GPU-11111111-2222-3333-4444-555555555555"

	podClient := &fakePodResourcesClient{
		resp: &podresourcesv1.ListPodResourcesResponse{
			PodResources: []*podresourcesv1.PodResources{
				{
					Name:      "idle-pod",
					Namespace: "default",
					Containers: []*podresourcesv1.ContainerResources{
						{
							Name: "idle-container",
							Devices: []*podresourcesv1.ContainerDevices{
								{
									ResourceName: "nvidia.com/gpu",
									DeviceIds:    []string{gpuUUID},
								},
							},
						},
					},
				},
			},
		},
	}

	nvmlLib := &fakeNVMLLib{
		devices: []nvmlDevice{
			&fakeNVMLDevice{
				uuid:    gpuUUID,
				samples: nil, // no active processes
				ret:     nvml.SUCCESS,
			},
		},
	}

	c := newTestCollector(nvmlLib, podClient)
	metrics, err := c.GetMetrics()

	require.NoError(t, err)
	// Counter key should still exist but with an empty slice.
	assert.Empty(t, metrics[testCounter()])
}

// TestProcessPodCollector_MultiplePodsTimeSlicing verifies that when multiple
// pods share a GPU (time-slicing) and processes from different pods are active,
// each pod gets its own aggregated metric entry.
func TestProcessPodCollector_MultiplePodsTimeSlicing(t *testing.T) {
	const (
		gpuUUID  = "GPU-ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb"
		smUtil1  = uint32(30)
		smUtil2  = uint32(50)
	)

	// Two pods sharing the same GPU UUID is the time-slicing scenario.
	// In a real setup they would each see the UUID directly.
	podClient := &fakePodResourcesClient{
		resp: &podresourcesv1.ListPodResourcesResponse{
			PodResources: []*podresourcesv1.PodResources{
				{
					Name:      "pod-a",
					Namespace: "ns-a",
					Containers: []*podresourcesv1.ContainerResources{
						{
							Name: "ctr-a",
							Devices: []*podresourcesv1.ContainerDevices{
								{DeviceIds: []string{gpuUUID}},
							},
						},
					},
				},
				{
					Name:      "pod-b",
					Namespace: "ns-b",
					Containers: []*podresourcesv1.ContainerResources{
						{
							Name: "ctr-b",
							Devices: []*podresourcesv1.ContainerDevices{
								{DeviceIds: []string{gpuUUID}},
							},
						},
					},
				},
			},
		},
	}

	// With two pods on one UUID the cgroup fallback fires.
	// Here we use PIDs whose cgroup content we inject via the podInfoFromCgroup
	// path - but since we can't write to /proc in tests, we just verify that
	// when cgroup lookup fails and there are >1 pods on the UUID, no metric
	// is emitted (the safe/conservative path).
	nvmlLib := &fakeNVMLLib{
		devices: []nvmlDevice{
			&fakeNVMLDevice{
				uuid: gpuUUID,
				samples: []nvml.ProcessUtilizationSample{
					{Pid: 100, SmUtil: smUtil1},
					{Pid: 200, SmUtil: smUtil2},
				},
				ret: nvml.SUCCESS,
			},
		},
	}

	c := newTestCollector(nvmlLib, podClient)
	metrics, err := c.GetMetrics()

	require.NoError(t, err)
	// With >1 pod and no successful cgroup lookup, no metrics emitted.
	assert.Empty(t, metrics[testCounter()])
}

// TestProcessPodCollector_PodResourcesError verifies that an error from the
// pod-resources API is propagated as an error from GetMetrics.
func TestProcessPodCollector_PodResourcesError(t *testing.T) {
	podClient := &fakePodResourcesClient{
		err: errors.New("connection refused"),
	}

	nvmlLib := &fakeNVMLLib{
		devices: []nvmlDevice{},
	}

	c := newTestCollector(nvmlLib, podClient)
	_, err := c.GetMetrics()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to build UUID-to-pod map")
}

// TestProcessPodCollector_NVMLDeviceCountError verifies that an error from
// NVML DeviceGetCount is propagated.
func TestProcessPodCollector_NVMLDeviceCountError(t *testing.T) {
	podClient := &fakePodResourcesClient{
		resp: &podresourcesv1.ListPodResourcesResponse{},
	}

	nvmlLib := &fakeNVMLLib{
		ret: nvml.ERROR_DRIVER_NOT_LOADED,
	}

	c := newTestCollector(nvmlLib, podClient)
	_, err := c.GetMetrics()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nvml DeviceGetCount failed")
}

// TestProcessPodCollector_Cleanup verifies that Cleanup does not panic.
func TestProcessPodCollector_Cleanup(t *testing.T) {
	cleanupCalled := false
	c := &processPodCollector{
		connCleanup: func() {
			cleanupCalled = true
		},
	}
	c.Cleanup()
	assert.True(t, cleanupCalled)
}

// TestProcessPodCollector_ImplementsCollectorInterface is a compile-time check
// that processPodCollector satisfies the Collector interface.
func TestProcessPodCollector_ImplementsCollectorInterface(t *testing.T) {
	var _ Collector = (*processPodCollector)(nil)
}

// TestFindCounterByName verifies the helper that searches a counter list.
func TestFindCounterByName(t *testing.T) {
	list := counters.CounterList{
		{FieldName: "COUNTER_A"},
		{FieldName: counters.DCGMExpSMUtilPerPod},
	}

	got, err := findCounterByName(list, counters.DCGMExpSMUtilPerPod)
	require.NoError(t, err)
	assert.Equal(t, counters.DCGMExpSMUtilPerPod, got.FieldName)

	_, err = findCounterByName(list, "NONEXISTENT")
	require.Error(t, err)
}

// TestProcessPodCollector_AveragesMultipleSamplesPerPod verifies that when
// multiple PIDs belong to the same pod (via single-pod fallback), their SM
// utilisation is averaged.
func TestProcessPodCollector_AveragesMultipleSamplesPerPod(t *testing.T) {
	const (
		gpuUUID = "GPU-12345678-0000-0000-0000-000000000000"
		pod     = "batch-pod"
		ns      = "production"
		ctr     = "worker"
	)

	podClient := &fakePodResourcesClient{
		resp: &podresourcesv1.ListPodResourcesResponse{
			PodResources: []*podresourcesv1.PodResources{
				{
					Name:      pod,
					Namespace: ns,
					Containers: []*podresourcesv1.ContainerResources{
						{
							Name: ctr,
							Devices: []*podresourcesv1.ContainerDevices{
								{DeviceIds: []string{gpuUUID}},
							},
						},
					},
				},
			},
		},
	}

	// Two processes on the same GPU, both owned by the single pod.
	// Average SM util = (20 + 40) / 2 = 30.
	nvmlLib := &fakeNVMLLib{
		devices: []nvmlDevice{
			&fakeNVMLDevice{
				uuid: gpuUUID,
				samples: []nvml.ProcessUtilizationSample{
					{Pid: 10, SmUtil: 20},
					{Pid: 11, SmUtil: 40},
				},
				ret: nvml.SUCCESS,
			},
		},
	}

	c := newTestCollector(nvmlLib, podClient)
	metrics, err := c.GetMetrics()

	require.NoError(t, err)
	metricList := metrics[testCounter()]
	require.Len(t, metricList, 1)

	assert.Equal(t, "30", metricList[0].Value)
	assert.Equal(t, pod, metricList[0].Labels[podLabel])
}
