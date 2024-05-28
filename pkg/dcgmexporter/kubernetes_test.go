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
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

var deviceWatcher = devicewatcher.NewDeviceWatcher()

const FailedToConvert = "ERROR - FAILED TO CONVERT TO STRING"

var expectedGPUMetrics = map[string]bool{
	testutils.SampleGPUTempCounter.FieldName:           true,
	testutils.SampleGPUTotalEnergyCounter.FieldName:    true,
	testutils.SampleGPUPowerUsageCounter.FieldName:     true,
	testutils.SampleVGPULicenseStatusCounter.FieldName: true,
}

// TODO (roarora): This is temporary. Remove this when kubernetes_test moves to integrationtest package.
func testDCGMGPUCollector(t *testing.T, counters []counters.Counter) *collector.DCGMCollector {
	dOpt := appconfig.DeviceOptions{
		Flex:       true,
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	}
	config := appconfig.Config{
		GPUDeviceOptions: dOpt,
		NoHostname:       false,
		UseOldNamespace:  false,
		UseFakeGPUs:      false,
		CollectInterval:  1,
	}

	// Store actual dcgm provider
	realDCGMProvider := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGMProvider)

	ctrl := gomock.NewController(t)
	mockDCGMProvider := testutils.MockDCGM(ctrl)

	// Calls where actual API calls and results are desirable
	mockDCGMProvider.EXPECT().FieldGetById(gomock.Any()).
		DoAndReturn(func(fieldID dcgm.Short) dcgm.FieldMeta {
			return realDCGMProvider.FieldGetById(fieldID)
		}).AnyTimes()

	mockDCGMProvider.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
			error,
		) {
			return realDCGMProvider.EntityGetLatestValues(entityGroup, entityId, fields)
		}).AnyTimes()

	// Set mock DCGM provider
	dcgmprovider.SetClient(mockDCGMProvider)

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(counters, &config)

	err := deviceWatchListManager.CreateEntityWatchList(dcgm.FE_GPU, deviceWatcher,
		int64(config.CollectInterval))
	require.NoError(t, err)

	gpuItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, exists)

	g, err := collector.NewDCGMCollector(counters, "", &config, gpuItem)
	require.NoError(t, err)

	/* Test for error when no switches are available to monitor. */
	switchItem, exists := deviceWatchListManager.EntityWatchList(dcgm.FE_SWITCH)
	assert.False(t, exists, "dcgm.FE_SWITCH should not be available")

	_, err = collector.NewDCGMCollector(counters, "", &config, switchItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	/* Test for error when no cpus are available to monitor. */
	cpuItem, exist := deviceWatchListManager.EntityWatchList(dcgm.FE_CPU)
	require.False(t, exist, "dcgm.FE_CPU should not be available")

	_, err = collector.NewDCGMCollector(counters, "", &config, cpuItem)
	require.Error(t, err, "NewDCGMCollector should return error")

	out, err := g.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that you have a GPU on this node")
	require.Len(t, out, len(expectedGPUMetrics), fmt.Sprintf("Expected: %+v \nGot: %+v", expectedGPUMetrics, out))

	seenMetrics := map[string]bool{}
	for _, metrics := range out {
		for _, metric := range metrics {
			seenMetrics[metric.Counter.FieldName] = true
			require.NotEmpty(t, metric.GPU)

			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, FailedToConvert)
		}
	}
	require.Equal(t, seenMetrics, expectedGPUMetrics)

	return g
}

func TestProcessPodMapper(t *testing.T) {
	testutils.RequireLinux(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()

	config := &appconfig.Config{
		UseRemoteHE: false,
	}

	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	c := testDCGMGPUCollector(t, testutils.SampleCounters)
	defer c.Cleanup()

	out, err := c.GetMetrics()
	require.NoError(t, err)

	original := out

	arbirtaryMetric := out[reflect.ValueOf(out).MapKeys()[0].Interface().(counters.Counter)]

	socketPath := tmpDir + "/kubelet.sock"
	server := grpc.NewServer()
	gpus := GetGPUUUIDs(arbirtaryMetric)
	podresourcesapi.RegisterPodResourcesListerServer(server,
		testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, gpus))

	cleanup = StartMockServer(t, server, socketPath)
	defer cleanup()

	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
	})
	require.NoError(t, err)
	var deviceInfo deviceinfo.Provider
	err = podMapper.Process(out, deviceInfo)
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

func GetGPUUUIDs(metrics []collector.Metric) []string {
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

				cleanup = StartMockServer(t, server, socketPath)
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
