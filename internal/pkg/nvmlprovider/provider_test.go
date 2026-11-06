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

package nvmlprovider

import (
	"errors"
	"math"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitializeDoesNotCacheFailedProvider verifies a transient NVML setup
// failure leaves the global client uninitialized so the next lifecycle retry
// performs initialization again.
func TestInitializeDoesNotCacheFailedProvider(t *testing.T) {
	reset()
	originalNewProvider := newNVMLProviderFunc
	t.Cleanup(func() {
		newNVMLProviderFunc = originalNewProvider
		reset()
	})

	wantErr := errors.New("NVML is temporarily unavailable")
	readyProvider := nvmlProvider{initialized: true}
	attempts := 0
	newNVMLProviderFunc = func() (NVML, error) {
		attempts++
		if attempts == 1 {
			return nvmlProvider{initialized: false}, wantErr
		}
		return readyProvider, nil
	}

	require.ErrorIs(t, Initialize(), wantErr)
	assert.Equal(t, nvmlProvider{initialized: false}, Client())

	require.NoError(t, Initialize())
	assert.Equal(t, readyProvider, Client())
	assert.Equal(t, 2, attempts)
}

// stubNewNVMLProvider replaces and restores the provider constructor for a test.
func stubNewNVMLProvider(t *testing.T, provider NVML, err error) {
	t.Helper()

	originalNewProvider := newNVMLProviderFunc
	newNVMLProviderFunc = func() (NVML, error) {
		return provider, err
	}
	t.Cleanup(func() { newNVMLProviderFunc = originalNewProvider })
}

// stubNVMLShutdown replaces and restores the NVML shutdown function for a test.
func stubNVMLShutdown(t *testing.T, result nvml.Return) {
	t.Helper()

	originalShutdown := shutdownNVML
	shutdownNVML = func() nvml.Return { return result }
	t.Cleanup(func() { shutdownNVML = originalShutdown })
}

// stubNVMLInitialization replaces and restores NVML initialization for a test.
func stubNVMLInitialization(t *testing.T, result nvml.Return) {
	t.Helper()

	originalInitialization := initNVML
	initNVML = func() nvml.Return { return result }
	t.Cleanup(func() { initNVML = originalInitialization })
}

// fakeGPUInstanceAPI provides configurable NVML behavior for placement-resolution tests.
type fakeGPUInstanceAPI struct {
	deviceHandleByUUIDFunc         func(string) (nvml.Device, nvml.Return)
	deviceMinorNumberFunc          func(nvml.Device) (int, nvml.Return)
	gpuInstanceProfileInfoByIDFunc func(nvml.Device, int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return)
	gpuInstancesFunc               func(nvml.Device, *nvml.GpuInstanceProfileInfo) ([]nvml.GpuInstance, nvml.Return)
	gpuInstanceInfoFunc            func(nvml.GpuInstance) (nvml.GpuInstanceInfo, nvml.Return)
}

// deviceHandleByUUID delegates UUID lookup to the configured test function.
func (f *fakeGPUInstanceAPI) deviceHandleByUUID(uuid string) (nvml.Device, nvml.Return) {
	return f.deviceHandleByUUIDFunc(uuid)
}

// deviceMinorNumber delegates minor-number lookup to the configured test function.
func (f *fakeGPUInstanceAPI) deviceMinorNumber(device nvml.Device) (int, nvml.Return) {
	return f.deviceMinorNumberFunc(device)
}

// gpuInstanceProfileInfoByID delegates profile lookup to the configured test function.
func (f *fakeGPUInstanceAPI) gpuInstanceProfileInfoByID(
	device nvml.Device,
	profileID int,
) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
	return f.gpuInstanceProfileInfoByIDFunc(device, profileID)
}

// gpuInstances delegates instance enumeration to the configured test function.
func (f *fakeGPUInstanceAPI) gpuInstances(
	device nvml.Device,
	profile *nvml.GpuInstanceProfileInfo,
) ([]nvml.GpuInstance, nvml.Return) {
	return f.gpuInstancesFunc(device, profile)
}

