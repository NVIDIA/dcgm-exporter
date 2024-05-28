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
// Source: github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher (interfaces: Watcher)
//
// Generated by this command:
//
//	mockgen -destination=../../mocks/pkg/devicewatcher/mock_device_watcher.go -package=devicewatcher -copyright_file=../../../hack/header.txt . Watcher
//

// Package devicewatcher is a generated GoMock package.
package devicewatcher

import (
	"reflect"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.uber.org/mock/gomock"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// MockWatcher is a mock of Watcher interface.
type MockWatcher struct {
	ctrl     *gomock.Controller
	recorder *MockWatcherMockRecorder
}

// MockWatcherMockRecorder is the mock recorder for MockWatcher.
type MockWatcherMockRecorder struct {
	mock *MockWatcher
}

// NewMockWatcher creates a new mock instance.
func NewMockWatcher(ctrl *gomock.Controller) *MockWatcher {
	mock := &MockWatcher{ctrl: ctrl}
	mock.recorder = &MockWatcherMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockWatcher) EXPECT() *MockWatcherMockRecorder {
	return m.recorder
}

// GetDeviceFields mocks base method.
func (m *MockWatcher) GetDeviceFields(arg0 []counters.Counter, arg1 dcgm.Field_Entity_Group) []dcgm.Short {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetDeviceFields", arg0, arg1)
	ret0, _ := ret[0].([]dcgm.Short)
	return ret0
}

// GetDeviceFields indicates an expected call of GetDeviceFields.
func (mr *MockWatcherMockRecorder) GetDeviceFields(arg0, arg1 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetDeviceFields", reflect.TypeOf((*MockWatcher)(nil).GetDeviceFields), arg0, arg1)
}

// WatchDeviceFields mocks base method.
func (m *MockWatcher) WatchDeviceFields(arg0 []dcgm.Short, arg1 deviceinfo.Provider, arg2 int64) ([]func(), error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "WatchDeviceFields", arg0, arg1, arg2)
	ret0, _ := ret[0].([]func())
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// WatchDeviceFields indicates an expected call of WatchDeviceFields.
func (mr *MockWatcherMockRecorder) WatchDeviceFields(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "WatchDeviceFields", reflect.TypeOf((*MockWatcher)(nil).WatchDeviceFields), arg0, arg1, arg2)
}
