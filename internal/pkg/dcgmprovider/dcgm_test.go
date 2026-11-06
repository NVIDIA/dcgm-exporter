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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
)

// TestGetCPUHierarchyFallsBackToV1WhenV2IsUnsupported makes the v2 call fail with a version mismatch or a
// missing function, as a typed error or as older wrapped text, and expects the v1 result with CPU IDs and
// cores copied, no serials, and a fallback log line.
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
			assert.Contains(t, logBuffer.String(), `level=WARN msg="DCGM CPU hierarchy v2 unavailable; listing CPUs and cores with the v1 call instead, without CPU serial numbers"`)
			assert.Contains(t, logBuffer.String(), `hint="`+dcgmerrors.SupportedVersionHint+`"`)
		})
	}
}

// TestGetCPUHierarchyDoesNotFallbackForOtherErrors expects an error other than a version
// mismatch from the v2 call to be returned as is, without trying the v1 call.
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

// TestGetCPUHierarchyReturnsFallbackError expects the v1 error, wrapped with a fallback
// message, when the v2 call reports a version mismatch and the v1 call also fails.
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

// TestCPUHierarchyV1AsV2ClampsExcessiveCPUCount converts a v1 hierarchy that claims more CPUs than the array
// holds, and expects no panic, a CPU count capped at the array size, and the first CPU copied.
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

// TestClientSetResetAndInitializeUsesExistingClient expects Initialize to keep a client
// already set with SetClient, and reset to clear it.
func TestClientSetResetAndInitializeUsesExistingClient(t *testing.T) {
	client := newNativeWatchFake()
	original := Client()
	t.Cleanup(func() { SetClient(original) })

	SetClient(client)
	assert.Same(t, client, Client())

	require.NoError(t, Initialize(&appconfig.Config{}))
	assert.Same(t, client, Client(), "Initialize should reuse an existing singleton client")

	reset()
	assert.Nil(t, Client())
}

// TestNewDCGMProviderPassesVsockRemoteHostengineInfoToStandaloneInit configures a remote hostengine at a
// vsock address and expects standalone startup to receive that address unchanged with the socket flag "0".
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

	provider, err := newDCGMProvider(&appconfig.Config{
		UseRemoteHE:  true,
		RemoteHEInfo: "vsock://3:5555",
	})

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, "vsock://3:5555", gotRemoteHostengineInfo)
	assert.Equal(t, "0", gotSocketFlag)
}

// TestNewDCGMProviderRollsBackDCGMWhenFieldsInitFails verifies that a failed
// fields-module setup releases the just-created DCGM session before returning.
func TestNewDCGMProviderRollsBackDCGMWhenFieldsInitFails(t *testing.T) {
	originalClient := Client()
	reset()
	t.Cleanup(func() { SetClient(originalClient) })
	restoreDCGMProviderSeams(t)

	shutdownCalls := 0
	initEmbeddedDCGMFunc = func() (func(), error) {
		return func() { shutdownCalls++ }, nil
	}
	dcgmFieldsInitFunc = func() int { return -1 }

	provider, err := newDCGMProvider(&appconfig.Config{})

	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Equal(t, 1, shutdownCalls)
	assert.Nil(t, Client())
}

// TestNewDCGMProviderRollsBackPartialInitialization verifies that a DCGM
// initializer cleanup is honored even when the initializer also returns an
// error. Test hardware fallback relies on this to avoid leaving a half-open
// standalone host-engine session behind before trying another mode.
func TestNewDCGMProviderRollsBackPartialInitialization(t *testing.T) {
	originalClient := Client()
	reset()
	t.Cleanup(func() { SetClient(originalClient) })
	restoreDCGMProviderSeams(t)

	cleanupCalls := 0
	initStandaloneDCGMFunc = func(string, string) (func(), error) {
		return func() { cleanupCalls++ }, errors.New("standalone initialization failed")
	}

	provider, err := newDCGMProvider(&appconfig.Config{UseRemoteHE: true})

	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Equal(t, 1, cleanupCalls)
}

// TestNewDCGMProviderRollsBackPartialEmbeddedInitialization makes embedded startup return both a cleanup and
// an error, and expects the cleanup to run once, an error, no provider and no stored client.
func TestNewDCGMProviderRollsBackPartialEmbeddedInitialization(t *testing.T) {
	originalClient := Client()
	reset()
	t.Cleanup(func() { SetClient(originalClient) })
	restoreDCGMProviderSeams(t)

	cleanupCalls := 0
	initEmbeddedDCGMFunc = func() (func(), error) {
		return func() { cleanupCalls++ }, errors.New("embedded initialization failed")
	}

	provider, err := newDCGMProvider(&appconfig.Config{})

	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Equal(t, 1, cleanupCalls)
	assert.Nil(t, Client())
}

// restoreCPUHierarchySeams restores the package hooks changed by CPU hierarchy tests.
func restoreCPUHierarchySeams(t *testing.T) {
	t.Helper()
	prevGetCPUHierarchyV2 := getCPUHierarchyV2Func
	prevGetCPUHierarchyV1 := getCPUHierarchyV1Func
	t.Cleanup(func() {
		getCPUHierarchyV2Func = prevGetCPUHierarchyV2
		getCPUHierarchyV1Func = prevGetCPUHierarchyV1
	})
}

// restoreDCGMProviderSeams restores the package hooks changed by provider initialization tests.
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

// TestNewDCGMProviderSetsEmbeddedDCGMLogVariables checks the embedded DCGM log
// settings reach DCGM's logging variables before the embedded engine starts:
// stdout as the file and the configured level. With the log off, both keep the
// values the process already had.
func TestNewDCGMProviderSetsEmbeddedDCGMLogVariables(t *testing.T) {
	tests := []struct {
		name      string
		config    appconfig.Config
		wantFile  string
		wantLevel string
	}{
		{"log on", appconfig.Config{EnableDCGMLog: true, DCGMLogLevel: "DEBUG"}, "-", "DEBUG"},
		{"log off", appconfig.Config{DCGMLogLevel: "DEBUG"}, "preexisting-file", "preexisting-level"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalClient := Client()
			reset()
			t.Cleanup(func() { SetClient(originalClient) })
			restoreDCGMProviderSeams(t)
			t.Setenv("__DCGM_DBG_FILE", "preexisting-file")
			t.Setenv("__DCGM_DBG_LVL", "preexisting-level")

			// Read the variables at the moment the embedded engine would start.
			var gotFile, gotLevel string
			initEmbeddedDCGMFunc = func() (func(), error) {
				gotFile, gotLevel = os.Getenv("__DCGM_DBG_FILE"), os.Getenv("__DCGM_DBG_LVL")
				return func() {}, errors.New("stop after reading the log variables")
			}

			_, err := newDCGMProvider(&tt.config)

			require.Error(t, err)
			assert.Equal(t, tt.wantFile, gotFile)
			assert.Equal(t, tt.wantLevel, gotLevel)
		})
	}
}