// gpuInstanceInfo delegates instance inspection to the configured test function.
func (f *fakeGPUInstanceAPI) gpuInstanceInfo(instance nvml.GpuInstance) (nvml.GpuInstanceInfo, nvml.Return) {
	return f.gpuInstanceInfoFunc(instance)
}

// newFakeGPUInstanceAPI returns a successful fake that enumerates the supplied instance metadata.
func newFakeGPUInstanceAPI(infos ...nvml.GpuInstanceInfo) *fakeGPUInstanceAPI {
	infoIndex := 0
	return &fakeGPUInstanceAPI{
		deviceHandleByUUIDFunc: func(string) (nvml.Device, nvml.Return) {
			return nil, nvml.SUCCESS
		},
		deviceMinorNumberFunc: func(nvml.Device) (int, nvml.Return) {
			return 0, nvml.SUCCESS
		},
		gpuInstanceProfileInfoByIDFunc: func(_ nvml.Device, profileID int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
			return nvml.GpuInstanceProfileInfo_v2{
				Id:            uint32(profileID),  //nolint:gosec // test profile IDs are non-negative
				InstanceCount: uint32(len(infos)), //nolint:gosec // test fixtures cannot exceed uint32 capacity
			}, nvml.SUCCESS
		},
		gpuInstancesFunc: func(_ nvml.Device, _ *nvml.GpuInstanceProfileInfo) ([]nvml.GpuInstance, nvml.Return) {
			return make([]nvml.GpuInstance, len(infos)), nvml.SUCCESS
		},
		gpuInstanceInfoFunc: func(nvml.GpuInstance) (nvml.GpuInstanceInfo, nvml.Return) {
			info := infos[infoIndex]
			infoIndex++
			return info, nvml.SUCCESS
		},
	}
}

// TestNewGPUInstanceAPIUsesPackageNVMLLifecycle replaces the package-level NVML lookup by UUID and checks
// that the GPU instance API calls it with the parent GPU's UUID.
func TestNewGPUInstanceAPIUsesPackageNVMLLifecycle(t *testing.T) {
	originalDeviceGetHandleByUUID := nvml.DeviceGetHandleByUUID
	t.Cleanup(func() {
		nvml.DeviceGetHandleByUUID = originalDeviceGetHandleByUUID
	})

	const parentUUID = "GPU-parent"
	called := false
	nvml.DeviceGetHandleByUUID = func(uuid string) (nvml.Device, nvml.Return) {
		called = true
		assert.Equal(t, parentUUID, uuid)
		return nil, nvml.SUCCESS
	}

	_, ret := newGPUInstanceAPI().deviceHandleByUUID(parentUUID)

	assert.Equal(t, nvml.SUCCESS, ret)
	assert.True(t, called, "GPU instance API did not use the initialized package-level NVML library")
}

// TestGetMIGDeviceInfoByID_When_NVML_Not_Initialized expects an error for a valid MIG UUID
// when NVML is not initialized.
func TestGetMIGDeviceInfoByID_When_NVML_Not_Initialized(t *testing.T) {
	validMIGUUID := "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5"
	newNvmlProvider := nvmlProvider{}

	deviceInfo, err := newNvmlProvider.GetMIGDeviceInfoByID(validMIGUUID)
	assert.Error(t, err, "uuid: %v, Device Info: %+v", validMIGUUID, deviceInfo)
}

