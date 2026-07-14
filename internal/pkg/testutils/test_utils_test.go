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

package testutils

import (
	"context"
	"errors"
	stdos "os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

func TestMockReader_ReadReturnsConfiguredError(t *testing.T) {
	want := errors.New("entropy unavailable")
	reader := &MockReader{Err: want}

	n, err := reader.Read(make([]byte, 8))

	assert.Zero(t, n)
	assert.ErrorIs(t, err, want)
}

func TestRequireLinux(t *testing.T) {
	RequireLinux(t)
}

func TestMockDeviceInfoHelpers(t *testing.T) {
	ctrl := gomock.NewController(t)

	gpuInfo := MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{
		1: {MockGPUInstanceInfo1},
	})
	assert.Equal(t, uint(2), gpuInfo.GPUCount())
	assert.Len(t, gpuInfo.GPUs(), 2)
	assert.Len(t, gpuInfo.GPU(1).GPUInstances, 1)

	cpuInfo := MockCPUDeviceInfo(
		ctrl, 1,
		map[int][]uint{0: {7}},
		map[uint]bool{0: true},
		map[WatchedEntityKey]bool{{ParentID: 0, ChildID: 7}: true},
		dcgm.FE_CPU,
	)
	assert.True(t, cpuInfo.IsCPUWatched(0))
	assert.True(t, cpuInfo.IsCoreWatched(7, 0))
	assert.Equal(t, dcgm.FE_CPU, cpuInfo.InfoType())

	switchInfo := MockSwitchDeviceInfo(
		ctrl, 1,
		map[int][]dcgm.NvLinkStatus{0: {MockNVLinkVal1}},
		map[uint]bool{0: true},
		map[WatchedEntityKey]bool{{ParentID: 0, ChildID: MockNVLinkVal1.Index}: true},
		dcgm.FE_SWITCH,
	)
	assert.True(t, switchInfo.IsSwitchWatched(0))
	assert.True(t, switchInfo.IsLinkWatched(MockNVLinkVal1.Index, 0))
	assert.Empty(t, switchInfo.GPUs())
}

func TestGetStructPrivateFieldValue(t *testing.T) {
	type sample struct {
		value string
	}
	s := &sample{value: "secret"}

	got := GetStructPrivateFieldValue[string](t, s, "value")

	assert.Equal(t, "secret", got)
}

func TestCreateTmpDir(t *testing.T) {
	path, cleanup := CreateTmpDir(t)
	_, err := stdos.Stat(path)
	require.NoError(t, err)

	cleanup()

	_, err = stdos.Stat(path)
	assert.True(t, stdos.IsNotExist(err))
}

func TestMockPodResourcesServer(t *testing.T) {
	server := NewMockPodResourcesServer("nvidia.com/gpu", []string{"GPU-0", "GPU-1"})

	list, err := server.List(context.Background(), &v1.ListPodResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, list.PodResources, 2)
	assert.Equal(t, "gpu-pod-0", list.PodResources[0].Name)
	assert.Equal(t, []string{"GPU-0"}, list.PodResources[0].Containers[0].Devices[0].DeviceIds)

	got, err := server.Get(context.Background(), &v1.GetPodResourcesRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"GPU-0", "GPU-1"}, got.PodResources.Containers[0].Devices[0].DeviceIds)

	allocatable, err := server.GetAllocatableResources(context.Background(), &v1.AllocatableResourcesRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"GPU-0", "GPU-1"}, allocatable.Devices[0].DeviceIds)
}

func TestGetFields(t *testing.T) {
	type sample struct {
		Name string
		fn   func()
	}
	s := &sample{Name: "dcgm", fn: func() {}}

	fields := GetFields(s, Fields)
	assert.Contains(t, fields, "Name")
	assert.NotContains(t, fields, "fn")

	functions := GetFields(s, Functions)
	assert.Contains(t, functions, "fn")
	assert.NotContains(t, functions, "Name")

	all := GetFields(s, All)
	assert.Contains(t, all, "Name")
	assert.Contains(t, all, "fn")

	assert.Empty(t, GetFields("not a struct", All))
}

func TestStrToByteArray(t *testing.T) {
	got := StrToByteArray("dcgm")

	assert.Equal(t, byte('d'), got[0])
	assert.Equal(t, byte('c'), got[1])
	assert.Equal(t, byte(0), got[4])
	assert.Len(t, got, 4096)
}

func TestStartMockServer(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "podresources.sock")
	server := grpc.NewServer()

	stop := StartMockServer(t, server, socket)
	_, err := stdos.Stat(socket)
	require.NoError(t, err)

	stop()
}
