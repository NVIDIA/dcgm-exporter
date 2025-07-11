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

package testutils

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// MockReader is a mock implementation of rand.Reader that always returns an error
type MockReader struct {
	Err error
}

func (r *MockReader) Read(_ []byte) (n int, err error) {
	return 0, r.Err
}

// RequireLinux checks if tests are being executed on a Linux platform or not
func RequireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("Test is not supported on %q", runtime.GOOS)
	}
}

func MockGPUDeviceInfo(
	ctrl *gomock.Controller, gpuCount int, gpuToGpuInstanceInfos map[int][]deviceinfo.GPUInstanceInfo,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockGPUs := make([]deviceinfo.GPUInfo, 0)

	for i := range gpuCount {
		gpuInfo := deviceinfo.GPUInfo{}
		gpuInfo.DeviceInfo.GPU = uint(i)

		if gpuInstanceInfos, exist := gpuToGpuInstanceInfos[i]; exist {
			gpuInfo.GPUInstances = gpuInstanceInfos
		}

		mockGPUs = append(mockGPUs, gpuInfo)
		mockSystemInfo.EXPECT().GPU(uint(i)).Return(gpuInfo).AnyTimes()
	}

	mockSystemInfo.EXPECT().GPUCount().Return(uint(gpuCount)).AnyTimes()
	mockSystemInfo.EXPECT().GPUs().Return(mockGPUs).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()

	return mockSystemInfo
}

func MockCPUDeviceInfo(
	ctrl *gomock.Controller, cpuCount int, cpuToCores map[int][]uint, watchedCPUs map[uint]bool,
	watchedCores map[WatchedEntityKey]bool, infoType dcgm.Field_Entity_Group,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockCPUs := make([]deviceinfo.CPUInfo, 0)

	for i := range cpuCount {
		cpuInfo := deviceinfo.CPUInfo{}
		cpuInfo.EntityId = uint(i)

		if cores, exist := cpuToCores[i]; exist {
			cpuInfo.Cores = []uint{}

			for _, core := range cores {
				cpuInfo.Cores = append(cpuInfo.Cores, core)

				mockSystemInfo.EXPECT().IsCoreWatched(core,
					uint(i)).Return(watchedCores[WatchedEntityKey{uint(i), core}]).AnyTimes()
			}
		}

		mockSystemInfo.EXPECT().IsCPUWatched(cpuInfo.EntityId).Return(watchedCPUs[cpuInfo.EntityId]).AnyTimes()
		mockSystemInfo.EXPECT().CPU(uint(i)).Return(cpuInfo).AnyTimes()

		mockCPUs = append(mockCPUs, cpuInfo)
	}

	mockSystemInfo.EXPECT().CPUs().Return(mockCPUs).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(infoType).AnyTimes()

	return mockSystemInfo
}

func MockSwitchDeviceInfo(
	ctrl *gomock.Controller, switchCount int, switchToNvLinks map[int][]dcgm.NvLinkStatus,
	watchedSwitches map[uint]bool, watchedLinks map[WatchedEntityKey]bool, infoType dcgm.Field_Entity_Group,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockSwitches := make([]deviceinfo.SwitchInfo, 0)

	for i := range switchCount {
		switchInfo := deviceinfo.SwitchInfo{}
		switchInfo.EntityId = uint(i)

		if nvLinks, exist := switchToNvLinks[i]; exist {
			switchInfo.NvLinks = []dcgm.NvLinkStatus{}

			for _, nvLink := range nvLinks {
				nvLink.ParentId = uint(i)
				nvLink.ParentType = dcgm.FE_SWITCH
				switchInfo.NvLinks = append(switchInfo.NvLinks, nvLink)

				mockSystemInfo.EXPECT().IsLinkWatched(nvLink.Index,
					uint(i)).Return(watchedLinks[WatchedEntityKey{uint(i), nvLink.Index}]).AnyTimes()
			}
		}

		mockSystemInfo.EXPECT().IsSwitchWatched(switchInfo.EntityId).Return(watchedSwitches[switchInfo.EntityId]).AnyTimes()
		mockSystemInfo.EXPECT().Switch(uint(i)).Return(switchInfo).AnyTimes()

		mockSwitches = append(mockSwitches, switchInfo)
	}

	mockSystemInfo.EXPECT().Switches().Return(mockSwitches).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(infoType).AnyTimes()

	return mockSystemInfo
}

