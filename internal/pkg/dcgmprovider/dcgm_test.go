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

package dcgmprovider

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestGetCPUHierarchyFallsBackToV1WhenV2IsUnsupported(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{
			name: "version mismatch",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH},
		},
		{
			name: "function not found",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND},
		},
		{
			name: "legacy go-dcgm wrapped API version mismatch text",
			err:  errors.New("error retrieving DCGM CPU hierarchy v2: API version mismatch"),
		},
		{
			name: "legacy go-dcgm wrapped function not found text",
			err:  errors.New("error retrieving DCGM CPU hierarchy v2: The requested function was not found"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreCPUHierarchySeams(t)
			logBuffer := captureDefaultSlog(t)
			getCPUHierarchyV2Func = func() (dcgm.CPUHierarchy_v2, error) {
				return dcgm.CPUHierarchy_v2{}, tt.err
			}
			getCPUHierarchyV1Func = func() (dcgm.CPUHierarchy_v1, error) {
				return dcgm.CPUHierarchy_v1{
					Version: 1,
					NumCPUs: 2,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v1{
						{CPUID: 7, OwnedCores: []uint64{0b101}},
						{CPUID: 8, OwnedCores: []uint64{0b010}},
					},
				}, nil
			}

			got, err := dcgmProvider{}.GetCPUHierarchy()

			require.NoError(t, err)
			assert.Zero(t, got.Version)
			assert.Equal(t, uint(2), got.NumCPUs)
			assert.Equal(t, uint(7), got.CPUs[0].CPUID)
			assert.Equal(t, []uint64{0b101}, got.CPUs[0].OwnedCores)
			assert.Empty(t, got.CPUs[0].Serial)
			assert.Equal(t, uint(8), got.CPUs[1].CPUID)
			assert.Equal(t, []uint64{0b010}, got.CPUs[1].OwnedCores)
			assert.Empty(t, got.CPUs[1].Serial)
			assert.Contains(t, logBuffer.String(), `msg="Falling back to dcgmGetCpuHierarchy after dcgmGetCpuHierarchy_v2 failed"`)
		})
	}
}

func TestGetCPUHierarchyDoesNotFallbackForOtherErrors(t *testing.T) {
	restoreCPUHierarchySeams(t)
	wantErr := &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}
	getCPUHierarchyV2Func = func() (dcgm.CPUHierarchy_v2, error) {
		return dcgm.CPUHierarchy_v2{}, wantErr
	}
	getCPUHierarchyV1Func = func() (dcgm.CPUHierarchy_v1, error) {
		t.Fatal("v1 fallback must not run for non-version errors")
		return dcgm.CPUHierarchy_v1{}, nil
	}

	_, err := dcgmProvider{}.GetCPUHierarchy()

	require.ErrorIs(t, err, wantErr)
}

func TestGetCPUHierarchyReturnsFallbackError(t *testing.T) {
	restoreCPUHierarchySeams(t)
	fallbackErr := errors.New("v1 failed")
	getCPUHierarchyV2Func = func() (dcgm.CPUHierarchy_v2, error) {
		return dcgm.CPUHierarchy_v2{}, &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH}
	}
	getCPUHierarchyV1Func = func() (dcgm.CPUHierarchy_v1, error) {
		return dcgm.CPUHierarchy_v1{}, fallbackErr
	}

	_, err := dcgmProvider{}.GetCPUHierarchy()

	require.ErrorIs(t, err, fallbackErr)
	assert.ErrorContains(t, err, "fallback to dcgmGetCpuHierarchy failed")
}

func TestCPUHierarchyV1AsV2ClampsExcessiveCPUCount(t *testing.T) {
	v1Hierarchy := dcgm.CPUHierarchy_v1{
		NumCPUs: dcgm.MAX_NUM_CPUS + 1,
		CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v1{
			{CPUID: 7, OwnedCores: []uint64{0b101}},
		},
	}

	var got dcgm.CPUHierarchy_v2
	require.NotPanics(t, func() {
		got = cpuHierarchyV1AsV2(v1Hierarchy)
	})

	assert.Equal(t, dcgm.MAX_NUM_CPUS, got.NumCPUs)
	assert.Equal(t, uint(7), got.CPUs[0].CPUID)
	assert.Equal(t, []uint64{0b101}, got.CPUs[0].OwnedCores)
}

func TestClientSetResetAndInitializeUsesExistingClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockClient := mockdcgmprovider.NewMockDCGM(ctrl)
	original := Client()
	t.Cleanup(func() { SetClient(original) })

	SetClient(mockClient)
	assert.Same(t, mockClient, Client())

	Initialize(&appconfig.Config{})
	assert.Same(t, mockClient, Client(), "Initialize should reuse an existing singleton client")

	reset()
	assert.Nil(t, Client())
}

func TestNewDCGMProviderPassesVsockRemoteHostengineInfoToStandaloneInit(t *testing.T) {
	originalClient := Client()
	reset()
	t.Cleanup(func() { SetClient(originalClient) })
	restoreDCGMProviderSeams(t)

	var gotRemoteHostengineInfo string
	var gotSocketFlag string
	initStandaloneDCGMFunc = func(remoteHostengineInfo string, socketFlag string) (func(), error) {
		gotRemoteHostengineInfo = remoteHostengineInfo
		gotSocketFlag = socketFlag
		return func() {}, nil
	}
	dcgmFieldsInitFunc = func() int { return 0 }

	provider := newDCGMProvider(&appconfig.Config{
		UseRemoteHE:  true,
		RemoteHEInfo: "vsock://3:5555",
	})

	require.NotNil(t, provider)
	assert.Equal(t, "vsock://3:5555", gotRemoteHostengineInfo)
	assert.Equal(t, "0", gotSocketFlag)
}

func restoreCPUHierarchySeams(t *testing.T) {
	t.Helper()
	prevGetCPUHierarchyV2 := getCPUHierarchyV2Func
	prevGetCPUHierarchyV1 := getCPUHierarchyV1Func
	t.Cleanup(func() {
		getCPUHierarchyV2Func = prevGetCPUHierarchyV2
		getCPUHierarchyV1Func = prevGetCPUHierarchyV1
	})
}

func restoreDCGMProviderSeams(t *testing.T) {
	t.Helper()
	prevInitStandaloneDCGM := initStandaloneDCGMFunc
	prevInitEmbeddedDCGM := initEmbeddedDCGMFunc
	prevDCGMFieldsInit := dcgmFieldsInitFunc
	t.Cleanup(func() {
		initStandaloneDCGMFunc = prevInitStandaloneDCGM
		initEmbeddedDCGMFunc = prevInitEmbeddedDCGM
		dcgmFieldsInitFunc = prevDCGMFieldsInit
	})
}
