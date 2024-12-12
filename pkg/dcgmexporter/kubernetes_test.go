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
	"context"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestProcessPodMapper(t *testing.T) {
	var kubeVirtualGPUs = []bool{false, true}
	for _, virtual := range kubeVirtualGPUs {
		testutils.RequireLinux(t)

		tmpDir, cleanup := CreateTmpDir(t)
		defer cleanup()

		cleanup, err := dcgm.Init(dcgm.Embedded)
		require.NoError(t, err)
		defer cleanup()

		c, cleanup := testDCGMGPUCollector(t, sampleCounters)
		defer cleanup()

		out, err := c.GetMetrics()
		require.NoError(t, err)

		original := out

		arbirtaryMetric := out[reflect.ValueOf(out).MapKeys()[0].Interface().(Counter)]

		socketPath := tmpDir + "/kubelet.sock"
		server := grpc.NewServer()
		gpus := GetGPUUUIDs(arbirtaryMetric)
		podresourcesapi.RegisterPodResourcesListerServer(server, NewPodResourcesMockServer(nvidiaResourceName, gpus))

		cleanup = StartMockServer(t, server, socketPath)
		defer cleanup()

		podMapper, err := NewPodMapper(&Config{KubernetesGPUIdType: GPUUID, PodResourcesKubeletSocket: socketPath, KubernetesVirtualGPUs: virtual})
		require.NoError(t, err)
		var sysInfo SystemInfo

		err = podMapper.Process(out, sysInfo)
		require.NoError(t, err)

		require.Len(t, out, len(original))
		for _, metrics := range out {
			for _, metric := range metrics {
				require.Contains(t, metric.Attributes, podAttribute)
				require.Contains(t, metric.Attributes, namespaceAttribute)
				require.Contains(t, metric.Attributes, containerAttribute)
				require.Equal(t, metric.Attributes[podAttribute], fmt.Sprintf("gpu-pod-%s", metric.GPU))
				require.Equal(t, metric.Attributes[namespaceAttribute], "default")
				require.Equal(t, metric.Attributes[containerAttribute], "default")
			}
		}
	}
}

func GetGPUUUIDs(metrics []Metric) []string {
	gpus := make([]string, len(metrics))
	for i, dev := range metrics {
		gpus[i] = dev.GPUUUID
	}

	return gpus
}

func StartMockServer(t *testing.T, server *grpc.Server, socket string) func() {
	l, err := net.Listen("unix", socket)
	require.NoError(t, err)

	stopped := make(chan interface{})

	go func() {
		err := server.Serve(l)
		assert.NoError(t, err)
		close(stopped)
	}()

	return func() {
		server.Stop()
		select {
		case <-stopped:
			return
		case <-time.After(1 * time.Second):
			t.Fatal("Failed waiting for gRPC server to stop.")
		}
	}
}

func CreateTmpDir(t *testing.T) (string, func()) {
	path, err := os.MkdirTemp("", "dcgm-exporter")
	require.NoError(t, err)

	return path, func() {
		require.NoError(t, os.RemoveAll(path))
	}
}

// Contains a list of UUIDs
type PodResourcesMockServer struct {
	resourceName string
	gpus         []string
}

func NewPodResourcesMockServer(resourceName string, gpus []string) *PodResourcesMockServer {
	return &PodResourcesMockServer{
		resourceName: resourceName,
		gpus:         gpus,
	}
}

func (s *PodResourcesMockServer) List(
	ctx context.Context, req *podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	podResources := make([]*podresourcesapi.PodResources, len(s.gpus))

	for i, gpu := range s.gpus {
		podResources[i] = &podresourcesapi.PodResources{
			Name:      fmt.Sprintf("gpu-pod-%d", i),
			Namespace: "default",
			Containers: []*podresourcesapi.ContainerResources{
				{
					Name: "default",
					Devices: []*podresourcesapi.ContainerDevices{
						{
							ResourceName: s.resourceName,
							DeviceIds:    []string{gpu},
						},
					},
				},
			},
		}
	}

	return &podresourcesapi.ListPodResourcesResponse{
		PodResources: podResources,
	}, nil
}

