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
)

func TestGetMIGDeviceInfoByID_When_NVML_Not_Initialized(t *testing.T) {
	validMIGUUID := "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5"
	newNvmlProvider := nvmlProvider{}

	deviceInfo, err := newNvmlProvider.GetMIGDeviceInfoByID(validMIGUUID)
	assert.Error(t, err, "uuid: %v, Device Info: %+v", validMIGUUID, deviceInfo)
}

func TestGetMIGDeviceInfoByID_When_DriverVersion_Below_R470(t *testing.T) {
	Initialize()
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
				return nvmlProvider{initialized: true}
			},
		},
		{
			name: "NVML already initialized",
			preRunFunc: func() NVML {
				Initialize()
				return Client()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.preRunFunc()
			defer reset()
			assert.Equalf(t, want, newNVMLProvider(), "Unexpected Output")
		})
	}
}
