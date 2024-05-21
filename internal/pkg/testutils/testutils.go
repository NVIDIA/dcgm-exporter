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
	"reflect"
	"runtime"
	"testing"
	"unsafe"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.uber.org/mock/gomock"

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

type WatchedEntityKey struct {
	ParentID uint
	ChildID  uint
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

// RequireLinux checks if
func RequireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("Test is not supported on %q", runtime.GOOS)
	}
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
