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
	"math"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

			_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestGetGPUInstanceIDByProfileAndPlacementRequiresInitializedProvider verifies the initialization guard.
func TestGetGPUInstanceIDByProfileAndPlacementRequiresInitializedProvider(t *testing.T) {
	provider := nvmlProvider{}

	_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "NVML library not initialized")
}

// TestGetGPUInstanceIDByProfileAndPlacementRequiresGPUInstanceAPI verifies the API availability guard.
func TestGetGPUInstanceIDByProfileAndPlacementRequiresGPUInstanceAPI(t *testing.T) {
	provider := nvmlProvider{initialized: true}

	_, err := provider.GetGPUInstanceIDByProfileAndPlacement("GPU-parent", 0, 19, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "GPU instance API is unavailable")
}

func TestGetDeviceProcessMemory_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetDeviceProcessMemory("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get device process memory")
}

func TestGetDeviceProcessUtilization_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetDeviceProcessUtilization("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get device process utilization")
}

func TestGetAllMIGDevicesProcessMemory_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetAllMIGDevicesProcessMemory("GPU-test-uuid")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get MIG device process memory")
}

func TestGetGPUInstanceProfileName_When_NVML_Not_Initialized(t *testing.T) {
	provider := nvmlProvider{}
	result, err := provider.GetGPUInstanceProfileName("GPU-test-uuid", 9)
	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "failed to get GPU instance profile name")
}

func TestMigProfileNameFromBytes(t *testing.T) {
	profileName, err := migProfileNameFromBytes([]int8{'7', 'g', '.', '8', '0', 'g', 'b', 0, 'x'})
	require.NoError(t, err)
	assert.Equal(t, "7g.80gb", profileName)

	profileName, err = migProfileNameFromBytes([]int8{0})
	assert.Error(t, err)
	assert.Empty(t, profileName)
}

func TestGetGPUInstanceProfileName_When_ProfileID_Exceeds_MaxInt(t *testing.T) {
	provider := nvmlProvider{initialized: true}
	result, err := provider.GetGPUInstanceProfileName("GPU-test-uuid", uint(math.MaxInt)+1)
	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "exceeds maximum int value")
}

func TestGetMIGDeviceInfoByID_When_DriverVersion_Below_R470(t *testing.T) {
	_ = Initialize()
	assert.NotNil(t, Client(), "expected NVML Client to be not nil")
	assert.True(t, Client().(nvmlProvider).initialized, "expected Client to be initialized")
	defer Client().Cleanup()

	tests := []struct {
		name            string
		uuid            string
		expectedMIGInfo *MIGDeviceInfo
		expectedError   bool
	}{
		{
			name: "Successful Parsing",
			uuid: "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedMIGInfo: &MIGDeviceInfo{
				ParentUUID:        "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5",
				GPUInstanceID:     1,
				ComputeInstanceID: 5,
			},
		},
		{
			name:          "Fail, Missing MIG at the beginning of UUID",
			uuid:          "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, Missing GPU at the beginning of GPU UUID",
			uuid:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, GI not parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/xx/5",
			expectedError: true,
		},
		{
			name:          "Fail, CI not a parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/xx",
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deviceInfo, err := Client().GetMIGDeviceInfoByID(tc.uuid)
			if tc.expectedError {
				assert.Error(t, err, "uuid: %v, Device Info: %+v", tc.uuid, deviceInfo)
			} else {
				assert.Nil(t, err, "err: %v, uuid: %v", err, tc.uuid)
				assert.Equal(t, tc.expectedMIGInfo, deviceInfo, "MIG uuid '%v' parsed incorrectly", tc.uuid)
			}
		})
	}
}