func TestProcessPodMapper_WithD_Different_Format_Of_DeviceID(t *testing.T) {
	testutils.RequireLinux(t)

	type TestCase struct {
		KubernetesGPUIDType  KubernetesGPUIDType
		GPUInstanceID        uint
		ResourceName         string
		MetricGPUID          string
		MetricGPUDevice      string
		MetricMigProfile     string
		PODGPUIDs            []string
		NvidiaResourceNames  []string
		KubernetesVirtualGPU bool
		VGPUs                []string
	}

	testCases := []TestCase{
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        nvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        nvidiaResourceName,
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricMigProfile:    "",
		},
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        nvidiaResourceName,
			GPUInstanceID:       3,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: DeviceName,
			ResourceName:        nvidiaResourceName,
			GPUInstanceID:       3,
			MetricMigProfile:    "mig",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: DeviceName,
			ResourceName:        nvidiaResourceName,
			MetricMigProfile:    "mig",
			PODGPUIDs:           []string{"nvidia0/gi0"},
		},
		{
			KubernetesGPUIDType: DeviceName,
			ResourceName:        nvidiaResourceName,
			MetricGPUDevice:     "0",
			PODGPUIDs:           []string{"0/vgpu"},
		},
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        nvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::"},
		},
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        "nvidia.com/mig-1g.10gb",
			MetricMigProfile:    "1g.10gb",
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricGPUDevice:     "0",
			GPUInstanceID:       3,
		},
		{
			KubernetesGPUIDType: GPUUID,
			ResourceName:        "nvidia.com/a100",
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			NvidiaResourceNames: []string{"nvidia.com/a100"},
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricMigProfile:     "",
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			GPUInstanceID:        3,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:     "",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			GPUInstanceID:        3,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi0"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			MetricGPUDevice:      "0",
			PODGPUIDs:            []string{"0/vgpu"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         "nvidia.com/mig-1g.10gb",
			MetricMigProfile:     "1g.10gb",
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricGPUDevice:      "0",
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         "nvidia.com/a100",
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			NvidiaResourceNames:  []string{"nvidia.com/a100"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi3/vgpu0"},
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"0"},
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			PODGPUIDs:            []string{"nvidia0/vgpu1"},
			MetricGPUDevice:      "nvidia0",
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"1"},
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::2"},
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"2"},
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         "nvidia.com/mig-1g.10gb",
			MetricMigProfile:     "1g.10gb",
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5::4"},
			MetricGPUDevice:      "0",
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"4"},
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi3/vgpu0", "nvidia0/gi3/vgpu1"},
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"0", "1"},
		},
		{
			KubernetesGPUIDType:  DeviceName,
			ResourceName:         nvidiaResourceName,
			PODGPUIDs:            []string{"nvidia0/vgpu1", "nvidia0/vgpu2"},
			MetricGPUDevice:      "nvidia0",
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"1", "2"},
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         nvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::2", "b8ea3855-276c-c9cb-b366-c6fa655957c5::3"},
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"2", "3"},
		},
		{
			KubernetesGPUIDType:  GPUUID,
			ResourceName:         "nvidia.com/mig-1g.10gb",
			MetricMigProfile:     "1g.10gb",
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5::4", "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5::5"},
			MetricGPUDevice:      "0",
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"4", "5"},
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("when type %s, pod device ids %s metric device id %s and gpu device %s with virtual GPUs: %t",
			tc.KubernetesGPUIDType,
			tc.PODGPUIDs,
			tc.MetricGPUID,
			tc.MetricGPUDevice,
			tc.KubernetesVirtualGPU,
		),
			func(t *testing.T) {
				tmpDir, cleanup := CreateTmpDir(t)
				defer cleanup()
				socketPath := tmpDir + "/kubelet.sock"
				server := grpc.NewServer()

				cleanup, err := dcgm.Init(dcgm.Embedded)
				require.NoError(t, err)
				defer cleanup()

				gpus := tc.PODGPUIDs
				podresourcesapi.RegisterPodResourcesListerServer(server, NewPodResourcesMockServer(tc.ResourceName, gpus))

				cleanup = StartMockServer(t, server, socketPath)
				defer cleanup()

				nvmlGetMIGDeviceInfoByIDHook = func(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
					return &nvmlprovider.MIGDeviceInfo{
						ParentUUID:        "00000000-0000-0000-0000-000000000000",
						GPUInstanceID:     3,
						ComputeInstanceID: 0,
					}, nil
				}

				defer func() {
					nvmlGetMIGDeviceInfoByIDHook = nvmlprovider.GetMIGDeviceInfoByID
				}()

				podMapper, err := NewPodMapper(&Config{
					KubernetesGPUIdType:       tc.KubernetesGPUIDType,
					PodResourcesKubeletSocket: socketPath,
					NvidiaResourceNames:       tc.NvidiaResourceNames,
					KubernetesVirtualGPUs:     tc.KubernetesVirtualGPU,
				})
				require.NoError(t, err)
				require.NotNil(t, podMapper)
				metrics := MetricsByCounter{}
				counter := Counter{
					FieldID:   155,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
				}

				metrics[counter] = append(metrics[counter], Metric{
					GPU:           "0",
					GPUUUID:       tc.MetricGPUID,
					GPUDevice:     tc.MetricGPUDevice,
					GPUInstanceID: fmt.Sprint(tc.GPUInstanceID),
					Value:         "42",
					MigProfile:    tc.MetricMigProfile,
					Counter: Counter{
						FieldID:   155,
						FieldName: "DCGM_FI_DEV_POWER_USAGE",
						PromType:  "gauge",
					},
					Attributes: map[string]string{},
				})

				sysInfo := SystemInfo{
					GPUCount: 1,
					GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
						{
							DeviceInfo: dcgm.Device{
								UUID: "00000000-0000-0000-0000-000000000000",
								GPU:  0,
							},
							MigEnabled: true,
						},
					},
				}
				err = podMapper.Process(metrics, sysInfo)
				require.NoError(t, err)
				assert.Len(t, metrics, 1)
				if tc.KubernetesVirtualGPU {
					assert.Len(t, metrics[counter], len(gpus))
				}

				for i, metric := range metrics[counter] {
					require.Contains(t, metric.Attributes, podAttribute)
					require.Contains(t, metric.Attributes, namespaceAttribute)
					require.Contains(t, metric.Attributes, containerAttribute)

					// TODO currently we rely on ordering and implicit expectations of the mock implementation
					// This should be a table comparison
					require.Equal(t, fmt.Sprintf("gpu-pod-%d", i), metric.Attributes[podAttribute])
					require.Equal(t, "default", metric.Attributes[namespaceAttribute])
					require.Equal(t, "default", metric.Attributes[containerAttribute])

					// Assert virtual GPU attributes.
					vgpu, ok := metric.Attributes[vgpuAttribute]
					// Ensure vgpu attribute only exists when vgpu is enabled.
					if ok && !tc.KubernetesVirtualGPU {
						t.Errorf("%s attribute should not be present unless configured", vgpuAttribute)
					}
					// Ensure we only populate non-empty values for the vgpu attribute.
					if ok {
						require.NotEqual(t, "", vgpu)
						require.Equal(t, tc.VGPUs[i], vgpu)
					}
				}
			})
	}
}

