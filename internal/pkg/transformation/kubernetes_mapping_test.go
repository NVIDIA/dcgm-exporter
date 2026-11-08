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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// TestDeviceMappingKeys preserves the alias order used when kubelet identifies
// a dedicated or shared GPU with a GKE, virtual-GPU, or GPU-instance name.
func TestDeviceMappingKeys(t *testing.T) {
	mapper := &PodMapper{}
	testCases := []struct {
		name     string
		deviceID string
		want     []string
	}{
		{
			name:     "ordinary GPU",
			deviceID: "GPU-0",
			want:     []string{"GPU-0"},
		},
		{
			name:     "GKE MIG GPU instance",
			deviceID: "nvidia0/gi3",
			want:     []string{"0-3", "nvidia0/gi3"},
		},
		{
			name:     "GKE virtual GPU",
			deviceID: "GPU-0/vgpu2",
			want:     []string{"GPU-0", "GPU-0/vgpu2"},
		},
		{
			name:     "GPU instance",
			deviceID: "GPU-0::instance-3",
			want:     []string{"GPU-0", "GPU-0::instance-3"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, mapper.deviceMappingKeys(testCase.deviceID, nil))
		})
	}
}

// countingPodIndexer records informer reads while serving real cached pod data.
type countingPodIndexer struct {
	cache.Indexer
	reads int
}

// GetByKey counts each metadata lookup made by the Kubernetes pod lister.
func (i *countingPodIndexer) GetByKey(key string) (any, bool, error) {
	i.reads++
	return i.Indexer.GetByKey(key)
}

// TestDedicatedMappingReadsMetadataOncePerContainer checks multiple GPU resource
// groups keep every device mapping without repeating the container's pod lookup.
func TestDedicatedMappingReadsMetadataOncePerContainer(t *testing.T) {
	indexer := &countingPodIndexer{Indexer: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})}
	require.NoError(t, indexer.Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "gpu-pod", Namespace: "default", Labels: map[string]string{"team": "gpu"},
	}}))
	mapper := &PodMapper{
		Config:           &appconfig.Config{KubernetesEnablePodLabels: true},
		podLister:        corelisters.NewPodLister(indexer),
		labelFilterCache: newLabelFilterCache(nil, 10),
	}
	resources := &podresourcesapi.ListPodResourcesResponse{PodResources: []*podresourcesapi.PodResources{{
		Name: "gpu-pod", Namespace: "default",
		Containers: []*podresourcesapi.ContainerResources{
			{Name: "first", Devices: []*podresourcesapi.ContainerDevices{
				{ResourceName: "example.com/other", DeviceIds: []string{"ignored"}},
				{ResourceName: appconfig.NvidiaResourceName, DeviceIds: []string{"GPU-0", "GPU-1"}},
				{ResourceName: appconfig.NvidiaMigResourcePrefix + "1g.5gb", DeviceIds: []string{"nvidia0/gi3"}},
			}},
			{Name: "second", Devices: []*podresourcesapi.ContainerDevices{
				{ResourceName: appconfig.NvidiaResourceName, DeviceIds: []string{"GPU-3"}},
			}},
		},
	}}}
	mappings := mapper.toDeviceToPod(resources, nil)
	require.Len(t, mappings, 5)
	for device, container := range map[string]string{
		"GPU-0": "first", "GPU-1": "first", "0-3": "first", "nvidia0/gi3": "first", "GPU-3": "second",
	} {
		require.Equal(t, container, mappings[device].Container, device)
		require.Equal(t, "gpu-pod", mappings[device].Name, device)
		require.Equal(t, map[string]string{"team": "gpu"}, mappings[device].Labels, device)
	}
	require.Equal(t, 2, indexer.reads, "each container should read pod metadata once")
}

// TestDeviceMappingKeysFallsBackWhenMIGLookupFails pins the best-effort
// lookup contract: a kubelet MIG UUID still maps to its UUID aliases when
// NVML cannot resolve its parent GPU instance.
func TestDeviceMappingKeysFallsBackWhenMIGLookupFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	previousClient := nvmlprovider.Client()
	t.Cleanup(func() { nvmlprovider.SetClient(previousClient) })
	nvmlprovider.SetClient(mockNVML)
	mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(nil, errors.New("NVML unavailable"))

	mapper := &PodMapper{}

	require.Equal(t, []string{"12345", "MIG-12345"}, mapper.deviceMappingKeys("MIG-12345", nil))
}

const (
	testMIGParentUUID = "GPU-11111111-2222-3333-4444-555555555555"
	testMIGDeviceUUID = "MIG-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testMIGInstanceID = 3
)