// TestGetGPUInstanceIDByProfileAndPlacement covers placement resolution and NVML failure modes.
func TestGetGPUInstanceIDByProfileAndPlacement(t *testing.T) {
	t.Run("resolves profile ID 19 and placement", func(t *testing.T) {
		api := newFakeGPUInstanceAPI(
			nvml.GpuInstanceInfo{
				Id:        2,
				ProfileId: 19,
				Placement: nvml.GpuInstancePlacement{Start: 0},
			},
			nvml.GpuInstanceInfo{
				Id:        7,
				ProfileId: 19,
				Placement: nvml.GpuInstancePlacement{Start: 4},
			},
		)
		api.gpuInstanceProfileInfoByIDFunc = func(_ nvml.Device, profileID int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
			assert.Equal(t, 19, profileID)
			return nvml.GpuInstanceProfileInfo_v2{
				Id:                  19,
				IsP2pSupported:      1,
				SliceCount:          1,
				InstanceCount:       2,
				MultiprocessorCount: 14,
				MemorySizeMB:        12288,
			}, nvml.SUCCESS
		}
		api.gpuInstancesFunc = func(_ nvml.Device, profile *nvml.GpuInstanceProfileInfo) ([]nvml.GpuInstance, nvml.Return) {
			assert.Equal(t, uint32(19), profile.Id)
			assert.Equal(t, uint32(2), profile.InstanceCount)
			assert.Equal(t, uint32(14), profile.MultiprocessorCount)
			assert.Equal(t, uint64(12288), profile.MemorySizeMB)
			return make([]nvml.GpuInstance, 2), nvml.SUCCESS
		}
		provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}

		id, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 4)

		require.NoError(t, err)
		assert.Equal(t, uint(7), id)
	})

	t.Run("requires canonical parent minor", func(t *testing.T) {
		api := newFakeGPUInstanceAPI()
		api.deviceMinorNumberFunc = func(nvml.Device) (int, nvml.Return) {
			return 1, nvml.SUCCESS
		}
		provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}

		_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "minor number 1, expected 0")
	})

	tests := []struct {
		name    string
		setup   func(*fakeGPUInstanceAPI)
		infos   []nvml.GpuInstanceInfo
		wantErr string
	}{
		{
			name: "parent lookup failure",
			setup: func(api *fakeGPUInstanceAPI) {
				api.deviceHandleByUUIDFunc = func(string) (nvml.Device, nvml.Return) {
					return nil, nvml.ERROR_NOT_FOUND
				}
			},
			wantErr: "failed to get parent device handle",
		},
		{
			name: "minor lookup failure",
			setup: func(api *fakeGPUInstanceAPI) {
				api.deviceMinorNumberFunc = func(nvml.Device) (int, nvml.Return) {
					return 0, nvml.ERROR_UNKNOWN
				}
			},
			wantErr: "failed to get minor number",
		},
		{
			name: "profile lookup failure",
			setup: func(api *fakeGPUInstanceAPI) {
				api.gpuInstanceProfileInfoByIDFunc = func(nvml.Device, int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
					return nvml.GpuInstanceProfileInfo_v2{}, nvml.ERROR_INVALID_ARGUMENT
				}
			},
			wantErr: "failed to get GPU instance profile ID",
		},
		{
			name: "profile lookup returns a different ID",
			setup: func(api *fakeGPUInstanceAPI) {
				api.gpuInstanceProfileInfoByIDFunc = func(nvml.Device, int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
					return nvml.GpuInstanceProfileInfo_v2{Id: 18}, nvml.SUCCESS
				}
			},
			wantErr: "returned GPU instance profile ID 18 for requested ID 19",
		},
		{
			name: "enumeration failure",
			setup: func(api *fakeGPUInstanceAPI) {
				api.gpuInstancesFunc = func(nvml.Device, *nvml.GpuInstanceProfileInfo) ([]nvml.GpuInstance, nvml.Return) {
					return nil, nvml.ERROR_UNKNOWN
				}
			},
			wantErr: "failed to enumerate GPU instances",
		},
		{
			name:  "instance info failure",
			infos: []nvml.GpuInstanceInfo{{}},
			setup: func(api *fakeGPUInstanceAPI) {
				api.gpuInstanceInfoFunc = func(nvml.GpuInstance) (nvml.GpuInstanceInfo, nvml.Return) {
					return nvml.GpuInstanceInfo{}, nvml.ERROR_UNKNOWN
				}
			},
			wantErr: "failed to inspect GPU instance",
		},
		{
			name:    "no matching placement",
			infos:   []nvml.GpuInstanceInfo{{Id: 2, ProfileId: 19, Placement: nvml.GpuInstancePlacement{Start: 1}}},
			wantErr: "no GPU instance matches",
		},
		{
			name: "ambiguous placement",
			infos: []nvml.GpuInstanceInfo{
				{Id: 2, ProfileId: 19, Placement: nvml.GpuInstancePlacement{Start: 0}},
				{Id: 3, ProfileId: 19, Placement: nvml.GpuInstancePlacement{Start: 0}},
			},
			wantErr: "multiple GPU instances match",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeGPUInstanceAPI(tc.infos...)
			if tc.setup != nil {
				tc.setup(api)
			}
			provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}

			id, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

			assert.Zero(t, id)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestGetGPUInstanceIDByProfileAndPlacementRequiresInitializedProvider expects an "NVML library not
// initialized" error when NVML is not initialized.
func TestGetGPUInstanceIDByProfileAndPlacementRequiresInitializedProvider(t *testing.T) {
	provider := nvmlProvider{}

	_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "NVML library not initialized")
}

