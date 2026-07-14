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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdos "os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
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

//nolint:gosec // G115: test helper for non-negative int-to-uint conversion
func toUint(v int) uint {
	return uint(v)
}

func TestNewPodMapperOutsideClusterDefaults(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	pm := NewPodMapper(&appconfig.Config{
		KubernetesPodLabelAllowlistRegex: []string{"["},
		KubernetesPodLabelCacheSize:      -1,
	})

	require.NotNil(t, pm)
	assert.Equal(t, "podMapper", pm.Name())
	assert.Equal(t, 150000, pm.labelFilterCache.maxSize)
	assert.False(t, pm.labelFilterCache.enabled)

	pm.Run()
	pm.Stop()
}

func restorePodMapperSeams(t *testing.T) {
	t.Helper()
	prevInClusterConfig := inClusterConfigFunc
	prevNewKubernetes := newKubernetesForConfigFunc
	prevNewDRA := newDRAResourceSliceManagerFunc
	t.Cleanup(func() {
		inClusterConfigFunc = prevInClusterConfig
		newKubernetesForConfigFunc = prevNewKubernetes
		newDRAResourceSliceManagerFunc = prevNewDRA
	})
}

func TestNewPodMapperInClusterInitialization(t *testing.T) {
	restorePodMapperSeams(t)
	inClusterConfigFunc = func() (*rest.Config, error) {
		return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
	}
	clientset := fake.NewClientset()
	newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
		return clientset, nil
	}

	t.Run("skips Kubernetes client without metadata enrichment", func(t *testing.T) {
		t.Setenv("NODE_NAME", "node-a")
		inClusterCalls := 0
		clientCalls := 0
		inClusterConfigFunc = func() (*rest.Config, error) {
			inClusterCalls++
			return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
		}
		newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
			clientCalls++
			return clientset, nil
		}
		defer func() {
			inClusterConfigFunc = func() (*rest.Config, error) {
				return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
			}
			newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
				return clientset, nil
			}
		}()

		pm := NewPodMapper(&appconfig.Config{})

		require.NotNil(t, pm)
		assert.Nil(t, pm.Client)
		assert.Nil(t, pm.podInformerFactory)
		assert.Nil(t, pm.podLister)
		assert.Equal(t, 0, inClusterCalls)
		assert.Equal(t, 0, clientCalls)
		pm.Stop()
	})

	t.Run("node scoped informer", func(t *testing.T) {
		t.Setenv("NODE_NAME", "node-a")

		pm := NewPodMapper(&appconfig.Config{KubernetesEnablePodLabels: true})

		require.NotNil(t, pm.Client)
		assert.NotNil(t, pm.podInformerFactory)
		assert.NotNil(t, pm.podLister)
		assert.NotNil(t, pm.podInformerSynced)
		pm.Stop()
	})

	t.Run("client construction error", func(t *testing.T) {
		newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
			return nil, errors.New("client failed")
		}
		defer func() {
			newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
				return clientset, nil
			}
		}()

		pm := NewPodMapper(&appconfig.Config{KubernetesEnablePodLabels: true})

		assert.Nil(t, pm.Client)
		pm.Stop()
	})

	t.Run("DRA manager success", func(t *testing.T) {
		t.Setenv("NODE_NAME", "")
		draManager := newTestDRAManager()
		newDRAResourceSliceManagerFunc = func() (*DRAResourceSliceManager, error) {
			return draManager, nil
		}

		pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})

		assert.Same(t, draManager, pm.ResourceSliceManager)
		pm.Stop()
	})

	t.Run("DRA manager error keeps pod mapper usable", func(t *testing.T) {
		newDRAResourceSliceManagerFunc = func() (*DRAResourceSliceManager, error) {
			return nil, errors.New("dra failed")
		}

		pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})

		assert.Nil(t, pm.ResourceSliceManager)
		pm.Stop()
	})
}

func TestPodMapperRunWaitsForInformerSync(t *testing.T) {
	clientset := fake.NewClientset(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "default"},
	})
	factory := informers.NewSharedInformerFactory(clientset, 0)
	informer := factory.Core().V1().Pods().Informer()
	pm := &PodMapper{
		Config:             &appconfig.Config{},
		podInformerFactory: factory,
		podInformerSynced:  informer.HasSynced,
		stopChan:           make(chan struct{}),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		pm.Run()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		pm.Stop()
		t.Fatal("pod mapper did not finish cache sync")
	}
	pm.Stop()
}

func TestPodMapperProcessMissingPodResourcesSocketIsNoop(t *testing.T) {
	pm := &PodMapper{
		Config: &appconfig.Config{
			KubernetesGPUIdType:       appconfig.GPUUID,
			PodResourcesKubeletSocket: filepath.Join(t.TempDir(), "missing.sock"),
		},
	}
	counter := counters.Counter{FieldID: 155, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	metrics := collector.MetricsByCounter{
		counter: {{
			GPUUUID:    "gpu-0",
			Counter:    counter,
			Value:      "42",
			Attributes: map[string]string{},
			Labels:     map[string]string{},
		}},
	}

	err := pm.Process(metrics, nil)

	require.NoError(t, err)
	require.Len(t, metrics[counter], 1)
	assert.Empty(t, metrics[counter][0].Attributes)
}

func TestPodMapperGetMappingsValidatesPodResourcesSocketPath(t *testing.T) {
	regularFilePath := filepath.Join(t.TempDir(), "regular-file")
	require.NoError(t, stdos.WriteFile(regularFilePath, []byte("not a socket"), 0o600))

	tests := []struct {
		name        string
		socketPath  string
		errContains string
	}{
		{
			name:        "relative path",
			socketPath:  "kubelet.sock",
			errContains: "must be absolute",
		},
		{
			name:        "regular file",
			socketPath:  regularFilePath,
			errContains: "must be a Unix socket",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pm := &PodMapper{
				Config: &appconfig.Config{
					PodResourcesKubeletSocket: tc.socketPath,
				},
			}

			_, _, _, err := pm.getMappings(nil)

			require.ErrorContains(t, err, tc.errContains)
		})
	}
}