func TestGetSharedGPU(t *testing.T) {
	cases := []struct {
		desc, deviceID string
		wantVGPU       string
		wantOK         bool
	}{
		{
			desc:     "gke device plugin, non-mig, shared",
			deviceID: "nvidia0/vgpu0",
			wantVGPU: "0",
			wantOK:   true,
		},
		{
			desc:     "gke device plugin, non-mig, non-shared",
			deviceID: "nvidia0",
		},
		{
			desc:     "gke device plugin, mig, shared",
			deviceID: "nvidia0/gi0/vgpu1",
			wantVGPU: "1",
			wantOK:   true,
		},
		{
			desc:     "gke device plugin, mig, non-shared",
			deviceID: "nvidia0/gi0",
		},
		{
			desc:     "nvidia device plugin, non-mig, shared",
			deviceID: "GPU-5a5a7118-e550-79a1-597e-7631e126c57a::3",
			wantVGPU: "3",
			wantOK:   true,
		},
		{
			desc:     "nvidia device plugin, non-mig, non-shared",
			deviceID: "GPU-5a5a7118-e550-79a1-597e-7631e126c57a",
		},
		{
			desc:     "nvidia device plugin, mig, shared",
			deviceID: "MIG-42f0f413-f7b0-58cc-aced-c1d1fb54db26::0",
			wantVGPU: "0",
			wantOK:   true,
		},
		{
			desc:     "nvidia device plugin, mig, non-shared",
			deviceID: "MIG-42f0f413-f7b0-58cc-aced-c1d1fb54db26",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotVGPU, gotOK := getSharedGPU(tc.deviceID)
			if gotVGPU != tc.wantVGPU {
				t.Errorf("expected: %s, got: %s", tc.wantVGPU, gotVGPU)
			}
			if gotOK != tc.wantOK {
				t.Errorf("expected: %t, got: %t", tc.wantOK, gotOK)
			}
		})
	}
}