// TestGetGPUInstanceIDByProfileAndPlacementRequiresGPUInstanceAPI expects a "GPU instance API is
// unavailable" error when NVML is initialized but the GPU instance API is not set up.
func TestGetGPUInstanceIDByProfileAndPlacementRequiresGPUInstanceAPI(t *testing.T) {
	provider := nvmlProvider{initialized: true}

	_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "GPU instance API is unavailable")
}

// TestGetDeviceProcessMemory_When_NVML_Not_Initialized expects a nil result and an error
// when NVML is not initialized.
func TestGetDeviceProcessMemory_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetDeviceProcessMemory("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get device process memory")
}

// TestGetDeviceProcessUtilization_When_NVML_Not_Initialized expects a nil result and an error
// when NVML is not initialized.
func TestGetDeviceProcessUtilization_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetDeviceProcessUtilization("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get device process utilization")
}

// TestGetAllMIGDevicesProcessMemory_When_NVML_Not_Initialized expects a nil result and an error
// when NVML is not initialized.
func TestGetAllMIGDevicesProcessMemory_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetAllMIGDevicesProcessMemory("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get MIG device process memory")
}

// TestGetGPUInstanceProfileName_When_NVML_Not_Initialized expects an empty name and an error
// when NVML is not initialized.
func TestGetGPUInstanceProfileName_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetGPUInstanceProfileName("GPU-test-uuid", 9)
	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "failed to get GPU instance profile name")
}

// TestMigProfileNameFromBytes trims a null-terminated MIG profile name and rejects an empty value.
func TestMigProfileNameFromBytes(t *testing.T) {
	profileName, err := migProfileNameFromBytes([]int8{'7', 'g', '.', '8', '0', 'g', 'b', 0, 'x'})
	require.NoError(t, err)
	assert.Equal(t, "7g.80gb", profileName)

	profileName, err = migProfileNameFromBytes([]int8{0})
	assert.Error(t, err)
	assert.Empty(t, profileName)
}

// TestGetGPUInstanceProfileName_When_ProfileID_Exceeds_MaxInt expects an empty name and an
// "exceeds maximum int value" error for a profile ID larger than the largest int.
func TestGetGPUInstanceProfileName_When_ProfileID_Exceeds_MaxInt(t *testing.T) {
	provider := nvmlProvider{initialized: true}
	result, err := provider.GetGPUInstanceProfileName("GPU-test-uuid", uint(math.MaxInt)+1)
	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "exceeds maximum int value")
}