func TestIterateGPUDevicesFiltersResources(t *testing.T) {
	pm := &PodMapper{Config: &appconfig.Config{NvidiaResourceNames: []string{"example.com/custom-gpu"}}}
	response := &podresourcesapi.ListPodResourcesResponse{
		PodResources: []*podresourcesapi.PodResources{{
			Name:      "pod-a",
			Namespace: "default",
			Containers: []*podresourcesapi.ContainerResources{{
				Name: "ctr-a",
				Devices: []*podresourcesapi.ContainerDevices{
					{ResourceName: "example.com/cpu", DeviceIds: []string{"cpu0"}},
					{ResourceName: appconfig.NvidiaResourceName, DeviceIds: []string{"gpu0"}},
					{ResourceName: "example.com/custom-gpu", DeviceIds: []string{"gpu1"}},
					{ResourceName: appconfig.NvidiaMigResourcePrefix + "1g.10gb", DeviceIds: []string{"mig0"}},
				},
			}},
		}},
	}

	var got []string
	pm.iterateGPUDevices(response, func(_ *podresourcesapi.PodResources, _ *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		got = append(got, device.GetDeviceIds()...)
	})

	assert.ElementsMatch(t, []string{"gpu0", "gpu1", "mig0"}, got)
}

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
		KubernetesEnableDRA  bool
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
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        "nvidia.com/mig-1g.10gb",
			MetricMigProfile:    "1g.10gb",
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			// Simulate no pods using the GPUs.
			PODGPUIDs:            []string{},
			MetricGPUDevice:      "0",
			GPUInstanceID:        3,
			KubernetesVirtualGPU: true,
		},
		{
			KubernetesGPUIDType: appconfig.GPUUID,
			ResourceName:        "nvidia.com/mig-1g.10gb",
			MetricMigProfile:    "1g.10gb",
			MetricGPUID:         "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			// Simulate no pods using the GPUs.
			PODGPUIDs:           []string{},
			MetricGPUDevice:     "0",
			GPUInstanceID:       3,
			KubernetesEnableDRA: false,
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf(
			"when type %s, pod device ids %s metric device id %s and gpu device %s with virtual GPUs: %t and DRA: %t",
			tc.KubernetesGPUIDType,
			tc.PODGPUIDs,
			tc.MetricGPUID,
			tc.MetricGPUDevice,
			tc.KubernetesVirtualGPU,
			tc.KubernetesEnableDRA,
		),
			func(t *testing.T) {
				tmpDir, cleanup := testutils.CreateTmpDir(t)
				defer cleanup()
				socketPath := tmpDir + "/kubelet.sock"
				server := grpc.NewServer()
				config := &appconfig.Config{
					UseRemoteHE:   false,
					Kubernetes:    true,
					EnableDCGMLog: true,
					DCGMLogLevel:  "DEBUG",
				}

				dcgmprovider.SmartDCGMInit(t, config)
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
				mockNVMLProvider.EXPECT().GetDeviceProcessMemory(gomock.Any()).Return(map[uint32]uint64{}, nil).AnyTimes()
				mockNVMLProvider.EXPECT().GetDeviceProcessUtilization(gomock.Any()).Return(map[uint32]uint32{}, nil).AnyTimes()
				mockNVMLProvider.EXPECT().GetAllMIGDevicesProcessMemory(gomock.Any()).Return(map[uint]map[uint32]uint64{}, nil).AnyTimes()
				nvmlprovider.SetClient(mockNVMLProvider)

				podMapper := NewPodMapper(&appconfig.Config{
					KubernetesGPUIdType:       tc.KubernetesGPUIDType,
					PodResourcesKubeletSocket: socketPath,
					NvidiaResourceNames:       tc.NvidiaResourceNames,
					KubernetesVirtualGPUs:     tc.KubernetesVirtualGPU,
					KubernetesEnableDRA:       tc.KubernetesEnableDRA,
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
				mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
				mockSystemInfo.EXPECT().GPU(toUint(0)).Return(mockGPU).AnyTimes()

				err := podMapper.Process(metrics, mockSystemInfo)
				require.NoError(t, err)
				assert.Len(t, metrics, 1)

				// We shouldn't omit metrics just because pods aren't using the GPUs.
				if len(metrics[counter]) < 1 {
					t.Errorf("expected at least one metric, got 0 for counter: %s", counter.FieldName)
				}

				for i, metric := range metrics[counter] {
					// Only require pod attributes when we expect a pod to be using the GPU.
					if len(tc.PODGPUIDs) > 0 {
						require.Contains(t, metric.Attributes, podAttribute)
						require.Contains(t, metric.Attributes, namespaceAttribute)
						require.Contains(t, metric.Attributes, containerAttribute)

						// TODO currently we rely on ordering and implicit expectations of the mock implementation
						// This should be a table comparison
						require.Equal(t, fmt.Sprintf("gpu-pod-%d", i), metric.Attributes[podAttribute])
						require.Equal(t, "default", metric.Attributes[namespaceAttribute])
						require.Equal(t, "default", metric.Attributes[containerAttribute])
					} else {
						require.NotContains(t, metric.Attributes, podAttribute)
						require.NotContains(t, metric.Attributes, namespaceAttribute)
						require.NotContains(t, metric.Attributes, containerAttribute)
					}

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
		name       string
		labels     map[string]string
		wantLabels map[string]string
	}{
		{
			name: "gpu-pod-0",
			labels: map[string]string{
				"hostname":           "pod-hostname",
				"pod_label_hostname": "existing-prefixed",
				"valid_label_key":    "label-value",
			},
			wantLabels: map[string]string{
				"pod_label_hostname":           "existing-prefixed",
				"pod_label_hostname_conflict1": "pod-hostname",
				"valid_label_key":              "label-value",
			},
		},
		{
			name: "gpu-pod-1",
			labels: map[string]string{
				"invalid.label/key": "another-value",
			},
			wantLabels: map[string]string{
				"invalid_label_key": "another-value",
			},
		},
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
	clientset := fake.NewClientset(objects...)

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
	setupMockInformer(t, podMapper, clientset)

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
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(toUint(len(gpus))).AnyTimes()
	for i := range gpus {
		mockDeviceInfo.EXPECT().GPU(toUint(i)).Return(mockGPU).AnyTimes()
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

		require.Equal(t, pod.wantLabels, metric.Labels)
	}
}

func TestCopyPodLabelsPreservesCollisions(t *testing.T) {
	metric := collector.Metric{
		UUID:       "UUID",
		Attributes: map[string]string{podAttribute: "gpu-pod-0"},
		Labels:     map[string]string{"xid": "13"},
	}
	podLabels := map[string]string{
		"UUID":               "pod-uuid-label",
		"app":                "demo",
		"hostname":           "pod-hostname",
		"pod":                "pod-label",
		"pod_label_hostname": "existing-prefixed",
		"xid":                "pod-xid",
	}

	copyPodLabels(&metric, podLabels, dcgm.FE_GPU)

	require.Equal(t, map[string]string{
		"app":                          "demo",
		"pod_label_UUID":               "pod-uuid-label",
		"pod_label_hostname":           "existing-prefixed",
		"pod_label_hostname_conflict1": "pod-hostname",
		"pod_label_pod":                "pod-label",
		"pod_label_xid":                "pod-xid",
		"xid":                          "13",
	}, metric.Labels)
}

func TestCopyPodLabelsRenamesCPUSerialForCPUMetrics(t *testing.T) {
	podLabels := map[string]string{
		"cpu_serial": "pod-cpu-serial",
	}
	cpuMetric := collector.Metric{
		Attributes: map[string]string{},
		Labels:     map[string]string{},
	}
	cpuCoreMetric := collector.Metric{
		Attributes: map[string]string{},
		Labels:     map[string]string{},
	}

	copyPodLabels(&cpuMetric, podLabels, dcgm.FE_CPU)
	copyPodLabels(&cpuCoreMetric, podLabels, dcgm.FE_CPU_CORE)

	require.Equal(t, map[string]string{
		"pod_label_cpu_serial": "pod-cpu-serial",
	}, cpuMetric.Labels)
	require.Equal(t, map[string]string{
		"cpu_serial": "pod-cpu-serial",
	}, cpuCoreMetric.Labels)
}

func TestCopyPodLabelsDetachesSharedLabelMaps(t *testing.T) {
	sharedLabels := map[string]string{
		"DCGM_FI_DRIVER_VERSION": "595.58.03",
	}
	podLabels := map[string]string{
		"app":      "demo",
		"hostname": "pod-hostname",
	}
	firstMetric := collector.Metric{
		Attributes: map[string]string{},
		Labels:     sharedLabels,
	}
	secondMetric := collector.Metric{
		Attributes: map[string]string{},
		Labels:     sharedLabels,
	}
	wantLabels := map[string]string{
		"DCGM_FI_DRIVER_VERSION": "595.58.03",
		"app":                    "demo",
		"pod_label_hostname":     "pod-hostname",
	}

	copyPodLabels(&firstMetric, podLabels, dcgm.FE_GPU)
	copyPodLabels(&secondMetric, podLabels, dcgm.FE_GPU)

	require.Equal(t, wantLabels, firstMetric.Labels)
	require.Equal(t, wantLabels, secondMetric.Labels)
	require.Equal(t, map[string]string{
		"DCGM_FI_DRIVER_VERSION": "595.58.03",
	}, sharedLabels)
}

func TestIsRendererReservedLabel(t *testing.T) {
	tests := []struct {
		name        string
		metric      collector.Metric
		metricGroup dcgm.Field_Entity_Group
		label       string
		want        bool
	}{
		{
			name:        "GPU hostname label is reserved",
			metricGroup: dcgm.FE_GPU,
			label:       "hostname",
			want:        true,
		},
		{
			name:        "GPU UUID label follows metric UUID key",
			metric:      collector.Metric{UUID: "UUID"},
			metricGroup: dcgm.FE_GPU,
			label:       "UUID",
			want:        true,
		},
		{
			name:        "Link GPU UUID label is reserved",
			metricGroup: dcgm.FE_LINK,
			label:       "gpu_uuid",
			want:        true,
		},
		{
			name:        "CPU core label is reserved only for CPU core metrics",
			metricGroup: dcgm.FE_CPU_CORE,
			label:       "cpucore",
			want:        true,
		},
		{
			name:        "CPU metrics do not reserve CPU core label",
			metricGroup: dcgm.FE_CPU,
			label:       "cpucore",
			want:        false,
		},
		{
			name:        "CPU serial label is reserved for CPU metrics",
			metricGroup: dcgm.FE_CPU,
			label:       "cpu_serial",
			want:        true,
		},
		{
			name:        "CPU core metrics do not reserve CPU serial label",
			metricGroup: dcgm.FE_CPU_CORE,
			label:       "cpu_serial",
			want:        false,
		},
		{
			name:        "Pod app label is not renderer-owned",
			metricGroup: dcgm.FE_GPU,
			label:       "app",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isRendererReservedLabel(tt.metric, tt.metricGroup, tt.label))
		})
	}
}

func TestPodDRAInfo(t *testing.T) {
	dra := &podresourcesapi.DynamicResource{
		ClaimName:      "claim1",
		ClaimNamespace: "ns1",
		ClaimResources: []*podresourcesapi.ClaimResource{{
			DriverName: DRAGPUDriverName,
			PoolName:   "poolA",
			DeviceName: "gpu-x",
		}},
	}

	tests := []struct {
		name      string
		devices   map[string]testDRADeviceMapping
		wantUUIDs []string
	}{
		{
			name: "uuid-exists",
			devices: map[string]testDRADeviceMapping{
				"poolA/gpu-x": {uuid: "GPU-8a748984-0fe7-297f-916c-4b998ce202d1"},
			},
			wantUUIDs: []string{"GPU-8a748984-0fe7-297f-916c-4b998ce202d1"},
		},
		{
			name: "uuid-updated",
			devices: map[string]testDRADeviceMapping{
				"poolA/gpu-x": {uuid: "GPU-UUID-Updated"},
			},
			wantUUIDs: []string{"GPU-UUID-Updated"},
		},
		{
			name:      "no-uuid",
			devices:   map[string]testDRADeviceMapping{},
			wantUUIDs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			draMgr := newTestDRAManagerWithDevices(tc.devices)

			pm := &PodMapper{
				Config:               &appconfig.Config{NvidiaResourceNames: []string{appconfig.NvidiaResourceName}},
				ResourceSliceManager: draMgr,
			}

			resp := &podresourcesapi.ListPodResourcesResponse{
				PodResources: []*podresourcesapi.PodResources{{
					Name:      "pod1",
					Namespace: "default",
					Containers: []*podresourcesapi.ContainerResources{{
						Name:             "ctr1",
						DynamicResources: []*podresourcesapi.DynamicResource{dra},
					}},
				}},
			}

			got := pm.toDeviceToPodsDRA(resp, nil)

			assert.Len(t, got, len(tc.wantUUIDs), "map size")
			for _, want := range tc.wantUUIDs {
				assert.Contains(t, got, want, "expected key %q", want)
			}

			if len(tc.wantUUIDs) == 1 {
				pi := got[tc.wantUUIDs[0]]
				require.Len(t, pi, 1, "should have one pod info")

				dr := *pi[0].DynamicResources
				require.NotNil(t, dr, "dynamic resources should not be nil")

				assert.Equal(t, "claim1", dr.ClaimName)
				assert.Equal(t, "ns1", dr.ClaimNamespace)
				assert.Equal(t, DRAGPUDriverName, dr.DriverName)
				assert.Equal(t, "poolA", dr.PoolName)
				assert.Equal(t, "gpu-x", dr.DeviceName)

				assert.Nil(t, dr.MIGInfo, "MIG info should be nil for full GPU device")

			}
		})
	}
}

func TestPodDRAInfo_MIGUsesGPUInstanceIdentifier(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	realNVML := nvmlprovider.Client()
	t.Cleanup(func() { nvmlprovider.SetClient(realNVML) })

	mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(&nvmlprovider.MIGDeviceInfo{
		ParentUUID:    "GPU-parent-uuid",
		GPUInstanceID: 3,
	}, nil)
	nvmlprovider.SetClient(mockNVMLProvider)

	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
	mockSystemInfo.EXPECT().GPU(toUint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			UUID: "GPU-parent-uuid",
			GPU:  0,
		},
		MigEnabled: true,
	}).AnyTimes()

	draMgr := newTestDRAManagerWithDevices(map[string]testDRADeviceMapping{
		"poolA/gpu-x": {
			uuid: "GPU-parent-uuid",
			mig: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				Profile:       "1g.12gb",
				ParentUUID:    "GPU-parent-uuid",
			},
		},
	})

	pm := &PodMapper{
		Config:               &appconfig.Config{NvidiaResourceNames: []string{appconfig.NvidiaResourceName}},
		ResourceSliceManager: draMgr,
	}

	resp := &podresourcesapi.ListPodResourcesResponse{
		PodResources: []*podresourcesapi.PodResources{{
			Name:      "pod1",
			Namespace: "default",
			Containers: []*podresourcesapi.ContainerResources{{
				Name: "ctr1",
				DynamicResources: []*podresourcesapi.DynamicResource{{
					ClaimName:      "claim1",
					ClaimNamespace: "ns1",
					ClaimResources: []*podresourcesapi.ClaimResource{{
						DriverName: DRAGPUDriverName,
						PoolName:   "poolA",
						DeviceName: "gpu-x",
					}},
				}},
			}},
		}},
	}

	got := pm.toDeviceToPodsDRA(resp, mockSystemInfo)

	require.Contains(t, got, "0-3", "MIG DRA mapping must use the same key as MIG metrics")
	assert.NotContains(t, got, "GPU-parent-uuid")
	require.Len(t, got["0-3"], 1)
	dr := got["0-3"][0].DynamicResources
	require.NotNil(t, dr)
	require.NotNil(t, dr.MIGInfo)
	assert.Equal(t, "MIG-12345", dr.MIGInfo.MIGDeviceUUID)
	assert.Equal(t, "1g.12gb", dr.MIGInfo.Profile)
	assert.Equal(t, "GPU-parent-uuid", dr.MIGInfo.ParentUUID)
}

