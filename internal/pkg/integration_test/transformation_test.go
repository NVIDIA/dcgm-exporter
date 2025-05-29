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
	"reflect"
	"testing"

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
	gpus := getGPUUUIDs(arbirtaryMetric)
	v1.RegisterPodResourcesListerServer(server,
		testutils.NewMockPodResourcesServer(appconfig.NvidiaResourceName, gpus))

	cleanup = testutils.StartMockServer(t, server, socketPath)
	defer cleanup()

	podMapper := transformation.NewPodMapper(&appconfig.Config{
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

func getGPUUUIDs(metrics []collector.Metric) []string {
	gpus := make([]string, len(metrics))
	for i, dev := range metrics {
		gpus[i] = dev.GPUUUID
	}

	return gpus
}