// TestGetMIGDeviceInfoForOldDriver parses legacy MIG UUID parent and instance IDs and rejects malformed UUIDs or IDs.
func TestGetMIGDeviceInfoForOldDriver(t *testing.T) {
	tests := []struct {
		name            string
		uuid            string
		expectedMIGInfo *MIGDeviceInfo
		expectedError   bool
		errorContains   string
	}{
		{
			name: "parses a valid old-driver UUID",
			uuid: "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedMIGInfo: &MIGDeviceInfo{
				ParentUUID:        "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5",
				GPUInstanceID:     1,
				ComputeInstanceID: 5,
			},
		},
		{
			name:          "rejects a missing MIG prefix",
			uuid:          "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
			errorContains: "unable to parse",
		},
		{
			name:          "rejects a missing GPU prefix",
			uuid:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
			errorContains: "invalid MIG device UUID",
		},
		{
			name:          "rejects an invalid GPU instance ID",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/xx/5",
			expectedError: true,
			errorContains: "invalid GPU instance ID",
		},
		{
			name:          "rejects an invalid compute instance ID",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/xx",
			expectedError: true,
			errorContains: "invalid Compute instance ID",
		},
		{
			name:          "rejects an empty UUID",
			uuid:          "",
			expectedError: true,
			errorContains: "unable to parse",
		},
		{
			name:          "rejects missing instance IDs",
			uuid:          "MIG-GPU-test",
			expectedError: true,
			errorContains: "invalid MIG device UUID",
		},
		{
			name:          "rejects a missing compute instance ID",
			uuid:          "MIG-GPU-test/1",
			expectedError: true,
			errorContains: "invalid MIG device UUID",
		},
		{
			name: "parses large instance IDs",
			uuid: "MIG-GPU-test/9999/9999",
			expectedMIGInfo: &MIGDeviceInfo{
				ParentUUID:        "GPU-test",
				GPUInstanceID:     9999,
				ComputeInstanceID: 9999,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deviceInfo, err := getMIGDeviceInfoForOldDriver(tc.uuid)
			if tc.expectedError {
				assert.Error(t, err, "uuid: %v, Device Info: %+v", tc.uuid, deviceInfo)
				assert.ErrorContains(t, err, tc.errorContains)
			} else {
				assert.Nil(t, err, "err: %v, uuid: %v", err, tc.uuid)
				assert.Equal(t, tc.expectedMIGInfo, deviceInfo, "MIG uuid '%v' parsed incorrectly", tc.uuid)
			}
		})
	}
}

// TestNewNVMLProvider checks that a fresh provider is initialized with a GPU instance API, that an already
// initialized provider is returned as is, and that a failed NVML start returns the error and an uninitialized
// provider.
func TestNewNVMLProvider(t *testing.T) {
	t.Run("initializes a new provider with the GPU instance API", func(t *testing.T) {
		stubNVMLInitialization(t, nvml.SUCCESS)
		stubNVMLShutdown(t, nvml.SUCCESS)
		reset()
		t.Cleanup(reset)
		provider, err := newNVMLProvider()
		require.NoError(t, err)
		t.Cleanup(provider.Cleanup)

		concrete, ok := provider.(nvmlProvider)
		require.True(t, ok)
		assert.True(t, concrete.initialized)
		assert.NotNil(t, concrete.gpuInstanceAPI)
	})

	t.Run("returns the existing initialized provider", func(t *testing.T) {
		stubNVMLInitialization(t, nvml.SUCCESS)
		stubNVMLShutdown(t, nvml.SUCCESS)
		reset()
		t.Cleanup(reset)
		require.NoError(t, Initialize())
		existing := Client()
		t.Cleanup(existing.Cleanup)

		provider, err := newNVMLProvider()

		require.NoError(t, err)
		assert.Equal(t, existing, provider)
	})

	t.Run("returns a failed provider when initialization fails", func(t *testing.T) {
		stubNVMLInitialization(t, nvml.ERROR_LIBRARY_NOT_FOUND)
		reset()
		t.Cleanup(reset)

		provider, err := newNVMLProvider()

		require.Error(t, err)
		assert.ErrorContains(t, err, "ERROR_LIBRARY_NOT_FOUND")
		assert.Equal(t, nvmlProvider{initialized: false}, provider)
	})
}

// TestClient_WhenNil tests that Client() returns a safe non-nil provider when not initialized
func TestClient_WhenNil(t *testing.T) {
	// Reset to ensure nvmlInterface is nil
	reset()

	client := Client()

	// Should return a non-nil provider
	assert.NotNil(t, client)

	// Should be a non-initialized provider
	provider, ok := client.(nvmlProvider)
	assert.True(t, ok, "Client should return nvmlProvider type")
	assert.False(t, provider.initialized, "Provider should not be initialized")

	// Calling methods on this provider should return appropriate errors
	_, err := client.GetMIGDeviceInfoByID("MIG-test")
	assert.Error(t, err, "Should return error when not initialized")
	assert.Contains(t, err.Error(), "NVML not initialized")
}