func TestDRAMappingKey_MIGUnresolvedSkipsAttribution(t *testing.T) {
	tests := []struct {
		name             string
		migInfo          *DRAMigDeviceInfo
		setupNVML        func(*mocknvmlprovider.MockNVML)
		setupDeviceInfo  func(*mockdeviceinfo.MockProvider)
		wantMappingKey   string
		expectNVMLMock   bool
		expectDeviceMock bool
	}{
		{
			name: "empty MIG UUID",
			migInfo: &DRAMigDeviceInfo{
				ParentUUID: "GPU-parent-uuid",
			},
		},
		{
			name: "NVML lookup failure",
			migInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				ParentUUID:    "GPU-parent-uuid",
			},
			setupNVML: func(mockNVML *mocknvmlprovider.MockNVML) {
				mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(nil, errors.New("lookup failed"))
			},
			expectNVMLMock: true,
		},
		{
			name: "nil NVML MIG info",
			migInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				ParentUUID:    "GPU-parent-uuid",
			},
			setupNVML: func(mockNVML *mocknvmlprovider.MockNVML) {
				mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(nil, nil)
			},
			expectNVMLMock: true,
		},
		{
			name: "negative GPU instance ID",
			migInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				ParentUUID:    "GPU-parent-uuid",
			},
			setupNVML: func(mockNVML *mocknvmlprovider.MockNVML) {
				mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(&nvmlprovider.MIGDeviceInfo{
					ParentUUID:    "GPU-parent-uuid",
					GPUInstanceID: -1,
				}, nil)
			},
			expectNVMLMock: true,
		},
		{
			name: "parent GPU not in device info",
			migInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				ParentUUID:    "GPU-parent-uuid",
			},
			setupNVML: func(mockNVML *mocknvmlprovider.MockNVML) {
				mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(&nvmlprovider.MIGDeviceInfo{
					ParentUUID:    "GPU-parent-uuid",
					GPUInstanceID: 3,
				}, nil)
			},
			setupDeviceInfo: func(mockSystemInfo *mockdeviceinfo.MockProvider) {
				mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
				mockSystemInfo.EXPECT().GPU(toUint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: "GPU-other", GPU: 0},
				}).AnyTimes()
			},
			expectNVMLMock:   true,
			expectDeviceMock: true,
		},
		{
			name: "resolved MIG key",
			migInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				ParentUUID:    "GPU-parent-uuid",
			},
			setupNVML: func(mockNVML *mocknvmlprovider.MockNVML) {
				mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(&nvmlprovider.MIGDeviceInfo{
					ParentUUID:    "GPU-parent-uuid",
					GPUInstanceID: 3,
				}, nil)
			},
			setupDeviceInfo: func(mockSystemInfo *mockdeviceinfo.MockProvider) {
				mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
				mockSystemInfo.EXPECT().GPU(toUint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: "GPU-parent-uuid", GPU: 0},
				}).AnyTimes()
			},
			wantMappingKey:   "0-3",
			expectNVMLMock:   true,
			expectDeviceMock: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			realNVML := nvmlprovider.Client()
			t.Cleanup(func() { nvmlprovider.SetClient(realNVML) })

			if tc.expectNVMLMock {
				mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
				tc.setupNVML(mockNVMLProvider)
				nvmlprovider.SetClient(mockNVMLProvider)
			}

			var deviceInfo deviceinfo.Provider
			if tc.expectDeviceMock {
				mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
				tc.setupDeviceInfo(mockSystemInfo)
				deviceInfo = mockSystemInfo
			} else if tc.expectNVMLMock {
				deviceInfo = mockdeviceinfo.NewMockProvider(ctrl)
			}

			got := draMappingKey("GPU-parent-uuid", tc.migInfo, deviceInfo)
			assert.Equal(t, tc.wantMappingKey, got)
		})
	}
}

