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
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

func TestProcessPodMapper_WithD_Different_Format_Of_DeviceID(t *testing.T) {
	testutils.RequireLinux(t)
	logrus.SetLevel(logrus.DebugLevel)
	type TestCase struct {
		KubernetesGPUIDType  appconfig.KubernetesGPUIDType
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
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricMigProfile:    "",
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			GPUInstanceID:       3,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:    "",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			GPUInstanceID:       3,
			MetricMigProfile:    "mig",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricMigProfile:    "mig",
			PODGPUIDs:           []string{"nvidia0/gi0"},
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUDevice:     "0",
			PODGPUIDs:           []string{"0/vgpu"},
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::"},
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        "nvidia.com/mig-1g.10gb",
			MetricMigProfile:    "1g.10gb",
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricGPUDevice:     "0",
			GPUInstanceID:       3,
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        "nvidia.com/a100",
			MetricGPUID:         "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:           []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			NvidiaResourceNames: []string{"nvidia.com/a100"},
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricMigProfile:    "1g.10gb",
			GPUInstanceID:       0,
			PODGPUIDs:           []string{"nvidia0/gi0/vgpu0"},
		},
		{
			KubernetesGPUIDType: appconfig.DeviceName,
			ResourceName:        appconfig.NvidiaResourceName,
			MetricMigProfile:    "1g.10gb",
			GPUInstanceID:       1,
			PODGPUIDs:           []string{"nvidia0/gi1/vgpu0"},
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricMigProfile:     "",
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			GPUInstanceID:        3,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			MetricMigProfile:     "",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			GPUInstanceID:        3,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi0"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUDevice:      "0",
			PODGPUIDs:            []string{"0/vgpu"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         "nvidia.com/mig-1g.10gb",
			MetricMigProfile:     "1g.10gb",
			MetricGPUID:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			MetricGPUDevice:      "0",
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         "nvidia.com/a100",
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5"},
			NvidiaResourceNames:  []string{"nvidia.com/a100"},
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi3/vgpu0"},
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"0"},
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			PODGPUIDs:            []string{"nvidia0/vgpu1"},
			MetricGPUDevice:      "nvidia0",
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"1"},
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::2"},
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"2"},
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
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
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricMigProfile:     "mig",
			PODGPUIDs:            []string{"nvidia0/gi3/vgpu0", "nvidia0/gi3/vgpu1"},
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"0", "1"},
		},
		{
			KubernetesGPUIDType:  appconfig.DeviceName,
			ResourceName:         appconfig.NvidiaResourceName,
			PODGPUIDs:            []string{"nvidia0/vgpu1", "nvidia0/vgpu2"},
			MetricGPUDevice:      "nvidia0",
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"1", "2"},
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
			ResourceName:         appconfig.NvidiaResourceName,
			MetricGPUID:          "b8ea3855-276c-c9cb-b366-c6fa655957c5",
			PODGPUIDs:            []string{"b8ea3855-276c-c9cb-b366-c6fa655957c5::2", "b8ea3855-276c-c9cb-b366-c6fa655957c5::3"},
			KubernetesVirtualGPU: true,
			VGPUs:                []string{"2", "3"},
		},
		{
			KubernetesGPUIDType:  appconfig.GPUUID,
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
				tmpDir, cleanup := testutils.CreateTmpDir(t)
				defer cleanup()
				socketPath := tmpDir + "/kubelet.sock"
				server := grpc.NewServer()
				config := &appconfig.Config{
					UseRemoteHE: false,
				}

				dcgmprovider.Initialize(config)
				defer dcgmprovider.Client().Cleanup()

				gpus := tc.PODGPUIDs
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
					NvidiaResourceNames:       tc.NvidiaResourceNames,
					KubernetesVirtualGPUs:     tc.KubernetesVirtualGPU,
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

func TestProcessPodMapper_WithLabels(t *testing.T) {
	testutils.RequireLinux(t)

	pods := []struct {
		name   string
		labels map[string]string
	}{
		{"gpu-pod-0", map[string]string{"valid_label_key": "label-value"}},
		{"gpu-pod-1", map[string]string{"invalid.label/key": "another-value"}},
	}

	// Create fake Kubernetes clientset with pods containing labels
	objects := make([]runtime.Object, len(pods))
	for i, pod := range pods {
		objects[i] = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.name,
				Namespace: "default",
				Labels:    pod.labels,
			},
		}
	}
	clientset := fake.NewSimpleClientset(objects...)

	// Setup mock gRPC server
	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := tmpDir + "/kubelet.sock"

	server := grpc.NewServer()
	gpus := []string{"gpu-uuid-0", "gpu-uuid-1"}
	podresourcesapi.RegisterPodResourcesListerServer(server,
		testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, gpus))
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	// Create PodMapper with label support enabled
	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesEnablePodLabels: true,
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
	})
	// Inject the fake clientset
	podMapper.Client = clientset

	// Setup metrics
	metrics := collector.MetricsByCounter{}
	counter := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}
	for i, gpuUUID := range gpus {
		metrics[counter] = append(metrics[counter], collector.Metric{
			GPU:        fmt.Sprint(i),
			GPUUUID:    gpuUUID,
			Attributes: map[string]string{},
			Labels:     map[string]string{},
			Counter: counters.Counter{
				FieldID:   155,
				FieldName: "DCGM_FI_DEV_POWER_USAGE",
				PromType:  "gauge",
			},
		})
	}

	// Setup mock device info
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGPU := deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			UUID: "00000000-0000-0000-0000-000000000000",
			GPU:  0,
		},
		MigEnabled: false,
	}

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(len(gpus))).AnyTimes()
	for i := range gpus {
		mockDeviceInfo.EXPECT().GPU(uint(i)).Return(mockGPU).AnyTimes()
	}

	// Process metrics
	err := podMapper.Process(metrics, mockDeviceInfo)
	require.NoError(t, err)

	// Verify that labels were added and sanitized correctly
	for i, metric := range metrics[counter] {
		pod := pods[i]

		// Verify pod attributes were set
		require.Contains(t, metric.Attributes, podAttribute)
		require.Contains(t, metric.Attributes, namespaceAttribute)
		require.Contains(t, metric.Attributes, containerAttribute)
		require.Equal(t, pod.name, metric.Attributes[podAttribute])
		require.Equal(t, "default", metric.Attributes[namespaceAttribute])
		require.Equal(t, "default", metric.Attributes[containerAttribute])

		// Verify labels were sanitized and added
		expectedLabelCount := len(pod.labels)
		require.Equal(t, expectedLabelCount, len(metric.Labels),
			"Expected %d labels for pod %s, but got %d", expectedLabelCount, pod.name, len(metric.Labels))

		for key, value := range pod.labels {
			sanitizedKey := utils.SanitizeLabelName(key)
			require.Contains(t, metric.Labels, sanitizedKey,
				"Expected sanitized key '%s' to exist in labels", sanitizedKey)
			require.Equal(t, value, metric.Labels[sanitizedKey],
				"Expected sanitized key '%s' to map to value '%s'", sanitizedKey, value)
		}
	}
}