// testPodResourcesJSON allocates deviceIDs of resourceName to one pod, in the
// kubelet pod-resources format.
func testPodResourcesJSON(resourceName string, deviceIDs ...string) string {
	return fmt.Sprintf(`{"pod_resources": [{
		"name": "test-pod", "namespace": "test-namespace",
		"containers": [{"name": "test-container", "devices": [{"resource_name": %q, "device_ids": [%s]}]}]
	}]}`, resourceName, `"`+strings.Join(deviceIDs, `","`)+`"`)
}

// testMIGParentGPU is GPU 0 in MIG mode with the GPU instance the test pod uses.
func testMIGParentGPU() deviceinfo.GPUInfo {
	return deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{UUID: testMIGParentUUID, GPU: 0},
		MigEnabled: true,
		GPUInstances: []deviceinfo.GPUInstanceInfo{{
			Info:        dcgm.MigEntityInfo{GpuUuid: testMIGParentUUID, NvmlInstanceId: testMIGInstanceID},
			ProfileName: "1g.10gb",
		}},
	}
}

// testEntityGroupProvider returns a device list for one entity group, as the
// server builds one per group. GPU and NVLink groups hold the MIG parent GPU;
// NVSwitch, CPU and CPU core groups hold no GPUs.
func testEntityGroupProvider(ctrl *gomock.Controller, group dcgm.Field_Entity_Group) *mockdeviceinfo.MockProvider {
	provider := mockdeviceinfo.NewMockProvider(ctrl)
	provider.EXPECT().InfoType().Return(group).AnyTimes()

	if group != dcgm.FE_GPU && group != dcgm.FE_LINK {
		provider.EXPECT().GPUCount().Return(uint(0)).AnyTimes()
		return provider
	}

	provider.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	provider.EXPECT().GPU(uint(0)).Return(testMIGParentGPU()).AnyTimes()

	return provider
}

// testMIGLookupMapper returns a mapper whose MIG lookups resolve only the
// test MIG device and fail for every other UUID.
func testMIGLookupMapper() *PodMapper {
	lookups := newMIGLookupCache(time.Minute)
	lookups.lookupFunc = func(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
		if uuid != testMIGDeviceUUID {
			return nil, errors.New("unknown MIG device")
		}

		return &nvmlprovider.MIGDeviceInfo{ParentUUID: testMIGParentUUID, GPUInstanceID: testMIGInstanceID}, nil
	}

	return &PodMapper{migLookups: lookups}
}

// TestDeviceMappingKeysNeverReturnsEmptyKey checks every valid kubelet device
// ID form. An empty key would match every metric with no GPU identity, such as
// CPU and NVSwitch metrics.
func TestDeviceMappingKeysNeverReturnsEmptyKey(t *testing.T) {
	strippedMIGUUID := testMIGDeviceUUID[len(appconfig.MIG_UUID_PREFIX):]
	testCases := []struct {
		name     string
		deviceID string
		group    dcgm.Field_Entity_Group
		want     []string
	}{
		{
			name:     "MIG device with its parent GPU in the device list",
			deviceID: testMIGDeviceUUID,
			group:    dcgm.FE_GPU,
			want:     []string{"0-3", strippedMIGUUID, testMIGDeviceUUID},
		},
		{
			name:     "MIG device with its parent GPU missing from the device list",
			deviceID: testMIGDeviceUUID,
			group:    dcgm.FE_CPU,
			want:     []string{strippedMIGUUID, testMIGDeviceUUID},
		},
		{
			name:     "MIG device whose lookup fails",
			deviceID: "MIG-unknown",
			group:    dcgm.FE_GPU,
			want:     []string{"unknown", "MIG-unknown"},
		},
		{name: "GKE MIG GPU instance", deviceID: "nvidia0/gi3", group: dcgm.FE_GPU, want: []string{"0-3", "nvidia0/gi3"}},
		{name: "GKE shared GPU", deviceID: "GPU-0/vgpu2", group: dcgm.FE_GPU, want: []string{"GPU-0", "GPU-0/vgpu2"}},
		{name: "GPU instance", deviceID: "GPU-0::instance-3", group: dcgm.FE_GPU, want: []string{"GPU-0", "GPU-0::instance-3"}},
		{name: "plain GPU UUID", deviceID: testMIGParentUUID, group: dcgm.FE_GPU, want: []string{testMIGParentUUID}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			provider := testEntityGroupProvider(gomock.NewController(t), testCase.group)

			keys := testMIGLookupMapper().deviceMappingKeys(testCase.deviceID, provider)

			require.NotContains(t, keys, "")
			require.Equal(t, testCase.want, keys)
		})
	}
}

// testCounter is the counter every enrichment test sample is reported under.
var testCounter = counters.Counter{FieldID: 1, FieldName: "DCGM_FI_TEST", PromType: "gauge"}

