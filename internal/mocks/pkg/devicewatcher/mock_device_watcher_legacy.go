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

package devicewatcher

import (
	"fmt"
	"reflect"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.uber.org/mock/gomock"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	production "github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

// Watch adapts established package test fixtures to the result-based contract.
// Production uses DeviceWatcher directly; this mock converts legacy expected
// calls into one closeable result so tests exercise the owning caller's path.
func (m *MockWatcher) Watch(
	groups []production.FieldWatchGroup,
	info deviceinfo.Provider,
	scope production.WatchScope,
) (*production.WatchResult, error) {
	var (
		groupHandles      []dcgm.GroupHandle
		fieldGroupHandles []dcgm.FieldHandle
		cleanups          []func()
		err               error
	)
	switch scope {
	case production.WatchConfiguredEntities, production.WatchAllGPUEntities, production.WatchPhysicalGPUs:
		groupHandles, fieldGroupHandles, cleanups, err = m.WatchDeviceFieldGroups(groups, info)
	case production.WatchComputeInstanceEntities:
		groupHandles, fieldGroupHandles, cleanups, err = m.WatchDeviceFieldGroupsForComputeInstanceFields(groups, info)
	case production.WatchUnselectedParentGPUs:
		groupHandles, fieldGroupHandles, cleanups, err = m.WatchDeviceFieldGroupsForParentGPUs(groups, info)
	default:
		return nil, fmt.Errorf("unsupported watch scope %d", scope)
	}
	if err != nil {
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanups[index]()
		}
		return nil, err
	}

	bindings := make([]production.WatchBinding, 0, len(groupHandles)*len(fieldGroupHandles))
	for _, group := range groupHandles {
		for _, fieldGroup := range fieldGroupHandles {
			bindings = append(bindings, production.WatchBinding{Group: group, FieldGroup: fieldGroup})
		}
	}
	// The result owns successful fixture cleanup in reverse acquisition order.
	return production.ResultFromBindings(bindings, func() {
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanups[index]()
		}
	}), nil
}

// ExplainWatchFailure returns err unchanged: test fixtures never produce the
// field refusals that DeviceWatcher.ExplainWatchFailure explains.
func (m *MockWatcher) ExplainWatchFailure(_ []production.FieldWatchGroup, err error) error {
	return err
}

// WatchDeviceFieldGroups keeps existing tests readable while their fake
// watchers move to the result-based Watch method used in production.
func (m *MockWatcher) WatchDeviceFieldGroups(
	groups []production.FieldWatchGroup,
	info deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	m.ctrl.T.Helper()
	result := m.ctrl.Call(m, "WatchDeviceFieldGroups", groups, info)
	groupsResult, _ := result[0].([]dcgm.GroupHandle)
	fieldGroupsResult, _ := result[1].([]dcgm.FieldHandle)
	cleanups, _ := result[2].([]func())
	err, _ := result[3].(error)
	return groupsResult, fieldGroupsResult, cleanups, err
}

// WatchDeviceFieldGroups records an expected configured-entity legacy watch.
func (mr *MockWatcherMockRecorder) WatchDeviceFieldGroups(groups, info any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(
		mr.mock,
		"WatchDeviceFieldGroups",
		reflect.TypeOf((*MockWatcher)(nil).WatchDeviceFieldGroups),
		groups,
		info,
	)
}

// WatchDeviceFieldGroupsForComputeInstanceFields keeps existing compute-scope
// test doubles readable while production calls Watch with a named scope.
func (m *MockWatcher) WatchDeviceFieldGroupsForComputeInstanceFields(
	groups []production.FieldWatchGroup,
	info deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	m.ctrl.T.Helper()
	result := m.ctrl.Call(m, "WatchDeviceFieldGroupsForComputeInstanceFields", groups, info)
	groupsResult, _ := result[0].([]dcgm.GroupHandle)
	fieldGroupsResult, _ := result[1].([]dcgm.FieldHandle)
	cleanups, _ := result[2].([]func())
	err, _ := result[3].(error)
	return groupsResult, fieldGroupsResult, cleanups, err
}

// WatchDeviceFieldGroupsForComputeInstanceFields records a legacy compute-scope watch.
func (mr *MockWatcherMockRecorder) WatchDeviceFieldGroupsForComputeInstanceFields(groups, info any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(
		mr.mock,
		"WatchDeviceFieldGroupsForComputeInstanceFields",
		reflect.TypeOf((*MockWatcher)(nil).WatchDeviceFieldGroupsForComputeInstanceFields),
		groups,
		info,
	)
}

// WatchDeviceFieldGroupsForParentGPUs keeps existing parent-scope test doubles
// readable while production calls Watch with a named scope.
func (m *MockWatcher) WatchDeviceFieldGroupsForParentGPUs(
	groups []production.FieldWatchGroup,
	info deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	m.ctrl.T.Helper()
	result := m.ctrl.Call(m, "WatchDeviceFieldGroupsForParentGPUs", groups, info)
	groupsResult, _ := result[0].([]dcgm.GroupHandle)
	fieldGroupsResult, _ := result[1].([]dcgm.FieldHandle)
	cleanups, _ := result[2].([]func())
	err, _ := result[3].(error)
	return groupsResult, fieldGroupsResult, cleanups, err
}

// WatchDeviceFieldGroupsForParentGPUs records a legacy parent-GPU watch.
func (mr *MockWatcherMockRecorder) WatchDeviceFieldGroupsForParentGPUs(groups, info any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(
		mr.mock,
		"WatchDeviceFieldGroupsForParentGPUs",
		reflect.TypeOf((*MockWatcher)(nil).WatchDeviceFieldGroupsForParentGPUs),
		groups,
		info,
	)
}
