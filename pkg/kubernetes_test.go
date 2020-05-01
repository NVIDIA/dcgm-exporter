/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubernetes/pkg/kubelet/apis/podresources/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

var tmpDir string

func TestProcessPodMapper(t *testing.T) {
	cleanup := CreateTmpDir(t)
	defer cleanup()

	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	c, cleanup := testDCGMCollector(t, sampleCounters)
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

	podMapper := NewPodMapper(&Config{})
	err = podMapper.Process(out)
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
	path, err := ioutil.TempDir("", "gpu-monitoring-tools")
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
				&podresourcesapi.ContainerResources{
					Name: "default",
					Devices: []*podresourcesapi.ContainerDevices{
						&podresourcesapi.ContainerDevices{
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
