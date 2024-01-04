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
	"os"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubernetes/pkg/kubelet/apis/podresources/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

var tmpDir string

func TestProcessPodMapper(t *testing.T) {

	testutils.RequireLinux(t)

	cleanup := CreateTmpDir(t)
	defer cleanup()

	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	c, cleanup := testDCGMGPUCollector(t, sampleCounters)
	defer cleanup()

	out, err := c.GetMetrics()
	require.NoError(t, err)
	original := append(out[:0:0], out...)

	socketPath = tmpDir + "/kubelet.sock"
	server := grpc.NewServer()
	gpus := GetGPUUUIDs(original)
	podresourcesapi.RegisterPodResourcesListerServer(server, NewPodResourcesMockServer(gpus))

	cleanup = StartMockServer(t, server, socketPath)
	defer cleanup()

	podMapper, err := NewPodMapper(&Config{KubernetesGPUIdType: GPUUID})
	require.NoError(t, err)
	var sysInfo SystemInfo
	err = podMapper.Process(out, sysInfo)
	require.NoError(t, err)

	require.Len(t, out, len(original))
	for i, dev := range out {
		for _, metric := range dev {
			require.Contains(t, metric.Attributes, podAttribute)
			require.Contains(t, metric.Attributes, namespaceAttribute)
			require.Contains(t, metric.Attributes, containerAttribute)

			// TODO currently we rely on ordering and implicit expectations of the mock implementation
			// This should be a table comparison
			require.Equal(t, metric.Attributes[podAttribute], fmt.Sprintf("gpu-pod-%d", i))
			require.Equal(t, metric.Attributes[namespaceAttribute], "default")
			require.Equal(t, metric.Attributes[containerAttribute], "default")
		}
	}
}

func GetGPUUUIDs(metrics [][]Metric) []string {
	gpus := make([]string, len(metrics))
	for i, dev := range metrics {
		gpus[i] = dev[0].GPUUUID
	}

	return gpus
}

func StartMockServer(t *testing.T, server *grpc.Server, socket string) func() {
	l, err := util.CreateListener("unix://" + socket)
	require.NoError(t, err)

	stopped := make(chan interface{})

	go func() {
		server.Serve(l)
		close(stopped)
	}()

	return func() {
		server.Stop()
		select {
		case <-stopped:
			return
		case <-time.After(1 * time.Second):
			t.Fatal("Failed waiting for gRPC server to stop")
		}
	}
}

func CreateTmpDir(t *testing.T) func() {
	path, err := os.MkdirTemp("", "dcgm-exporter")
	require.NoError(t, err)

	tmpDir = path

	return func() {
		require.NoError(t, os.RemoveAll(tmpDir))
	}
}

// Contains a list of UUIDs
type PodResourcesMockServer struct {
	gpus []string
}

func NewPodResourcesMockServer(used []string) *PodResourcesMockServer {
	return &PodResourcesMockServer{
		gpus: used,
	}
}

func (s *PodResourcesMockServer) List(ctx context.Context, req *podresourcesapi.ListPodResourcesRequest) (*podresourcesapi.ListPodResourcesResponse, error) {
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
							ResourceName: nvidiaResourceName,
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
		KubernetesGPUIDType KubernetesGPUIDType
		MetricGPUID         string
		MetricGPUDevice     string
		MetricMigProfile    string
		PODGPUID            string
	}

	testCases := []TestCase{
		{
			KubernetesGPUIDType: GPUUID,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: GPUUID,
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
		},
		{
			KubernetesGPUIDType: GPUUID,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: DeviceName,
			MetricMigProfile:    "mig",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: DeviceName,
			MetricMigProfile:    "mig",
			PODGPUID:            "nvidia0/gi0",
		},
		{
			KubernetesGPUIDType: DeviceName,
			MetricGPUDevice:     "0",
			PODGPUID:            "0/vgpu",
		},
		{
			KubernetesGPUIDType: GPUUID,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "b8ea3855-276c-c9cb-b366-c6fa655957c5::",
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("when type %s, pod device id %s metric device id %s and gpu device %s",
			tc.KubernetesGPUIDType,
			tc.PODGPUID,
			tc.MetricGPUID,
			tc.MetricGPUDevice,
		),
			func(t *testing.T) {
				cleanup := CreateTmpDir(t)
				defer cleanup()
				socketPath = tmpDir + "/kubelet.sock"
				server := grpc.NewServer()

				cleanup, err := dcgm.Init(dcgm.Embedded)
				require.NoError(t, err)
				defer cleanup()

				gpus := []string{tc.PODGPUID}
				podresourcesapi.RegisterPodResourcesListerServer(server, NewPodResourcesMockServer(gpus))

				cleanup = StartMockServer(t, server, socketPath)
				defer cleanup()

				nvmlGetMIGDeviceInfoByIDHook = func(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
					return &nvmlprovider.MIGDeviceInfo{
						ParentUUID:        "00000000-0000-0000-0000-000000000000",
						GPUInstanceID:     0,
						ComputeInstanceID: 0,
					}, nil
				}

				defer func() {
					nvmlGetMIGDeviceInfoByIDHook = nvmlprovider.GetMIGDeviceInfoByID
				}()

				podMapper, err := NewPodMapper(&Config{KubernetesGPUIdType: tc.KubernetesGPUIDType})
				require.NoError(t, err)
				require.NotNil(t, podMapper)
				metrics := [][]Metric{
					{
						{
							GPU:           "0",
							GPUInstanceID: "0",
							GPUUUID:       tc.MetricGPUID,
							GPUDevice:     tc.MetricGPUDevice,
							Value:         "42",
							MigProfile:    tc.MetricMigProfile,
							Counter: &Counter{
								FieldID:   155,
								FieldName: "DCGM_FI_DEV_POWER_USAGE",
								PromType:  "gauge",
							},
							Attributes: map[string]string{},
						},
					},
				}
				sysInfo := SystemInfo{
					GPUCount: 1,
					GPUs: [32]GPUInfo{
						{
							DeviceInfo: dcgm.Device{
								UUID: "00000000-0000-0000-0000-000000000000",
							},
							MigEnabled: true,
						},
					},
				}
				err = podMapper.Process(metrics, sysInfo)
				require.NoError(t, err)
				assert.Len(t, metrics, 1)
				for _, metric := range metrics[0] {
					require.Contains(t, metric.Attributes, podAttribute)
					require.Contains(t, metric.Attributes, namespaceAttribute)
					require.Contains(t, metric.Attributes, containerAttribute)

					// TODO currently we rely on ordering and implicit expectations of the mock implementation
					// This should be a table comparison
					require.Equal(t, fmt.Sprintf("gpu-pod-%d", 0), metric.Attributes[podAttribute])
					require.Equal(t, "default", metric.Attributes[namespaceAttribute])
					require.Equal(t, "default", metric.Attributes[containerAttribute])
				}
			})
	}
}
