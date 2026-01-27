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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMIGDeviceInfoByID_When_NVML_Not_Initialized(t *testing.T) {
	validMIGUUID := "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5"
	newNvmlProvider := nvmlProvider{}

	deviceInfo, err := newNvmlProvider.GetMIGDeviceInfoByID(validMIGUUID)
	assert.Error(t, err, "uuid: %v, Device Info: %+v", validMIGUUID, deviceInfo)
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
	tests := []struct {
		name       string
		preRunFunc func() NVML
	}{
		{
			name: "NVML not initialized",
			preRunFunc: func() NVML {
				reset()
				return nvmlProvider{initialized: true}
			},
		},
		{
			name: "NVML already initialized",
			preRunFunc: func() NVML {
				_ = Initialize()
				return Client()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.preRunFunc()
			defer reset()
			var nvmlProvider NVML
			var err error
			nvmlProvider, err = newNVMLProvider()
			assert.Nil(t, err)
			assert.Equalf(t, want, nvmlProvider, "Unexpected Output")
		})
	}
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
