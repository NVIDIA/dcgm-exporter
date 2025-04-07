// Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo (interfaces: Provider)
//
// Generated by this command:
//
//	mockgen -destination=../../mocks/pkg/deviceinfo/mock_device_info.go -package=deviceinfo -copyright_file=../../../hack/header.txt . Provider
//

// Package deviceinfo is a generated GoMock package.
package deviceinfo

import (
	reflect "reflect"

	appconfig "github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	deviceinfo "github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	dcgm "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	gomock "go.uber.org/mock/gomock"
)

// MockProvider is a mock of Provider interface.
type MockProvider struct {
	ctrl     *gomock.Controller
	recorder *MockProviderMockRecorder
	isgomock struct{}
}

// MockProviderMockRecorder is the mock recorder for MockProvider.
type MockProviderMockRecorder struct {
	mock *MockProvider
}

// NewMockProvider creates a new mock instance.
func NewMockProvider(ctrl *gomock.Controller) *MockProvider {
	mock := &MockProvider{ctrl: ctrl}
	mock.recorder = &MockProviderMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockProvider) EXPECT() *MockProviderMockRecorder {
	return m.recorder
}

// COpts mocks base method.
func (m *MockProvider) COpts() appconfig.DeviceOptions {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "COpts")
	ret0, _ := ret[0].(appconfig.DeviceOptions)
	return ret0
}

// COpts indicates an expected call of COpts.
func (mr *MockProviderMockRecorder) COpts() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "COpts", reflect.TypeOf((*MockProvider)(nil).COpts))
}

// CPU mocks base method.
func (m *MockProvider) CPU(i uint) deviceinfo.CPUInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CPU", i)
	ret0, _ := ret[0].(deviceinfo.CPUInfo)
	return ret0
}

// CPU indicates an expected call of CPU.
func (mr *MockProviderMockRecorder) CPU(i any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CPU", reflect.TypeOf((*MockProvider)(nil).CPU), i)
}

// CPUs mocks base method.
func (m *MockProvider) CPUs() []deviceinfo.CPUInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CPUs")
	ret0, _ := ret[0].([]deviceinfo.CPUInfo)
	return ret0
}

// CPUs indicates an expected call of CPUs.
func (mr *MockProviderMockRecorder) CPUs() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CPUs", reflect.TypeOf((*MockProvider)(nil).CPUs))
}

// GOpts mocks base method.
func (m *MockProvider) GOpts() appconfig.DeviceOptions {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GOpts")
	ret0, _ := ret[0].(appconfig.DeviceOptions)
	return ret0
}

// GOpts indicates an expected call of GOpts.
func (mr *MockProviderMockRecorder) GOpts() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GOpts", reflect.TypeOf((*MockProvider)(nil).GOpts))
}

// GPU mocks base method.
func (m *MockProvider) GPU(i uint) deviceinfo.GPUInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GPU", i)
	ret0, _ := ret[0].(deviceinfo.GPUInfo)
	return ret0
}

// GPU indicates an expected call of GPU.
func (mr *MockProviderMockRecorder) GPU(i any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GPU", reflect.TypeOf((*MockProvider)(nil).GPU), i)
}

// GPUCount mocks base method.
func (m *MockProvider) GPUCount() uint {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GPUCount")
	ret0, _ := ret[0].(uint)
	return ret0
}

// GPUCount indicates an expected call of GPUCount.
func (mr *MockProviderMockRecorder) GPUCount() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GPUCount", reflect.TypeOf((*MockProvider)(nil).GPUCount))
}

// GPUs mocks base method.
func (m *MockProvider) GPUs() []deviceinfo.GPUInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GPUs")
	ret0, _ := ret[0].([]deviceinfo.GPUInfo)
	return ret0
}

// GPUs indicates an expected call of GPUs.
func (mr *MockProviderMockRecorder) GPUs() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GPUs", reflect.TypeOf((*MockProvider)(nil).GPUs))
}

// InfoType mocks base method.
func (m *MockProvider) InfoType() dcgm.Field_Entity_Group {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "InfoType")
	ret0, _ := ret[0].(dcgm.Field_Entity_Group)
	return ret0
}

// InfoType indicates an expected call of InfoType.
func (mr *MockProviderMockRecorder) InfoType() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "InfoType", reflect.TypeOf((*MockProvider)(nil).InfoType))
}

// IsCPUWatched mocks base method.
func (m *MockProvider) IsCPUWatched(cpuID uint) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsCPUWatched", cpuID)
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsCPUWatched indicates an expected call of IsCPUWatched.
func (mr *MockProviderMockRecorder) IsCPUWatched(cpuID any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsCPUWatched", reflect.TypeOf((*MockProvider)(nil).IsCPUWatched), cpuID)
}

// IsCoreWatched mocks base method.
func (m *MockProvider) IsCoreWatched(coreID, cpuID uint) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsCoreWatched", coreID, cpuID)
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsCoreWatched indicates an expected call of IsCoreWatched.
func (mr *MockProviderMockRecorder) IsCoreWatched(coreID, cpuID any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsCoreWatched", reflect.TypeOf((*MockProvider)(nil).IsCoreWatched), coreID, cpuID)
}

// IsLinkWatched mocks base method.
func (m *MockProvider) IsLinkWatched(linkIndex, switchID uint) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsLinkWatched", linkIndex, switchID)
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsLinkWatched indicates an expected call of IsLinkWatched.
func (mr *MockProviderMockRecorder) IsLinkWatched(linkIndex, switchID any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsLinkWatched", reflect.TypeOf((*MockProvider)(nil).IsLinkWatched), linkIndex, switchID)
}

// IsSwitchWatched mocks base method.
func (m *MockProvider) IsSwitchWatched(switchID uint) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsSwitchWatched", switchID)
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsSwitchWatched indicates an expected call of IsSwitchWatched.
func (mr *MockProviderMockRecorder) IsSwitchWatched(switchID any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsSwitchWatched", reflect.TypeOf((*MockProvider)(nil).IsSwitchWatched), switchID)
}

// SOpts mocks base method.
func (m *MockProvider) SOpts() appconfig.DeviceOptions {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "SOpts")
	ret0, _ := ret[0].(appconfig.DeviceOptions)
	return ret0
}

// SOpts indicates an expected call of SOpts.
func (mr *MockProviderMockRecorder) SOpts() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "SOpts", reflect.TypeOf((*MockProvider)(nil).SOpts))
}

// Switch mocks base method.
func (m *MockProvider) Switch(i uint) deviceinfo.SwitchInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Switch", i)
	ret0, _ := ret[0].(deviceinfo.SwitchInfo)
	return ret0
}

// Switch indicates an expected call of Switch.
func (mr *MockProviderMockRecorder) Switch(i any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Switch", reflect.TypeOf((*MockProvider)(nil).Switch), i)
}

// Switches mocks base method.
func (m *MockProvider) Switches() []deviceinfo.SwitchInfo {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Switches")
	ret0, _ := ret[0].([]deviceinfo.SwitchInfo)
	return ret0
}

// Switches indicates an expected call of Switches.
func (mr *MockProviderMockRecorder) Switches() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Switches", reflect.TypeOf((*MockProvider)(nil).Switches))
}