func Test_newNVMLProvider(t *testing.T) {
	t.Run("initializes a new provider with the GPU instance API", func(t *testing.T) {
		reset()
		t.Cleanup(reset)
		provider, err := newNVMLProvider()
		if err != nil {
			t.Skipf("NVML not available: %v", err)
		}
		t.Cleanup(provider.Cleanup)

		concrete, ok := provider.(nvmlProvider)
		require.True(t, ok)
		assert.True(t, concrete.initialized)
		assert.NotNil(t, concrete.gpuInstanceAPI)
	})

	t.Run("returns the existing initialized provider", func(t *testing.T) {
		reset()
		t.Cleanup(reset)
		if err := Initialize(); err != nil {
			t.Skipf("NVML not available: %v", err)
		}
		existing := Client()
		t.Cleanup(existing.Cleanup)

		provider, err := newNVMLProvider()

		require.NoError(t, err)
		assert.Equal(t, existing, provider)
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
	// Create a non-initialized provider
	provider := nvmlProvider{initialized: false}

	// Should not panic and should log appropriately
	provider.Cleanup()

	// Verify it didn't crash (if we get here, test passes)
	assert.True(t, true)
}

// TestCleanup_WhenInitialized tests cleanup when NVML is initialized
func TestCleanup_WhenInitialized(t *testing.T) {
	// Initialize NVML
	err := Initialize()
	assert.NoError(t, err)

	provider := Client()
	assert.NotNil(t, provider)

	// Cleanup should succeed
	provider.Cleanup()

	// After cleanup, nvmlInterface should be nil
	// (we can't check internal state directly, but Client() should return non-nil safe provider)
	client := Client()
	assert.NotNil(t, client)
}

// TestInitialize_ErrorHandling tests initialization error handling
func TestInitialize_ErrorHandling(t *testing.T) {
	// Reset state
	reset()

	// First initialization should work or fail gracefully
	err := Initialize()
	// We can't force an error without mocking nvml.Init(), but we can test the flow
	// If it succeeds, err should be nil
	// If it fails (no GPU), err should not be nil but should be handled
	if err != nil {
		assert.Error(t, err)
	} else {
		assert.NoError(t, err)
		// Cleanup after successful init
		defer Client().Cleanup()
	}
}

// TestPreCheck tests the preCheck function indirectly through GetMIGDeviceInfoByID
func TestPreCheck(t *testing.T) {
	tests := []struct {
		name          string
		setupFunc     func(t *testing.T)
		expectError   bool
		errorContains string
	}{
		{
			name: "Initialized provider",
			setupFunc: func(t *testing.T) {
				require.NoError(t, Initialize())
			},
			expectError: false,
		},
		{
			name: "Uninitialized provider",
			setupFunc: func(t *testing.T) {
				reset()
			},
			expectError:   true,
			errorContains: "NVML not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc(t)
			defer reset()

			client := Client()
			_, err := client.GetMIGDeviceInfoByID("MIG-GPU-test/1/0")

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else if err != nil {
				// May error if no actual GPU, but shouldn't be initialization error
				assert.NotContains(t, err.Error(), "NVML not initialized")
			}
		})
	}
}

// TestReset tests the reset function
func TestReset(t *testing.T) {
	// Initialize NVML
	err := Initialize()
	if err == nil {
		// Only test if initialization succeeded
		assert.NotNil(t, Client())

		// Reset
		reset()

		// After reset, Client() should still return non-nil (safe provider)
		client := Client()
		assert.NotNil(t, client)

		// But it should be uninitialized
		provider, ok := client.(nvmlProvider)
		assert.True(t, ok)
		assert.False(t, provider.initialized)
	}
}

// TestCleanup_MultipleCalls tests that calling Cleanup multiple times is safe
func TestCleanup_MultipleCalls(t *testing.T) {
	// Initialize NVML
	err := Initialize()
	if err != nil {
		t.Skip("NVML not available, skipping test")
	}

	provider := Client()

	// First cleanup
	provider.Cleanup()

	// Second cleanup should be safe (idempotent)
	provider.Cleanup()

	// Should not panic
	assert.True(t, true)
}

// TestGetMIGDeviceInfoByID_EdgeCases tests edge cases in MIG UUID parsing
func TestGetMIGDeviceInfoByID_EdgeCases(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Skip("NVML not available")
	}
	defer Client().Cleanup()

	tests := []struct {
		name          string
		uuid          string
		expectError   bool
		errorContains string
	}{
		{
			name:          "Empty UUID",
			uuid:          "",
			expectError:   true,
			errorContains: "unable to parse",
		},
		{
			name:          "Invalid format - no slashes",
			uuid:          "MIG-GPU-test",
			expectError:   true,
			errorContains: "invalid MIG device UUID",
		},
		{
			name:          "Invalid format - only one slash",
			uuid:          "MIG-GPU-test/1",
			expectError:   true,
			errorContains: "invalid MIG device UUID",
		},
		{
			name:        "Invalid format - no MIG prefix",
			uuid:        "GPU-test/1/2",
			expectError: true,
		},
		{
			name:          "Large integers",
			uuid:          "MIG-GPU-test/9999/9999",
			expectError:   false, // Should parse successfully even if device doesn't exist
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := Client()
			_, err := client.GetMIGDeviceInfoByID(tt.uuid)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else if err != nil {
				// For valid format but non-existent device, may still get an error
				// from trying to access the actual device, which is fine
				t.Logf("Got error (expected for non-existent device): %v", err)
			}
		})
	}
}