type dynamicResourcePodResourcesServer struct {
	podresourcesapi.UnimplementedPodResourcesListerServer
	response *podresourcesapi.ListPodResourcesResponse
}

func (s *dynamicResourcePodResourcesServer) List(
	context.Context,
	*podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	return s.response, nil
}

func (s *dynamicResourcePodResourcesServer) Get(
	context.Context,
	*podresourcesapi.GetPodResourcesRequest,
) (*podresourcesapi.GetPodResourcesResponse, error) {
	if len(s.response.GetPodResources()) == 0 {
		return &podresourcesapi.GetPodResourcesResponse{}, nil
	}
	return &podresourcesapi.GetPodResourcesResponse{PodResources: s.response.GetPodResources()[0]}, nil
}

func (s *dynamicResourcePodResourcesServer) GetAllocatableResources(
	context.Context,
	*podresourcesapi.AllocatableResourcesRequest,
) (*podresourcesapi.AllocatableResourcesResponse, error) {
	return &podresourcesapi.AllocatableResourcesResponse{}, nil
}

func TestPodMapperProcessAddsDRAAttributesForMIGMetric(t *testing.T) {
	testutils.RequireLinux(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	realNVML := nvmlprovider.Client()
	t.Cleanup(func() { nvmlprovider.SetClient(realNVML) })

	mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID("MIG-12345").Return(&nvmlprovider.MIGDeviceInfo{
		ParentUUID:    "GPU-parent-uuid",
		GPUInstanceID: 3,
	}, nil)
	nvmlprovider.SetClient(mockNVMLProvider)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := filepath.Join(tmpDir, "kubelet.sock")

	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, &dynamicResourcePodResourcesServer{
		response: &podresourcesapi.ListPodResourcesResponse{
			PodResources: []*podresourcesapi.PodResources{{
				Name:      "pod1",
				Namespace: "default",
				Containers: []*podresourcesapi.ContainerResources{{
					Name: "ctr1",
					DynamicResources: []*podresourcesapi.DynamicResource{{
						ClaimName:      "claim1",
						ClaimNamespace: "ns1",
						ClaimResources: []*podresourcesapi.ClaimResource{{
							DriverName: DRAGPUDriverName,
							PoolName:   "poolA",
							DeviceName: "gpu-x",
						}},
					}},
				}},
			}},
		},
	})
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	clientset := fake.NewClientset(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			Labels: map[string]string{
				"app":            "demo",
				"dra_claim_name": "pod-claim-label",
				"hostname":       "pod-hostname",
			},
		},
	})

	draManager := newTestDRAManagerWithDevices(map[string]testDRADeviceMapping{
		"poolA/gpu-x": {
			uuid: "GPU-parent-uuid",
			mig: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-12345",
				Profile:       "1g.12gb",
				ParentUUID:    "GPU-parent-uuid",
			},
		},
	})

	pm := &PodMapper{
		Config: &appconfig.Config{
			KubernetesEnableDRA:       true,
			KubernetesEnablePodLabels: true,
			KubernetesGPUIdType:       appconfig.GPUUID,
			PodResourcesKubeletSocket: socketPath,
			NvidiaResourceNames:       []string{appconfig.NvidiaResourceName},
		},
		ResourceSliceManager: draManager,
		Client:               clientset,
		labelFilterCache:     newLabelFilterCache(nil, 1000),
	}
	setupMockInformer(t, pm, clientset)

	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockSystemInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
	mockSystemInfo.EXPECT().GPU(toUint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			UUID: "GPU-parent-uuid",
			GPU:  0,
		},
		MigEnabled: true,
	}).AnyTimes()

	counter := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}
	metrics := collector.MetricsByCounter{
		counter: {{
			GPU:           "0",
			GPUUUID:       "MIG-12345",
			GPUInstanceID: "3",
			MigProfile:    "1g.12gb",
			Value:         "42",
			Attributes:    map[string]string{},
			Labels:        map[string]string{},
			Counter:       counter,
		}},
	}

	err := pm.Process(metrics, mockSystemInfo)
	require.NoError(t, err)
	require.Len(t, metrics[counter], 1)

	got := metrics[counter][0]
	assert.Equal(t, "pod1", got.Attributes[podAttribute])
	assert.Equal(t, "default", got.Attributes[namespaceAttribute])
	assert.Equal(t, "ctr1", got.Attributes[containerAttribute])
	assert.Equal(t, "claim1", got.Attributes[draClaimName])
	assert.Equal(t, "ns1", got.Attributes[draClaimNamespace])
	assert.Equal(t, DRAGPUDriverName, got.Attributes[draDriverName])
	assert.Equal(t, "poolA", got.Attributes[draPoolName])
	assert.Equal(t, "gpu-x", got.Attributes[draDeviceName])
	assert.Equal(t, "1g.12gb", got.Attributes[draMigProfile])
	assert.Equal(t, "MIG-12345", got.Attributes[draMigDeviceUUID])
	assert.Equal(t, map[string]string{
		"app":                      "demo",
		"pod_label_dra_claim_name": "pod-claim-label",
		"pod_label_hostname":       "pod-hostname",
	}, got.Labels)
}

func TestProcessPodMapper_WithUID(t *testing.T) {
	testutils.RequireLinux(t)

	pods := []struct {
		name string
		uid  string
	}{
		{"gpu-pod-0", "pod-uid-123"},
		{"gpu-pod-1", "pod-uid-456"},
	}

	// Create fake Kubernetes clientset with pods containing UIDs
	objects := make([]runtime.Object, len(pods))
	for i, pod := range pods {
		objects[i] = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.name,
				Namespace: "default",
				UID:       types.UID(pod.uid),
			},
		}
	}
	clientset := fake.NewClientset(objects...)

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

	// Create PodMapper with UID support enabled
	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesEnablePodUID:    true,
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
	})
	// Inject the fake clientset
	podMapper.Client = clientset
	setupMockInformer(t, podMapper, clientset)

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
	mockDeviceInfo.EXPECT().GPUCount().Return(toUint(len(gpus))).AnyTimes()
	for i := range gpus {
		mockDeviceInfo.EXPECT().GPU(toUint(i)).Return(mockGPU).AnyTimes()
	}

	// Process metrics
	err := podMapper.Process(metrics, mockDeviceInfo)
	require.NoError(t, err)

	// Verify that UIDs were added correctly
	for i, metric := range metrics[counter] {
		pod := pods[i]

		// Verify pod attributes were set
		require.Contains(t, metric.Attributes, podAttribute)
		require.Contains(t, metric.Attributes, namespaceAttribute)
		require.Contains(t, metric.Attributes, containerAttribute)
		require.Equal(t, pod.name, metric.Attributes[podAttribute])
		require.Equal(t, "default", metric.Attributes[namespaceAttribute])
		require.Equal(t, "default", metric.Attributes[containerAttribute])

		// Verify UID was added as attribute - check if it exists in the PodInfo struct
		// Note: The UID is stored in PodInfo.UID field but not directly in metric attributes
		// We need to verify the UID was properly fetched and stored
		require.NotEmpty(t, pod.uid, "Test pod UID should not be empty")
	}
}

