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

package transformation

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestProcessPodMapper_WithD_Different_Format_Of_DeviceID(t *testing.T) {
	testutils.RequireLinux(t)

	type TestCase struct {
		KubernetesGPUIDType appconfig.KubernetesGPUIDType
		GPUInstanceID       uint
		ResourceName        string
		MetricGPUID         string
		MetricGPUDevice     string
		MetricMigProfile    string
		PODGPUID            string
	}

	testCases := []TestCase{
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			GPUInstanceID:       3,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			GPUInstanceID:       3,
			MetricMigProfile:    "mig",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricMigProfile:    "mig",
			PODGPUID:            "nvidia0/gi0",
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUDevice:     "0",
			PODGPUID:            "0/vgpu",
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "b8ea3855-276c-c9cb-b366-c6fa655957c5::",
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        "nvidia.com/mig-1g.10gb",
			MetricMigProfile:    "1g.10gb",
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUID:            "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricGPUDevice:     "0",
			GPUInstanceID:       3,
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
				tmpDir, cleanup := testutils.CreateTmpDir(t)
				defer cleanup()
				socketPath := tmpDir + "/kubelet.sock"
				server := grpc.NewServer()

				config := &appconfig.Config{
					UseRemoteHE: false,
				}

				dcgmprovider.Initialize(config)
				defer dcgmprovider.Client().Cleanup()

				gpus := []string{tc.PODGPUID}
				podresourcesapi.RegisterPodResourcesListerServer(server,
					testutils.NewMockPodResourcesServer(tc.ResourceName, gpus))

				cleanup = testutils.StartMockServer(t, server, socketPath)
				defer cleanup()

				migDeviceInfo := &nvmlprovider.MIGDeviceInfo{
					ParentUUID:        "00000000-0000-0000-0000-000000000000",
					GPUInstanceID:     3,
					ComputeInstanceID: 0,
				}

				ctrl := gomock.NewController(t)
				mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID(gomock.Any()).Return(migDeviceInfo, nil).AnyTimes()
				nvmlprovider.SetClient(mockNVMLProvider)

				podMapper := NewPodMapper(&appconfig.Config{
					KubernetesGPUIdType:       tc.KubernetesGPUIDType,
					PodResourcesKubeletSocket: socketPath,
				})
				require.NotNil(t, podMapper)
				metrics := collector.MetricsByCounter{}
				counter := counters.Counter{
					FieldID:   155,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
				}

				metrics[counter] = append(metrics[counter], collector.Metric{
					GPU:           "0",
					GPUUUID:       tc.MetricGPUID,
					GPUDevice:     tc.MetricGPUDevice,
					GPUInstanceID: fmt.Sprint(tc.GPUInstanceID),
					Value:         "42",
					MigProfile:    tc.MetricMigProfile,
					Counter: counters.Counter{
						FieldID:   155,
						FieldName: "DCGM_FI_DEV_POWER_USAGE",
						PromType:  "gauge",
					},
					Attributes: map[string]string{},
				})

				mockGPU := deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{
						UUID: "00000000-0000-0000-0000-000000000000",
						GPU:  0,
					},
					MigEnabled: true,
				}

				mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockSystemInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockSystemInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

				err := podMapper.Process(metrics, mockSystemInfo)
				require.NoError(t, err)
				assert.Len(t, metrics, 1)
				for _, metric := range metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)] {
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