// TestSetClient tests the SetClient function
func TestSetClient(t *testing.T) {
	// Create a custom provider
	customProvider := nvmlProvider{initialized: true}

	// Set the custom provider
	SetClient(customProvider)

	// Verify it was set
	client := Client()
	assert.Equal(t, customProvider, client)

	// Reset for cleanup
	reset()
}

// TestCleanup_WhenNotInitialized tests cleanup when NVML was never initialized
func TestCleanup_WhenNotInitialized(t *testing.T) {
	provider := nvmlProvider{initialized: false}
	SetClient(provider)
	t.Cleanup(reset)

	provider.Cleanup()

	assert.Equal(t, provider, Client())
}

// TestCleanup_WhenInitialized tests cleanup when NVML is initialized
func TestCleanup_WhenInitialized(t *testing.T) {
	stubNVMLShutdown(t, nvml.SUCCESS)
	SetClient(nvmlProvider{initialized: true})
	t.Cleanup(reset)

	Client().Cleanup()

	client := Client()
	provider, ok := client.(nvmlProvider)
	require.True(t, ok)
	assert.False(t, provider.initialized)
}

// TestNVMLInitializationError adds runtime setup guidance for a missing library and preserves other NVML error messages.
func TestNVMLInitializationError(t *testing.T) {
	t.Run("library not found explains container runtime setup", func(t *testing.T) {
		err := nvmlInitializationError(nvml.ERROR_LIBRARY_NOT_FOUND)

		assert.ErrorContains(t, err, "ERROR_LIBRARY_NOT_FOUND")
		assert.ErrorContains(t, err, "libnvidia-ml.so.1")
		assert.ErrorContains(t, err, "NVIDIA runtime")
		assert.ErrorContains(t, err, "runtimeClassName")
	})

	t.Run("other NVML errors keep their original message", func(t *testing.T) {
		assert.EqualError(t, nvmlInitializationError(nvml.ERROR_NOT_SUPPORTED), "ERROR_NOT_SUPPORTED")
	})
}