func TestProcessPodMapper_WithLabelsAndUID(t *testing.T) {
	testutils.RequireLinux(t)

	pods := []struct {
		name   string
		uid    string
		labels map[string]string
	}{
		{"gpu-pod-0", "pod-uid-123", map[string]string{"app": "test", "version": "v1"}},
		{"gpu-pod-1", "pod-uid-456", map[string]string{"app": "prod", "env": "staging"}},
	}

	// Create fake Kubernetes clientset with pods containing both labels and UIDs
	objects := make([]runtime.Object, len(pods))
	for i, pod := range pods {
		objects[i] = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.name,
				Namespace: "default",
				UID:       types.UID(pod.uid),
				Labels:    pod.labels,
			},
		}
	}
	clientset := fake.NewClientset(objects...)

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

	// Create PodMapper with both labels and UID support enabled
	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesEnablePodLabels: true,
		KubernetesEnablePodUID:    true,
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
	})
	// Inject the fake clientset
	podMapper.Client = clientset
	setupMockInformer(t, podMapper, clientset)

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
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(toUint(len(gpus))).AnyTimes()
	for i := range gpus {
		mockDeviceInfo.EXPECT().GPU(toUint(i)).Return(mockGPU).AnyTimes()
	}

	// Process metrics
	err := podMapper.Process(metrics, mockDeviceInfo)
	require.NoError(t, err)

	// Verify that both labels and UIDs were processed correctly
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

func setupMockInformer(t *testing.T, mapper *PodMapper, client kubernetes.Interface) {
	factory := informers.NewSharedInformerFactory(client, 0)
	mapper.podInformerFactory = factory
	mapper.podLister = factory.Core().V1().Pods().Lister()
	mapper.podInformerSynced = factory.Core().V1().Pods().Informer().HasSynced

	stopChan := make(chan struct{})
	t.Cleanup(func() { close(stopChan) })

	go factory.Start(stopChan)
	if !cache.WaitForCacheSync(stopChan, mapper.podInformerSynced) {
		t.Fatalf("Failed to sync mock informer")
	}
}

func TestPodMapper_createPodInfo_WithInformer(t *testing.T) {
	// 1. Setup Fake Client
	client := fake.NewSimpleClientset()

	// 2. Create PodMapper with injected dependencies
	// Use NewPodMapper or manual construction. Manual is safer here to avoid NewPodMapper side effects.
	config := &appconfig.Config{
		KubernetesEnablePodLabels: true,
		KubernetesEnablePodUID:    true,
	}

	mapper := &PodMapper{
		Config:           config,
		Client:           client,
		labelFilterCache: newLabelFilterCache(nil, 1000),
	}

	// Setup Informer using the helper
	setupMockInformer(t, mapper, client)

	// 3. Add a Pod to the Store (simulating K8s state)
	podName := "test-gpu-pod"
	namespace := "default"
	podUID := "test-uid-12345"
	labels := map[string]string{
		"app": "gpu-app",
		"env": "production",
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			UID:       types.UID(podUID),
			Labels:    labels,
		},
	}

	// Add to fake client (which updates informer via watch)
	_, err := client.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)

	// Wait for informer to observe the addition
	// In unit tests with fake client, we need to give a moment for the watch event to propagate
	time.Sleep(100 * time.Millisecond)

	// 4. Create Dummy PodResources (simulating Kubelet socket data)
	podRes := &podresourcesapi.PodResources{
		Name:      podName,
		Namespace: namespace,
		Containers: []*podresourcesapi.ContainerResources{
			{
				Name: "gpu-container",
				Devices: []*podresourcesapi.ContainerDevices{
					{
						ResourceName: "nvidia.com/gpu",
						DeviceIds:    []string{"GPU-1"},
					},
				},
			},
		},
	}
	containerRes := podRes.Containers[0]

	// 5. Test createPodInfo
	// createPodInfo is a private method, but we are in the same package (transformation)
	podInfo := mapper.createPodInfo(podRes, containerRes)

	// 6. Verify Results
	assert.Equal(t, podName, podInfo.Name)
	assert.Equal(t, namespace, podInfo.Namespace)
	assert.Equal(t, "gpu-container", podInfo.Container)
	assert.Equal(t, podUID, podInfo.UID, "Should retrieve UID from Informer")
	assert.Equal(t, "gpu-app", podInfo.Labels["app"], "Should retrieve labels from Informer")
	assert.Equal(t, "production", podInfo.Labels["env"])
}

func TestBuildPodValueMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pidToPod  map[uint32]*PodInfo
		data      *perProcessMetrics
		fieldName string
		expected  map[string]string
	}{
		{
			name:      "nil data returns empty map",
			pidToPod:  map[uint32]*PodInfo{1001: {UID: "uid1"}},
			data:      nil,
			fieldName: metricGPUUtil,
			expected:  map[string]string{},
		},
		{
			name:     "empty pidToPod returns empty map",
			pidToPod: map[uint32]*PodInfo{},
			data: &perProcessMetrics{
				pidToSMUtil: map[uint32]uint32{1001: 50},
			},
			fieldName: metricGPUUtil,
			expected:  map[string]string{},
		},
		{
			name:     "maps PID values to pod UIDs for GPU util",
			pidToPod: map[uint32]*PodInfo{1001: {UID: "uid1"}, 1002: {UID: "uid2"}},
			data: &perProcessMetrics{
				pidToSMUtil: map[uint32]uint32{1001: 50, 1002: 75},
			},
			fieldName: metricGPUUtil,
			expected:  map[string]string{"uid1": "50", "uid2": "75"},
		},
		{
			name:     "maps PID values to pod UIDs for FB used",
			pidToPod: map[uint32]*PodInfo{1001: {UID: "uid1"}},
			data: &perProcessMetrics{
				pidToMemory: map[uint32]uint64{1001: 1024 * 1024 * 1024},
			},
			fieldName: metricFBUsed,
			expected:  map[string]string{"uid1": "1024"},
		},
		{
			name:     "skips PIDs without metric data",
			pidToPod: map[uint32]*PodInfo{1001: {UID: "uid1"}, 2002: {UID: "uid2"}},
			data: &perProcessMetrics{
				pidToSMUtil: map[uint32]uint32{1001: 50},
			},
			fieldName: metricGPUUtil,
			expected:  map[string]string{"uid1": "50"},
		},
		{
			name:     "multiple PIDs same pod - accumulates GPU util",
			pidToPod: map[uint32]*PodInfo{1001: {UID: "uid1"}, 1002: {UID: "uid1"}},
			data: &perProcessMetrics{
				pidToSMUtil: map[uint32]uint32{1001: 30, 1002: 45},
			},
			fieldName: metricGPUUtil,
			expected:  map[string]string{"uid1": "75"},
		},
		{
			name:     "multiple PIDs same pod - accumulates FB used",
			pidToPod: map[uint32]*PodInfo{1001: {UID: "uid1"}, 1002: {UID: "uid1"}},
			data: &perProcessMetrics{
				pidToMemory: map[uint32]uint64{1001: 500 * 1024 * 1024, 1002: 300 * 1024 * 1024},
			},
			fieldName: metricFBUsed,
			expected:  map[string]string{"uid1": "800"},
		},
		{
			name: "mixed pods - some with multiple PIDs, some with single PID",
			pidToPod: map[uint32]*PodInfo{
				1001: {UID: "uid1"}, 1002: {UID: "uid1"},
				2001: {UID: "uid2"},
			},
			data: &perProcessMetrics{
				pidToSMUtil: map[uint32]uint32{1001: 20, 1002: 30, 2001: 50},
			},
			fieldName: metricGPUUtil,
			expected:  map[string]string{"uid1": "50", "uid2": "50"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildPodValueMap(tc.pidToPod, tc.data, tc.fieldName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildIdlePodValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		existingValues map[string]string
		devicePods     []PodInfo
		expected       map[string]string
	}{
		{
			name:           "adds zero values for idle pods",
			existingValues: map[string]string{"uid1": "50"},
			devicePods:     []PodInfo{{UID: "uid1"}, {UID: "uid2"}, {UID: "uid3"}},
			expected:       map[string]string{"uid2": "0", "uid3": "0"},
		},
		{
			name:           "skips pods with existing values",
			existingValues: map[string]string{"uid1": "50", "uid2": "75"},
			devicePods:     []PodInfo{{UID: "uid1"}, {UID: "uid2"}},
			expected:       map[string]string{},
		},
		{
			name:           "all pods idle",
			existingValues: map[string]string{},
			devicePods:     []PodInfo{{UID: "uid1"}, {UID: "uid2"}},
			expected:       map[string]string{"uid1": "0", "uid2": "0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildIdlePodValues(tc.existingValues, tc.devicePods)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestPodMapper_CreatePerProcessMetrics(t *testing.T) {
	t.Parallel()
	gpuUUID := "GPU-00000000-0000-0000-0000-000000000000"
	podUID := "a9c80282-3f6b-4d5b-84d5-a137a6668011"

	tests := []struct {
		name           string
		useOldNS       bool
		dataMap        *perProcessDataMap
		counter        counters.Counter
		originalMetric collector.Metric
		validate       func(t *testing.T, result []collector.Metric, err error)
	}{
		{
			name:     "no deviceToPods returns nil",
			useOldNS: false,
			dataMap: &perProcessDataMap{
				metrics:      map[string]*perProcessMetrics{gpuUUID: {pidToSMUtil: map[uint32]uint32{1001: 50}}},
				pidToPod:     map[uint32]*PodInfo{1001: {UID: podUID}},
				deviceToPods: map[string][]PodInfo{},
			},
			counter: counters.Counter{FieldName: metricGPUUtil},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Attributes: map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				assert.Nil(t, result)
			},
		},
		{
			name:     "creates metrics with new namespace attributes",
			useOldNS: false,
			dataMap: &perProcessDataMap{
				metrics: map[string]*perProcessMetrics{
					gpuUUID: {
						pidToSMUtil: map[uint32]uint32{1001: 50},
						pidToMemory: map[uint32]uint64{1001: 1024 * 1024 * 1024},
					},
				},
				pidToPod: map[uint32]*PodInfo{
					1001: {
						Name:      "test-pod",
						Namespace: "default",
						UID:       podUID,
						Container: "app",
						Labels: map[string]string{
							"app":      "demo",
							"hostname": "pod-hostname",
							"pod":      "pod-label",
						},
					},
				},
				deviceToPods: map[string][]PodInfo{
					gpuUUID: {{
						Name:      "test-pod",
						Namespace: "default",
						UID:       podUID,
						Container: "app",
						Labels: map[string]string{
							"app":      "demo",
							"hostname": "pod-hostname",
							"pod":      "pod-label",
						},
					}},
				},
			},
			counter: counters.Counter{FieldName: metricGPUUtil},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Value:      "0",
				Attributes: map[string]string{},
				Labels:     map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				require.Len(t, result, 1)
				assert.Equal(t, "50", result[0].Value)
				assert.Equal(t, "test-pod", result[0].Attributes[podAttribute])
				assert.Equal(t, "default", result[0].Attributes[namespaceAttribute])
				assert.Equal(t, "app", result[0].Attributes[containerAttribute])
				assert.Equal(t, podUID, result[0].Attributes[uidAttribute])
				assert.Equal(t, map[string]string{
					"app":                "demo",
					"pod_label_hostname": "pod-hostname",
					"pod_label_pod":      "pod-label",
				}, result[0].Labels)
			},
		},
		{
			name:     "creates metrics with old namespace attributes",
			useOldNS: true,
			dataMap: &perProcessDataMap{
				metrics: map[string]*perProcessMetrics{
					gpuUUID: {
						pidToSMUtil: map[uint32]uint32{1001: 75},
					},
				},
				pidToPod: map[uint32]*PodInfo{
					1001: {Name: "old-pod", Namespace: "kube-system", UID: podUID, Container: "container"},
				},
				deviceToPods: map[string][]PodInfo{
					gpuUUID: {{Name: "old-pod", Namespace: "kube-system", UID: podUID, Container: "container"}},
				},
			},
			counter: counters.Counter{FieldName: metricGPUUtil},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Attributes: map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				require.Len(t, result, 1)
				assert.Equal(t, "75", result[0].Value)
				assert.Equal(t, "old-pod", result[0].Attributes[oldPodAttribute])
				assert.Equal(t, "kube-system", result[0].Attributes[oldNamespaceAttribute])
				assert.Equal(t, "container", result[0].Attributes[oldContainerAttribute])
			},
		},
		{
			name:     "includes VGPU attribute when present",
			useOldNS: false,
			dataMap: &perProcessDataMap{
				metrics: map[string]*perProcessMetrics{
					gpuUUID: {
						pidToSMUtil: map[uint32]uint32{1001: 25},
					},
				},
				pidToPod: map[uint32]*PodInfo{
					1001: {Name: "vgpu-pod", Namespace: "default", UID: podUID, VGPU: "vgpu-0"},
				},
				deviceToPods: map[string][]PodInfo{
					gpuUUID: {{Name: "vgpu-pod", Namespace: "default", UID: podUID, VGPU: "vgpu-0"}},
				},
			},
			counter: counters.Counter{FieldName: metricGPUUtil},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Attributes: map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				require.Len(t, result, 1)
				assert.Equal(t, "vgpu-0", result[0].Attributes[vgpuAttribute])
			},
		},
		{
			name:     "backfills idle pods with zero for GPU util",
			useOldNS: false,
			dataMap: &perProcessDataMap{
				metrics: map[string]*perProcessMetrics{
					gpuUUID: {
						pidToSMUtil: map[uint32]uint32{1001: 50},
					},
				},
				pidToPod: map[uint32]*PodInfo{
					1001: {Name: "active-pod", Namespace: "ns1", UID: "uid1"},
				},
				deviceToPods: map[string][]PodInfo{
					gpuUUID: {
						{Name: "active-pod", Namespace: "ns1", UID: "uid1"},
						{Name: "idle-pod", Namespace: "ns2", UID: "uid2"},
					},
				},
			},
			counter: counters.Counter{FieldName: metricGPUUtil},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Attributes: map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				require.Len(t, result, 2)
				values := map[string]string{}
				for _, m := range result {
					values[m.Attributes[podAttribute]] = m.Value
				}
				assert.Equal(t, "50", values["active-pod"])
				assert.Equal(t, "0", values["idle-pod"])
			},
		},
		{
			name:     "backfills idle pods with zero for FB used",
			useOldNS: false,
			dataMap: &perProcessDataMap{
				metrics: map[string]*perProcessMetrics{
					gpuUUID: {
						pidToMemory: map[uint32]uint64{1001: 1024 * 1024 * 1024},
					},
				},
				pidToPod: map[uint32]*PodInfo{
					1001: {Name: "active-pod", Namespace: "ns1", UID: "uid1"},
				},
				deviceToPods: map[string][]PodInfo{
					gpuUUID: {
						{Name: "active-pod", Namespace: "ns1", UID: "uid1"},
						{Name: "idle-pod", Namespace: "ns2", UID: "uid2"},
					},
				},
			},
			counter: counters.Counter{FieldName: metricFBUsed},
			originalMetric: collector.Metric{
				GPUUUID:    gpuUUID,
				Attributes: map[string]string{},
			},
			validate: func(t *testing.T, result []collector.Metric, err error) {
				assert.NoError(t, err)
				require.Len(t, result, 2)
				values := map[string]string{}
				for _, m := range result {
					values[m.Attributes[podAttribute]] = m.Value
				}
				assert.Equal(t, "1024", values["active-pod"])
				assert.Equal(t, "0", values["idle-pod"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			podMapper := &PodMapper{
				Config: &appconfig.Config{
					UseOldNamespace: tc.useOldNS,
				},
			}

			result, err := podMapper.createPerProcessMetrics(
				tc.originalMetric,
				tc.counter,
				tc.originalMetric,
				tc.dataMap,
				func() dcgm.Field_Entity_Group { return dcgm.FE_GPU },
			)

			tc.validate(t, result, err)
		})
	}
}

func TestStripVGPUSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		deviceID string
		expected string
	}{
		{
			name:     "MIG device ID with vgpu suffix",
			deviceID: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55::7",
			expected: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55",
		},
		{
			name:     "Plain MIG UUID without suffix",
			deviceID: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55",
			expected: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55",
		},
		{
			name:     "Regular GPU UUID",
			deviceID: "GPU-65759866-6a45-99ff-bc37-c534ea0ae191",
			expected: "GPU-65759866-6a45-99ff-bc37-c534ea0ae191",
		},
		{
			name:     "Non-MIG device ID with vgpu suffix",
			deviceID: "b8ea3855-276c-c9cb-b366-c6fa655957c5::2",
			expected: "b8ea3855-276c-c9cb-b366-c6fa655957c5",
		},
		{
			name:     "Empty string",
			deviceID: "",
			expected: "",
		},
		{
			name:     "Device ID with empty suffix",
			deviceID: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55::",
			expected: "MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := stripVGPUSuffix(tc.deviceID)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestKubernetesVirtualGPUs_UnusedGPUsPreserveMetrics(t *testing.T) {
	testutils.RequireLinux(t)

	testCases := []struct {
		name              string
		counter           counters.Counter
		wantPodMetrics    int
		wantDeviceMetrics int
	}{
		{
			name: "non-per-process metric",
			counter: counters.Counter{
				FieldID:   155,
				FieldName: "DCGM_FI_DEV_POWER_USAGE",
				PromType:  "gauge",
			},
			wantPodMetrics:    1,
			wantDeviceMetrics: 2,
		},
		{
			name: "per-process metric",
			counter: counters.Counter{
				FieldID:   203,
				FieldName: metricGPUUtil,
				PromType:  "gauge",
			},
			wantPodMetrics:    1,
			wantDeviceMetrics: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allGPUUUIDs := []string{"gpu-uuid-0", "gpu-uuid-1", "gpu-uuid-2"}
			inUseGPUUUIDs := []string{"gpu-uuid-0"}

			tmpDir, cleanup := testutils.CreateTmpDir(t)
			defer cleanup()
			socketPath := tmpDir + "/kubelet.sock"
			server := grpc.NewServer()
			defer server.Stop()

			config := &appconfig.Config{
				UseRemoteHE:   false,
				Kubernetes:    true,
				EnableDCGMLog: true,
				DCGMLogLevel:  "DEBUG",
			}
			dcgmprovider.SmartDCGMInit(t, config)
			defer dcgmprovider.Client().Cleanup()

			podresourcesapi.RegisterPodResourcesListerServer(server,
				testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, inUseGPUUUIDs))
			cleanupServer := testutils.StartMockServer(t, server, socketPath)
			defer cleanupServer()

			clientset := fake.NewClientset(&v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gpu-pod-0",
					Namespace: "default",
					Labels: map[string]string{
						"app":      "demo",
						"hostname": "pod-hostname",
					},
				},
			})

			ctrl := gomock.NewController(t)
			mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
			mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID(gomock.Any()).Return(&nvmlprovider.MIGDeviceInfo{}, nil).AnyTimes()
			mockNVMLProvider.EXPECT().GetDeviceProcessMemory(gomock.Any()).Return(map[uint32]uint64{}, nil).AnyTimes()
			mockNVMLProvider.EXPECT().GetDeviceProcessUtilization(gomock.Any()).Return(map[uint32]uint32{}, nil).AnyTimes()
			mockNVMLProvider.EXPECT().GetAllMIGDevicesProcessMemory(gomock.Any()).Return(map[uint]map[uint32]uint64{}, nil).AnyTimes()
			nvmlprovider.SetClient(mockNVMLProvider)

			podMapper := NewPodMapper(&appconfig.Config{
				KubernetesGPUIdType:       appconfig.GPUUID,
				KubernetesEnablePodLabels: true,
				PodResourcesKubeletSocket: socketPath,
				KubernetesVirtualGPUs:     true,
			})
			require.NotNil(t, podMapper)
			podMapper.Client = clientset
			setupMockInformer(t, podMapper, clientset)

			metrics := collector.MetricsByCounter{}
			for i, gpuUUID := range allGPUUUIDs {
				metrics[tc.counter] = append(metrics[tc.counter], collector.Metric{
					GPU:        fmt.Sprint(i),
					GPUUUID:    gpuUUID,
					Value:      fmt.Sprint(42 + i),
					Counter:    tc.counter,
					Attributes: map[string]string{},
				})
			}

			mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
			mockSystemInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			mockSystemInfo.EXPECT().GPUCount().Return(toUint(len(allGPUUUIDs))).AnyTimes()
			for i, uuid := range allGPUUUIDs {
				mockSystemInfo.EXPECT().GPU(toUint(i)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{
						UUID: uuid,
						GPU:  toUint(i),
					},
				}).AnyTimes()
			}

			err := podMapper.Process(metrics, mockSystemInfo)
			require.NoError(t, err)

			var deviceMetrics, podMetrics []collector.Metric
			for _, m := range metrics[tc.counter] {
				if _, hasPod := m.Attributes[podAttribute]; hasPod {
					podMetrics = append(podMetrics, m)
				} else {
					deviceMetrics = append(deviceMetrics, m)
				}
			}

			require.Len(t, podMetrics, tc.wantPodMetrics)
			require.Equal(t, "gpu-pod-0", podMetrics[0].Attributes[podAttribute])
			require.Equal(t, "default", podMetrics[0].Attributes[namespaceAttribute])
			require.Equal(t, "demo", podMetrics[0].Labels["app"])
			require.Equal(t, "pod-hostname", podMetrics[0].Labels["pod_label_hostname"])

			require.Len(t, deviceMetrics, tc.wantDeviceMetrics)
			for _, m := range deviceMetrics {
				require.NotContains(t, m.Attributes, podAttribute)
			}
		})
	}
}

