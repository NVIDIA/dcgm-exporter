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
// Source: github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager (interfaces: Manager)
//
// Generated by this command:
//
//	mockgen -destination=../../mocks/pkg/devicewatchlistmanager/mock_device_watchlist_manager.go -package=devicewatchlistmanager -copyright_file=../../../hack/header.txt . Manager
//

// Package devicewatchlistmanager is a generated GoMock package.
package devicewatchlistmanager

import (
	reflect "reflect"

	devicewatcher "github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	devicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	dcgm "github.com/NVIDIA/go-dcgm/pkg/dcgm"
	gomock "go.uber.org/mock/gomock"
)

// MockManager is a mock of Manager interface.
type MockManager struct {
	ctrl     *gomock.Controller
	recorder *MockManagerMockRecorder
}

// MockManagerMockRecorder is the mock recorder for MockManager.
type MockManagerMockRecorder struct {
	mock *MockManager
}

// NewMockManager creates a new mock instance.
func NewMockManager(ctrl *gomock.Controller) *MockManager {
	mock := &MockManager{ctrl: ctrl}
	mock.recorder = &MockManagerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockManager) EXPECT() *MockManagerMockRecorder {
	return m.recorder
}

// CreateEntityWatchList mocks base method.
func (m *MockManager) CreateEntityWatchList(arg0 dcgm.Field_Entity_Group, arg1 devicewatcher.Watcher, arg2 int64) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateEntityWatchList", arg0, arg1, arg2)
	ret0, _ := ret[0].(error)
	return ret0
}

// CreateEntityWatchList indicates an expected call of CreateEntityWatchList.
func (mr *MockManagerMockRecorder) CreateEntityWatchList(arg0, arg1, arg2 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateEntityWatchList", reflect.TypeOf((*MockManager)(nil).CreateEntityWatchList), arg0, arg1, arg2)
}

// EntityWatchList mocks base method.
func (m *MockManager) EntityWatchList(arg0 dcgm.Field_Entity_Group) (devicewatchlistmanager.WatchList, bool) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "EntityWatchList", arg0)
	ret0, _ := ret[0].(devicewatchlistmanager.WatchList)
	ret1, _ := ret[1].(bool)
	return ret0, ret1
}

// EntityWatchList indicates an expected call of EntityWatchList.
func (mr *MockManagerMockRecorder) EntityWatchList(arg0 any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "EntityWatchList", reflect.TypeOf((*MockManager)(nil).EntityWatchList), arg0)
}
