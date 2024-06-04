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
// Source: github.com/NVIDIA/dcgm-exporter/internal/pkg/exec (interfaces: Exec)
//
// Generated by this command:
//
//	mockgen -destination=../../mocks/pkg/exec/mock_exec.go -package=exec -copyright_file=../../../hack/header.txt . Exec
//

// Package exec is a generated GoMock package.
package exec

import (
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"

	exec "github.com/NVIDIA/dcgm-exporter/internal/pkg/exec"
)

// MockExec is a mock of Exec interface.
type MockExec struct {
	ctrl     *gomock.Controller
	recorder *MockExecMockRecorder
}

// MockExecMockRecorder is the mock recorder for MockExec.
type MockExecMockRecorder struct {
	mock *MockExec
}

// NewMockExec creates a new mock instance.
func NewMockExec(ctrl *gomock.Controller) *MockExec {
	mock := &MockExec{ctrl: ctrl}
	mock.recorder = &MockExecMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockExec) EXPECT() *MockExecMockRecorder {
	return m.recorder
}

// Command mocks base method.
func (m *MockExec) Command(arg0 string, arg1 ...string) exec.Cmd {
	m.ctrl.T.Helper()
	varargs := []any{arg0}
	for _, a := range arg1 {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "Command", varargs...)
	ret0, _ := ret[0].(exec.Cmd)
	return ret0
}

// Command indicates an expected call of Command.
func (mr *MockExecMockRecorder) Command(arg0 any, arg1 ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{arg0}, arg1...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Command", reflect.TypeOf((*MockExec)(nil).Command), varargs...)
}