func TestKubernetesVirtualGPUs_UnusedMIGInstancesPreserveMetrics(t *testing.T) {
	testutils.RequireLinux(t)

	testCases := []struct {
		name              string
		counter           counters.Counter
		wantPodMetrics    int
		wantDeviceMetrics int
	}{
		{
			name: "non-per-process metric",
			counter: counters.Counter{
				FieldID:   155,
				FieldName: "DCGM_FI_DEV_POWER_USAGE",
				PromType:  "gauge",
			},
			wantPodMetrics:    1,
			wantDeviceMetrics: 2,
		},
		{
			name: "per-process metric",
			counter: counters.Counter{
				FieldID:   203,
				FieldName: metricGPUUtil,
				PromType:  "gauge",
			},
			// in-use instance: 1 device-level + 1 per-process (has pod attr)
			// unused instances: 2 device-level
			wantPodMetrics:    1,
			wantDeviceMetrics: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gpuUUID := "GPU-test-mig-uuid"
			allInstances := []uint{7, 12, 13}
			podDeviceID := "nvidia0/gi7/vgpu0"

			tmpDir, cleanup := testutils.CreateTmpDir(t)
			defer cleanup()
			socketPath := tmpDir + "/kubelet.sock"
			server := grpc.NewServer()
			defer server.Stop()

			config := &appconfig.Config{
				UseRemoteHE:   false,
				Kubernetes:    true,
				EnableDCGMLog: true,
				DCGMLogLevel:  "DEBUG",
			}
			dcgmprovider.SmartDCGMInit(t, config)
			defer dcgmprovider.Client().Cleanup()

			podresourcesapi.RegisterPodResourcesListerServer(server,
				testutils.NewMockPodResourcesServer("nvidia.com/mig-1g.5gb", []string{podDeviceID}))
			cleanupServer := testutils.StartMockServer(t, server, socketPath)
			defer cleanupServer()

			ctrl := gomock.NewController(t)
			mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)
			mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID(gomock.Any()).Return(&nvmlprovider.MIGDeviceInfo{
				ParentUUID:        gpuUUID,
				GPUInstanceID:     3,
				ComputeInstanceID: 0,
			}, nil).AnyTimes()
			mockNVMLProvider.EXPECT().GetAllMIGDevicesProcessMemory(gomock.Any()).Return(map[uint]map[uint32]uint64{}, nil).AnyTimes()
			nvmlprovider.SetClient(mockNVMLProvider)

			podMapper := NewPodMapper(&appconfig.Config{
				KubernetesGPUIdType:       appconfig.DeviceName,
				PodResourcesKubeletSocket: socketPath,
				KubernetesVirtualGPUs:     true,
			})
			require.NotNil(t, podMapper)

			metrics := collector.MetricsByCounter{}
			for _, instID := range allInstances {
				metrics[tc.counter] = append(metrics[tc.counter], collector.Metric{
					GPU:           "0",
					GPUUUID:       gpuUUID,
					GPUDevice:     "0",
					GPUInstanceID: fmt.Sprint(instID),
					Value:         "100",
					MigProfile:    "1g.5gb",
					Counter:       tc.counter,
					Attributes:    map[string]string{},
				})
			}

			var gpuInstances []deviceinfo.GPUInstanceInfo
			for _, instID := range allInstances {
				gpuInstances = append(gpuInstances, deviceinfo.GPUInstanceInfo{
					Info:        dcgm.MigEntityInfo{NvmlInstanceId: instID},
					ProfileName: "1g.5gb",
				})
			}

			mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
			mockSystemInfo.EXPECT().GPUCount().Return(toUint(1)).AnyTimes()
			mockSystemInfo.EXPECT().GPU(toUint(0)).Return(deviceinfo.GPUInfo{
				DeviceInfo:   dcgm.Device{UUID: gpuUUID, GPU: 0},
				MigEnabled:   true,
				GPUInstances: gpuInstances,
			}).AnyTimes()

			err := podMapper.Process(metrics, mockSystemInfo)
			require.NoError(t, err)

			var deviceMetrics, podMetrics []collector.Metric
			for _, m := range metrics[tc.counter] {
				if _, hasPod := m.Attributes[podAttribute]; hasPod {
					podMetrics = append(podMetrics, m)
				} else {
					deviceMetrics = append(deviceMetrics, m)
				}
			}

			require.Len(t, podMetrics, tc.wantPodMetrics)
			require.Equal(t, "gpu-pod-0", podMetrics[0].Attributes[podAttribute])

			require.Len(t, deviceMetrics, tc.wantDeviceMetrics)
			for _, m := range deviceMetrics {
				require.NotContains(t, m.Attributes, podAttribute)
			}
		})
	}
}

