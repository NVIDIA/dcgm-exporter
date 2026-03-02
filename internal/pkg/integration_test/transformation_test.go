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

package integration_test

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
)

const (
	// Note standard resource attributes
	podAttribute       = "pod"
	namespaceAttribute = "namespace"
	containerAttribute = "container"
)

// TestProcessPodMapper tests the pod mapper transformation.
// This test is isolated from collector tests - it creates its own fake GPU and mock metrics.
func TestProcessPodMapper(t *testing.T) {
	testutils.RequireLinux(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()

	// Initialize DCGM
	config := &appconfig.Config{
		UseRemoteHE:   false,
		Kubernetes:    true,
		EnableDCGMLog: true,
		DCGMLogLevel:  "DEBUG",
		UseFakeGPUs:   true,
	}
	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	// Create a fake GPU for this test
	entityList := []dcgm.MigHierarchyInfo{
		{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}},
	}
	gpuIDs, err := dcgmprovider.Client().CreateFakeEntities(entityList)
	require.NoError(t, err)
	require.NotEmpty(t, gpuIDs)
	gpuID := gpuIDs[0]

	// Create mock metrics with known GPU UUID
	fakeUUID := fmt.Sprintf("GPU-fake-uuid-%d", gpuID)
	gpuIDStr := strconv.FormatUint(uint64(gpuID), 10)

	testCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
	}

	mockMetrics := collector.MetricsByCounter{
		testCounter: []collector.Metric{
			{
				GPU:        gpuIDStr,
				GPUUUID:    fakeUUID,
				Value:      "42",
				Counter:    testCounter,
				Attributes: map[string]string{},
			},
		},
	}

	// Set up mock pod resources server with the fake GPU UUID
	socketPath := tmpDir + "/kubelet.sock"
	server := grpc.NewServer()
	v1.RegisterPodResourcesListerServer(server,
		testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, []string{fakeUUID}))

	cleanup = testutils.StartMockServer(t, server, socketPath)
	defer cleanup()

	// Create pod mapper
	podMapper := transformation.NewPodMapper(&appconfig.Config{
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
	})

	// Create deviceInfo provider restricted to our fake GPU
	deviceInfo, err := deviceinfo.Initialize(
		appconfig.DeviceOptions{Flex: true, MajorRange: []int{int(gpuID)}}, //nolint:gosec // GPU IDs are small
		appconfig.DeviceOptions{},
		appconfig.DeviceOptions{},
		true, // useFakeGPUs
		dcgm.FE_GPU,
	)
	require.NoError(t, err)

	// Process metrics through pod mapper
	err = podMapper.Process(mockMetrics, deviceInfo)
	require.NoError(t, err)

	// Verify pod attributes were added
	require.Len(t, mockMetrics, 1)
	for _, metrics := range mockMetrics {
		for i, metric := range metrics {
			require.Contains(t, metric.Attributes, podAttribute)
			require.Contains(t, metric.Attributes, namespaceAttribute)
			require.Contains(t, metric.Attributes, containerAttribute)
			// Mock server creates pod names as gpu-pod-{index} based on position in gpus list
			require.Equal(t, fmt.Sprintf("gpu-pod-%d", i), metric.Attributes[podAttribute])
			require.Equal(t, "default", metric.Attributes[namespaceAttribute])
			require.Equal(t, "default", metric.Attributes[containerAttribute])
		}
	}
}
