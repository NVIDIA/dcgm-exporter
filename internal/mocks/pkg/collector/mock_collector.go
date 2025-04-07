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
// Source: github.com/NVIDIA/dcgm-exporter/internal/pkg/collector (interfaces: Collector)
//
// Generated by this command:
//
//	mockgen -destination=../../mocks/pkg/collector/mock_collector.go -package=collector -copyright_file=../../../hack/header.txt . Collector
//

// Package collector is a generated GoMock package.
package collector

import (
	reflect "reflect"

	collector "github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	gomock "go.uber.org/mock/gomock"
)

// MockCollector is a mock of Collector interface.
type MockCollector struct {
	ctrl     *gomock.Controller
	recorder *MockCollectorMockRecorder
	isgomock struct{}
}

// MockCollectorMockRecorder is the mock recorder for MockCollector.
type MockCollectorMockRecorder struct {
	mock *MockCollector
}

// NewMockCollector creates a new mock instance.
func NewMockCollector(ctrl *gomock.Controller) *MockCollector {
	mock := &MockCollector{ctrl: ctrl}
	mock.recorder = &MockCollectorMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockCollector) EXPECT() *MockCollectorMockRecorder {
	return m.recorder
}

// Cleanup mocks base method.
func (m *MockCollector) Cleanup() {
	m.ctrl.T.Helper()
	m.ctrl.Call(m, "Cleanup")
}

// Cleanup indicates an expected call of Cleanup.
func (mr *MockCollectorMockRecorder) Cleanup() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Cleanup", reflect.TypeOf((*MockCollector)(nil).Cleanup))
}

// GetMetrics mocks base method.
func (m *MockCollector) GetMetrics() (collector.MetricsByCounter, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetMetrics")
	ret0, _ := ret[0].(collector.MetricsByCounter)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetMetrics indicates an expected call of GetMetrics.
func (mr *MockCollectorMockRecorder) GetMetrics() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetMetrics", reflect.TypeOf((*MockCollector)(nil).GetMetrics))
}