type largePodResourcesServer struct {
	podresourcesapi.UnimplementedPodResourcesListerServer
	response *podresourcesapi.ListPodResourcesResponse
}

func (s *largePodResourcesServer) List(
	_ context.Context,
	_ *podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	return s.response, nil
}

type errorPodResourcesServer struct {
	podresourcesapi.UnimplementedPodResourcesListerServer
	err error
}

func (s *errorPodResourcesServer) List(
	_ context.Context,
	_ *podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	return nil, s.err
}

func captureDefaultSlog(t *testing.T) *bytes.Buffer {
	t.Helper()

	var logBuffer bytes.Buffer
	previousLogger := slog.Default()
	// Global slog mutation: tests using this helper must not run in parallel.
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	return &logBuffer
}

func newListPodResourcesResponse(deviceID string) *podresourcesapi.ListPodResourcesResponse {
	return &podresourcesapi.ListPodResourcesResponse{
		PodResources: []*podresourcesapi.PodResources{{
			Name:      "gpu-pod",
			Namespace: "default",
			Containers: []*podresourcesapi.ContainerResources{{
				Name: "container",
				Devices: []*podresourcesapi.ContainerDevices{{
					ResourceName: appconfig.NvidiaResourceName,
					DeviceIds:    []string{deviceID},
				}},
			}},
		}},
	}
}

func TestConnectToServerRejectsInvalidSocketPath(t *testing.T) {
	regularFilePath := filepath.Join(t.TempDir(), "regular-file")
	require.NoError(t, stdos.WriteFile(regularFilePath, []byte("not a socket"), 0o600))

	tests := []struct {
		name        string
		socketPath  string
		errContains string
	}{
		{
			name:        "relative path",
			socketPath:  "kubelet.sock",
			errContains: "must be absolute",
		},
		{
			name:        "regular file",
			socketPath:  regularFilePath,
			errContains: "must be a Unix socket",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn, cleanup, err := connectToServer(tc.socketPath)

			require.ErrorContains(t, err, tc.errContains)
			require.Nil(t, conn)
			cleanup()
		})
	}
}

func TestListPodsAllowsBoundedPodResourcesResponses(t *testing.T) {
	testutils.RequireLinux(t)

	tests := []struct {
		name             string
		deviceIDByteSize int
	}{
		{name: "small standard response", deviceIDByteSize: len("gpu-0")},
		{name: "larger than grpc default but below bounded limit", deviceIDByteSize: 5 * 1024 * 1024},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, cleanup := testutils.CreateTmpDir(t)
			defer cleanup()
			socketPath := filepath.Join(tmpDir, "kubelet.sock")

			deviceID := strings.Repeat("a", tc.deviceIDByteSize)
			server := grpc.NewServer()
			podresourcesapi.RegisterPodResourcesListerServer(server, &largePodResourcesServer{
				response: newListPodResourcesResponse(deviceID),
			})
			cleanupServer := testutils.StartMockServer(t, server, socketPath)
			defer cleanupServer()

			conn, cleanupConn, err := connectToServer(socketPath)
			require.NoError(t, err)
			defer cleanupConn()

			pm := &PodMapper{}
			resp, err := pm.listPods(conn)
			require.NoError(t, err)
			require.Len(t, resp.GetPodResources(), 1)
			require.Equal(t, deviceID, resp.GetPodResources()[0].GetContainers()[0].GetDevices()[0].GetDeviceIds()[0])
		})
	}
}

func TestListPodsRejectsPodResourcesResponseAboveBound(t *testing.T) {
	testutils.RequireLinux(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := filepath.Join(tmpDir, "kubelet.sock")

	deviceID := strings.Repeat("a", kubeletPodResourcesMaxRecvMsgSize+1024)
	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, &largePodResourcesServer{
		response: newListPodResourcesResponse(deviceID),
	})
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	conn, cleanupConn, err := connectToServer(socketPath)
	require.NoError(t, err)
	defer cleanupConn()

	pm := &PodMapper{}
	resp, err := pm.listPods(conn)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestProcessWarnsOnResourceExhaustedPodResourcesResponse(t *testing.T) {
	testutils.RequireLinux(t)

	logBuffer := captureDefaultSlog(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := filepath.Join(tmpDir, "kubelet.sock")

	deviceID := strings.Repeat("a", kubeletPodResourcesMaxRecvMsgSize+1024)
	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, &largePodResourcesServer{
		response: newListPodResourcesResponse(deviceID),
	})
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	pm := &PodMapper{Config: &appconfig.Config{PodResourcesKubeletSocket: socketPath}}
	err := pm.Process(collector.MetricsByCounter{}, nil)
	require.NoError(t, err)

	gotLog := logBuffer.String()
	require.Contains(t, gotLog, "Kubelet pod-resources response exceeded gRPC receive limit")
	require.Contains(t, gotLog, "limit_bytes")
}

func TestProcessKeepsGenericWarningOnNonResourceExhaustedPodResourcesError(t *testing.T) {
	testutils.RequireLinux(t)

	logBuffer := captureDefaultSlog(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := filepath.Join(tmpDir, "kubelet.sock")

	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, &errorPodResourcesServer{
		err: status.Error(codes.Unavailable, "boom"),
	})
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	pm := &PodMapper{Config: &appconfig.Config{PodResourcesKubeletSocket: socketPath}}
	err := pm.Process(collector.MetricsByCounter{}, nil)
	require.NoError(t, err)

	gotLog := logBuffer.String()
	require.Contains(t, gotLog, "Failed to get pod mappings")
	require.NotContains(t, gotLog, "Kubelet pod-resources response exceeded gRPC receive limit")
}
