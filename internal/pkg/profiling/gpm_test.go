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

package profiling

import (
	"errors"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

func TestDecideDCPFromGPMStatuses(t *testing.T) {
	tests := []struct {
		name        string
		statuses    []gpuGPMStatus
		wantDisable bool
	}{
		{
			name:        "no GPUs keeps profiling",
			statuses:    nil,
			wantDisable: false,
		},
		{
			name:        "single non-GPM GPU (pre-Hopper A100) keeps profiling via DCP module",
			statuses:    []gpuGPMStatus{gpmNotApplicable},
			wantDisable: false,
		},
		{
			name:        "single healthy GPM GPU (full GPU shape) keeps profiling",
			statuses:    []gpuGPMStatus{gpmHealthy},
			wantDisable: false,
		},
		{
			name:        "single broken GPM GPU (fractional vGPU) disables profiling",
			statuses:    []gpuGPMStatus{gpmBroken},
			wantDisable: true,
		},
		{
			name:        "homogeneous fractional vGPU node disables profiling",
			statuses:    []gpuGPMStatus{gpmBroken, gpmBroken, gpmBroken, gpmBroken},
			wantDisable: true,
		},
		{
			name:        "mixed healthy and broken GPM keeps profiling for healthy GPUs",
			statuses:    []gpuGPMStatus{gpmHealthy, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "non-GPM plus broken GPM keeps profiling for the DCP-module GPU",
			statuses:    []gpuGPMStatus{gpmNotApplicable, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "unknown plus broken stays conservative and keeps profiling",
			statuses:    []gpuGPMStatus{gpmUnknown, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "all unknown keeps profiling",
			statuses:    []gpuGPMStatus{gpmUnknown},
			wantDisable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disable, reason := decideDCPFromGPMStatuses(tt.statuses)
			assert.Equal(t, tt.wantDisable, disable)
			if tt.wantDisable {
				assert.NotEmpty(t, reason, "a disable decision must carry a reason")
			} else {
				assert.Empty(t, reason, "a keep decision must not carry a reason")
			}
		})
	}
}

func TestCountStatus(t *testing.T) {
	statuses := []gpuGPMStatus{gpmHealthy, gpmBroken, gpmBroken, gpmNotApplicable, gpmUnknown}

	assert.Equal(t, 1, countStatus(statuses, gpmHealthy))
	assert.Equal(t, 2, countStatus(statuses, gpmBroken))
	assert.Equal(t, 1, countStatus(statuses, gpmNotApplicable))
	assert.Equal(t, 1, countStatus(statuses, gpmUnknown))
	assert.Equal(t, 0, countStatus(nil, gpmBroken))
}

// fakeChecker is a test double for gpmDeviceChecker that returns pre-programmed
// per-UUID statuses without touching NVML or a real GPU.
type fakeChecker struct {
	initOK         bool
	statuses       map[string]gpuGPMStatus
	shutdownCalled bool
}

func (f *fakeChecker) init() bool { return f.initOK }

func (f *fakeChecker) shutdown() { f.shutdownCalled = true }

func (f *fakeChecker) statusForUUID(uuid string) gpuGPMStatus {
	if s, ok := f.statuses[uuid]; ok {
		return s
	}
	return gpmUnknown
}

// withChecker swaps the package-level deviceChecker for the duration of a test.
func withChecker(t *testing.T, c gpmDeviceChecker) {
	t.Helper()
	prev := deviceChecker
	deviceChecker = c
	t.Cleanup(func() { deviceChecker = prev })
}

// withMockDCGM installs a mock DCGM client for the duration of a test.
func withMockDCGM(t *testing.T) *mockdcgm.MockDCGM {
	t.Helper()
	ctrl := gomock.NewController(t)
	mock := mockdcgm.NewMockDCGM(ctrl)
	prev := dcgmprovider.Client()
	dcgmprovider.SetClient(mock)
	t.Cleanup(func() { dcgmprovider.SetClient(prev) })
	return mock
}

// TestValidateGPMSupportRemoteHostengine verifies that remote hostengine deployments
// skip local NVML validation entirely: local NVML cannot resolve GPUs owned by a
// remote DCGM host, so DCGM's own capability result must be trusted.
func TestValidateGPMSupportRemoteHostengine(t *testing.T) {
	// A checker whose init would fail proves the remote path returns before touching it.
	withChecker(t, &fakeChecker{initOK: false})

	disable, reason := ValidateGPMSupport(&appconfig.Config{UseRemoteHE: true})
	assert.False(t, disable)
	assert.Empty(t, reason)
}

func TestValidateGPMSupport(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(m *mockdcgm.MockDCGM)
		checker     *fakeChecker
		wantDisable bool
	}{
		{
			name: "NVML unavailable keeps profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
			},
			checker:     &fakeChecker{initOK: false},
			wantDisable: false,
		},
		{
			name: "device count error keeps profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("dcgm unavailable"))
			},
			checker:     &fakeChecker{initOK: true},
			wantDisable: false,
		},
		{
			name: "zero GPUs keeps profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
			},
			checker:     &fakeChecker{initOK: true},
			wantDisable: false,
		},
		{
			name: "single healthy GPM GPU keeps profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{UUID: "GPU-0"}, nil)
			},
			checker:     &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{"GPU-0": gpmHealthy}},
			wantDisable: false,
		},
		{
			name: "single pre-Hopper GPU keeps profiling via DCP module",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{UUID: "GPU-0"}, nil)
			},
			checker:     &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{"GPU-0": gpmNotApplicable}},
			wantDisable: false,
		},
		{
			name: "single broken GPM GPU disables profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{UUID: "GPU-0"}, nil)
			},
			checker:     &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{"GPU-0": gpmBroken}},
			wantDisable: true,
		},
		{
			name: "homogeneous fractional vGPU node disables profiling",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{UUID: "GPU-0"}, nil)
				m.EXPECT().GetDeviceInfo(uint(1)).Return(dcgm.Device{UUID: "GPU-1"}, nil)
			},
			checker: &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{
				"GPU-0": gpmBroken,
				"GPU-1": gpmBroken,
			}},
			wantDisable: true,
		},
		{
			name: "mixed healthy and broken GPM keeps profiling for healthy GPU",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{UUID: "GPU-0"}, nil)
				m.EXPECT().GetDeviceInfo(uint(1)).Return(dcgm.Device{UUID: "GPU-1"}, nil)
			},
			checker: &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{
				"GPU-0": gpmHealthy,
				"GPU-1": gpmBroken,
			}},
			wantDisable: false,
		},
		{
			name: "device info error yields unknown and stays conservative",
			setup: func(m *mockdcgm.MockDCGM) {
				m.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
				m.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{}, errors.New("device gone"))
				m.EXPECT().GetDeviceInfo(uint(1)).Return(dcgm.Device{UUID: "GPU-1"}, nil)
			},
			checker: &fakeChecker{initOK: true, statuses: map[string]gpuGPMStatus{
				"GPU-1": gpmBroken,
			}},
			wantDisable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := withMockDCGM(t)
			tt.setup(mock)
			withChecker(t, tt.checker)

			disable, reason := ValidateGPMSupport(&appconfig.Config{})
			assert.Equal(t, tt.wantDisable, disable)
			if tt.wantDisable {
				assert.NotEmpty(t, reason)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}