// testMetrics puts samples under testCounter.
func testMetrics(samples ...collector.Metric) collector.MetricsByCounter {
	for i := range samples {
		samples[i].Counter = testCounter
		samples[i].Value = "1"
		samples[i].UUID = "UUID"
		samples[i].Labels = map[string]string{}
	}

	return collector.MetricsByCounter{testCounter: samples}
}

// processTestGroup enriches samples as one entity group and returns the
// result. It fails the test unless the result still holds testCounter with
// every sample, so a later label check never passes on an empty set.
func processTestGroup(
	t *testing.T, podMapper *PodMapper, ctrl *gomock.Controller, group dcgm.Field_Entity_Group,
	samples ...collector.Metric,
) collector.MetricsByCounter {
	t.Helper()

	metrics := testMetrics(samples...)
	require.NoError(t, podMapper.Process(metrics, testEntityGroupProvider(ctrl, group)))

	require.Contains(t, metrics, testCounter)
	require.Len(t, metrics[testCounter], len(samples))
	return metrics
}

// testMIGInstanceMetric is a sample of the test MIG instance on GPU 0.
func testMIGInstanceMetric() collector.Metric {
	return collector.Metric{
		GPU: "0", GPUUUID: testMIGParentUUID, GPUDevice: "nvidia0", MigProfile: "1g.10gb", GPUInstanceID: "3",
	}
}

// testNonGPUMetrics returns the samples of a non-GPU entity group. None has a
// GPU UUID. CPU and CPU core samples are built the way the collector builds
// them, with GPUDevice set to the parent CPU ID ("0"). The collector sets no
// GPUDevice on NVSwitch and NVSwitch link samples; they carry "0" here as a
// worst case that the entity-group check must still reject. An NVLink group
// also holds a GPU NVLink sample of GPU 0, built the way the collector builds
// GPU links.
func testNonGPUMetrics(group dcgm.Field_Entity_Group) []collector.Metric {
	switch group {
	case dcgm.FE_SWITCH:
		return []collector.Metric{{GPUDevice: "0", NvSwitch: "nvswitch0", ParentType: dcgm.FE_SWITCH}}
	case dcgm.FE_LINK:
		return []collector.Metric{
			{GPUDevice: "0", NvSwitch: "nvswitch0", NvLink: "3", ParentType: dcgm.FE_SWITCH},
			{GPU: "0", GPUUUID: testMIGParentUUID, GPUDevice: "nvidia0", NvLink: "1", ParentType: dcgm.FE_GPU},
		}
	case dcgm.FE_CPU:
		return []collector.Metric{{GPUDevice: "0", CPU: "0", ParentType: dcgm.FE_CPU}}
	case dcgm.FE_CPU_CORE:
		return []collector.Metric{{GPUDevice: "0", CPU: "0", CPUCore: "5", ParentType: dcgm.FE_CPU}}
	}

	return nil
}

// requirePodAttribution fails the test unless every sample names the test pod
// when wantPod is true, or no sample carries pod, namespace or container
// labels when it is false.
func requirePodAttribution(t *testing.T, metrics collector.MetricsByCounter, wantPod bool) {
	t.Helper()

	for counter, samples := range metrics {
		require.NotEmpty(t, samples, counter.FieldName)
		for _, sample := range samples {
			if !wantPod {
				require.NotContains(t, sample.Attributes, podAttribute, sample)
				require.NotContains(t, sample.Attributes, namespaceAttribute, sample)
				require.NotContains(t, sample.Attributes, containerAttribute, sample)
				continue
			}

			require.Equal(t, "test-pod", sample.Attributes[podAttribute], sample)
			require.Equal(t, "test-namespace", sample.Attributes[namespaceAttribute], sample)
			require.Equal(t, "test-container", sample.Attributes[containerAttribute], sample)
		}
	}
}

// startTestKubelet serves podResourcesJSON on a temporary kubelet
// pod-resources socket and returns the socket path.
func startTestKubelet(t *testing.T, podResourcesJSON string) string {
	t.Helper()

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	t.Cleanup(cleanup)
	socketPath := tmpDir + "/kubelet.sock"

	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, &CustomMockPodResourcesServer{sampleData: podResourcesJSON})
	t.Cleanup(testutils.StartMockServer(t, server, socketPath))

	return socketPath
}