// TestPreCheck tests the initialization guard without requiring an NVIDIA driver.
func TestPreCheck(t *testing.T) {
	tests := []struct {
		name          string
		provider      nvmlProvider
		expectError   bool
		errorContains string
	}{
		{
			name:        "Initialized provider",
			provider:    nvmlProvider{initialized: true},
			expectError: false,
		},
		{
			name:          "Uninitialized provider",
			provider:      nvmlProvider{},
			expectError:   true,
			errorContains: "NVML library not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.provider.preCheck()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestReset tests the reset function
func TestReset(t *testing.T) {
	reset()
	t.Cleanup(reset)
	stubNewNVMLProvider(t, nvmlProvider{initialized: true}, nil)
	require.NoError(t, Initialize())

	reset()

	client := Client()
	provider, ok := client.(nvmlProvider)
	require.True(t, ok)
	assert.False(t, provider.initialized)
}

// TestCleanup_MultipleCalls tests that calling Cleanup multiple times is safe
func TestCleanup_MultipleCalls(t *testing.T) {
	reset()
	t.Cleanup(reset)
	stubNewNVMLProvider(t, nvmlProvider{initialized: true}, nil)
	stubNVMLShutdown(t, nvml.SUCCESS)
	require.NoError(t, Initialize())

	provider := Client()

	// First cleanup
	provider.Cleanup()

	// Second cleanup should be safe (idempotent)
	provider.Cleanup()

	client, ok := Client().(nvmlProvider)
	require.True(t, ok)
	assert.False(t, client.initialized)
}

// TestProfileInfoMissingSymbol uses a nil device so entering native code would fail the process.
func TestProfileInfoMissingSymbol(t *testing.T) {
	originalLookup := lookupNVMLSymbol
	t.Cleanup(func() { lookupNVMLSymbol = originalLookup })
	lookupNVMLSymbol = func(symbol string) error {
		require.Equal(t, "nvmlDeviceGetGpuInstanceProfileInfoByIdV", symbol)
		return errors.New("missing symbol")
	}
	info, ret := (goNVMLGPUInstanceAPI{}).gpuInstanceProfileInfoByID(nil, 9)
	assert.Equal(t, nvml.ERROR_FUNCTION_NOT_FOUND, ret)
	assert.Equal(t, nvml.GpuInstanceProfileInfo_v2{}, info)

	api := newFakeGPUInstanceAPI()
	api.gpuInstanceProfileInfoByIDFunc = (goNVMLGPUInstanceAPI{}).gpuInstanceProfileInfoByID
	provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}
	name, err := provider.GetGPUInstanceProfileName("GPU-parent", 9)
	require.ErrorContains(t, err, "UUID GPU-parent profile 9: "+nvml.ErrorString(nvml.ERROR_FUNCTION_NOT_FOUND))
	assert.Empty(t, name)
}

// TestGetGPUInstanceProfileName covers canonical names and contextual lookup errors through the shared adapter.
func TestGetGPUInstanceProfileName(t *testing.T) {
	tests := []struct {
		name        string
		result      nvml.Return
		profileName [96]int8
		wantName    string
		wantErr     string
	}{
		{name: "canonical NVML name", result: nvml.SUCCESS, profileName: [96]int8{'7', 'g', '.', '8', '0', 'g', 'b'}, wantName: "7g.80gb"},
		{name: "invalid profile", result: nvml.ERROR_INVALID_ARGUMENT, wantErr: "INVALID_ARGUMENT"},
		{name: "missing symbol", result: nvml.ERROR_FUNCTION_NOT_FOUND, wantErr: "FUNCTION_NOT_FOUND"},
		{name: "empty name", result: nvml.SUCCESS, wantErr: "profile name is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeGPUInstanceAPI()
			api.gpuInstanceProfileInfoByIDFunc = func(_ nvml.Device, profileID int) (nvml.GpuInstanceProfileInfo_v2, nvml.Return) {
				require.Equal(t, 9, profileID)
				info := nvml.GpuInstanceProfileInfo_v2{Name: tt.profileName}
				return info, tt.result
			}
			provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}
			name, err := provider.GetGPUInstanceProfileName("GPU-parent", 9)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, name)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

// TestPlacementMissingProfileSymbol requires a real instance ID and must stop before enumeration.
func TestPlacementMissingProfileSymbol(t *testing.T) {
	originalLookup := lookupNVMLSymbol
	t.Cleanup(func() { lookupNVMLSymbol = originalLookup })
	lookupNVMLSymbol = func(string) error { return errors.New("missing symbol") }
	api := newFakeGPUInstanceAPI()
	api.gpuInstanceProfileInfoByIDFunc = (goNVMLGPUInstanceAPI{}).gpuInstanceProfileInfoByID
	api.gpuInstancesFunc = func(nvml.Device, *nvml.GpuInstanceProfileInfo) ([]nvml.GpuInstance, nvml.Return) {
		t.Fatal("must not enumerate instances without profile metadata")
		return nil, nvml.ERROR_UNKNOWN
	}
	provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}
	id, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)
	require.ErrorContains(t, err, "FUNCTION_NOT_FOUND")
	assert.Zero(t, id)
}

// TestProfileNameAdapterErrors preserves early errors before profile metadata is requested.
func TestProfileNameAdapterErrors(t *testing.T) {
	t.Run("API unavailable", func(t *testing.T) {
		provider := nvmlProvider{initialized: true}
		name, err := provider.GetGPUInstanceProfileName("GPU-parent", 9)
		require.ErrorContains(t, err, "NVML GPU instance API is unavailable")
		assert.Empty(t, name)
	})
	t.Run("parent lookup fails", func(t *testing.T) {
		api := newFakeGPUInstanceAPI()
		api.deviceHandleByUUIDFunc = func(uuid string) (nvml.Device, nvml.Return) {
			require.Equal(t, "GPU-parent", uuid)
			return nil, nvml.ERROR_NOT_FOUND
		}
		provider := nvmlProvider{initialized: true, gpuInstanceAPI: api}
		name, err := provider.GetGPUInstanceProfileName("GPU-parent", 9)
		require.ErrorContains(t, err, "failed to get parent device handle for UUID GPU-parent: "+nvml.ErrorString(nvml.ERROR_NOT_FOUND))
		assert.Empty(t, name)
	})
}