// GetStructPrivateFieldValue returns private field value
func GetStructPrivateFieldValue[T any](t *testing.T, v any, fieldName string) T {
	t.Helper()
	var result T
	value := reflect.ValueOf(v)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		t.Errorf("The type %s is not stuct", value.Type())
		return result
	}

	fieldVal := value.FieldByName(fieldName)

	if !fieldVal.IsValid() {
		t.Errorf("The field %s is invalid for the %s type", fieldName, value.Type())
		return result
	}

	fieldPtr := unsafe.Pointer(fieldVal.UnsafeAddr())

	// Cast the field pointer to a pointer of the correct type
	realPtr := (*T)(fieldPtr)

	return *realPtr
}

func CreateTmpDir(t *testing.T) (string, func()) {
	path, err := os.MkdirTemp("", "dcgm-exporter")
	require.NoError(t, err)

	return path, func() {
		require.NoError(t, os.RemoveAll(path))
	}
}

type MockPodResourcesServer struct {
	resourceName string
	gpus         []string
}

func NewMockPodResourcesServer(resourceName string, gpus []string) *MockPodResourcesServer {
	return &MockPodResourcesServer{
		resourceName: resourceName,
		gpus:         gpus,
	}
}

func (s *MockPodResourcesServer) List(
	ctx context.Context, req *v1.ListPodResourcesRequest,
) (*v1.ListPodResourcesResponse, error) {
	podResources := make([]*v1.PodResources, len(s.gpus))

	for i, gpu := range s.gpus {
		podResources[i] = &v1.PodResources{
			Name:      fmt.Sprintf("gpu-pod-%d", i),
			Namespace: "default",
			Containers: []*v1.ContainerResources{
				{
					Name: "default",
					Devices: []*v1.ContainerDevices{
						{
							ResourceName: s.resourceName,
							DeviceIds:    []string{gpu},
						},
					},
				},
			},
		}
	}

	return &v1.ListPodResourcesResponse{
		PodResources: podResources,
	}, nil
}

func (s *MockPodResourcesServer) Get(
	ctx context.Context, req *v1.GetPodResourcesRequest,
) (*v1.GetPodResourcesResponse, error) {
	return &v1.GetPodResourcesResponse{
		PodResources: &v1.PodResources{
			Name:      "gpu-pod-0",
			Namespace: "default",
			Containers: []*v1.ContainerResources{
				{
					Name: "default",
					Devices: []*v1.ContainerDevices{
						{
							ResourceName: s.resourceName,
							DeviceIds:    s.gpus,
						},
					},
				},
			},
		},
	}, nil
}

func (s *MockPodResourcesServer) GetAllocatableResources(
	ctx context.Context, req *v1.AllocatableResourcesRequest,
) (*v1.AllocatableResourcesResponse, error) {
	return &v1.AllocatableResourcesResponse{
		Devices: []*v1.ContainerDevices{
			{
				ResourceName: s.resourceName,
				DeviceIds:    s.gpus,
			},
		},
	}, nil
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

type FieldType int

const (
	Fields FieldType = iota
	Functions
	All
)

// GetFields returns a map of fields of a struct, including unexported fields, based on the specified field type.
func GetFields(input interface{}, fieldType FieldType) map[string]interface{} {
	result := make(map[string]interface{})
	val := reflect.ValueOf(input)

	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return result
	}

	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldTyp := typ.Field(i)

		// Determine if the field should be included based on the specified field type
		includeField := false
		switch fieldType {
		case Fields:
			includeField = field.Kind() != reflect.Func
		case Functions:
			includeField = field.Kind() == reflect.Func
		case All:
			includeField = true
		}

		if !includeField {
			continue
		}

		// Access unexported fields
		if !field.CanInterface() {
			field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
		}

		result[fieldTyp.Name] = field.Interface()
	}

	return result
}

func StrToByteArray(str string) [4096]byte {
	var byteArray [4096]byte
	copy(byteArray[:], str)
	return byteArray
}