// newTestPodMapper returns a pod mapper that reads podResourcesJSON from a
// test kubelet, resolves only the test MIG device, and sees no per-process
// data in virtual-GPU mode.
func newTestPodMapper(
	t *testing.T, idType appconfig.KubernetesGPUIDType, virtualGPUs bool, podResourcesJSON string,
) *PodMapper {
	t.Helper()

	mockNVML := mocknvmlprovider.NewMockNVML(gomock.NewController(t))
	mockNVML.EXPECT().GetAllMIGDevicesProcessMemory(gomock.Any()).
		Return(nil, errors.New("no process data")).AnyTimes()
	previousClient := nvmlprovider.Client()
	t.Cleanup(func() { nvmlprovider.SetClient(previousClient) })
	nvmlprovider.SetClient(mockNVML)

	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesGPUIdType:       idType,
		KubernetesVirtualGPUs:     virtualGPUs,
		PodResourcesKubeletSocket: startTestKubelet(t, podResourcesJSON),
	})
	t.Cleanup(podMapper.Stop)
	podMapper.migLookups = testMIGLookupMapper().migLookups

	return podMapper
}

// nonGPUGroups are the entity groups whose samples must never name a pod,
// apart from GPU NVLink samples of the pod's own GPU.
var nonGPUGroups = []dcgm.Field_Entity_Group{dcgm.FE_SWITCH, dcgm.FE_LINK, dcgm.FE_CPU, dcgm.FE_CPU_CORE}

// TestPodMapperProcessKeepsMIGPodOffNonGPUEntityGroups enriches each entity
// group separately, as the server does on every scrape, with a pod on a MIG
// device. Only the MIG instance sample may name the pod. The device-name
// cases are guards: they passed before non-GPU samples were kept off pods.
func TestPodMapperProcessKeepsMIGPodOffNonGPUEntityGroups(t *testing.T) {
	testutils.RequireLinux(t)

	testCases := []struct {
		name        string
		idType      appconfig.KubernetesGPUIDType
		virtualGPUs bool
	}{
		{name: "uid", idType: appconfig.GPUUID},
		{name: "device-name", idType: appconfig.DeviceName},
		{name: "uid with virtual GPUs", idType: appconfig.GPUUID, virtualGPUs: true},
		{name: "device-name with virtual GPUs", idType: appconfig.DeviceName, virtualGPUs: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			podMapper := newTestPodMapper(t, testCase.idType, testCase.virtualGPUs,
				testPodResourcesJSON("nvidia.com/mig-1g.10gb", testMIGDeviceUUID))

			t.Run("GPU", func(t *testing.T) {
				metrics := processTestGroup(t, podMapper, ctrl, dcgm.FE_GPU, testMIGInstanceMetric())
				requirePodAttribution(t, metrics, true)
			})

			for _, group := range nonGPUGroups {
				t.Run(group.String(), func(t *testing.T) {
					metrics := processTestGroup(t, podMapper, ctrl, group, testNonGPUMetrics(group)...)
					requirePodAttribution(t, metrics, false)
				})
			}
		})
	}
}

// TestPodMapperProcessKeepsDevicePodOffNonGPUEntityGroups uses device-name
// matching. GPU 0's whole-GPU sample has the collector's device name
// "nvidia0", so kubelet device ID "nvidia0" names the pod on it. Kubelet IDs
// "0" and "0::1" equal a non-GPU sample's parent ID; they name the pod on no
// sample. GPU 0's NVLink sample names the pod exactly when GPU 0's sample
// does. No NVSwitch, NVSwitch link, CPU or CPU core sample names the pod in
// any case.
func TestPodMapperProcessKeepsDevicePodOffNonGPUEntityGroups(t *testing.T) {
	testutils.RequireLinux(t)

	testCases := []struct {
		name        string
		deviceID    string
		virtualGPUs bool
		gpuNamesPod bool
	}{
		{name: "device ID nvidia0", deviceID: "nvidia0", gpuNamesPod: true},
		{name: "device ID nvidia0 with virtual GPUs", deviceID: "nvidia0", virtualGPUs: true, gpuNamesPod: true},
		{name: "device ID 0", deviceID: "0"},
		{name: "device ID 0::1", deviceID: "0::1"},
		{name: "device ID 0 with virtual GPUs", deviceID: "0", virtualGPUs: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			podMapper := newTestPodMapper(t, appconfig.DeviceName, testCase.virtualGPUs,
				testPodResourcesJSON(appconfig.NvidiaResourceName, testCase.deviceID))

			t.Run("GPU", func(t *testing.T) {
				gpuSample := collector.Metric{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0"}
				metrics := processTestGroup(t, podMapper, ctrl, dcgm.FE_GPU, gpuSample)
				requirePodAttribution(t, metrics, testCase.gpuNamesPod)
			})

			for _, group := range nonGPUGroups {
				t.Run(group.String(), func(t *testing.T) {
					for _, sample := range testNonGPUMetrics(group) {
						metrics := processTestGroup(t, podMapper, ctrl, group, sample)

						// A GPU NVLink sample belongs to GPU 0, so it names GPU 0's pod.
						requirePodAttribution(t, metrics, testCase.gpuNamesPod && sample.ParentType == dcgm.FE_GPU)
					}
				})
			}
		})
	}
}
